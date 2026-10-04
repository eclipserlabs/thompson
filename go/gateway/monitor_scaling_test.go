package gateway

import (
	"fmt"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// Scaling benchmark for the monitoring fold (Phase 4). armHealth sorts and
// copies full history per evaluation; this measures where the settlement
// path bends. Run: go test -run TestMonitorScaling -v ./gateway/
func TestMonitorScaling(t *testing.T) {
	if testing.Short() {
		t.Skip("scaling measurement skipped in short mode")
	}
	for _, n := range []int{1000, 10000, 100000} {
		evs := make([]outcome.OutcomeEvent, 0, n)
		for i := 0; i < n; i++ {
			arm := "cheap"
			if i%3 == 0 {
				arm = "strong"
			}
			c := 0.01
			st := outcome.StatusAccepted
			ver := outcome.VerifiedSuccess
			if i%10 == 9 {
				st, ver = outcome.StatusRejected, outcome.VerifiedFailure
			}
			jid := fmt.Sprintf("job-%07d", i)
			evs = append(evs, outcome.OutcomeEvent{
				SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
				DecisionID: "dec-" + jid, JobID: jid, StrategyID: "t3",
				Version: 1, Status: st,
				Attempts: []outcome.Attempt{{
					AttemptID: jid + "-a0", Seq: 0, ExecutorID: arm, ArmID: arm,
					Transport: outcome.TransportOK, LatencyMs: 10, CostUSD: &c,
					Validation: outcome.ValidationPass, Verified: ver, VerifiedBy: "m",
				}},
				DecidingAttemptID: jid + "-a0", VerifiedBy: "m",
				OccurredAt: "2026-01-05T00:00:00Z",
				VerifiedAt: "2026-01-05T01:00:00Z",
				Seq:        uint64(i),
			})
		}
		start := time.Now()
		const iters = 5
		for k := 0; k < iters; k++ {
			_ = armHealth(evs, "cheap", 20)
			_ = armHealth(evs, "strong", 20)
		}
		t.Logf("n=%d fold_per_settlement=%s", n, time.Since(start)/(2*iters))
	}
}
