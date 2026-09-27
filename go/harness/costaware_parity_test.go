package harness

import (
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// F3 parity: the cost-aware learner's counting rule must agree with the
// evaluator's jobCost on every constructible event (except malformed costs,
// which the learner rejects and the pre-cost-aware evaluator predates).
func TestCostBookReportParity(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	cases := []outcome.OutcomeEvent{
		{ // fully metered single attempt
			SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
			DecisionID: "d", JobID: "j1", Version: 1, Status: outcome.StatusAccepted,
			Attempts: []outcome.Attempt{{AttemptID: "j1-a0", Seq: 0, ExecutorID: "m", ArmID: "m",
				Transport: outcome.TransportOK, CostUSD: f(0.02),
				Validation: outcome.ValidationPass, Verified: outcome.VerifiedSuccess, VerifiedBy: "v"}},
			DecidingAttemptID: "j1-a0", OccurredAt: "2026-01-05T00:00:00Z",
		},
		{ // missing attempt cost → unmetered
			SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
			DecisionID: "d", JobID: "j2", Version: 1, Status: outcome.StatusRejected,
			Attempts: []outcome.Attempt{{AttemptID: "j2-a0", Seq: 0, ExecutorID: "m", ArmID: "m",
				Transport:  outcome.TransportOK,
				Validation: outcome.ValidationPass, Verified: outcome.VerifiedFailure, VerifiedBy: "v"}},
			DecidingAttemptID: "j2-a0", OccurredAt: "2026-01-05T00:00:00Z",
		},
		{ // human leg with recorded review cost: shared conservative rule
			// counts the nil human-attempt cost as unmetered.
			SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
			DecisionID: "d", JobID: "j3", Version: 1, Status: outcome.StatusAccepted,
			Attempts: []outcome.Attempt{
				{AttemptID: "j3-a0", Seq: 0, ExecutorID: "m", ArmID: "m",
					Transport: outcome.TransportOK, CostUSD: f(0.02),
					Validation: outcome.ValidationPass, Verified: outcome.VerifiedFailure, VerifiedBy: "v"},
				{AttemptID: "j3-ah", Seq: 1, ExecutorID: "human-pool",
					Transport:  outcome.TransportOK,
					Validation: outcome.ValidationPass, Verified: outcome.VerifiedSuccess, VerifiedBy: "human:pool"},
			},
			DecidingAttemptID: "j3-ah", HumanReviewCostUSD: f(1.5),
			OccurredAt: "2026-01-05T00:00:00Z",
		},
		{ // human leg WITHOUT recorded review cost → unmetered, both agree
			SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
			DecisionID: "d", JobID: "j4", Version: 1, Status: outcome.StatusAccepted,
			Attempts: []outcome.Attempt{
				{AttemptID: "j4-ah", Seq: 0, ExecutorID: "human-pool",
					Transport:  outcome.TransportOK,
					Validation: outcome.ValidationPass, Verified: outcome.VerifiedSuccess, VerifiedBy: "human:pool"},
			},
			DecidingAttemptID: "j4-ah",
			OccurredAt:        "2026-01-05T00:00:00Z",
		},
	}
	for _, ev := range cases {
		gotM, gotU := jobCost(ev)
		wantM, wantU, err := outcome.FullyLoadedCost(ev)
		if err != nil {
			t.Fatalf("%s: unexpected validation error: %v", ev.JobID, err)
		}
		if gotM != wantM || gotU != wantU {
			t.Fatalf("%s: jobCost=(%v,%d) book=(%v,%d)", ev.JobID, gotM, gotU, wantM, wantU)
		}
	}
}
