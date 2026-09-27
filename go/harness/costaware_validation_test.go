package harness

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// Independent validation battery (Phase 2/5). Frozen seeds, no tuning.
// Each scenario reports metrics; assertions target exposure of failure
// (gates fire, wins refused), never suppression.

func validationClock(i int) time.Time {
	return time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute)
}

func openV3T3(t *testing.T, dir string) *CostAwareTreatment {
	t.Helper()
	pol := thompson.NewDefault("cheap", "strong")
	cfg := thompson.CostAwareConfig{QualityFloor: 0.5, MinMeteredN: 2, ColdStartPulls: 5, Epsilon: 1e-3}
	tr, err := OpenCostAwareTreatment(dir, "t3", pol, cfg)
	if err != nil {
		t.Fatal(err)
	}
	tr.Strategy.MaxRetries = 0
	return tr
}

// V1 quality deterioration: cheap reliable then collapses mid-run.
func TestValidationDeterioration(t *testing.T) {
	dir := t.TempDir()
	tr := openV3T3(t, dir+"/t3")
	defer tr.Close()
	rng := rand.New(rand.NewPCG(500, 500))
	n := 280
	cheapPicks := make([]int, n)
	for i := 0; i < n; i++ {
		cp := 0.90
		if i >= 80 {
			cp = 0.10
		}
		job := JobTruth{JobID: "d" + costAwarePad(i), Strata: "web", AssignedAt: validationClock(i),
			ArmSuccess: map[string]float64{"cheap": cp, "strong": 0.85},
			ArmCost:    map[string]float64{"cheap": 0.002, "strong": 0.05},
			ArmLatency: map[string]float64{"cheap": 100, "strong": 200}}
		nb := len(tr.Strategy.Decisions)
		if _, err := tr.RunJob(rng, job, Assignment{JobID: job.JobID, Strata: "web", Treatment: "t3", Probability: 1}); err != nil {
			t.Fatal(err)
		}
		for _, d := range tr.Strategy.Decisions[nb:] {
			if d.ArmID == "cheap" {
				cheapPicks[i] = 1
			}
		}
	}
	windows := [][2]int{{0, 80}, {80, 120}, {120, 160}, {160, 200}, {200, 240}, {240, 280}}
	counts := make([]int, len(windows))
	for w, bounds := range windows {
		for i := bounds[0]; i < bounds[1]; i++ {
			counts[w] += cheapPicks[i]
		}
	}
	t.Logf("deterioration windows pre80=%d post40=%d %d %d %d %d", counts[0], counts[1], counts[2], counts[3], counts[4], counts[5])
	if counts[0] < 60 {
		t.Fatalf("baseline commitment missing: pre-collapse cheap picks %d/80", counts[0])
	}
	// Eventual exclusion: once the posterior mean crosses the floor the
	// mean-gate must stop genuine-optimum picks of the collapsed arm.
	if counts[5] > 5 {
		t.Fatalf("no eventual exclusion 160 jobs after collapse: %d/40", counts[5])
	}
	// Detection delay is reported, not asserted away: posterior inertia is
	// shared with the cost-blind learner (documented limitation).
}

