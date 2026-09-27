package thompson

import "testing"

// RuleV3: masked-out arms are excluded from every pool, including fallback.
func TestCostAwareV3MaskExcludesSuspended(t *testing.T) {
	cfg := CostAwareConfig{QualityFloor: 0.5, MinMeteredN: 1, ColdStartPulls: 0, Epsilon: 1e-3}
	scores := map[string]float64{"bad": 0.95, "good": 0.70}
	qmeans := map[string]float64{"bad": 0.95, "good": 0.70}
	means := map[string]float64{"bad": 0.001, "good": 0.05}
	known := map[string]bool{"bad": true, "good": true}
	pulls := map[string]uint64{"bad": 50, "good": 50}
	allowed := map[string]bool{"good": true}
	res, err := SelectCostAwareFromSamplesV3(scores, qmeans, means, known, pulls, allowed, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.ArmID != "good" {
		t.Fatalf("suspended arm selected: %+v", res)
	}
	if res.RuleVersion != CostAwareRuleV3 {
		t.Fatalf("masked decision must report RuleV3: %+v", res)
	}
}

// RuleV3: empty mask fails closed, never selects.
func TestCostAwareV3EmptyMaskFailsClosed(t *testing.T) {
	cfg := DefaultCostAwareConfig()
	scores := map[string]float64{"a": 0.9}
	qmeans := map[string]float64{"a": 0.9}
	if _, err := SelectCostAwareFromSamplesV3(scores, qmeans, nil, nil, map[string]uint64{"a": 50}, map[string]bool{}, cfg); err == nil {
		t.Fatal("empty safety mask must fail closed")
	}
}

// RuleV3 with nil mask reproduces RuleV2 exactly.
func TestCostAwareV3NilMaskEqualsV2(t *testing.T) {
	cfg := CostAwareConfig{QualityFloor: 0.5, MinMeteredN: 1, ColdStartPulls: 5, Epsilon: 1e-3}
	scores := map[string]float64{"cheap": 0.80, "strong": 0.70}
	qmeans := map[string]float64{"cheap": 0.10, "strong": 0.85}
	means := map[string]float64{"cheap": 0.001, "strong": 0.05}
	known := map[string]bool{"cheap": true, "strong": true}
	pulls := map[string]uint64{"cheap": 50, "strong": 50}
	r2, err := SelectCostAwareFromSamplesV2(scores, qmeans, means, known, pulls, cfg)
	if err != nil {
		t.Fatal(err)
	}
	r3, err := SelectCostAwareFromSamplesV3(scores, qmeans, means, known, pulls, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r2.ArmID != r3.ArmID || r2.RuleVersion != r3.RuleVersion {
		t.Fatalf("nil mask must equal V2: %+v vs %+v", r2, r3)
	}
}

func TestCombinedConfigHashStable(t *testing.T) {
	q := DefaultConfig()
	c := DefaultCostAwareConfig()
	h1, h2 := CombinedConfigHash(q, c), CombinedConfigHash(q, c)
	if h1 == "" || h1 != h2 {
		t.Fatalf("unstable combined hash: %q %q", h1, h2)
	}
	c2 := c
	c2.QualityFloor = 0.9
	if CombinedConfigHash(q, c2) == h1 {
		t.Fatal("objective change must change combined hash")
	}
}
