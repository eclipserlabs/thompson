package gateway

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// R3: quality-learned-but-cost-refused skew heals on recovery. Simulates the
// gateway 500 window (Submit + quality applied, book not) by applying the
// learner directly, then proves RecoverVerifiedLearning reconverges the book
// from the authoritative ledger.
func TestR3SkewHealsOnRecovery(t *testing.T) {
	f := newCostAwareFixture(t, []string{"cheap", "strong"})
	defer f.close(t)
	// Three clean settled jobs through the handler.
	for i := 0; i < 3; i++ {
		did, jid, arm, code := f.serve(t)
		if code != 200 {
			t.Fatalf("serve %d", code)
		}
		if code, _ := f.settle(t, did, jid, 1, outcome.StatusAccepted,
			[]outcome.Attempt{meteredAttempt(jid+"-a0", 0, arm, 0.01, outcome.VerifiedSuccess)}, jid+"-a0"); code != 200 {
			t.Fatalf("settle %d", code)
		}
	}
	// Skew: submit + quality-learn a 4th job, bypassing the cost book
	// exactly as a book-refusal 500 would leave behind.
	did, jid, arm, code := f.serve(t)
	if code != 200 {
		t.Fatalf("serve %d", code)
	}
	ev := outcome.OutcomeEvent{SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
		DecisionID: did, JobID: jid, StrategyID: "t3", Version: 1, Status: outcome.StatusAccepted,
		Attempts:          []outcome.Attempt{meteredAttempt(jid+"-a0", 0, arm, 0.02, outcome.VerifiedSuccess)},
		DecidingAttemptID: jid + "-a0", VerifiedBy: "checker:ca-v1", OccurredAt: "2026-01-05T00:00:00Z"}
	if _, err := outcome.Settle(f.outStore, f.router.learner, ev); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.book.Mean(arm); !ok {
		// Book may or may not have the arm yet; the point is recovery.
		t.Logf("pre-recovery: book has no mean for %s (skew installed)", arm)
	}
	ck := filepath.Join(f.dir, "ck.json")
	if err := f.router.CheckpointVerifiedLearning(ck); err != nil {
		t.Fatal(err)
	}
	if err := f.router.RecoverVerifiedLearning(ck); err != nil {
		t.Fatal(err)
	}
	// Post-recovery the book must reflect ALL ledger jobs including the
	// skewed one: recompute expected means from the ledger directly.
	want := map[string][]float64{}
	for _, e := range f.outStore.Events() {
		if e.Status != outcome.StatusAccepted && e.Status != outcome.StatusRejected {
			continue
		}
		var a string
		for _, at := range e.Attempts {
			if at.AttemptID == e.DecidingAttemptID {
				a = at.ArmID
			}
		}
		if a == "" {
			continue
		}
		m, u, err := outcome.FullyLoadedCost(e)
		if err != nil || u > 0 {
			continue
		}
		want[a] = append(want[a], m)
	}
	for a, costs := range want {
		sum := 0.0
		for _, c := range costs {
			sum += c
		}
		if got, ok := f.book.Mean(a); !ok || got != sum/float64(len(costs)) {
			t.Fatalf("R3: arm %s book=%v,%v want mean %v over %d", a, got, ok, sum/float64(len(costs)), len(costs))
		}
	}
}

// R3: corrupt quality checkpoint refuses recovery loudly; corrupt cost
// sidecar is ignored because the book always rebuilds from the ledger
// (the sidecar is an audit artifact, never authoritative).
func TestR3CheckpointCorruption(t *testing.T) {
	f := newCostAwareFixture(t, []string{"cheap", "strong"})
	defer f.close(t)
	did, jid, arm, code := f.serve(t)
	if code != 200 {
		t.Fatalf("serve %d", code)
	}
	if code, _ := f.settle(t, did, jid, 1, outcome.StatusAccepted,
		[]outcome.Attempt{meteredAttempt(jid+"-a0", 0, arm, 0.01, outcome.VerifiedSuccess)}, jid+"-a0"); code != 200 {
		t.Fatalf("settle %d", code)
	}
	ck := filepath.Join(f.dir, "ck.json")
	if err := f.router.CheckpointVerifiedLearning(ck); err != nil {
		t.Fatal(err)
	}
	// Corrupt the quality checkpoint: recovery must refuse.
	raw, _ := os.ReadFile(ck)
	bad := append([]byte(nil), raw...)
	if len(bad) > 20 {
		copy(bad[10:20], []byte("CORRUPTED!"))
	}
	os.WriteFile(ck, bad, 0o600)
	if err := f.router.RecoverVerifiedLearning(ck); err == nil {
		t.Fatal("R3: recovery with corrupt quality checkpoint must refuse")
	}
	// Restore a good checkpoint, corrupt only the cost sidecar: recovery
	// must still succeed (ledger rebuild is authoritative).
	if err := f.router.CheckpointVerifiedLearning(ck); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(ck+".cost.json", []byte("{broken"), 0o600)
	if err := f.router.RecoverVerifiedLearning(ck); err != nil {
		t.Fatalf("R3: cost-sidecar corruption must not block recovery: %v", err)
	}
	if _, ok := f.book.Mean(arm); !ok {
		t.Fatal("R3: book lost its mean across recovery")
	}
}
