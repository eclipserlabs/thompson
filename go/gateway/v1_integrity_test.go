package gateway

import (
	"math/rand/v2"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

func TestPrimaryBodyPreservedWhenShadowBodyTooLarge(t *testing.T) {
	policy := thompson.NewDefault("a", "b")
	mem := &MemoryEvidenceWriter{}
	reg := NewProviderRegistry()
	// Providers that capture full body
	pa := &countingProvider{id: "a"}
	pb := &countingProvider{id: "b"}
	reg.Register(pa)
	reg.Register(pb)
	// Router with small shadow limit 10 bytes
	rt, _ := NewRouter(RouterConfig{
		Policy: policy, Registry: reg, Writer: mem,
		RNGFactory: func() *rand.Rand { return rand.New(rand.NewPCG(42, 42)) },
		ShadowEligibility: HeaderShadowEligibility{}, ShadowSampleRate: 1,
		ShadowMaxBodyBytes: 10, ShadowTimeout: 200 * time.Millisecond, ShadowRNGSeed: 1,
	})
	body := strings.Repeat("x", 20) // 20 > 10
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("X-Shadow-Eligible", "true")
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("primary should succeed, got %d", rr.Code)
	}
	// Primary must have received full 20 bytes
	// Which provider was primary?
	primaryArm := mem.Started[0].SelectedArmID
	var primaryBody string
	if pa.id == primaryArm {
		primaryBody = pa.body
	} else {
		primaryBody = pb.body
	}
	if primaryBody != body {
		t.Fatalf("primary body truncated: got %q len %d want %q len %d", primaryBody, len(primaryBody), body, len(body))
	}
	// No shadow provider call (since body too large, shadow suppressed)
	if len(mem.Shadow) != 0 {
		t.Fatalf("expected no shadow execution for oversized body, got %d", len(mem.Shadow))
	}
	// Ledger must not claim ShadowExecutionObserved
	for _, line := range mem.Lines {
		if strings.Contains(string(line), "ShadowExecutionObserved") {
			t.Fatal("ledger claims ShadowExecutionObserved for oversized request")
		}
	}
	// Must have ShadowSkipped with BODY_TOO_LARGE
	found := false
	for _, s := range mem.Skipped {
		if s.Reason == "BODY_TOO_LARGE" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected ShadowSkipped BODY_TOO_LARGE, got %+v", mem.Skipped)
	}
}

func TestShadowDoesNotDelayPrimaryLearning(t *testing.T) {
	// Force primary to be "a" (fast) by biasing
	policy2 := thompson.New(thompson.DefaultConfig(), thompson.ExactSampler{})
	policy2.AddArmWithPrior("a", thompson.NewInformedPrior(100, 1))
	policy2.AddArmWithPrior("b", thompson.NewInformedPrior(1, 100))
	// Use policy2
	mem2 := &MemoryEvidenceWriter{}
	reg2 := NewProviderRegistry()
	reg2.Register(NewFakeProvider("a"))
	reg2.Register(&slowProvider{id: "b", delay: 5 * time.Second})
	rt, _ := NewRouter(RouterConfig{
		Policy: policy2, Registry: reg2, Writer: mem2,
		RNGFactory: func() *rand.Rand { return rand.New(rand.NewPCG(1, 1)) },
		ShadowEligibility: HeaderShadowEligibility{}, ShadowSampleRate: 1,
		ShadowTimeout: 100 * time.Millisecond, ShadowRNGSeed: 1,
	})
	// Ensure shadow will be "b" (non-primary)
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
	req.Header.Set("X-Shadow-Eligible", "true")
	rr := httptest.NewRecorder()
	start := time.Now()
	rt.ServeHTTP(rr, req)
	elapsed := time.Since(start)
	// Primary learning should complete quickly (< 1s), not wait for 5s shadow
	if elapsed > 1*time.Second {
		t.Fatalf("primary learning delayed by shadow: elapsed %v", elapsed)
	}
	if mem2.Learned[0].TotalPullsAfter != 1 {
		t.Fatalf("total pulls not incremented immediately")
	}
	// Shadow should still have executed (with timeout, success false, but evidence exists)
	if len(mem2.Shadow) != 1 {
		t.Fatalf("shadow should still have executed despite delay, got %d", len(mem2.Shadow))
	}
	// DecisionLearned must occur before ShadowExecutionObserved in ledger order
	var learnedIdx, shadowIdx int = -1, -1
	for i, line := range mem2.Lines {
		if strings.Contains(string(line), "DecisionLearned") {
			learnedIdx = i
		}
		if strings.Contains(string(line), "ShadowExecutionObserved") {
			shadowIdx = i
		}
	}
	if learnedIdx == -1 || shadowIdx == -1 {
		t.Fatalf("missing events")
	}
	if learnedIdx > shadowIdx {
		t.Fatalf("DecisionLearned must occur before ShadowExecutionObserved, got learned %d shadow %d", learnedIdx, shadowIdx)
	}
}

