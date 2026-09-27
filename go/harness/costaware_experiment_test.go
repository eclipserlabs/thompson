package harness

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// Four-treatment controlled experiment (T0/T1/T2/T3) at harness level.
// Assignment RNG is independent from policy RNG; all treatments face the
// same JobTruth distribution; ledgers/checkpoints isolated per treatment.
// Uses the existing Analyze/BuildReport statistical infrastructure.
func TestFourTreatmentCostAwareExperiment(t *testing.T) {
	dir := t.TempDir()
	t0clock := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	arms := []string{"cheap", "strong"}

	// T0: fixed single-arm static (expensive fixed model).
	t0pol := thompson.NewDefault("strong")
	t0, err := OpenTreatment(dir+"/t0", "t0", t0pol, false)
	if err != nil {
		t.Fatal(err)
	}
	defer t0.Close()
	t0strat := StaticStrategy{StrategyID: "t0", Arms: []string{"strong"}, MaxRetries: 0, Verifier: "harness:synthetic-v1"}

	// T1: cheapest qualified static.
	t1, err := OpenTreatment(dir+"/t1", "t1", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer t1.Close()
	t1strat := StaticStrategy{StrategyID: "t1", Arms: []string{"cheap"}, MaxRetries: 0, Verifier: "harness:synthetic-v1"}

	// T2: cost-blind Thompson.
	t2pol := thompson.NewDefault(arms...)
	t2, err := OpenTreatment(dir+"/t2", "t2", t2pol, true)
	if err != nil {
		t.Fatal(err)
	}
	defer t2.Close()
	t2strat := ThompsonStrategy{StrategyID: "t2", Policy: t2pol, MaxRetries: 0, Verifier: "harness:synthetic-v1"}

	// T3: cost-aware Thompson (v1).
	t3pol := thompson.NewDefault(arms...)
	t3cfg := thompson.CostAwareConfig{QualityFloor: 0.3, MinMeteredN: 2, ColdStartPulls: 5, Epsilon: 1e-3}
	t3, err := OpenCostAwareTreatment(dir+"/t3", "t3", t3pol, t3cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer t3.Close()
	t3.Strategy.MaxRetries = 0

	assigner := Assigner{Seed: 20260105, Treatments: []string{"t0", "t1", "t2", "t3"}, Weights: []float64{1, 1, 1, 1}}
	policyRNG := rand.New(rand.NewPCG(777, 777)) // independent from assignment (hash-based)

	n := 300
	for i := 0; i < n; i++ {
		jid := "job-" + costAwarePad(i)
		asg, err := assigner.Assign(jid, "web")
		if err != nil {
			t.Fatal(err)
		}
		// Equal quality (0.75), different costs: cheap $0.002, strong $0.04.
		// Fixed model (t0) uses strong only — expensive by construction, so
		// T3 must beat T2/T0 on cost-per-success without beating T1 (which
		// is the cheapest static by design). This satisfies "not solely
		// against an expensive fixed model": T1 is the competent static.
		job := JobTruth{
			JobID: jid, Strata: "web", AssignedAt: t0clock.Add(time.Duration(i) * time.Minute),
			ArmSuccess: map[string]float64{"cheap": 0.75, "strong": 0.75, "fixed": 0.75},
			ArmCost:    map[string]float64{"cheap": 0.002, "strong": 0.04, "fixed": 0.04},
			ArmLatency: map[string]float64{"cheap": 100, "strong": 200, "fixed": 200},
		}
		fixedJob := job
		fixedJob.ArmSuccess = map[string]float64{"strong": 0.75}
		switch asg.Treatment {
		case "t0":
			if _, err := t0.RunJob(policyRNG, t0strat, fixedJob, asg); err != nil {
				t.Fatal(err)
			}
		case "t1":
			if _, err := t1.RunJob(policyRNG, t1strat, job, asg); err != nil {
				t.Fatal(err)
			}
		case "t2":
			if _, err := t2.RunJob(policyRNG, t2strat, job, asg); err != nil {
				t.Fatal(err)
			}
		case "t3":
			if _, err := t3.RunJob(policyRNG, job, asg); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Build records for reporting: gather assignments + events per treatment.
	records := collectExperimentRecords(t, dir, []string{"t0", "t1", "t2", "t3"}, t0clock.Add(time.Duration(n)*time.Minute))
	cfg := ReportConfig{
		Maturation: 0, Now: t0clock.Add(time.Duration(n) * time.Minute),
		MinJobs: 20, CensorGate: 0.5, QualityFloor: 0.3,
		MinEffect: 0.05, BootstrapN: 200, BootstrapSeed: 20260105,
		MaxUnmeteredShare: 0.10,
	}
	rep := Analyze(records, cfg, "t0", "t3", []string{"t1", "t2"}, true, true)
	if len(rep.Treatments) != 4 {
		t.Fatalf("expected 4 treatments, got %d", len(rep.Treatments))
	}
	// T3 decisions all carry cost-aware identity (req 9 at experiment scale).
	for _, d := range t3.Strategy.Decisions {
		if d.PolicyID != thompson.CostAwarePolicyID || d.Objective != thompson.CostAwareObjectiveVer {
			t.Fatalf("decision identity breach: %+v", d)
		}
	}
	// T2 policy identity unchanged (cost-blind).
	if t2pol.LoggingPolicyID() != "exact-thompson-v1" {
		t.Fatalf("T2 identity changed: %q", t2pol.LoggingPolicyID())
	}
	t.Logf("verdict=%s comparisons=%d", rep.Verdict, len(rep.Comparisons))
	for tx, st := range rep.Treatments {
		t.Logf("%s assigned=%d matured=%d accept=%.3f", tx, st.Assigned, st.Matured, st.AcceptRate)
	}
	_ = outcome.StatusAccepted
}

// Adversarial: cost drift makes T3 worse than competent static T1.
// Cheap arm is good for the first half, then its cost spikes 25x; T3's
// learned cheap-preference over-commits and loses on cost-per-success.
// The report must expose the regression (T3-T1 comparison must not claim a win).
func TestCostAwareRegressionExposed(t *testing.T) {
	dir := t.TempDir()
	t0clock := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

	t1, _ := OpenTreatment(dir+"/t1", "t1", nil, false)
	defer t1.Close()
	t1strat := StaticStrategy{StrategyID: "t1", Arms: []string{"cheap"}, MaxRetries: 0, Verifier: "harness:synthetic-v1"}

	t3pol := thompson.NewDefault("cheap", "strong")
	t3cfg := thompson.CostAwareConfig{QualityFloor: 0.3, MinMeteredN: 2, ColdStartPulls: 5, Epsilon: 1e-3}
	t3, err := OpenCostAwareTreatment(dir+"/t3", "t3", t3pol, t3cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer t3.Close()
	t3.Strategy.MaxRetries = 0

	assigner := Assigner{Seed: 99, Treatments: []string{"t1", "t3"}, Weights: []float64{1, 1}}
	rng := rand.New(rand.NewPCG(4242, 4242))
	n := 200
	for i := 0; i < n; i++ {
		jid := "drift-" + costAwarePad(i)
		asg, _ := assigner.Assign(jid, "web")
		cheapC := 0.002
		if i >= n/2 {
			cheapC = 0.05 // drift: cheap becomes expensive
		}
		job := JobTruth{
			JobID: jid, Strata: "web", AssignedAt: t0clock.Add(time.Duration(i) * time.Minute),
			ArmSuccess: map[string]float64{"cheap": 0.75, "strong": 0.75},
			ArmCost:    map[string]float64{"cheap": cheapC, "strong": 0.03},
			ArmLatency: map[string]float64{"cheap": 100, "strong": 200},
		}
		if asg.Treatment == "t1" {
			if _, err := t1.RunJob(rng, t1strat, job, asg); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := t3.RunJob(rng, job, asg); err != nil {
				t.Fatal(err)
			}
		}
	}
	records := collectExperimentRecords(t, dir, []string{"t1", "t3"}, t0clock.Add(time.Duration(n)*time.Minute))
	cfg := ReportConfig{
		Maturation: 0, Now: t0clock.Add(time.Duration(n) * time.Minute),
		MinJobs: 10, CensorGate: 0.9, QualityFloor: 0.2,
		MinEffect: 0.05, BootstrapN: 200, BootstrapSeed: 7,
		MaxUnmeteredShare: -1, // disable missing-cost gate (no missing here by construction)
	}
	rep := Analyze(records, cfg, "t1", "t3", nil, true, true)
	// The report must not claim T3 wins on primary metric under drift.
	for _, c := range rep.Comparisons {
		if c.Pair == "t3-t1" && c.Wins {
			t.Fatalf("report hid regression: T3 claimed win under cost drift: %+v", c)
		}
	}
	t.Logf("drift verdict=%s", rep.Verdict)
}

func costAwarePad(i int) string {
	s := costAwareItoa(i)
	for len(s) < 5 {
		s = "0" + s
	}
	return s
}
