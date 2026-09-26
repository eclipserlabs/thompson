package gateway

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

func testCommittedDecision(id string) CommittedDecision {
	return CommittedDecision{
		DecisionID:     id,
		JobID:          "job-" + id,
		StrategyID:     "live-v1",
		SelectedArmID:  "a",
		EligibleArmIDs: []string{"a", "b"},
		EligibleArmState: []EligibleArmState{
			{ArmID: "a", Alpha: 2, Beta: 1, Pulls: 1},
			{ArmID: "b", Alpha: 1, Beta: 1, Pulls: 0},
		},
		SampledScores:   map[string]float64{"a": 0.7, "b": 0.4},
		ScoreKind:       ScoreBetaSamples,
		LoggingPolicyID: "exact-thompson-v1",
		ConfigHash:      "deadbeef",
		OccurredAt:      "2026-09-27T00:00:00Z",
	}
}

func TestDecisionStoreCommitLookupConflict(t *testing.T) {
	s := NewMemoryDecisionStore()
	d := testCommittedDecision("d1")

	stored, committed, err := s.Commit(d)
	if err != nil || !committed {
		t.Fatalf("commit: %v %v", committed, err)
	}
	if stored.Seq != 1 {
		t.Fatalf("seq=%d want 1", stored.Seq)
	}
	// Exact-ID exact-content retry: idempotent, returns existing.
	stored2, committed, err := s.Commit(d)
	if err != nil || committed {
		t.Fatalf("retry: %v %v", committed, err)
	}
	if stored2.Seq != 1 || s.Len() != 1 {
		t.Fatalf("retry mutated ledger: %+v len=%d", stored2, s.Len())
	}
	// Conflicting reuse: rejected, nothing persisted.
	conflict := d
	conflict.SelectedArmID = "b"
	if _, _, err := s.Commit(conflict); err == nil {
		t.Fatal("conflicting decision ID reuse accepted")
	}
	if s.Len() != 1 {
		t.Fatalf("conflict grew ledger: %d", s.Len())
	}
	// Unknown ID: absent.
	if _, ok := s.Lookup("ghost"); ok {
		t.Fatal("uncommitted decision lookup hit")
	}
	// Marking an uncommitted decision fails.
	if err := s.MarkExecution(DecisionExecution{DecisionID: "ghost", Phase: PhaseDispatched}); err == nil {
		t.Fatal("marked uncommitted decision")
	}
	// Execution lifecycle: committed (implicit) -> dispatched -> observed.
	if err := s.MarkExecution(DecisionExecution{DecisionID: "d1", Phase: PhaseDispatched, OccurredAt: "2026-09-27T00:00:01Z"}); err != nil {
		t.Fatalf("dispatched: %v", err)
	}
	lat := 12.5
	if err := s.MarkExecution(DecisionExecution{DecisionID: "d1", Phase: PhaseObserved, Transport: "ok", LatencyMs: &lat}); err != nil {
		t.Fatalf("observed: %v", err)
	}
	e, ok := s.Execution("d1")
	if !ok || e.Phase != PhaseObserved || e.Transport != "ok" {
		t.Fatalf("latest marker wrong: %+v", e)
	}
}

func TestDecisionStoreRestartSurvival(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	s, err := NewFileDecisionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	d := testCommittedDecision("d1")
	stored, _, err := s.Commit(d)
	if err != nil {
		t.Fatal(err)
	}
	lat := 12.5
	if err := s.MarkExecution(DecisionExecution{DecisionID: "d1", Phase: PhaseObserved, Transport: "ok", LatencyMs: &lat}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulated gateway restart: identical identity + immutable evidence.
	r, err := NewFileDecisionStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer r.Close()
	got, ok := r.Lookup("d1")
	if !ok {
		t.Fatal("committed decision lost across restart")
	}
	if got.DecisionID != stored.DecisionID || got.JobID != stored.JobID ||
		got.SelectedArmID != stored.SelectedArmID || got.Seq != stored.Seq ||
		got.LoggingPolicyID != stored.LoggingPolicyID || got.ConfigHash != stored.ConfigHash ||
		len(got.EligibleArmState) != len(stored.EligibleArmState) {
		t.Fatalf("decision diverged:\n%+v\n%+v", got, stored)
	}
	for i := range got.EligibleArmState {
		if got.EligibleArmState[i] != stored.EligibleArmState[i] {
			t.Fatalf("eligible state diverged at %d", i)
		}
	}
	e, ok := r.Execution("d1")
	if !ok || e.Phase != PhaseObserved {
		t.Fatalf("execution marker lost: %+v", e)
	}
	// Second writer refused while the first holds the lock.
	if _, err := NewFileDecisionStore(path); err == nil {
		t.Fatal("second decision writer opened without error")
	}
}

func TestDecisionStoreTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	s, err := NewFileDecisionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Commit(testCommittedDecision("d1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"type":"committed","trunca`)
	_ = f.Close()

	r, err := NewFileDecisionStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer r.Close()
	if !r.TornTail() {
		t.Fatal("torn tail not reported")
	}
	if r.Len() != 1 {
		t.Fatalf("prefix not salvaged: %d", r.Len())
	}
}

// The persisted eligible_arm_state must reconstruct the same propensities as
// the live selection state: the recorded propensity is the probability used.
func TestCommittedStateReconstructsLivePropensity(t *testing.T) {
	policy := thompson.NewDefault("a", "b")
	rng := newTestRNG()
	snap, err := policy.SelectSnapshot(rng)
	if err != nil {
		t.Fatal(err)
	}
	s := NewMemoryDecisionStore()
	state := make([]EligibleArmState, 0, len(snap.Eligible))
	for _, id := range snap.Eligible {
		post := snap.Posteriors[id]
		state = append(state, EligibleArmState{ArmID: id, Alpha: post.Alpha, Beta: post.Beta, Pulls: post.Pulls})
	}
	d := CommittedDecision{
		DecisionID: "d1", JobID: "job-d1", StrategyID: "live-v1",
		SelectedArmID: snap.Selected, EligibleArmIDs: snap.Eligible,
		EligibleArmState: state, SampledScores: snap.Scores,
		ScoreKind: ScoreBetaSamples, LoggingPolicyID: policy.LoggingPolicyID(),
		ConfigHash: snap.ConfigHash,
	}
	stored, _, err := s.Commit(d)
	if err != nil {
		t.Fatal(err)
	}
	posteriors := make(map[string]thompson.Posterior, len(stored.EligibleArmState))
	for _, st := range stored.EligibleArmState {
		posteriors[st.ArmID] = thompson.Posterior{Alpha: st.Alpha, Beta: st.Beta, Pulls: st.Pulls}
	}
	live := EstimateThompsonPropensities(posteriorsFromPolicy(policy), 20000, 7)
	persisted := EstimateThompsonPropensities(posteriors, 20000, 7)
	for arm, p := range live {
		if persisted[arm] != p {
			t.Fatalf("arm %s: persisted propensity %v != live %v", arm, persisted[arm], p)
		}
	}
}

func posteriorsFromPolicy(p *thompson.Policy) map[string]thompson.Posterior {
	out := make(map[string]thompson.Posterior)
	for _, id := range p.EligibleArmIDs() {
		post, _ := p.PosteriorFor(id)
		out[id] = post
	}
	return out
}
