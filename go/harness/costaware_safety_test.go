package harness

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// F1 end-to-end regression (validates SAFETY_DECISION mean-gate): with cheap
// true p=0.10 against floor 0.50 over 200 jobs, unsafe picks are bounded to
// cold-phase exploration. Pre-fix sample rule: 18/200 (9%). Post-fix: <= 10.
func TestCostAwareUnsafeFractionBounded(t *testing.T) {
	dir := t.TempDir()
	pol := thompson.NewDefault("cheap", "strong")
	cfg := thompson.CostAwareConfig{QualityFloor: 0.5, MinMeteredN: 1, ColdStartPulls: 5, Epsilon: 1e-3}
	tr, err := OpenCostAwareTreatment(dir+"/t3", "t3", pol, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	tr.Strategy.MaxRetries = 0
	rng := rand.New(rand.NewPCG(100, 100))
	cheap := 0
	genuineCheap := 0
	n := 200
	for i := 0; i < n; i++ {
		job := JobTruth{
			JobID: "m" + costAwarePad(i), Strata: "web",
			AssignedAt: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
			ArmSuccess: map[string]float64{"cheap": 0.10, "strong": 0.90},
			ArmCost:    map[string]float64{"cheap": 0.001, "strong": 0.05},
			ArmLatency: map[string]float64{"cheap": 100, "strong": 200},
		}
		nBefore := len(tr.Strategy.Decisions)
		if _, err := tr.RunJob(rng, job, Assignment{JobID: job.JobID, Strata: "web", Treatment: "t3", Probability: 1}); err != nil {
			t.Fatal(err)
		}
		for _, d := range tr.Strategy.Decisions[nBefore:] {
			if d.RuleVersion != thompson.CostAwareRuleV2 {
				t.Fatalf("decision missing RuleVersion=2: %+v", d)
			}
			if d.ArmID == "cheap" {
				cheap++
				if !d.Fallback {
					genuineCheap++
				}
			}
		}
	}
	t.Logf("unsafe picks %d/%d (genuine-optimum %d)", cheap, n, genuineCheap)
	if cheap > 10 {
		t.Fatalf("F1: unsafe exploration exceeded cold budget: %d/200", cheap)
	}
}

// F4 regression: config hash is a stable sha256 binding and the full frozen
// config is persisted in the treatment dir (decisions carry only the hash).
func TestCostAwareConfigBinding(t *testing.T) {
	dir := t.TempDir()
	pol := thompson.NewDefault("cheap", "strong")
	cfg := thompson.CostAwareConfig{QualityFloor: 0.4, MinMeteredN: 2, ColdStartPulls: 5, Epsilon: 1e-3}
	tr, err := OpenCostAwareTreatment(dir+"/t3", "t3", pol, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	h1, h2 := configHashForCostAware(cfg), tr.Strategy.ConfigHash
	if h1 == "" || h1 != h2 {
		t.Fatalf("unstable config hash: %q vs %q", h1, h2)
	}
	if len(h1) < len("costaware-v1-")+16 {
		t.Fatalf("config hash too short for sha256 binding: %q", h1)
	}
	raw, err := readCostAwareConfig(dir + "/t3/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if raw.QualityFloor != 0.4 || raw.MinMeteredN != 2 {
		t.Fatalf("persisted config mismatch: %+v", raw)
	}
}
