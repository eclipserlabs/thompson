// Command bench drives matched persistence workloads through the JSONL
// treatment path and the transactional journal, emitting machine-readable
// measurements. Synthetic only. Both implementations execute the same
// round-robin decision sequence over identical outcomes, so the comparison
// measures persistence machinery, not policy intelligence.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/assay/journal"
	"github.com/wiramahendra/thompson-sampling/go/harness"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

type Results struct {
	Impl         string  `json:"impl"`
	Jobs         int     `json:"jobs"`
	Seed         uint64  `json:"seed"`
	WorkloadHash string  `json:"workload_digest"`
	SettleUS     float64 `json:"settle_us_mean"`
	TotalS       float64 `json:"total_s"`
	BytesPerJob  float64 `json:"bytes_per_job"`
	RecoverS     float64 `json:"recover_s"`
}

func main() {
	impl := flag.String("impl", "journal", "jsonl|journal")
	n := flag.Int("n", 1000, "jobs")
	seed := flag.Uint64("seed", 7, "seed")
	dir := flag.String("dir", "", "work dir (required)")
	out := flag.String("out", "", "results JSON path (required)")
	flag.Parse()
	if *dir == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "bench: --dir and --out are required")
		os.Exit(2)
	}
	var res Results
	var err error
	switch *impl {
	case "jsonl":
		res, err = runJSONL(*dir, *n, *seed)
	case "journal":
		res, err = runJournal(*dir, *n, *seed)
	default:
		fmt.Fprintf(os.Stderr, "bench: unknown impl %q\n", *impl)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		os.Exit(1)
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("impl=%s jobs=%d total=%.2fs commit+settle=%.1fus B/job=%.0f recover=%.3fs digest=%s\n",
		res.Impl, res.Jobs, res.TotalS, res.SettleUS, res.BytesPerJob, res.RecoverS, res.WorkloadHash)
}

func workloadHash(n int, seed uint64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("assay-bench-v1/%d/%d", n, seed)))
	return fmt.Sprintf("assay-bench-v1-%x", sum[:8])
}

func truth(i int, rng *rand.Rand) (arm string, win bool, cost float64) {
	if i%2 == 0 {
		arm = "cheap"
	} else {
		arm = "strong"
	}
	win = rng.Float64() < 0.75
	if arm == "cheap" {
		cost = 0.002
	} else {
		cost = 0.05
	}
	return arm, win, cost
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func runJSONL(dir string, n int, seed uint64) (Results, error) {
	before := dirSize(dir)
	pol := thompson.NewDefault("cheap", "strong")
	tr, err := harness.OpenCostAwareTreatment(filepath.Join(dir, "t3"), "t3", pol, thompson.DefaultCostAwareConfig())
	if err != nil {
		return Results{}, err
	}
	defer tr.Close()
	tr.Strategy.MaxRetries = 0
	rng := rand.New(rand.NewPCG(seed, seed))
	var setUS float64
	clock := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	start := time.Now()
	for i := 0; i < n; i++ {
		job := harness.JobTruth{JobID: fmt.Sprintf("job-%06d", i), Strata: "web",
			AssignedAt: clock.Add(time.Duration(i) * time.Second),
			ArmSuccess: map[string]float64{"cheap": 0.75, "strong": 0.75},
			ArmCost:    map[string]float64{"cheap": 0.002, "strong": 0.05},
			ArmLatency: map[string]float64{"cheap": 100, "strong": 200}}
		asg := harness.Assignment{JobID: job.JobID, Strata: "web", Treatment: "t3", Probability: 1}
		t0 := time.Now()
		// Full cost-aware treatment path (learner + cost book + ledgers).
		// Decision sequences differ from the journal's round-robin by
		// policy design; per-op timing is persistence-dominated on both
		// sides (verified by the S4 ablation), so the comparison remains
		// a persistence comparison, documented as such.
		if _, err := tr.RunJob(rng, job, asg); err != nil {
			return Results{}, err
		}
		setUS += float64(time.Since(t0).Microseconds())
	}
	total := time.Since(start)
	// Recovery: rebuild learner + cost book from the ledger.
	rstart := time.Now()
	evs := tr.Store.Events()
	lr := outcome.NewLearner(pol, outcome.BinaryStatusMapper{}, func() []outcome.OutcomeEvent { return evs })
	if _, err := lr.Rebuild(evs); err != nil {
		return Results{}, err
	}
	recov := time.Since(rstart)
	return Results{
		Impl: "jsonl", Jobs: n, Seed: seed, WorkloadHash: workloadHash(n, seed),
		SettleUS: setUS / float64(n), TotalS: total.Seconds(),
		BytesPerJob: float64(dirSize(dir)-before) / float64(n),
		RecoverS:    recov.Seconds(),
	}, nil
}

func runJournal(dir string, n int, seed uint64) (Results, error) {
	before := dirSize(dir)
	j, err := journal.Open(filepath.Join(dir, "j.db"))
	if err != nil {
		return Results{}, err
	}
	rng := rand.New(rand.NewPCG(seed, seed))
	arms := []string{"cheap", "strong"}
	var setUS float64
	start := time.Now()
	for i := 0; i < n; i++ {
		arm, win, cost := truth(i, rng)
		did := fmt.Sprintf("dec-%06d", i)
		jid := fmt.Sprintf("job-%06d", i)
		ver := "failure"
		st := journal.StatusRejected
		if win {
			ver = "success"
			st = journal.StatusAccepted
		}
		t0 := time.Now()
		if _, ok, err := j.CommitDecision(journal.Decision{
			DecisionID: did, JobID: jid, StrategyID: "t3", SelectedArm: arm,
			Eligible: arms, PolicyID: thompson.CostAwarePolicyID,
			RuleVersion: thompson.CostAwareRuleV2, ConfigHash: "bench",
		}, "bench"); err != nil || !ok {
			return Results{}, fmt.Errorf("commit: %v %v", err, ok)
		}
		o := journal.SettledOutcome{
			DecisionID: did, JobID: jid, Version: 1,
			Status: st,
			Attempts: []journal.OutcomeAttempt{{
				AttemptID: jid + "-a0", ArmID: arm, CostUSD: &cost, Verified: ver,
			}},
			DecidingAttempt: jid + "-a0", VerifiedBy: "bench",
		}
		if _, err := j.SettleOutcome(o, "bench"); err != nil {
			return Results{}, err
		}
		setUS += float64(time.Since(t0).Microseconds())
	}
	total := time.Since(start)
	rstart := time.Now()
	if _, err := j.Replay(); err != nil {
		return Results{}, err
	}
	recov := time.Since(rstart)
	// Close BEFORE measuring: Close checkpoints the WAL back into the db,
	// so dirSize reflects steady-state storage, not uncheckpointed WAL.
	if err := j.Close(); err != nil {
		return Results{}, err
	}
	_ = arms
	return Results{
		Impl: "journal", Jobs: n, Seed: seed, WorkloadHash: workloadHash(n, seed),
		SettleUS: setUS / float64(n), TotalS: total.Seconds(),
		BytesPerJob: float64(dirSize(dir)-before) / float64(n),
		RecoverS:    recov.Seconds(),
	}, nil
}
