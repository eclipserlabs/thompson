package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	if e.N != 2 {
		t.Fatalf("observed marker N=%d want 2 (dispatched was 1)", e.N)
	}
}

func TestExecutionNumberingAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	s, err := NewFileDecisionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(id string) CommittedDecision {
		d := testCommittedDecision(id)
		return d
	}
	if _, _, err := s.Commit(mk("d1")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Commit(mk("d2")); err != nil {
		t.Fatal(err)
	}
	mark := func(id string, phase ExecutionPhase, wantN uint64) {
		t.Helper()
		if err := s.MarkExecution(DecisionExecution{DecisionID: id, Phase: phase}); err != nil {
			t.Fatal(err)
		}
		e, _ := s.Execution(id)
		if e.N != wantN {
			t.Fatalf("%s/%s N=%d want %d", id, phase, e.N, wantN)
		}
	}
	mark("d1", PhaseDispatched, 1)
	mark("d1", PhaseObserved, 2)
	mark("d2", PhaseDispatched, 1)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := NewFileDecisionStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer r.Close()
	if e, _ := r.Execution("d1"); e.N != 2 || e.Phase != PhaseObserved {
		t.Fatalf("d1 marker wrong after restart: %+v", e)
	}
	if e, _ := r.Execution("d2"); e.N != 1 {
		t.Fatalf("d2 marker wrong after restart: %+v", e)
	}
	// Numbering continues, it does not restart.
	if err := r.MarkExecution(DecisionExecution{DecisionID: "d1", Phase: PhaseUnknown}); err != nil {
		t.Fatal(err)
	}
	if e, _ := r.Execution("d1"); e.N != 3 {
		t.Fatalf("post-restart N=%d want 3", e.N)
	}
}

// Old files carry execution markers with "seq" and no "n": recovery must
// ignore the legacy field and assign per-decision N in encounter order.
func TestOldFormatExecutionRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	var buf strings.Builder
	writeLine := func(v any) {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	d1 := testCommittedDecision("d1")
	d1.Seq = 1
	writeLine(map[string]any{"type": "committed", "committed": d1})
	writeLine(map[string]any{"type": "execution", "execution": map[string]any{
		"decision_id": "d1", "phase": "dispatched", "seq": 7, "occurred_at": "2026-09-27T00:00:01Z"}})
	writeLine(map[string]any{"type": "execution", "execution": map[string]any{
		"decision_id": "d1", "phase": "observed", "seq": 8, "occurred_at": "2026-09-27T00:00:02Z"}})
	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewFileDecisionStore(path)
	if err != nil {
		t.Fatalf("old format rejected: %v", err)
	}
	defer s.Close()
	e, ok := s.Execution("d1")
	if !ok || e.N != 2 || e.Phase != PhaseObserved {
		t.Fatalf("legacy markers misnumbered: %+v", e)
	}
	if _, ok := s.Lookup("d1"); !ok {
		t.Fatal("committed decision lost")
	}
}

func TestMalformedLedgerRejected(t *testing.T) {
	// Terminated garbage is corruption, not a crash tear: the open fails
	// rather than truncating valid history that follows.
	valid := testCommittedDecision("d9")
	vb, _ := json.Marshal(map[string]any{"type": "committed", "committed": valid})
	cases := map[string]string{
		"garbage line":      "NOT-JSON\n" + string(vb) + "\n",
		"unknown type":      "{\"type\":\"nope\"}\n",
		"execution unknown": "{\"type\":\"execution\",\"execution\":{\"decision_id\":\"ghost\",\"phase\":\"dispatched\"}}\n",
	}
	for name, content := range cases {
		path := filepath.Join(t.TempDir(), "d.jsonl")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewFileDecisionStore(path); err == nil {
			t.Fatalf("%s: malformed ledger opened without error", name)
		}
	}
	// Conflicting committed content for one ID is rejected.
	path := filepath.Join(t.TempDir(), "d.jsonl")
	d1 := testCommittedDecision("d1")
	d2 := d1
	d2.SelectedArmID = "b"
	var buf strings.Builder
	for _, d := range []CommittedDecision{d1, d2} {
		b, _ := json.Marshal(map[string]any{"type": "committed", "committed": d})
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileDecisionStore(path); err == nil {
		t.Fatal("conflicting ledger opened without error")
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