// V5 delayed + missing + unresolved: gates must fire, never rank silently.
func TestValidationDelayedMissingUnresolved(t *testing.T) {
	dir := t.TempDir()
	t1, _ := OpenTreatment(dir+"/t1", "t1", nil, false)
	defer t1.Close()
	t1strat := StaticStrategy{StrategyID: "t1", Arms: []string{"cheap"}, MaxRetries: 0, Verifier: "harness:synthetic-v1"}
	tr := openV3T3(t, dir+"/t3")
	defer tr.Close()
	assigner := Assigner{Seed: 606, Treatments: []string{"t1", "t3"}, Weights: []float64{1, 1}}
	rng := rand.New(rand.NewPCG(606, 606))
	n := 120
	for i := 0; i < n; i++ {
		jid := "u" + costAwarePad(i)
		asg, _ := assigner.Assign(jid, "web")
		job := JobTruth{JobID: jid, Strata: "web", AssignedAt: validationClock(i),
			ArmSuccess: map[string]float64{"cheap": 0.75, "strong": 0.75},
			ArmCost:    map[string]float64{"cheap": 0.002, "strong": 0.04},
			ArmLatency: map[string]float64{"cheap": 100, "strong": 200}}
		switch {
		case i%8 == 7:
			job.Unresolved = true
		case i%4 == 3:
			job.MissingCostArms = map[string]bool{"cheap": true, "strong": true}
		}
		if asg.Treatment == "t1" {
			if _, err := t1.RunJob(rng, t1strat, job, asg); err != nil {
				t.Fatal(err)
			}
		} else if _, err := tr.RunJob(rng, job, asg); err != nil {
			t.Fatal(err)
		}
	}
	now := validationClock(n)
	records := collectExperimentRecords(t, dir, []string{"t1", "t3"}, now)
	rep := Analyze(records, ReportConfig{Maturation: 0, Now: now,
		MinJobs: 5, CensorGate: 0.10, QualityFloor: 0.3,
		MinEffect: 0.05, BootstrapN: 100, BootstrapSeed: 606,
		MaxUnmeteredShare: 0.10}, "t1", "t3", nil, true, true)
	fired := map[string]bool{}
	for _, g := range rep.Gates {
		fired[g.Name] = true
	}
	t.Logf("verdict=%s gates=%v", rep.Verdict, fired)
	if !fired["censoring-t1"] && !fired["censoring-t3"] {
		t.Fatal("expected censoring gate to fire with 1/8 unresolved jobs")
	}
	if !fired["missing-cost-t1"] && !fired["missing-cost-t3"] {
		t.Fatal("expected missing-cost gate to fire with 1/4 unmetered jobs")
	}
	if rep.Verdict == "RANKABLE" {
		t.Fatal("must not rank under heavy censoring + missing costs")
	}
}

