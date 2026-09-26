package gateway

import (
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// The recorded logging-policy identity must be derived from the live
// configuration and identical across live and shadow paths (audit A3).
// Only exact-Thompson rows may pass the OPE reliability gate.
func TestRouterLoggingPolicyIdentityLiveEqualsShadow(t *testing.T) {
	policy := thompson.NewDefault("a", "b")
	mem := &MemoryEvidenceWriter{}
	reg := NewProviderRegistry()
	reg.Register(NewFakeProvider("a"))
	reg.Register(NewFakeProvider("b"))
	rt, err := NewRouter(RouterConfig{
		Policy: policy, Registry: reg, Writer: mem,
		RNGFactory:        func() *rand.Rand { return rand.New(rand.NewPCG(9, 9)) },
		ShadowEligibility: HeaderShadowEligibility{},
		ShadowSampleRate:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	req.Header.Set("X-Shadow-Eligible", "true")
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if len(mem.Started) != 1 {
		t.Fatalf("started=%d", len(mem.Started))
	}
	want := policy.LoggingPolicyID()
	if want != "exact-thompson-v1" {
		t.Fatalf("default policy ID=%q", want)
	}
	if mem.Started[0].LoggingPolicyID != want {
		t.Fatalf("live ID=%q want %q", mem.Started[0].LoggingPolicyID, want)
	}
	if len(mem.Shadow) != 1 {
		t.Fatalf("shadow events=%d (want 1 observed)", len(mem.Shadow))
	}
	if mem.Shadow[0].PrimaryLoggingPolicyID != want {
		t.Fatalf("shadow primary ID=%q want %q", mem.Shadow[0].PrimaryLoggingPolicyID, want)
	}
}

func TestNonThompsonDecisionsAreOPEIneligible(t *testing.T) {
	cfg := thompson.DefaultConfig()
	cfg.Selection = thompson.Selection{Kind: thompson.UCBRegularized, C: 2.0, UntilPulls: 30}
	policy := thompson.New(cfg, thompson.ExactSampler{})
	policy.AddArm("a")
	policy.AddArm("b")
	mem := &MemoryEvidenceWriter{}
	reg := NewProviderRegistry()
	reg.Register(NewFakeProvider("a"))
	reg.Register(NewFakeProvider("b"))
	rt, err := NewRouter(RouterConfig{
		Policy: policy, Registry: reg, Writer: mem,
		RNGFactory: func() *rand.Rand { return rand.New(rand.NewPCG(11, 11)) },
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	rt.ServeHTTP(httptest.NewRecorder(), req)
	if len(mem.Started) != 1 {
		t.Fatalf("started=%d", len(mem.Started))
	}
	if got := mem.Started[0].LoggingPolicyID; got != "ucb-regularized-v1" {
		t.Fatalf("UCB decision logged as %q: silent denominator corruption (audit A3)", got)
	}
	// The OPE gate must refuse the row: neither estimator models UCB.
	decisions, err := ledgerFromStarted(mem)
	if err != nil {
		t.Fatal(err)
	}
	row := ToBanditLogWith(decisions[0], NewMCEstimator(2000, 7))
	if row.Status != OPEIneligible {
		t.Fatalf("UCB row status=%q reason=%q: gate admitted a non-Thompson denominator", row.Status, row.Reason)
	}
}

func ledgerFromStarted(mem *MemoryEvidenceWriter) ([]*LedgerDecision, error) {
	out := make([]*LedgerDecision, 0, len(mem.Started))
	for i := range mem.Started {
		s := mem.Started[i]
		out = append(out, &LedgerDecision{Started: &s, Eligible: s.EligibleArmIDs})
	}
	return out, nil
}
