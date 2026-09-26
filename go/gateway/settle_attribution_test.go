package gateway

import (
	"net/http"
	"sync"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// B2: decision A settling unrelated arm B is forged attribution. A
// single-attempt outcome must name the selected arm; anything else is
// rejected before the ledger or policy is touched.
func TestSettleRejectsForgedSingleArmAttribution(t *testing.T) {
	policy := thompson.NewDefault("a", "b")
	fx := newVerifiedFixture(t, policy, map[string]Provider{"a": NewFakeProvider("a"), "b": NewFakeProvider("b")}, nil)
	defer fx.close(t)

	d1, j1, code := fx.serve(t, `{}`)
	if code != http.StatusOK {
		t.Fatalf("serve=%d", code)
	}
	selected := fx.mem.Started[0].SelectedArmID
	other := "a"
	if selected == "a" {
		other = "b"
	}
	before := policy.Snapshot()

	ev := settleEvent(d1, j1, 1, outcome.StatusAccepted, other)
	if code, _ := fx.settle(t, ev, true); code != http.StatusConflict {
		t.Fatalf("forged attribution status=%d want 409", code)
	}
	// Ledger and policy untouched.
	if fx.outStore.Len() != 0 {
		t.Fatalf("ledger has %d events after rejected settlement", fx.outStore.Len())
	}
	after := policy.Snapshot()
	if len(after.Arms) != len(before.Arms) || after.TotalPulls != before.TotalPulls {
		t.Fatal("policy mutated by rejected settlement")
	}
	for i := range after.Arms {
		if after.Arms[i] != before.Arms[i] {
			t.Fatalf("arm %d mutated by rejected settlement", i)
		}
	}
	// The honest settlement still works afterwards.
	ev2 := settleEvent(d1, j1, 1, outcome.StatusAccepted, selected)
	if code, resp := fx.settle(t, ev2, true); code != http.StatusOK || !resp.Learned {
		t.Fatalf("honest settle=%d %+v", code, resp)
	}
}

// B2: multi-attempt outcomes must name only eligible arms; an arm outside
// the decision's eligible set is rejected.
func TestSettleRejectsIneligibleFallbackArm(t *testing.T) {
	policy := thompson.NewDefault("a", "b")
	fx := newVerifiedFixture(t, policy, map[string]Provider{"a": NewFakeProvider("a"), "b": NewFakeProvider("b")}, nil)
	defer fx.close(t)

	d1, j1, code := fx.serve(t, `{}`)
	if code != http.StatusOK {
		t.Fatalf("serve=%d", code)
	}
	selected := fx.mem.Started[0].SelectedArmID
	ev := settleEvent(d1, j1, 1, outcome.StatusAccepted, selected)
	ev.Attempts = append(ev.Attempts, verifiedAttempt("a2", 1, "ghost", outcome.VerifiedFailure))
	if code, _ := fx.settle(t, ev, true); code != http.StatusConflict {
		t.Fatalf("ineligible fallback arm status=%d want 409", code)
	}
	if fx.outStore.Len() != 0 {
		t.Fatal("ledger mutated by rejected settlement")
	}
}

// B2: attempts mapped to no arm (human review) remain allowed and learn
// nothing at the arm level.
func TestSettleAllowsArmlessDecider(t *testing.T) {
	policy := thompson.NewDefault("a")
	fx := newVerifiedFixture(t, policy, map[string]Provider{"a": NewFakeProvider("a")}, nil)
	defer fx.close(t)

	d1, j1, code := fx.serve(t, `{}`)
	if code != http.StatusOK {
		t.Fatalf("serve=%d", code)
	}
	ev := settleEvent(d1, j1, 1, outcome.StatusAccepted, "a")
	ev.Attempts[0].ArmID = ""
	ev.Attempts[0].ExecutorID = "human-pool-b"
	if code, resp := fx.settle(t, ev, true); code != http.StatusOK || resp.Learned {
		t.Fatalf("armless settle=%d %+v (want applied, not learned)", code, resp)
	}
	if policy.TotalPulls() != 0 {
		t.Fatal("armless outcome moved a posterior")
	}
}

// B4: concurrent duplicate settlement reports honestly: exactly one caller
// sees applied=true, the rest see duplicate=true, and the policy moves once.
func TestConcurrentDuplicateSettlementFlags(t *testing.T) {
	policy := thompson.NewDefault("a", "b")
	fx := newVerifiedFixture(t, policy, map[string]Provider{"a": NewFakeProvider("a"), "b": NewFakeProvider("b")}, nil)
	defer fx.close(t)

	d1, j1, code := fx.serve(t, `{}`)
	if code != http.StatusOK {
		t.Fatalf("serve=%d", code)
	}
	ev := settleEvent(d1, j1, 1, outcome.StatusAccepted, fx.mem.Started[0].SelectedArmID)

	const n = 16
	type result struct {
		code int
		resp settleResponse
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, resp := fx.settle(t, ev, true)
			results[i] = result{code, resp}
		}(i)
	}
	wg.Wait()
	applied, dups := 0, 0
	for _, r := range results {
		if r.code != http.StatusOK {
			t.Fatalf("status=%d", r.code)
		}
		if r.resp.Applied {
			applied++
		}
		if r.resp.Duplicate {
			dups++
		}
	}
	if applied != 1 || dups != n-1 {
		t.Fatalf("applied=%d dups=%d want 1/%d", applied, dups, n-1)
	}
	if policy.TotalPulls() != 1 {
		t.Fatalf("pulls=%d want 1", policy.TotalPulls())
	}
}
