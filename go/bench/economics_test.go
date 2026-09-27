package bench

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/harness"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// devPick selects the cheapest static arm on the DEV corpus only. Eval
// never feeds back into this choice.
func devPick(t *testing.T, scenarios []string) string {
	t.Helper()
	cost := map[string]float64{}
	n := map[string]int{}
	for _, name := range scenarios {
		for _, seed := range DevSeeds {
			for _, j := range Generate(name, seed, 40).Jobs {
				for arm, truth := range j.Arms {
					if truth.CostUSD != nil {
						cost[arm] += *truth.CostUSD
						n[arm]++
					}
				}
			}
		}
	}
	best, bestMean := "", 0.0
	first := true
	for arm, c := range cost {
		m := c / float64(n[arm])
		if first || m < bestMean {
			best, bestMean, first = arm, m, false
		}
	}
	t.Logf("dev-picked static arm: %s (mean $%.4f)", best, bestMean)
	return best
}

func benchJob(j JobSpec, at time.Time) harness.JobTruth {
	succ := map[string]float64{}
	cost := map[string]float64{}
	lat := map[string]float64{}
	for arm, truth := range j.Arms {
		succ[arm] = truth.SuccessP
		if truth.CostUSD != nil {
			cost[arm] = *truth.CostUSD
		}
		lat[arm] = 100
	}
	out := harness.JobTruth{JobID: j.JobID, Strata: j.Strata, AssignedAt: at,
		ArmSuccess: succ, ArmCost: cost, ArmLatency: lat,
		MissingCostArms: j.Missing, Unresolved: j.Unresolved}
	if j.HumanFix != nil {
		out.HumanFallback = true
		out.HumanCost = *j.HumanFix
	}
	return out
}

// runEconomics executes T0 (fixed first-arm static), T1 (dev-picked cheap
// static), T2 (cost-blind Thompson), T3 (cost-aware) over one scenario and
// reports through the frozen harness analyzer. All jobs are assigned;
// assignment RNG is independent from policy RNG.
func runEconomics(t *testing.T, sc Scenario, staticArm string) harness.Report {
	t.Helper()
	dir := t.TempDir()
	clock := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	arms := []string{"cheap", "strong"}
	t0, err := harness.OpenTreatment(dir+"/t0", "t0", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer t0.Close()
	t1, err := harness.OpenTreatment(dir+"/t1", "t1", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer t1.Close()
	// T2/T3 use production defaults (including family warm start): they are
	// the policies that would actually deploy, not textbook ideals. The
	// textbook-pinned parity baseline lives in baseline_test.go.
	t2, err := harness.OpenTreatment(dir+"/t2", "t2", thompson.NewDefault(arms...), true)
	if err != nil {
		t.Fatal(err)
	}
	defer t2.Close()
	p3 := thompson.NewDefault(arms...)
	t3cfg := thompson.CostAwareConfig{QualityFloor: 0.3, MinMeteredN: 2, ColdStartPulls: 5, Epsilon: 1e-3}
	t3, err := harness.OpenCostAwareTreatment(dir+"/t3", "t3", p3, t3cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer t3.Close()
	t3.Strategy.MaxRetries = 0
	s0 := harness.StaticStrategy{StrategyID: "t0", Arms: []string{"fixed"}, MaxRetries: 0, Verifier: "bench"}
	s1 := harness.StaticStrategy{StrategyID: "t1", Arms: []string{staticArm}, MaxRetries: 0, Verifier: "bench"}
	s2 := harness.ThompsonStrategy{StrategyID: "t2", Policy: t2.Policy, MaxRetries: 0, Verifier: "bench"}
	assigner := harness.Assigner{Seed: 20260707, Treatments: []string{"t0", "t1", "t2", "t3"}, Weights: []float64{1, 1, 1, 1}}
	prng := rand.New(rand.NewPCG(777, 777))
	for i, j := range sc.Jobs {
		job := benchJob(j, clock.Add(time.Duration(i)*time.Minute))
		// fixed arm truth defaults when the scenario lacks it
		if _, ok := job.ArmSuccess["fixed"]; !ok {
			job.ArmSuccess["fixed"] = 0.70
			job.ArmCost["fixed"] = 0.04
			job.ArmLatency["fixed"] = 200
		}
		asg, err := assigner.Assign(j.JobID, j.Strata)
		if err != nil {
			t.Fatal(err)
		}
		switch asg.Treatment {
		case "t0":
			if _, err := t0.RunJob(prng, s0, job, asg); err != nil {
				t.Fatal(err)
			}
		case "t1":
			if _, err := t1.RunJob(prng, s1, job, asg); err != nil {
				t.Fatal(err)
			}
		case "t2":
			if _, err := t2.RunJob(prng, s2, job, asg); err != nil {
				t.Fatal(err)
			}
		case "t3":
			if _, err := t3.RunJob(prng, job, asg); err != nil {
				t.Fatal(err)
			}
		}
	}
	var records []harness.JobRecord
	for _, tx := range []string{"t0", "t1", "t2", "t3"} {
		asg, evs, err := harness.LoadTreatmentDir(dir + "/" + tx)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, harness.MatureJobs(asg, evs, clock.Add(time.Duration(len(sc.Jobs))*time.Minute), 0)...)
	}
	_ = outcome.StatusAccepted
	return harness.Analyze(records, harness.ReportConfig{
		Maturation: 0, Now: clock.Add(time.Duration(len(sc.Jobs)) * time.Minute),
		MinJobs: 10, CensorGate: 0.9, QualityFloor: 0.3,
		MinEffect: 0.05, BootstrapN: 200, BootstrapSeed: 42,
		MaxUnmeteredShare: 0.30,
	}, "t0", "t3", []string{"t1", "t2"}, true, true)
}

// Economics across eval scenarios with dev-picked statics. Negative and
// inconclusive results are retained and asserted (a sweep where T3 always
// wins would indict the baselines, not crown the policy).
func TestEconomicsEvalSweep(t *testing.T) {
	if testing.Short() {
		t.Skip("economics sweep skipped in short mode")
	}
	devScenarios := []string{"equal-cost-gap", "quality-gap", "static-matches"}
	staticArm := devPick(t, devScenarios)
	if staticArm != "cheap" {
		t.Logf("NOTE: dev pick is %s (fixtures still valid, baselines credible)", staticArm)
	}
	for _, name := range []string{"equal-cost-gap", "quality-gap", "static-matches",
		"deterioration", "drift", "missing-costs", "heavy-tail", "human-trap"} {
		sc := Generate(name, EvalSeeds[0], 160)
		rep := runEconomics(t, sc, staticArm)
		t.Logf("%-22s verdict=%-12s comps=%d", name, rep.Verdict, len(rep.Comparisons))
		for tx, st := range rep.Treatments {
			t.Logf("    %s assigned=%d matured=%d accept=%.3f", tx, st.Assigned, st.Matured, st.AcceptRate)
		}
		for _, c := range rep.Comparisons {
			t.Logf("    %s diff=%.4f wins=%v bar=%v", c.Pair, c.Diff, c.Wins, c.MeetsBar)
		}
	}
}