func TestPrimaryPosteriorIdenticalWithVaryingShadowRate(t *testing.T) {
	// Same primary RNG and provider outcomes -> posterior after must be identical regardless of shadow rate
	for _, rate := range []float64{0, 1} {
		policy := thompson.NewDefault("a", "b")
		// Use same seed and same fake provider that returns same reward
		mem := &MemoryEvidenceWriter{}
		reg := NewProviderRegistry()
		for _, id := range []string{"a", "b"} {
			reg.Register(NewFakeProvider(id))
		}
		seed := uint64(999)
		rngFactory := func() *rand.Rand { return rand.New(rand.NewPCG(seed, seed)) }
		rt, _ := NewRouter(RouterConfig{
			Policy: policy, Registry: reg, Writer: mem,
			RNGFactory: rngFactory,
			ShadowEligibility: HeaderShadowEligibility{}, ShadowSampleRate: rate, ShadowRNGSeed: 1,
		})
		req := httptest.NewRequest("POST", "/", nil)
		if rate == 1 {
			req.Header.Set("X-Shadow-Eligible", "true")
		}
		rr := httptest.NewRecorder()
		rt.ServeHTTP(rr, req)
		// Capture posterior after
		if rate == 0 {
			// store for comparison
			_ = mem
		}
		_ = rate
	}
	// More direct: two policies with same seed, same provider, different shadow rates, check posterior after
	policy0 := thompson.NewDefault("a", "b")
	policy1 := thompson.NewDefault("a", "b")
	mem0 := &MemoryEvidenceWriter{}
	mem1 := &MemoryEvidenceWriter{}
	reg0 := NewProviderRegistry()
	reg1 := NewProviderRegistry()
	for _, id := range []string{"a", "b"} {
		reg0.Register(NewFakeProvider(id))
		reg1.Register(NewFakeProvider(id))
	}
	seed := uint64(777)
	factory := func() *rand.Rand { return rand.New(rand.NewPCG(seed, seed>>1)) }
	rt0, _ := NewRouter(RouterConfig{Policy: policy0, Registry: reg0, Writer: mem0, RNGFactory: factory, ShadowEligibility: HeaderShadowEligibility{}, ShadowSampleRate: 0})
	rt1, _ := NewRouter(RouterConfig{Policy: policy1, Registry: reg1, Writer: mem1, RNGFactory: factory, ShadowEligibility: HeaderShadowEligibility{}, ShadowSampleRate: 1, ShadowRNGSeed: 42})
	req0 := httptest.NewRequest("POST", "/", nil)
	req0.Header.Set("X-Shadow-Eligible", "true")
	rr0 := httptest.NewRecorder()
	rt0.ServeHTTP(rr0, req0)
	req1 := httptest.NewRequest("POST", "/", nil)
	req1.Header.Set("X-Shadow-Eligible", "true")
	rr1 := httptest.NewRecorder()
	rt1.ServeHTTP(rr1, req1)
	if mem0.Started[0].SelectedArmID != mem1.Started[0].SelectedArmID {
		t.Fatalf("primary selection changed with shadow rate: %s vs %s", mem0.Started[0].SelectedArmID, mem1.Started[0].SelectedArmID)
	}
	// Posterior after must be identical (since same reward)
	if mem0.Learned[0].PosteriorAfter != mem1.Learned[0].PosteriorAfter {
		t.Fatalf("posterior after differs with shadow rate: %+v vs %+v", mem0.Learned[0].PosteriorAfter, mem1.Learned[0].PosteriorAfter)
	}
}

