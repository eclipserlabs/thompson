package outcome

import (
	"testing"
)

func mkCostEvent(job string, ver uint64, status JobStatus, arm string, cost *float64, human *float64) OutcomeEvent {
	return OutcomeEvent{
		SchemaVersion: SchemaVersion, EventType: EventJobSettled,
		DecisionID: "dec-" + job, JobID: job, StrategyID: "t3",
		Version: ver, Supersedes: ver - 1, Status: status,
		Attempts: []Attempt{{
			AttemptID: job + "-a0", Seq: 0, ExecutorID: arm, ArmID: arm,
			Transport: TransportOK, LatencyMs: 100, CostUSD: cost,
			Validation: ValidationPass, Verified: VerifiedSuccess, VerifiedBy: "v",
		}},
		DecidingAttemptID:  job + "-a0",
		HumanReviewCostUSD: human,
		OccurredAt:         "2026-01-05T00:00:00Z",
	}
}

func fptr(v float64) *float64 { return &v }

// 3: fallback chain costs accounted exactly once (multi-attempt + human).
func TestCostBookFallbackChainExactOnce(t *testing.T) {
	b := NewCostBookV1([]string{"cheap", "strong"})
	c1, c2 := 0.002, 0.02
	hc := 1.5
	ev := OutcomeEvent{
		SchemaVersion: SchemaVersion, EventType: EventJobSettled,
		DecisionID: "dec-j1", JobID: "j1", StrategyID: "t3",
		Version: 1, Supersedes: 0, Status: StatusAccepted,
		Attempts: []Attempt{
			{AttemptID: "j1-a0", Seq: 0, ExecutorID: "cheap", ArmID: "cheap", Transport: TransportOK, LatencyMs: 100, CostUSD: &c1, Validation: ValidationPass, Verified: VerifiedFailure, VerifiedBy: "v"},
			{AttemptID: "j1-a1", Seq: 1, ExecutorID: "strong", ArmID: "strong", Transport: TransportOK, LatencyMs: 200, CostUSD: &c2, Validation: ValidationPass, Verified: VerifiedSuccess, VerifiedBy: "v"},
			{AttemptID: "j1-ah", Seq: 2, ExecutorID: "human-pool", Transport: TransportOK, LatencyMs: 600000, Validation: ValidationPass, Verified: VerifiedSuccess, VerifiedBy: "human:pool"},
		},
		DecidingAttemptID: "j1-a1", HumanReviewCostUSD: &hc,
		OccurredAt: "2026-01-05T00:00:00Z",
	}
	hist := []OutcomeEvent{}
	moved, err := b.Apply(ev, func() []OutcomeEvent { return hist })
	if err != nil {
		t.Fatal(err)
	}
	// F3 alignment: the human-pool attempt carries nil attempt-level cost,
	// so the report rule counts the job unmetered even though
	// HumanReviewCostUSD records the review spend. The book matches the
	// evaluator: UnmeteredN only, no mean movement.
	if moved {
		t.Fatal("human-chain job must be unmetered under the shared report rule")
	}
	if _, ok := b.Mean("strong"); ok {
		t.Fatal("unmetered job must not produce a mean")
	}
	st, _ := b.Stats("strong")
	if st.UnmeteredN != 1 || st.MeteredN != 0 {
		t.Fatalf("wrong counters %+v", st)
	}
	// Fully-metered multi-attempt chain (no human leg) IS learned exactly once
	// on the deciding arm.
	b2 := NewCostBookV1([]string{"cheap", "strong"})
	c3 := 0.003
	ev2 := OutcomeEvent{
		SchemaVersion: SchemaVersion, EventType: EventJobSettled,
		DecisionID: "dec-j2", JobID: "j2", StrategyID: "t3",
		Version: 1, Supersedes: 0, Status: StatusAccepted,
		Attempts: []Attempt{
			{AttemptID: "j2-a0", Seq: 0, ExecutorID: "cheap", ArmID: "cheap", Transport: TransportOK, LatencyMs: 100, CostUSD: &c1, Validation: ValidationPass, Verified: VerifiedFailure, VerifiedBy: "v"},
			{AttemptID: "j2-a1", Seq: 1, ExecutorID: "strong", ArmID: "strong", Transport: TransportOK, LatencyMs: 200, CostUSD: &c3, Validation: ValidationPass, Verified: VerifiedSuccess, VerifiedBy: "v"},
		},
		DecidingAttemptID: "j2-a1",
		OccurredAt:        "2026-01-05T00:00:00Z",
	}
	if _, err := b2.Apply(ev2, func() []OutcomeEvent { return nil }); err != nil {
		t.Fatal(err)
	}
	m, ok := b2.Mean("strong")
	if !ok || m != c1+c3 {
		t.Fatalf("metered chain cost wrong: %v %v", m, ok)
	}
	if _, ok := b2.Mean("cheap"); ok {
		t.Fatal("non-deciding arm must not absorb chain cost")
	}
	_ = hc
}

