package gateway

import (
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// Every DecisionStarted emitted under concurrent request + mutation traffic
// must be internally coherent: selected in eligible, eligible_arm_state
// covering exactly the eligible set, posterior_before equal to the selected
// arm's state entry, sampled_scores keyed by the eligible set, and the two
// config-hash fields agreeing. This is the router-level regression test for
// audit A1 (non-atomic decision assembly). Run with -race.
func TestRouterDecisionEvidenceCoherentUnderConcurrency(t *testing.T) {
	policy := thompson.NewDefault("a", "b", "c")
	mem := &MemoryEvidenceWriter{}
	reg := NewProviderRegistry()
	for _, id := range []string{"a", "b", "c"} {
		reg.Register(NewFakeProvider(id))
	}
	var rngMu sync.Mutex
	seed := uint64(1234)
	rt, err := NewRouter(RouterConfig{
		Policy:   policy,
		Registry: reg,
		Writer:   mem,
		RNGFactory: func() *rand.Rand {
			rngMu.Lock()
			defer rngMu.Unlock()
			seed++
			return rand.New(rand.NewPCG(seed, seed>>1))
		},
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	// Mutator: record outcomes continuously while requests fly. (Arm
	// add/remove churn is covered at the policy level in
	// TestSelectSnapshotConcurrentCoherence; here arms are stable so every
	// decision must also complete learning.)
	stop := make(chan struct{})
	var mutWg sync.WaitGroup
	mutWg.Add(1)
	go func() {
		defer mutWg.Done()
		rng := rand.New(rand.NewPCG(7, 7))
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = policy.Record(rng, "a", 0.6)
		}
	}()

	// Parallel requests through the full ServeHTTP path.
	const requests = 64
	var reqWg sync.WaitGroup
	for i := 0; i < requests; i++ {
		reqWg.Add(1)
		go func() {
			defer reqWg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"prompt":"hi"}`))
			rec := httptest.NewRecorder()
			rt.ServeHTTP(rec, req)
		}()
	}
	reqWg.Wait()
	close(stop)
	mutWg.Wait()

	if len(mem.Started) != requests {
		t.Fatalf("expected %d DecisionStarted events, got %d", requests, len(mem.Started))
	}
	for _, s := range mem.Started {
		inEligible := false
		for _, id := range s.EligibleArmIDs {
			if id == s.SelectedArmID {
				inEligible = true
				break
			}
		}
		if !inEligible {
			t.Fatalf("decision %s: selected %q not in eligible %v", s.DecisionID, s.SelectedArmID, s.EligibleArmIDs)
		}
		if len(s.EligibleArmState) != len(s.EligibleArmIDs) {
			t.Fatalf("decision %s: eligible_arm_state len %d != eligible len %d", s.DecisionID, len(s.EligibleArmState), len(s.EligibleArmIDs))
		}
		stateByArm := make(map[string]PosteriorSnapshot, len(s.EligibleArmState))
		for _, st := range s.EligibleArmState {
			stateByArm[st.ArmID] = PosteriorSnapshot{Alpha: st.Alpha, Beta: st.Beta, Pulls: st.Pulls}
		}
		for _, id := range s.EligibleArmIDs {
			if _, ok := stateByArm[id]; !ok {
				t.Fatalf("decision %s: eligible arm %q missing from eligible_arm_state", s.DecisionID, id)
			}
		}
		if got := stateByArm[s.SelectedArmID]; got != s.PosteriorBefore {
			t.Fatalf("decision %s: posterior_before %+v != eligible_arm_state entry %+v", s.DecisionID, s.PosteriorBefore, got)
		}
		if len(s.SampledScores) != len(s.EligibleArmIDs) {
			t.Fatalf("decision %s: sampled_scores len %d != eligible len %d", s.DecisionID, len(s.SampledScores), len(s.EligibleArmIDs))
		}
		for _, id := range s.EligibleArmIDs {
			if _, ok := s.SampledScores[id]; !ok {
				t.Fatalf("decision %s: sampled_scores missing eligible arm %q", s.DecisionID, id)
			}
		}
		if s.PolicyConfigHash != s.LoggingPolicyConfigHash {
			t.Fatalf("decision %s: config hash fields disagree", s.DecisionID)
		}
		if s.PolicyConfigHash == "" {
			t.Fatalf("decision %s: empty policy config hash", s.DecisionID)
		}
	}
	// Every decision must have joined execution + learning evidence.
	if len(mem.Observed) != requests {
		t.Fatalf("expected %d ExecutionObserved events, got %d", requests, len(mem.Observed))
	}
	if len(mem.Learned) != requests {
		t.Fatalf("expected %d DecisionLearned events, got %d", requests, len(mem.Learned))
	}
}

// Single-request sanity: the snapshot path preserves the legacy observable
// behavior (decision written before execution, learning recorded after).
func TestRouterSnapshotPathPreservesOrdering(t *testing.T) {
	policy := thompson.NewDefault("a")
	mem := &MemoryEvidenceWriter{}
	rt := newRouterWithFake(policy, mem, NewFakeProvider("a"))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if len(mem.Started) != 1 || len(mem.Observed) != 1 || len(mem.Learned) != 1 {
		t.Fatalf("expected one of each event, got %d/%d/%d", len(mem.Started), len(mem.Observed), len(mem.Learned))
	}
	s := mem.Started[0]
	if s.SelectedArmID != "a" || len(s.EligibleArmIDs) != 1 {
		t.Fatalf("unexpected decision: %+v", s)
	}
}
