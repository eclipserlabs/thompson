package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// Human-trap at the gateway: cheap always fails its model attempt and a
// human rescues every job ($2 each). Armless human-fixed jobs move no
// estimator, so cheap stays perpetually cold+unknown: every pick is flagged
// exploration until the budget is spent, then suspension, then explicit
// refusal — never a commercial conclusion.
func TestSafetyHumanTrapRefuses(t *testing.T) {
	cfg := safetyTestConfig()
	cfg.MaxExplorationPerArm = 6
	f := newSafetyFixture(t, cfg)
	defer f.close(t)
	served, cheapPicks := 0, 0
	for i := 0; i < 30; i++ {
		did, jid, arm, code := f.serve(t)
		if code != 200 {
			break
		}
		served++
		if arm == "cheap" {
			cheapPicks++
		}
		c := 0.05
		ver := outcome.VerifiedSuccess
		st := outcome.StatusAccepted
		atts := []outcome.Attempt{{AttemptID: jid + "-a0", Seq: 0, ExecutorID: arm, ArmID: arm,
			Transport: outcome.TransportOK, LatencyMs: 10, CostUSD: &c,
			Validation: outcome.ValidationPass, Verified: ver, VerifiedBy: "checker:safety"}}
		dec := jid + "-a0"
		var hc *float64
		if arm == "cheap" {
			// Model fails; human rescues at $2.
			atts[0].Verified = outcome.VerifiedFailure
			h2 := 2.0
			hc = &h2
			atts = append(atts, outcome.Attempt{AttemptID: jid + "-ah", Seq: 1, ExecutorID: "human-pool",
				Transport: outcome.TransportOK, LatencyMs: 600000,
				Validation: outcome.ValidationPass, Verified: outcome.VerifiedSuccess, VerifiedBy: "human:pool"})
			dec = jid + "-ah"
			st = outcome.StatusAccepted
		}
		ev := outcome.OutcomeEvent{SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
			DecisionID: did, JobID: jid, StrategyID: "t3", Version: 1, Status: st,
			Attempts: atts, DecidingAttemptID: dec, HumanReviewCostUSD: hc,
			VerifiedBy: "checker:safety", OccurredAt: "2026-01-05T00:00:00Z"}
		b, _ := json.Marshal(ev)
		req := httptest.NewRequest(http.MethodPost, "/v1/outcomes", bytes.NewReader(b))
		rec := httptest.NewRecorder()
		f.router.SettleHandler(rec, req)
		if rec.Code != 200 {
			t.Fatalf("settle %d", rec.Code)
		}
	}
	t.Logf("trap phase A: served=%d cheapPicks=%d", served, cheapPicks)
	if cheapPicks > 6 {
		t.Fatalf("trap exposure exceeded budget: %d", cheapPicks)
	}
	// Phase B: honest arm suspended by the operator (provider incident) —
	// only the trap remains. Exploration budget spends out, then explicit
	// refusal: the trap cannot produce a commercial conclusion.
	if code := f.operator(t, "/v1/operator/suspend", "strong", "provider incident", "op:alice"); code != 200 {
		t.Fatalf("suspend %d", code)
	}
	terminal := 0
	for i := 0; i < 12; i++ {
		did, jid, arm, code := f.serve(t)
		if code != 200 {
			terminal = code
			break
		}
		_ = did
		_ = jid
		_ = arm
		// Settle trap outcomes so budgets actually spend.
		c := 0.001
		atts := []outcome.Attempt{{AttemptID: jid + "-a0", Seq: 0, ExecutorID: "cheap", ArmID: "cheap",
			Transport: outcome.TransportOK, LatencyMs: 10, CostUSD: &c,
			Validation: outcome.ValidationPass, Verified: outcome.VerifiedFailure, VerifiedBy: "checker:safety"}}
		h2 := 2.0
		atts = append(atts, outcome.Attempt{AttemptID: jid + "-ah", Seq: 1, ExecutorID: "human-pool",
			Transport: outcome.TransportOK, LatencyMs: 600000,
			Validation: outcome.ValidationPass, Verified: outcome.VerifiedSuccess, VerifiedBy: "human:pool"})
		ev := outcome.OutcomeEvent{SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
			DecisionID: did, JobID: jid, StrategyID: "t3", Version: 1, Status: outcome.StatusAccepted,
			Attempts: atts, DecidingAttemptID: jid + "-ah", HumanReviewCostUSD: &h2,
			VerifiedBy: "checker:safety", OccurredAt: "2026-01-05T00:00:00Z"}
		b, _ := json.Marshal(ev)
		req := httptest.NewRequest(http.MethodPost, "/v1/outcomes", bytes.NewReader(b))
		rec := httptest.NewRecorder()
		f.router.SettleHandler(rec, req)
	}
	if terminal != 503 {
		t.Fatalf("trap-only traffic must end in explicit refusal, got %d", terminal)
	}
	// Correction burden is visible to the operator instead of hidden.
	h := armHealth(f.outStore.Events(), "cheap", 50)
	t.Logf("cheap health: humanFixed=%d humanCost=%.2f matured=%d", h.HumanFixed, h.HumanCostSum, h.Matured)
	if h.HumanFixed == 0 {
		t.Fatal("human correction burden invisible")
	}
}
