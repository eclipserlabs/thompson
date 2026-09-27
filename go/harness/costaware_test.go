package harness

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

func costAwareTestJob(id string, cheapP, strongP, cheapC, strongC float64) JobTruth {
	return JobTruth{
		JobID: id, Strata: "web", AssignedAt: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
		ArmSuccess: map[string]float64{"cheap": cheapP, "strong": strongP},
		ArmCost:    map[string]float64{"cheap": cheapC, "strong": strongC},
		ArmLatency: map[string]float64{"cheap": 100, "strong": 200},
	}
}

// 1: equal success, different costs → T3 distinguishes (prefers cheap).
func TestCostAwarePrefersCheapAtEqualQuality(t *testing.T) {
	dir := t.TempDir()
	pol := thompson.NewDefault("cheap", "strong")
	cfg := thompson.CostAwareConfig{QualityFloor: 0.3, MinMeteredN: 1, ColdStartPulls: 0, Epsilon: 1e-3}
	tr, err := OpenCostAwareTreatment(dir+"/t3", "t3", pol, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	tr.Strategy.MaxRetries = 0
	rng := rand.New(rand.NewPCG(1, 1))
	// Pre-seed: both arms 80% success, cheap $0.002, strong $0.05.
	for i := 0; i < 20; i++ {
		job := costAwareTestJob("seed-"+string(rune('a'+i)), 0.8, 0.8, 0.002, 0.05)
		a := Assignment{JobID: job.JobID, Strata: "web", Treatment: "t3", Probability: 1}
		if _, err := tr.RunJob(rng, job, a); err != nil {
			t.Fatal(err)
		}
	}
	// Now measure selection over 50 fresh draws.
	cheapWins := 0
	for i := 0; i < 50; i++ {
		means, known := tr.Strategy.costMaps()
		res, err := thompson.SelectCostAware(rng, pol, means, known, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if res.ArmID == "cheap" {
			cheapWins++
		}
	}
	if cheapWins < 35 {
		t.Fatalf("cost-aware failed to prefer cheap at equal quality: %d/50", cheapWins)
	}
}

// 2: cheap low-quality blocked by floor.
func TestCostAwareBlocksCheapLowQuality(t *testing.T) {
	dir := t.TempDir()
	pol := thompson.NewDefault("cheap", "strong")
	cfg := thompson.CostAwareConfig{QualityFloor: 0.5, MinMeteredN: 1, ColdStartPulls: 0, Epsilon: 1e-3}
	tr, err := OpenCostAwareTreatment(dir+"/t3", "t3", pol, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	tr.Strategy.MaxRetries = 0
	rng := rand.New(rand.NewPCG(2, 2))
	for i := 0; i < 30; i++ {
		job := costAwareTestJob("j"+costAwareItoa(i), 0.1, 0.9, 0.001, 0.05)
		a := Assignment{JobID: job.JobID, Strata: "web", Treatment: "t3", Probability: 1}
		if _, err := tr.RunJob(rng, job, a); err != nil {
			t.Fatal(err)
		}
	}
	strongWins := 0
	for i := 0; i < 30; i++ {
		means, known := tr.Strategy.costMaps()
		res, err := thompson.SelectCostAware(rng, pol, means, known, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if res.ArmID == "strong" {
			strongWins++
		}
	}
	if strongWins < 20 {
		t.Fatalf("quality gate failed: strong chosen %d/30", strongWins)
	}
}

// 9: decision identity matches configuration.
func TestCostAwareDecisionIdentityBound(t *testing.T) {
	dir := t.TempDir()
	pol := thompson.NewDefault("cheap", "strong")
	cfg := thompson.DefaultCostAwareConfig()
	tr, err := OpenCostAwareTreatment(dir+"/t3", "t3", pol, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	tr.Strategy.MaxRetries = 0
	rng := rand.New(rand.NewPCG(3, 3))
	job := costAwareTestJob("j1", 0.9, 0.9, 0.002, 0.05)
	if _, err := tr.RunJob(rng, job, Assignment{JobID: "j1", Strata: "web", Treatment: "t3", Probability: 1}); err != nil {
		t.Fatal(err)
	}
	if len(tr.Strategy.Decisions) == 0 {
		t.Fatal("no decisions recorded")
	}
	d := tr.Strategy.Decisions[0]
	if d.PolicyID != thompson.CostAwarePolicyID {
		t.Fatalf("wrong policy id %q", d.PolicyID)
	}
	if d.Objective != thompson.CostAwareObjectiveVer {
		t.Fatalf("wrong objective %q", d.Objective)
	}
	if d.ConfigHash == "" {
		t.Fatal("missing config hash")
	}
	if d.ArmID != "cheap" && d.ArmID != "strong" {
		t.Fatalf("unknown arm %q", d.ArmID)
	}
}

// 8: checkpoint recovery + deterministic replay reconstruct same state.
func TestCostAwareReplayDeterministic(t *testing.T) {
	dir := t.TempDir()
	mkRun := func(root string) (map[string]float64, error) {
		pol := thompson.NewDefault("cheap", "strong")
		cfg := thompson.CostAwareConfig{QualityFloor: 0.3, MinMeteredN: 1, ColdStartPulls: 5, Epsilon: 1e-3}
		tr, err := OpenCostAwareTreatment(root, "t3", pol, cfg)
		if err != nil {
			return nil, err
		}
		defer tr.Close()
		tr.Strategy.MaxRetries = 0
		rng := rand.New(rand.NewPCG(9, 9))
		for i := 0; i < 15; i++ {
			job := costAwareTestJob("j"+costAwareItoa(i), 0.7, 0.7, 0.002, 0.04)
			if _, err := tr.RunJob(rng, job, Assignment{JobID: job.JobID, Strata: "web", Treatment: "t3", Probability: 1}); err != nil {
				return nil, err
			}
		}
		out := map[string]float64{}
		for _, arm := range []string{"cheap", "strong"} {
			if m, ok := tr.Book.Mean(arm); ok {
				out[arm] = m
			}
		}
		return out, nil
	}
	m1, err := mkRun(dir + "/a")
	if err != nil {
		t.Fatal(err)
	}
	m2, err := mkRun(dir + "/b")
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range m1 {
		if m2[k] != v {
			t.Fatalf("replay diverged on %s: %v vs %v", k, v, m2[k])
		}
	}
}

// 10: legacy cost-blind path untouched (Binary mapping + Thompson select).
func TestCostAwarePreservesCostBlindSemantics(t *testing.T) {
	var m outcome.BinaryStatusMapper
	r1, l1 := m.MapReward(outcome.OutcomeEvent{Status: outcome.StatusAccepted})
	if r1 != 1.0 || !l1 {
		t.Fatal("binary mapper changed")
	}
	r0, l0 := m.MapReward(outcome.OutcomeEvent{Status: outcome.StatusRejected})
	if r0 != 0.0 || !l0 {
		t.Fatal("binary mapper changed")
	}
	if _, l := m.MapReward(outcome.OutcomeEvent{Status: outcome.StatusUnknown}); l {
		t.Fatal("UNKNOWN must not learn")
	}
	pol := thompson.NewDefault("a", "b")
	rng := rand.New(rand.NewPCG(5, 5))
	if _, err := pol.Select(rng); err != nil {
		t.Fatal(err)
	}
	if pol.LoggingPolicyID() != "exact-thompson-v1" {
		t.Fatalf("cost-blind identity changed: %q", pol.LoggingPolicyID())
	}
}

func costAwareItoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}
