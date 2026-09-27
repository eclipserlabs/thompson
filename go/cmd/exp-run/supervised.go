package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
)

// supervisedSafetyDefaults freezes the t3 safety envelope for supervised
// synthetic experiments. Frozen per run and persisted as an artifact; the
// binary refuses to start t3 without it.
func supervisedSafetyFor(arms []string, fallback string) gateway.SafetyConfig {
	// Budget sizing (runbook formula): cold exit needs ColdStartPulls pulls
	// per arm, but PENDING/UNKNOWN/timeout jobs consume decisions without
	// producing pulls, and the all-unknown opening phase is pure fallback.
	// Budget covers cold (5) + opening exploration + non-learning margin for
	// a 60-job experiment (t3 share ~15 jobs x up to 3 attempts). Smaller
	// budgets fail closed mid-experiment (safe, but abort the run); larger
	// budgets defer the backstop. The runner surfaces exhaustion as a loud
	// refusal, never a phantom settlement.
	return gateway.SafetyConfig{
		Workload: "supervised-synth-v1", Arms: arms, FallbackArm: fallback,
		QualityFloor: 0.5, MonitorWindow: 20, MonitorMinObs: 8,
		MaxExplorationPerArm: 30, MaxMissingShare: 0.2, ColdStartPulls: 5,
	}
}

// generateSupervisedManifest builds the versioned 4-treatment synthetic
// workload. Truth varies deterministically by job third (healthy,
// deterioration, cost drift) so one frozen manifest exercises quality
// collapse, cost regime change, and steady operation. No customer data.
func generateSupervisedManifest(seed uint64, n int) *Manifest {
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
		ExperimentID: fmt.Sprintf("supervised-%d", seed),
		WorkloadName: "supervised-synthetic-v1",
		Seed:         seed,
		MaturationH:  24,
		Synthetic:    true,
		Treatments: []TreatmentConfig{
			{ID: "t0", Description: "fixed single-arm static", Arms: []string{"fixed"}, MaxAttempts: 1},
			{ID: "t1", Description: "cheapest qualified static + retries", Arms: []string{"cheap"}, MaxAttempts: 3},
			{ID: "t2", Description: "cost-blind Thompson adaptive", Arms: []string{"cheap", "strong"}, MaxAttempts: 3, Learn: true},
			{ID: "t3", Description: "supervised cost-aware Thompson", Arms: []string{"cheap", "strong"}, MaxAttempts: 3, Learn: true, CostAware: true},
		},
	}
	strata := []string{"web", "api", "batch"}
	for i := 0; i < n; i++ {
		jid := fmt.Sprintf("job-%05d", i)
		third := i * 3 / n // 0 healthy, 1 deterioration, 2 cost drift
		cheapP, cheapC := 0.55+rng.Float64()*0.40, 0.001+rng.Float64()*0.002
		strongP, strongC := 0.55+rng.Float64()*0.40, 0.020+rng.Float64()*0.030
		fixedP := 0.60 + rng.Float64()*0.20
		if third == 1 {
			cheapP = 0.05 + rng.Float64()*0.15 // quality collapse
		}
		if third == 2 {
			cheapC = 0.060 + rng.Float64()*0.030 // cost drift above strong
		}
		cost := func(v float64) *float64 {
			if rng.Float64() < 0.10 {
				return nil // missing cost stays unknown
			}
			return f64p(v)
		}
		job := ManifestJob{
			JobID:  jid,
			Strata: strata[i%len(strata)],
			Arms: map[string]ArmTruth{
				"fixed":  {SuccessP: fixedP, CostUSD: cost(0.010 + rng.Float64()*0.005), LatencyMs: 400 + rng.Float64()*200},
				"cheap":  {SuccessP: cheapP, CostUSD: cost(cheapC), LatencyMs: 150 + rng.Float64()*150},
				"strong": {SuccessP: strongP, CostUSD: cost(strongC), LatencyMs: 700 + rng.Float64()*500},
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

// supervisedCmd runs the four-treatment supervised experiment through real
// gateway binaries: generate → execute (with emergency-stop demo) → report.
func supervisedCmd(args []string) error {
	fs, f := runFlagSet("supervised")
	seed := fs.Uint64("seed", 20260105, "manifest generator seed")
	n := fs.Int("n", 120, "synthetic jobs")
	demoStop := fs.Bool("demo-stop", true, "demonstrate emergency stop + release on t3 mid-run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if f.manifest == "" {
		f.manifest = f.root + "/manifest.json"
	}
	m := generateSupervisedManifest(*seed, *n)
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
	return executeSupervised(f, m, *demoStop)
}

func executeSupervised(f *runFlags, m *Manifest, demoStop bool) error {
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
	safetyCfg := supervisedSafetyFor([]string{"cheap", "strong"}, "strong")
	scb, err := json.MarshalIndent(safetyCfg, "", "  ")
	if err != nil {
		return err
	}
	safetyConfigs := map[string][]byte{}
	for _, t := range m.Treatments {
		if t.CostAware {
			safetyConfigs[t.ID] = scb
		}
	}
	opToken := f.opToken
	if opToken == "" {
		return fmt.Errorf("exp-run: supervised requires --operator-token")
	}
	r, err := OpenRunner(RunnerConfig{
		Manifest: m, Root: f.root, RouterBin: f.routerBin,
		PubPorts: pub, SettlePorts: settle, Token: f.token,
		Timeout: f.timeout, T0Clock: t0, Step: time.Duration(f.step) * time.Second,
		CrashAfter: f.crashAfter, SelectionSeed: f.selSeed,
		SafetyConfigs: safetyConfigs, OperatorToken: opToken,
	})
	if err != nil {
		return err
	}
	if demoStop {
		stopAt := len(m.Jobs) * 85 / 100
		releaseAt := stopAt + 5
		r.cfg.AfterJob = func(idx, doneJobs int) error {
			g := r.gateways["t3"]
			if g == nil {
				return nil
			}
			switch {
			case doneJobs == stopAt:
				code, err := g.Operator("suspend", "", "supervised demo: emergency stop", opToken, "demo-operator")
				if err != nil || code != 200 {
					return fmt.Errorf("exp-run: demo stop failed: %v code=%d", err, code)
				}
			case doneJobs == releaseAt:
				code, err := g.Operator("resume", "", "supervised demo: release", opToken, "demo-operator")
				if err != nil || code != 200 {
					return fmt.Errorf("exp-run: demo release failed: %v code=%d", err, code)
				}
			}
			return nil
		}
	}
	if err := r.Boot(); err != nil {
		return err
	}
	defer r.Shutdown()
	if err := r.Run(context.Background()); err != nil {
		return err
	}
	fmt.Printf("supervised experiment %s complete: %d jobs\n", m.ExperimentID, len(m.Jobs))
	mf := mustManifest(f.manifest)
	txNames := []string{}
	for _, t := range mf.Treatments {
		txNames = append(txNames, t.ID)
	}
	lastAssigned := lastAssignmentTime(f)
	now := lastAssigned.Add(25 * time.Hour)
	return writeReportFile(f, txNames, now, 24*time.Hour)
}
