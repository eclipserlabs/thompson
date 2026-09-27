package main

import (
	"encoding/json"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
)

// Serialization-invariance: indented, compact, and reordered documents of
// the same envelope share one frozen hash, so resume never fails on
// formatting drift (regression: compact-vs-indent mismatch refused
// legitimate resume).
func TestSafetyConfigHashSerializationInvariant(t *testing.T) {
	cfg := gateway.SafetyConfig{
		Workload: "w", Arms: []string{"a", "b"}, FallbackArm: "b",
		QualityFloor: 0.5, MonitorWindow: 20, MonitorMinObs: 8,
		MaxExplorationPerArm: 30, MaxMissingShare: 0.2, ColdStartPulls: 5,
	}
	indented, _ := json.MarshalIndent(cfg, "", "  ")
	compact, _ := json.Marshal(cfg)
	h1, err := safetyConfigHash(indented)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := safetyConfigHash(compact)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("same config hashed differently: %s vs %s", h1, h2)
	}
	other := cfg
	other.QualityFloor = 0.9
	otherRaw, _ := json.Marshal(other)
	h3, err := safetyConfigHash(otherRaw)
	if err != nil {
		t.Fatal(err)
	}
	if h3 == h1 {
		t.Fatal("different config must hash differently")
	}
	if _, err := safetyConfigHash([]byte("{broken")); err == nil {
		t.Fatal("invalid JSON must be rejected")
	}
}
