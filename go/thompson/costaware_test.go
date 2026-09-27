package thompson

import (
	"math/rand/v2"
	"testing"
)

func TestCostAwareDistinguishesEqualQualityDifferentCost(t *testing.T) {
	cfg := CostAwareConfig{QualityFloor: 0.3, MinMeteredN: 1, ColdStartPulls: 0, Epsilon: 1e-3}
	scores := map[string]float64{"cheap": 0.8, "strong": 0.8}
	means := map[string]float64{"cheap": 0.002, "strong": 0.05}
	known := map[string]bool{"cheap": true, "strong": true}
	pulls := map[string]uint64{"cheap": 50, "strong": 50}
	res, err := SelectCostAwareFromSamples(scores, means, known, pulls, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fallback {
		t.Fatal("expected genuine optimum, got fallback")
	}
	if res.ArmID != "cheap" {
		t.Fatalf("expected cheap, got %q", res.ArmID)
	}
	if res.PolicyID != CostAwarePolicyID || res.Objective != CostAwareObjectiveVer {
		t.Fatalf("wrong identity %+v", res)
	}
}

func TestCostAwareQualityGateBlocksCheapLowQuality(t *testing.T) {
	cfg := CostAwareConfig{QualityFloor: 0.5, MinMeteredN: 1, ColdStartPulls: 0, Epsilon: 1e-3}
	scores := map[string]float64{"cheap": 0.2, "strong": 0.8}
	means := map[string]float64{"cheap": 0.001, "strong": 0.05}
	known := map[string]bool{"cheap": true, "strong": true}
	pulls := map[string]uint64{"cheap": 50, "strong": 50}
	res, err := SelectCostAwareFromSamples(scores, means, known, pulls, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.ArmID != "strong" {
		t.Fatalf("quality gate failed: chose %q", res.ArmID)
	}
}

func TestCostAwareColdStartExplorableButNeverOptimal(t *testing.T) {
	cfg := CostAwareConfig{QualityFloor: 0.9, MinMeteredN: 3, ColdStartPulls: 5, Epsilon: 1e-3}
	scores := map[string]float64{"new": 0.1, "old": 0.95}
	means := map[string]float64{"old": 0.05}
	known := map[string]bool{"old": true}
	pulls := map[string]uint64{"new": 0, "old": 50}
	res, err := SelectCostAwareFromSamples(scores, means, known, pulls, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// new is cold-qualified but cost-unknown; old is qualified+known → old wins.
	if res.ArmID != "old" || res.Fallback {
		t.Fatalf("expected old genuine optimum, got %+v", res)
	}
}

func TestCostAwareFallbackExplicitWhenNoKnownCost(t *testing.T) {
	cfg := CostAwareConfig{QualityFloor: 0.3, MinMeteredN: 10, ColdStartPulls: 5, Epsilon: 1e-3}
	scores := map[string]float64{"a": 0.6, "b": 0.7}
	means := map[string]float64{}
	known := map[string]bool{}
	pulls := map[string]uint64{"a": 0, "b": 0}
	res, err := SelectCostAwareFromSamples(scores, means, known, pulls, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Fallback {
		t.Fatal("expected explicit fallback when no known costs")
	}
	if res.PolicyID != CostAwarePolicyID {
		t.Fatal("fallback must retain cost-aware identity, not silently become cost-blind")
	}
}

func TestCostAwareRejectsBadConfig(t *testing.T) {
	bad := CostAwareConfig{QualityFloor: 2, MinMeteredN: 1, Epsilon: 1e-3}
	if _, err := SelectCostAwareFromSamples(map[string]float64{"a": 0.5}, nil, nil, map[string]uint64{"a": 1}, bad); err == nil {
		t.Fatal("expected config validation error")
	}
	if _, err := SelectCostAware(nil, NewDefault("a"), nil, nil, DefaultCostAwareConfig()); err == nil {
		t.Fatal("expected nil-RNG error")
	}
}

func TestCostAwareUsesLiveSamplerParity(t *testing.T) {
	p := NewDefault("cheap", "strong")
	rng := rand.New(rand.NewPCG(7, 7))
	means := map[string]float64{"cheap": 0.002, "strong": 0.05}
	known := map[string]bool{"cheap": true, "strong": true}
	res, err := SelectCostAware(rng, p, means, known, DefaultCostAwareConfig())
	if err != nil {
		t.Fatal(err)
	}
	if res.ArmID != "cheap" && res.ArmID != "strong" {
		t.Fatalf("unknown arm %q", res.ArmID)
	}
}
