package gateway

import (
	"os"
	"testing"
)

func TestLedgerFromFileGroupsPaired(t *testing.T) {
	tmp, _ := os.CreateTemp("", "ledger-*.jsonl")
	path := tmp.Name()
	defer os.Remove(path)
	w, _ := NewFileEvidenceWriter(path)
	// Simulate one shadowed decision manually
	decID := "test-dec-1"
	w.WriteDecisionStarted(DecisionStarted{
		SchemaVersion: 1, EventType: "DecisionStarted", DecisionID: decID,
		OccurredAt: nowRFC3339Nano(), EligibleArmIDs: []string{"a", "b"}, SelectedArmID: "a",
		SampledScores: map[string]float64{"a": 0.9, "b": 0.2}, PolicyConfigHash: "h",
		PosteriorBefore: PosteriorSnapshot{Alpha: 1, Beta: 1, Pulls: 0},
		ShadowEligible: true, ShadowSampled: true, ShadowArmID: strPtr("b"),
	})
	w.WriteExecutionObserved(ExecutionObserved{
		SchemaVersion: 1, EventType: "ExecutionObserved", DecisionID: decID,
		OccurredAt: nowRFC3339Nano(), ArmID: "a", LatencyMs: 100, Success: true,
	})
	w.WriteShadowExecutionObserved(ShadowExecutionObserved{
		SchemaVersion: 1, EventType: "ShadowExecutionObserved", DecisionID: decID,
		OccurredAt: nowRFC3339Nano(), ArmID: "b", PrimaryArmID: "a", LatencyMs: 120, Success: true,
		ComputedReward: 0.8,
	})
	w.WriteDecisionLearned(DecisionLearned{
		SchemaVersion: 1, EventType: "DecisionLearned", DecisionID: decID,
		OccurredAt: nowRFC3339Nano(), ArmID: "a", ComputedReward: 0.9,
		PosteriorBefore: PosteriorSnapshot{Alpha: 1, Beta: 1, Pulls: 0},
		PosteriorAfter: PosteriorSnapshot{Alpha: 2, Beta: 1, Pulls: 1},
		TotalPullsAfter: 1,
	})
	w.Close()

	decisions, err := LedgerFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(decisions))
	}
	d := decisions[0]
	if d.Started == nil || d.Primary == nil || d.Shadow == nil || d.Learned == nil {
		t.Fatalf("missing grouped events: %+v", d)
	}
	stats := AnalyzePaired(decisions, 0.01)
	if stats.PairedCount != 1 {
		t.Fatalf("paired count 1 expected, got %d", stats.PairedCount)
	}
	if stats.MeanDelta < -0.11 || stats.MeanDelta > -0.09 {
		t.Fatalf("delta expected ~-0.1, got %v", stats.MeanDelta)
	}
}

func strPtr(s string) *string { return &s }