// V6 human-correction trap: cheap always needs human fix. Armless human-fixed
// jobs move neither estimator (inherited outcome semantics), so T3 cannot
// learn cheap is expensive — every pick stays flagged exploration. The honest
// outcomes: bounded report refusal + visible fallback burden, not silent wins.
func TestValidationHumanCorrectionTrap(t *testing.T) {
	dir := t.TempDir()
	tr := openV3T3(t, dir+"/t3")
	defer tr.Close()
	rng := rand.New(rand.NewPCG(707, 707))
	n := 60
	for i := 0; i < n; i++ {
		job := JobTruth{JobID: "h" + costAwarePad(i), Strata: "web", AssignedAt: validationClock(i),
			ArmSuccess:    map[string]float64{"cheap": 0.0, "strong": 0.0},
			ArmCost:       map[string]float64{"cheap": 0.001, "strong": 0.05},
			ArmLatency:    map[string]float64{"cheap": 100, "strong": 200},
			HumanFallback: true, HumanCost: 2.0}
		if _, err := tr.RunJob(rng, job, Assignment{JobID: job.JobID, Strata: "web", Treatment: "t3", Probability: 1}); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("human trap: fallbacks=%d/%d decisions", tr.Strategy.Fallbacks, len(tr.Strategy.Decisions))
	if tr.Strategy.Fallbacks < n/2 {
		t.Fatalf("human-fixed jobs must stay flagged exploration, got %d/%d", tr.Strategy.Fallbacks, n)
	}
	records := collectExperimentRecords(t, dir, []string{"t3"}, validationClock(n))
	rep := Analyze(records, ReportConfig{Maturation: 0, Now: validationClock(n),
		MinJobs: 5, CensorGate: 0.9, QualityFloor: 0.5,
		MinEffect: 0.05, BootstrapN: 100, BootstrapSeed: 707,
		MaxUnmeteredShare: 0.10}, "t3", "t3", nil, true, true)
	t.Logf("human trap verdict=%s", rep.Verdict)
}

// V7 Missing costs blind the learner but not the evaluator: cheap is good
// and cheap, yet 90% unmetered, so T3 cannot deem its cost known and rides
// strong long after T1 (cheap-fixed) banks the savings. T3 demonstrably
// loses to the competent static; the report must expose it (diff>0, no wins).
// T3 still beats T0 by eventually learning cheap — adaptation visible inside
// the loss.
func TestValidationMissingBlindsLearner(t *testing.T) {
	dir := t.TempDir()
	t0, _ := OpenTreatment(dir+"/t0", "t0", nil, false)
	defer t0.Close()
	t1, _ := OpenTreatment(dir+"/t1", "t1", nil, false)
	defer t1.Close()
	s0 := StaticStrategy{StrategyID: "t0", Arms: []string{"strong"}, MaxRetries: 0, Verifier: "harness:synthetic-v1"}
	s1 := StaticStrategy{StrategyID: "t1", Arms: []string{"cheap"}, MaxRetries: 0, Verifier: "harness:synthetic-v1"}
	pol := thompson.NewDefault("cheap", "strong")
	cfg := thompson.CostAwareConfig{QualityFloor: 0.3, MinMeteredN: 2, ColdStartPulls: 5, Epsilon: 1e-3}
	tr, err := OpenCostAwareTreatment(dir+"/t3", "t3", pol, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	tr.Strategy.MaxRetries = 0
	assigner := Assigner{Seed: 808, Treatments: []string{"t0", "t1", "t3"}, Weights: []float64{1, 1, 1}}
	rng := rand.New(rand.NewPCG(808, 808))
	n := 240
	for i := 0; i < n; i++ {
		jid := "w" + costAwarePad(i)
		asg, _ := assigner.Assign(jid, "web")
		job := JobTruth{JobID: jid, Strata: "web", AssignedAt: validationClock(i),
			ArmSuccess: map[string]float64{"cheap": 0.9, "strong": 0.9},
			ArmCost:    map[string]float64{"cheap": 0.002, "strong": 0.05},
			ArmLatency: map[string]float64{"cheap": 100, "strong": 200}}
		if (i*7)%10 < 9 {
			job.MissingCostArms = map[string]bool{"cheap": true}
		}
		switch asg.Treatment {
		case "t0":
			if _, err := t0.RunJob(rng, s0, job, asg); err != nil {
				t.Fatal(err)
			}
		case "t1":
			if _, err := t1.RunJob(rng, s1, job, asg); err != nil {
				t.Fatal(err)
			}
		default:
			if _, err := tr.RunJob(rng, job, asg); err != nil {
				t.Fatal(err)
			}
		}
	}
	now := validationClock(n)
	records := collectExperimentRecords(t, dir, []string{"t0", "t1", "t3"}, now)
	rep := Analyze(records, ReportConfig{Maturation: 0, Now: now,
		MinJobs: 10, CensorGate: 0.9, QualityFloor: 0.3,
		MinEffect: 0.05, BootstrapN: 200, BootstrapSeed: 808,
		MaxUnmeteredShare: -1}, "t0", "t3", []string{"t1"}, true, true)
	for _, c := range rep.Comparisons {
		t.Logf("comparison %s diff=%.4f wins=%v", c.Pair, c.Diff, c.Wins)
		if c.Wins {
			t.Fatalf("report hid T3 regression: %+v", c)
		}
	}
	// Direct primary-metric comparison (metered spend per verified success).
	cps := map[string]float64{}
	for _, tx := range []string{"t0", "t1", "t3"} {
		spend, succ := 0.0, 0
		for _, r := range records {
			if r.Treatment != tx || !r.Matured || !r.FullyMetered {
				continue
			}
			spend += r.CostMetered
			if r.Accepted {
				succ++
			}
		}
		if succ > 0 {
			cps[tx] = spend / float64(succ)
		}
		t.Logf("primary %s cost-per-verified-success=%.4f", tx, cps[tx])
	}
	if !(cps["t3"] > cps["t1"]) {
		t.Fatalf("scenario failed to make T3 lose to competent static: %v", cps)
	}
	t.Logf("drift verdict=%s", rep.Verdict)
}
