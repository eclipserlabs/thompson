package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/harness"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

func main() {
	if len(os.Args) < 2 {
		fatal("usage: exp-run <gen|run|all> [flags]")
	}
	var err error
	switch os.Args[1] {
	case "gen":
		err = genCmd(os.Args[2:])
	case "run":
		err = runCmd(os.Args[2:])
	case "all":
		err = allCmd(os.Args[2:])
	default:
		fatal("unknown subcommand %q (gen|run|all)", os.Args[1])
	}
	if err != nil {
		fatal("%v", err)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "exp-run: "+format+"\n", args...)
	os.Exit(1)
}

func parsePorts(s string) ([]int, error) {
	var out []int
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(p, "%d", &n); err != nil || n <= 0 {
			return nil, fmt.Errorf("bad port %q", p)
		}
		out = append(out, n)
	}
	return out, nil
}

func parseTimeFlag(s string) (time.Time, error) {
	if s == "" {
		return time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// genCmd builds a deterministic synthetic manifest. Every number derives
// from the seed; the manifest is the versioned artifact (SYNTHETIC labeled).
func genCmd(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	seed := fs.Uint64("seed", 20260105, "generator seed")
	n := fs.Int("n", 300, "jobs")
	out := fs.String("out", "manifest.json", "output manifest path")
	t0 := fs.String("t0", "2026-01-05T00:00:00Z", "sim clock start RFC3339")
	step := fs.Int("step-seconds", 60, "sim seconds per job")
	if err := fs.Parse(args); err != nil {
		return err
	}
	t0t, err := parseTimeFlag(*t0)
	if err != nil {
		return err
	}
	_ = t0t
	_ = step
	m := generateManifest(*seed, *n)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s: %d jobs experiment=%s version=%s synthetic=%v\n",
		*out, len(m.Jobs), m.ExperimentID, m.WorkloadVersion, m.Synthetic)
	return nil
}

func f64p(v float64) *float64 { return &v }

// generateManifest is deterministic in seed: same seed, same manifest.
func generateManifest(seed uint64, n int) *Manifest {
	rng := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
	behaviors := []JobBehavior{
		BehaviorNormal, BehaviorNormal, BehaviorNormal, BehaviorNormal, BehaviorNormal, BehaviorNormal,
		BehaviorNormal, BehaviorNormal, BehaviorNormal, BehaviorNormal, BehaviorNormal,
		BehaviorInvalidOutput, BehaviorInvalidOutput,
		BehaviorTimeoutThenAccept,
		BehaviorDelayedAccept, BehaviorDelayedAccept,
		BehaviorCorrectToReject,
		BehaviorUnknownThenAccept,
		BehaviorUnresolved, BehaviorUnresolved,
	}
	m := &Manifest{
		ExperimentID: fmt.Sprintf("synth-%d", seed),
		WorkloadName: "synthetic-dry-run",
		Seed:         seed,
		MaturationH:  24,
		Synthetic:    true,
		Treatments: []TreatmentConfig{
			{ID: "t0", Description: "fixed single-arm static", Arms: []string{"fixed"}, MaxAttempts: 1},
			{ID: "t1", Description: "cheapest qualified static + retries", Arms: []string{"cheap"}, MaxAttempts: 3},
			{ID: "t2", Description: "cost-blind Thompson adaptive", Arms: []string{"cheap", "strong"}, MaxAttempts: 3, Learn: true},
		},
	}
	strata := []string{"web", "api", "batch"}
	for i := 0; i < n; i++ {
		jid := fmt.Sprintf("job-%05d", i)
		cheapP := 0.55 + rng.Float64()*0.40
		strongP := 0.35 + rng.Float64()*0.40
		fixedP := 0.60 + rng.Float64()*0.20
		cost := func(base, spread float64) *float64 {
			if rng.Float64() < 0.10 {
				return nil // missing cost stays unknown
			}
			return f64p(base + rng.Float64()*spread)
		}
		job := ManifestJob{
			JobID:  jid,
			Strata: strata[i%len(strata)],
			Arms: map[string]ArmTruth{
				"fixed":  {SuccessP: fixedP, CostUSD: cost(0.010, 0.005), LatencyMs: 400 + rng.Float64()*200},
				"cheap":  {SuccessP: cheapP, CostUSD: cost(0.001, 0.002), LatencyMs: 150 + rng.Float64()*150},
				"strong": {SuccessP: strongP, CostUSD: cost(0.020, 0.030), LatencyMs: 700 + rng.Float64()*500},
			},
			Behavior: behaviors[int(rng.Uint64()%uint64(len(behaviors)))],
		}
		if rng.Float64() < 0.15 {
			job.Human = HumanTruth{Enabled: true, CostUSD: 1.5 + rng.Float64()*1.5, LatencyMs: 600000, AlwaysSucceed: true}
		}
		if job.Behavior == BehaviorDelayedAccept || job.Behavior == BehaviorTimeoutThenAccept ||
			job.Behavior == BehaviorCorrectToReject || job.Behavior == BehaviorUnknownThenAccept {
			job.VerifyLagH = 20
		}
		m.Jobs = append(m.Jobs, job)
	}
	sum, err := m.contentHash()
	if err != nil {
		panic(err)
	}
	m.WorkloadVersion = sum
	return m
}

type runFlags struct {
	manifest    string
	root        string
	routerBin   string
	pubPorts    string
	settlePorts string
	token       string
	timeout     time.Duration
	t0          string
	step        int
	crashAfter  int
	selSeed     uint64
}

func runFlagSet(name string) (*flag.FlagSet, *runFlags) {
	f := &runFlags{}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.StringVar(&f.manifest, "manifest", "", "workload manifest path")
	fs.StringVar(&f.root, "root", "./exp", "experiment root dir")
	fs.StringVar(&f.routerBin, "router-bin", "", "built router binary path")
	fs.StringVar(&f.pubPorts, "pub-ports", "18081,18082,18083", "public ports per treatment")
	fs.StringVar(&f.settlePorts, "settle-ports", "18091,18092,18093", "settle ports per treatment")
	fs.StringVar(&f.token, "token", "exp-settle-token", "settlement bearer token")
	fs.DurationVar(&f.timeout, "timeout", 60*time.Second, "gateway boot + request ceiling")
	fs.StringVar(&f.t0, "t0", "2026-01-05T00:00:00Z", "sim clock start RFC3339")
	fs.IntVar(&f.step, "step-seconds", 60, "sim seconds per job")
	fs.IntVar(&f.crashAfter, "crash-after", 0, "exit(3) after N jobs (resume proof only)")
	fs.Uint64Var(&f.selSeed, "selection-seed", 0, "fixed gateway selection seed base (0 = time-seeded; required for reproducible dry runs)")
	return fs, f
}

// runCmd executes (or resumes) the manifest against gateway binaries.
func runCmd(args []string) error {
	fs, f := runFlagSet("run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return execute(f)
}

func execute(f *runFlags) error {
	m, err := LoadManifest(f.manifest)
	if err != nil {
		return err
	}
	pub, err := parsePorts(f.pubPorts)
	if err != nil {
		return err
	}
	settle, err := parsePorts(f.settlePorts)
	if err != nil {
		return err
	}
	t0, err := parseTimeFlag(f.t0)
	if err != nil {
		return err
	}
	r, err := OpenRunner(RunnerConfig{
		Manifest: m, Root: f.root, RouterBin: f.routerBin,
		PubPorts: pub, SettlePorts: settle, Token: f.token,
		Timeout: f.timeout, T0Clock: t0, Step: time.Duration(f.step) * time.Second,
		CrashAfter: f.crashAfter, SelectionSeed: f.selSeed,
	})
	if err != nil {
		return err
	}
	if err := r.Boot(); err != nil {
		return err
	}
	defer r.Shutdown()
	if err := r.Run(context.Background()); err != nil {
		return err
	}
	fmt.Printf("experiment %s complete: %d jobs\n", m.ExperimentID, len(m.Jobs))
	return nil
}

// allCmd is the one-command dry run: generate → execute → report.
func allCmd(args []string) error {
	fs, f := runFlagSet("all")
	seed := fs.Uint64("seed", 20260105, "manifest generator seed")
	n := fs.Int("n", 300, "synthetic jobs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if f.manifest == "" {
		f.manifest = f.root + "/manifest.json"
	}
	m := generateManifest(*seed, *n)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(f.root, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(f.manifest, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := execute(f); err != nil {
		return err
	}
	mf := mustManifest(f.manifest)
	txNames := []string{}
	for _, t := range mf.Treatments {
		txNames = append(txNames, t.ID)
	}
	// Analysis clock: last assignment + maturation + 1h margin, so the dry
	// run always analyzes matured data deterministically.
	lastAssigned := lastAssignmentTime(f)
	now := lastAssigned.Add(25 * time.Hour)
	return writeReportFile(f, txNames, now, 24*time.Hour)
}

func mustManifest(path string) *Manifest {
	m, err := LoadManifest(path)
	if err != nil {
		panic(err)
	}
	return m
}

func lastAssignmentTime(f *runFlags) time.Time {
	m := mustManifest(f.manifest)
	t0, _ := parseTimeFlag(f.t0)
	last := t0
	for i := range m.Jobs {
		if at := SimClock(t0, time.Duration(f.step)*time.Second, i); at.After(last) {
			last = at
		}
	}
	return last
}

// writeReportFile renders the offline report from ledgers via the shared
// harness API (the same code path as the exp-report CLI) and writes
// report.json + report.txt. All generated results are synthetic.
func writeReportFile(f *runFlags, txNames []string, now time.Time, maturation time.Duration) error {
	m, err := LoadManifest(f.manifest)
	if err != nil {
		return err
	}
	var allAssign []harness.Assignment
	var allEvents []outcome.OutcomeEvent
	for _, t := range m.Treatments {
		as, evs, err := harness.LoadTreatmentDir(f.root + "/" + t.ID)
		if err != nil {
			return fmt.Errorf("treatment %s: %w", t.ID, err)
		}
		allAssign = append(allAssign, as...)
		allEvents = append(allEvents, evs...)
	}
	others := []string{}
	for _, n := range txNames {
		if n != "t0" && n != "t2" {
			others = append(others, n)
		}
	}
	cfg := harness.ReportConfig{
		Maturation: maturation, Now: now,
		MinJobs: 5, CensorGate: 0.5, QualityFloor: 0.3, MinEffect: 0.15,
		BootstrapN: 500, BootstrapSeed: m.Seed,
	}
	jobMaps := map[string]harness.JobMap{}
	for _, t := range m.Treatments {
		jm, err := harness.LoadJobMap(f.root + "/" + t.ID)
		if err != nil {
			return err
		}
		jobMaps[t.ID] = jm
	}
	rep, err := harness.BuildReport(allAssign, allEvents, txNames, "t0", "t2", others, cfg, jobMaps)
	if err != nil {
		return err
	}
	jb, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	jb = append(jb, '\n')
	if err := os.WriteFile(f.root+"/report.json", jb, 0o644); err != nil {
		return err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "SYNTHETIC dry-run report experiment=%s workload=%s\n", m.ExperimentID, m.WorkloadVersion)
	fmt.Fprintf(&sb, "verdict=%s\n", rep.Verdict)
	for _, r := range rep.Reasons {
		fmt.Fprintf(&sb, "  - %s\n", r)
	}
	for _, c := range rep.Comparisons {
		fmt.Fprintf(&sb, "%s diff=%+.4f rel=%.3f ci95=[%+.4f,%+.4f] wins=%v bar=%v\n",
			c.Pair, c.Diff, c.RelImprovement, c.CILow, c.CIHigh, c.Wins, c.MeetsBar)
	}
	if err := os.WriteFile(f.root+"/report.txt", []byte(sb.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("verdict=%s (report.json + report.txt in %s)\n", rep.Verdict, f.root)
	return nil
}