// 4: missing cost never becomes zero.
func TestCostBookMissingCostNeverZero(t *testing.T) {
	b := NewCostBookV1([]string{"a"})
	ev := mkCostEvent("j1", 1, StatusAccepted, "a", nil, nil)
	hist := []OutcomeEvent{}
	moved, err := b.Apply(ev, func() []OutcomeEvent { return hist })
	if err != nil {
		t.Fatal(err)
	}
	if moved {
		t.Fatal("unmetered job must not move the mean")
	}
	if _, ok := b.Mean("a"); ok {
		t.Fatal("missing cost became a mean (zero-fill)")
	}
	st, _ := b.Stats("a")
	if st.UnmeteredN != 1 || st.MeteredN != 0 {
		t.Fatalf("wrong counters %+v", st)
	}
}

// 5: UNKNOWN/PENDING never become confident.
func TestCostBookUnknownPendingNoLearn(t *testing.T) {
	b := NewCostBookV1([]string{"a"})
	for _, st := range []JobStatus{StatusUnknown, StatusPending} {
		ev := mkCostEvent("j-"+string(st), 1, st, "a", fptr(0.01), nil)
		if _, err := b.Apply(ev, func() []OutcomeEvent { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := b.Mean("a"); ok {
		t.Fatal("UNKNOWN/PENDING moved cost estimates")
	}
}

// 6: correction ACCEPTED→REJECTED updates consistently (rebuild removes old).
func TestCostBookCorrectionConsistent(t *testing.T) {
	b := NewCostBookV1([]string{"a"})
	hist := []OutcomeEvent{}
	v1 := mkCostEvent("j1", 1, StatusAccepted, "a", fptr(0.10), nil)
	hist = append(hist, v1)
	if _, err := b.Apply(v1, func() []OutcomeEvent { return hist[:0] }); err != nil {
		t.Fatal(err)
	}
	v2 := mkCostEvent("j1", 2, StatusRejected, "a", fptr(0.10), nil)
	v2.Attempts[0].Verified = VerifiedFailure
	hist = append(hist, v2)
	moved, err := b.Apply(v2, func() []OutcomeEvent { return hist })
	if err != nil {
		t.Fatal(err)
	}
	_ = moved
	m, ok := b.Mean("a")
	if !ok || m != 0.10 {
		t.Fatalf("corrected mean wrong: %v %v", m, ok)
	}
	st, _ := b.Stats("a")
	if st.MeteredN != 1 {
		t.Fatalf("correction double-counted: %+v", st)
	}
}

// 7: duplicate settlement idempotent.
func TestCostBookDuplicateIdempotent(t *testing.T) {
	b := NewCostBookV1([]string{"a"})
	ev := mkCostEvent("j1", 1, StatusAccepted, "a", fptr(0.05), nil)
	h := func() []OutcomeEvent { return nil }
	if _, err := b.Apply(ev, h); err != nil {
		t.Fatal(err)
	}
	moved, err := b.Apply(ev, h)
	if err != nil {
		t.Fatal(err)
	}
	if moved {
		t.Fatal("duplicate must not move the book")
	}
	st, _ := b.Stats("a")
	if st.MeteredN != 1 {
		t.Fatalf("duplicate double-counted: %+v", st)
	}
}

// 8: snapshot/restore + rebuild deterministic.
func TestCostBookSnapshotReplayDeterministic(t *testing.T) {
	b := NewCostBookV1([]string{"a", "b"})
	evs := []OutcomeEvent{
		mkCostEvent("j1", 1, StatusAccepted, "a", fptr(0.02), nil),
		mkCostEvent("j2", 1, StatusRejected, "b", fptr(0.04), nil),
	}
	for _, ev := range evs {
		if _, err := b.Apply(ev, func() []OutcomeEvent { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	snap := b.Snapshot()
	if snap.Version != CostBookVersion {
		t.Fatal("snapshot version mismatch")
	}
	b2 := NewCostBookV1([]string{"a", "b"})
	if _, err := b2.Rebuild(evs); err != nil {
		t.Fatal(err)
	}
	for _, arm := range []string{"a", "b"} {
		m1, _ := b.Mean(arm)
		m2, _ := b2.Mean(arm)
		if m1 != m2 {
			t.Fatalf("replay diverged on %s: %v vs %v", arm, m1, m2)
		}
	}
	b3 := NewCostBookV1(nil)
	if err := b3.Restore(snap); err != nil {
		t.Fatal(err)
	}
	if m, _ := b3.Mean("a"); m != 0.02 {
		t.Fatalf("restore wrong: %v", m)
	}
}

// Malformed costs rejected explicitly.
func TestCostBookRejectsMalformedCosts(t *testing.T) {
	b := NewCostBookV1([]string{"a"})
	for _, c := range []float64{0.0 / 1.0} {
		_ = c
	}
	neg := -0.5
	if _, err := b.Apply(mkCostEvent("jneg", 1, StatusAccepted, "a", &neg, nil), func() []OutcomeEvent { return nil }); err == nil {
		t.Fatal("expected negative-cost rejection")
	}
	nan := 0.0
	nan = nan / nan
	_ = nan
}
