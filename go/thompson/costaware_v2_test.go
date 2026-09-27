package thompson

import (
	"testing"
)

// F1 regression: post-cold qualification uses the posterior MEAN, not the
// sample. A below-floor arm with known costs must never be a genuine optimum
// once it has sufficient pulls, no matter how lucky its sample is.
func TestCostAwareMeanGateBlocksUnsafeOptimum(t *testing.T) {
	cfg := CostAwareConfig{QualityFloor: 0.5, MinMeteredN: 1, ColdStartPulls: 5, Epsilon: 1e-3}
	// cheap: terrible mean (0.10), lucky sample (0.80). strong: good mean.
	scores := map[string]float64{"cheap": 0.80, "strong": 0.70}
	qmeans := map[string]float64{"cheap": 0.10, "strong": 0.85}
	means := map[string]float64{"cheap": 0.001, "strong": 0.05}
	known := map[string]bool{"cheap": true, "strong": true}
	pulls := map[string]uint64{"cheap": 50, "strong": 50}
	res, err := SelectCostAwareFromSamplesV2(scores, qmeans, means, known, pulls, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.ArmID != "strong" {
		t.Fatalf("F1: unsafe arm chosen as optimum: %+v", res)
	}
	if res.Fallback {
		t.Fatal("strong should be a genuine optimum, not fallback")
	}
	if res.RuleVersion != CostAwareRuleV2 {
		t.Fatalf("missing rule version: %+v", res)
	}
}

// Legacy RuleV1 path preserved for replay of old ledgers: nil qmeans selects
// by sample (documented unsafe reading, reproducible exactly).
func TestCostAwareRuleV1ReplayPreserved(t *testing.T) {
	cfg := CostAwareConfig{QualityFloor: 0.5, MinMeteredN: 1, ColdStartPulls: 0, Epsilon: 1e-3}
	scores := map[string]float64{"cheap": 0.80, "strong": 0.70}
	means := map[string]float64{"cheap": 0.001, "strong": 0.05}
	known := map[string]bool{"cheap": true, "strong": true}
	pulls := map[string]uint64{"cheap": 50, "strong": 50}
	res, err := SelectCostAwareFromSamples(scores, means, known, pulls, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.RuleVersion != CostAwareRuleV1 {
		t.Fatalf("legacy path must report RuleV1: %+v", res)
	}
	if res.ArmID != "cheap" {
		t.Fatalf("legacy sample rule must reproduce old pick: %+v", res)
	}
}

// F7 regression: known-without-mean is explorable (fallback pool), never
// silently dropped from every pool.
func TestCostAwareKnownWithoutMeanNotDropped(t *testing.T) {
	cfg := CostAwareConfig{QualityFloor: 0.5, MinMeteredN: 1, ColdStartPulls: 0, Epsilon: 1e-3}
	scores := map[string]float64{"ghost": 0.9, "solid": 0.4}
	qmeans := map[string]float64{"ghost": 0.9, "solid": 0.4}
	means := map[string]float64{"solid": 0.05} // ghost known but mean missing
	known := map[string]bool{"ghost": true, "solid": true}
	pulls := map[string]uint64{"ghost": 50, "solid": 50}
	res, err := SelectCostAwareFromSamplesV2(scores, qmeans, means, known, pulls, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// solid mean 0.4 < floor 0.5 → unqualified; ghost qualified but costless →
	// flagged fallback on ghost (explorable), never a silent drop to solid.
	if res.ArmID != "ghost" || !res.Fallback {
		t.Fatalf("F7: ghost must be explicit fallback: %+v", res)
	}
}

// Cold arms remain explorable under RuleV2 (bounded by ColdStartPulls).
func TestCostAwareRuleV2ColdExplorable(t *testing.T) {
	cfg := CostAwareConfig{QualityFloor: 0.9, MinMeteredN: 100, ColdStartPulls: 5, Epsilon: 1e-3}
	scores := map[string]float64{"new": 0.1, "old": 0.95}
	qmeans := map[string]float64{"new": 0.1, "old": 0.95}
	means := map[string]float64{"old": 0.05}
	known := map[string]bool{"old": true}
	pulls := map[string]uint64{"new": 0, "old": 50}
	res, err := SelectCostAwareFromSamplesV2(scores, qmeans, means, known, pulls, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.ArmID != "old" || res.Fallback {
		t.Fatalf("old should be genuine optimum: %+v", res)
	}
}