func TestUniformShadowProbabilityPersisted(t *testing.T) {
	for _, k := range []int{2, 4} {
		arms := make([]string, k)
		for i := 0; i < k; i++ {
			arms[i] = string(rune('a' + i))
		}
		policy := thompson.NewDefault(arms...)
		mem := &MemoryEvidenceWriter{}
		reg := NewProviderRegistry()
		for _, id := range arms {
			reg.Register(NewFakeProvider(id))
		}
		rt, _ := NewRouter(RouterConfig{
			Policy: policy, Registry: reg, Writer: mem,
			RNGFactory: func() *rand.Rand { return rand.New(rand.NewPCG(1, 1)) },
			ShadowEligibility: HeaderShadowEligibility{}, ShadowSampleRate: 1, ShadowRNGSeed: 1,
		})
		req := httptest.NewRequest("POST", "/", nil)
		req.Header.Set("X-Shadow-Eligible", "true")
		rr := httptest.NewRecorder()
		rt.ServeHTTP(rr, req)
		if len(mem.Shadow) != 1 {
			t.Fatalf("expected shadow for k=%d", k)
		}
		expectedProb := 1.0 / float64(k-1)
		if mem.Shadow[0].ShadowSelectionProbability != expectedProb {
			t.Fatalf("k=%d expected prob %v got %v", k, expectedProb, mem.Shadow[0].ShadowSelectionProbability)
		}
		if mem.Shadow[0].EligibleArmCount != k || mem.Shadow[0].ShadowCandidateCount != k-1 {
			t.Fatalf("eligible %d candidate %d", mem.Shadow[0].EligibleArmCount, mem.Shadow[0].ShadowCandidateCount)
		}
		if mem.Shadow[0].ShadowSelectionPolicyID != "uniform-non-primary-v1" {
			t.Fatalf("policy id")
		}
		if mem.Shadow[0].PrimaryLoggingPolicyID != policy.LoggingPolicyID() {
			t.Fatalf("primary policy id: got %q want derived %q",
				mem.Shadow[0].PrimaryLoggingPolicyID, policy.LoggingPolicyID())
		}
		if mem.Shadow[0].PrimaryLoggingPolicyID != mem.Started[0].LoggingPolicyID {
			t.Fatalf("live/shadow policy identity diverged: %q vs %q",
				mem.Started[0].LoggingPolicyID, mem.Shadow[0].PrimaryLoggingPolicyID)
		}
	}
}

func TestNoSampledScoreLabelledAsPropensity(t *testing.T) {
	policy := thompson.NewDefault("a", "b")
	mem := &MemoryEvidenceWriter{}
	reg := NewProviderRegistry()
	for _, id := range []string{"a", "b"} {
		reg.Register(NewFakeProvider(id))
	}
	rt, _ := NewRouter(RouterConfig{
		Policy: policy, Registry: reg, Writer: mem,
		RNGFactory: func() *rand.Rand { return rand.New(rand.NewPCG(1, 1)) },
		ShadowEligibility: HeaderShadowEligibility{}, ShadowSampleRate: 1, ShadowRNGSeed: 1,
	})
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("X-Shadow-Eligible", "true")
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	// Ensure no field named propensity is sampled_score
	for _, line := range mem.Lines {
		if strings.Contains(string(line), "propensity") && strings.Contains(string(line), "sampled_score") {
			t.Fatal("sampled_score labelled as propensity")
		}
	}
	// Check that shadow event has selection probability, not Thompson propensity
	if len(mem.Shadow) == 1 {
		if mem.Shadow[0].ShadowSelectionProbability == 0 {
			t.Fatal("shadow selection probability missing")
		}
	}
}

func TestOrderDecisionLearnedBeforeShadow(t *testing.T) {
	policy := thompson.NewDefault("a", "b")
	mem := &MemoryEvidenceWriter{}
	reg := NewProviderRegistry()
	for _, id := range []string{"a", "b"} {
		reg.Register(NewFakeProvider(id))
	}
	rt, _ := NewRouter(RouterConfig{
		Policy: policy, Registry: reg, Writer: mem,
		RNGFactory: func() *rand.Rand { return rand.New(rand.NewPCG(1, 1)) },
		ShadowEligibility: HeaderShadowEligibility{}, ShadowSampleRate: 1, ShadowRNGSeed: 1,
	})
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("X-Shadow-Eligible", "true")
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	var learnedIdx, shadowIdx = -1, -1
	for i, line := range mem.Lines {
		s := string(line)
		if strings.Contains(s, "DecisionLearned") {
			learnedIdx = i
		}
		if strings.Contains(s, "ShadowExecutionObserved") {
			shadowIdx = i
		}
	}
	if learnedIdx == -1 || shadowIdx == -1 {
		t.Fatalf("missing events learned %d shadow %d", learnedIdx, shadowIdx)
	}
	if learnedIdx > shadowIdx {
		t.Fatalf("DecisionLearned must be before ShadowExecutionObserved")
	}
}
