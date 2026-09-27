package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// An accepted job with a human leg but no recorded review cost stays
// incomplete for cost-aware learning (unmetered, never zero-filled).
func TestSafetyHumanReviewMissingStaysIncomplete(t *testing.T) {
	f := newSafetyFixture(t, safetyTestConfig())
	defer f.close(t)
	did, jid, arm, code := f.serve(t)
	if code != 200 {
		t.Fatalf("serve %d", code)
	}
	c := 0.01
	ev := outcome.OutcomeEvent{SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
		DecisionID: did, JobID: jid, StrategyID: "t3", Version: 1, Status: outcome.StatusAccepted,
		Attempts: []outcome.Attempt{
			{AttemptID: jid + "-a0", Seq: 0, ExecutorID: arm, ArmID: arm, Transport: outcome.TransportOK,
				LatencyMs: 10, CostUSD: &c, Validation: outcome.ValidationPass, Verified: outcome.VerifiedFailure, VerifiedBy: "checker:safety"},
			{AttemptID: jid + "-ah", Seq: 1, ExecutorID: "human-pool", Transport: outcome.TransportOK,
				LatencyMs: 600000, Validation: outcome.ValidationPass, Verified: outcome.VerifiedSuccess, VerifiedBy: "human:pool"},
		},
		DecidingAttemptID: jid + "-ah", VerifiedBy: "human:pool", OccurredAt: "2026-01-05T00:00:00Z"}
	b, _ := json.Marshal(ev)
	req := httptest.NewRequest(http.MethodPost, "/v1/outcomes", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	f.router.SettleHandler(rec, req)
	if rec.Code != 200 {
		t.Fatalf("settle %d", rec.Code)
	}
	h := armHealth(f.outStore.Events(), arm, 10)
	// The job is decided by the human (armless): it enters no arm window,
	// but the arm's correction burden is visible with its recorded spend.
	if h.HumanFixed != 1 || h.HumanCostSum != 0 {
		t.Fatalf("correction burden invisible: %+v", h)
	}
	if h.Matured != 0 || h.HasEstimate {
		t.Fatalf("human-fixed job must not form arm estimates: %+v", h)
	}
	if _, ok := f.book.Mean(arm); ok {
		t.Fatal("incomplete human job entered the cost mean")
	}
	// Recorded review spend is visible on the arm (accounting, not learning).
	did2, jid2, arm2, code := f.serve(t)
	if code != 200 {
		t.Fatalf("serve2 %d", code)
	}
	hc := 2.0
	ev2 := outcome.OutcomeEvent{SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
		DecisionID: did2, JobID: jid2, StrategyID: "t3", Version: 1, Status: outcome.StatusAccepted,
		Attempts: []outcome.Attempt{
			{AttemptID: jid2 + "-a0", Seq: 0, ExecutorID: arm2, ArmID: arm2, Transport: outcome.TransportOK,
				LatencyMs: 10, CostUSD: &c, Validation: outcome.ValidationPass, Verified: outcome.VerifiedFailure, VerifiedBy: "checker:safety"},
			{AttemptID: jid2 + "-ah", Seq: 1, ExecutorID: "human-pool", Transport: outcome.TransportOK,
				LatencyMs: 600000, Validation: outcome.ValidationPass, Verified: outcome.VerifiedSuccess, VerifiedBy: "human:pool"},
		},
		DecidingAttemptID: jid2 + "-ah", HumanReviewCostUSD: &hc,
		VerifiedBy: "human:pool", OccurredAt: "2026-01-05T00:00:00Z"}
	b2, _ := json.Marshal(ev2)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/outcomes", bytes.NewReader(b2))
	rec2 := httptest.NewRecorder()
	f.router.SettleHandler(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("settle2 %d", rec2.Code)
	}
	h2 := armHealth(f.outStore.Events(), arm2, 10)
	if h2.HumanFixed < 1 || h2.HumanCostSum != 2.0 {
		t.Fatalf("recorded review spend invisible: %+v", h2)
	}
}

// Unresolved-only arms never form estimates and are never suspended for
// quality: insufficient verification is visible, not punishable, and cannot
// inflate any rate.
func TestSafetyUnresolvedNeverInflates(t *testing.T) {
	f := newSafetyFixture(t, safetyTestConfig())
	defer f.close(t)
	for i := 0; i < 8; i++ {
		did, jid, arm, code := f.serve(t)
		if code != 200 {
			t.Fatalf("serve %d", code)
		}
		ev := outcome.OutcomeEvent{SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
			DecisionID: did, JobID: jid, StrategyID: "t3", Version: 1, Status: outcome.StatusUnknown,
			Attempts: []outcome.Attempt{{
				AttemptID: jid + "-a0", Seq: 0, ExecutorID: arm, ArmID: arm, Transport: outcome.TransportTimeout,
				LatencyMs: 10, Validation: outcome.ValidationNotRun, Verified: outcome.VerifiedUnknown, VerifiedBy: "checker:safety"}},
			DecidingAttemptID: jid + "-a0", VerifiedBy: "checker:safety", OccurredAt: "2026-01-05T00:00:00Z"}
		b, _ := json.Marshal(ev)
		req := httptest.NewRequest(http.MethodPost, "/v1/outcomes", bytes.NewReader(b))
		rec := httptest.NewRecorder()
		f.router.SettleHandler(rec, req)
		if rec.Code != 200 {
			t.Fatalf("settle %d", rec.Code)
		}
	}
	for _, arm := range []string{"cheap", "strong"} {
		h := armHealth(f.outStore.Events(), arm, 10)
		if h.HasEstimate {
			t.Fatalf("censored-only arm must not form estimates: %+v", h)
		}
		if st, _, _ := f.safety.State(arm); st == ArmSuspended {
			t.Fatalf("arm suspended without any verified observation: %s", arm)
		}
	}
}
