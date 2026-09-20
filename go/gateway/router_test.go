package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

func newTestRNG() *rand.Rand { return rand.New(rand.NewPCG(42, 42)) }

func newTestPolicy() *thompson.Policy {
	// Two arms with uniform priors for determinism in tests
	return thompson.NewDefault("openai/gpt-4", "anthropic/claude-3-opus")
}

func newRouterWithFake(policy *thompson.Policy, writer EvidenceWriter, provider Provider) *Router {
	reg := NewProviderRegistry()
	if provider != nil {
		reg.Register(provider)
	} else {
		// register fakes for all arms
		for _, id := range policy.EligibleArmIDs() {
			reg.Register(NewFakeProvider(id))
		}
	}
	rngFactory := func() *rand.Rand { return newTestRNG() }
	rt, _ := NewRouter(RouterConfig{Policy: policy, Registry: reg, Writer: writer, RNGFactory: rngFactory})
	return rt
}

func TestDecisionStartedEmittedBeforeExecution(t *testing.T) {
	policy := newTestPolicy()
	mem := &MemoryEvidenceWriter{}
	// Provider that records when it was invoked relative to evidence
	var startedBeforeInvoke bool
	prov := &orderCheckingProvider{
		id: "openai/gpt-4",
		check: func() {
			// DecisionStarted should already be present when provider is invoked
			if len(mem.Started) == 1 {
				startedBeforeInvoke = true
			}
		},
		allIDs: []string{"openai/gpt-4", "anthropic/claude-3-opus"},
		policy: policy,
	}
	reg := NewProviderRegistry()
	reg.Register(prov)
	reg.Register(NewFakeProvider("anthropic/claude-3-opus"))
	rt, _ := NewRouter(RouterConfig{Policy: policy, Registry: reg, Writer: mem, RNGFactory: func() *rand.Rand { return newTestRNG() }})
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"prompt":"hi"}`))
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	if !startedBeforeInvoke {
		t.Fatalf("DecisionStarted not emitted before provider execution: started=%d", len(mem.Started))
	}
	if rr.Code >= 500 {
		t.Fatalf("expected success, got %d body %s", rr.Code, rr.Body.String())
	}
}

type orderCheckingProvider struct {
	id     string
	check  func()
	policy *thompson.Policy
	allIDs []string
}

func (o *orderCheckingProvider) ID() string { return o.id }
func (o *orderCheckingProvider) Invoke(ctx context.Context, r *http.Request) (ProviderOutcome, error) {
	// Called only if selected; check ordering
	if o.check != nil {
		o.check()
	}
	return ProviderOutcome{Success: true, StatusCode: 200, ResponseBody: []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5}}`)}, nil
}

// Ensure import for context
var _ = fmt.Sprintf

func TestAllThreeEventsShareDecisionID(t *testing.T) {
	policy := newTestPolicy()
	mem := &MemoryEvidenceWriter{}
	rt := newRouterWithFake(policy, mem, nil)
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	if len(mem.Started) != 1 || len(mem.Observed) != 1 || len(mem.Learned) != 1 {
		t.Fatalf("expected 1 each, got %d %d %d", len(mem.Started), len(mem.Observed), len(mem.Learned))
	}
	id := mem.Started[0].DecisionID
	if mem.Observed[0].DecisionID != id || mem.Learned[0].DecisionID != id {
		t.Fatalf("decision_id mismatch: %s %s %s", mem.Started[0].DecisionID, mem.Observed[0].DecisionID, mem.Learned[0].DecisionID)
	}
	if id == "" {
		t.Fatal("empty decision_id")
	}
}

func TestSelectedArmMatchesProviderExecution(t *testing.T) {
	policy := newTestPolicy()
	mem := &MemoryEvidenceWriter{}
	var executedArm string
	reg := NewProviderRegistry()
	for _, id := range policy.EligibleArmIDs() {
		idCopy := id
		reg.Register(&captureProvider{id: idCopy, capture: &executedArm})
	}
	rt, _ := NewRouter(RouterConfig{Policy: policy, Registry: reg, Writer: mem, RNGFactory: func() *rand.Rand { return newTestRNG() }})
	req := httptest.NewRequest("POST", "/", nil)
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	if len(mem.Started) != 1 {
		t.Fatalf("no DecisionStarted")
	}
	if mem.Started[0].SelectedArmID != executedArm {
		t.Fatalf("selected %q != executed %q", mem.Started[0].SelectedArmID, executedArm)
	}
	if mem.Observed[0].ArmID != executedArm || mem.Learned[0].ArmID != executedArm {
		t.Fatalf("observed/learned arm mismatch")
	}
}

type captureProvider struct {
	id      string
	capture *string
}

func (c *captureProvider) ID() string { return c.id }
func (c *captureProvider) Invoke(ctx context.Context, r *http.Request) (ProviderOutcome, error) {
	*c.capture = c.id
	return ProviderOutcome{Success: true, StatusCode: 200, ResponseBody: []byte(`ok`)}, nil
}

func TestSampledScoresAreTrueSamplesNotMeans(t *testing.T) {
	// Use uniform priors Beta(1,1) mean=0.5. Sampled scores should vary and not equal 0.5
	policy := thompson.NewDefault("a", "b")
	mem := &MemoryEvidenceWriter{}
	rt := newRouterWithFake(policy, mem, nil)
	// Do many requests to see variance
	seenNonMean := false
	for i := 0; i < 20; i++ {
		mem2 := &MemoryEvidenceWriter{}
		// fresh router per iteration with same policy? policy state evolves, but first iterations are near uniform
		// reset policy for each iteration to keep Beta(1,1)
		p := thompson.NewDefault("a", "b")
		r := newRouterWithFake(p, mem2, nil)
		req := httptest.NewRequest("POST", "/", nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if len(mem2.Started) == 1 {
			for _, s := range mem2.Started[0].SampledScores {
				if math.Abs(s-0.5) > 0.01 {
					seenNonMean = true
				}
			}
		}
	}
	if !seenNonMean {
		t.Fatalf("sampled scores appear to be posterior means (all ~0.5) rather than samples")
	}
	// Also check decision matched argmax of sampled scores
	policy2 := thompson.NewDefault("a", "b")
	mem3 := &MemoryEvidenceWriter{}
	rt2 := newRouterWithFake(policy2, mem3, nil)
	req := httptest.NewRequest("POST", "/", nil)
	rr := httptest.NewRecorder()
	_ = rt // avoid unused
	rt2.ServeHTTP(rr, req)
	if len(mem3.Started) == 1 {
		// argmax of sampledScores should equal selected
		best := ""
		bestScore := math.Inf(-1)
		for id, s := range mem3.Started[0].SampledScores {
			if best == "" || s > bestScore {
				best, bestScore = id, s
			}
		}
		if best != mem3.Started[0].SelectedArmID {
			t.Fatalf("selected %q != argmax of sampled_scores %q", mem3.Started[0].SelectedArmID, best)
		}
	}
}

func TestProviderFailureProducesSuccessFalse(t *testing.T) {
	policy := thompson.NewDefault("a")
	// create a fake that returns 500
	fake := NewFakeProvider("a", FakeWithStatus(500))
	mem := &MemoryEvidenceWriter{}
	reg := NewProviderRegistry()
	reg.Register(fake)
	rt, _ := NewRouter(RouterConfig{Policy: policy, Registry: reg, Writer: mem, RNGFactory: func() *rand.Rand { return newTestRNG() }})
	req := httptest.NewRequest("POST", "/", nil)
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	if len(mem.Observed) != 1 {
		t.Fatal("no observed")
	}
	if mem.Observed[0].Success != false {
		t.Fatalf("expected success false for 500, got true")
	}
	if rr.Code == 200 {
		// Should still return provider's status or gateway 502? Our router writes provider status
		// For fake 500, StatusCode 500 will be written
	}
}

func TestMissingTokensProduceNullNotFabricated(t *testing.T) {
	policy := thompson.NewDefault("a")
	fake := NewFakeProvider("a") // no tokens
	mem := &MemoryEvidenceWriter{}
	reg := NewProviderRegistry()
	reg.Register(fake)
	rt, _ := NewRouter(RouterConfig{Policy: policy, Registry: reg, Writer: mem, RNGFactory: func() *rand.Rand { return newTestRNG() }})
	req := httptest.NewRequest("POST", "/", nil)
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	if len(mem.Observed) != 1 {
		t.Fatal("no observed")
	}
	if mem.Observed[0].InputTokens != nil || mem.Observed[0].OutputTokens != nil || mem.Observed[0].CostUSD != nil {
		t.Fatalf("expected nil tokens/cost for missing usage, got %+v", mem.Observed[0])
	}
	// JSON null check
	var m map[string]json.RawMessage
	b, _ := json.Marshal(mem.Observed[0])
	json.Unmarshal(b, &m)
	if string(m["input_tokens"]) != "null" {
		t.Fatalf("input_tokens JSON should be null, got %s", string(m["input_tokens"]))
	}
	if string(m["cost_usd"]) != "null" {
		t.Fatalf("cost_usd JSON should be null, got %s", string(m["cost_usd"]))
	}
}

func TestRewardIsExactlyRewardPolicy(t *testing.T) {
	policy := thompson.NewDefault("a")
	// Fake to control latency/success: we need deterministic latency for reward check.
	// Router measures wall latency; we can inject via provider that sleeps? Instead test computed reward equals policy.Reward
	fake := NewFakeProvider("a")
	mem := &MemoryEvidenceWriter{}
	reg := NewProviderRegistry()
	reg.Register(fake)
	// Use fixed reward policy; compute expected via same function router uses
	rt, _ := NewRouter(RouterConfig{Policy: policy, Registry: reg, Writer: mem, RNGFactory: func() *rand.Rand { return newTestRNG() }})
	req := httptest.NewRequest("POST", "/", nil)
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	if len(mem.Learned) != 1 || len(mem.Observed) != 1 {
		t.Fatal("missing learned/observed")
	}
	obs := mem.Observed[0]
	learned := mem.Learned[0]
	base := policy.ConfigSnapshot().Reward
	expected := computeReward(base, obs.LatencyMs, obs.Success, obs.CostUSD)
	if math.Abs(expected-learned.ComputedReward) > 1e-9 {
		t.Fatalf("reward mismatch %v != %v", expected, learned.ComputedReward)
	}
	// When cost nil, direct Reward with cost 0 and weight 0.15 would differ; we verify router's redistribution is used
	// So just assert computed is within [0,1]
	if learned.ComputedReward < 0 || learned.ComputedReward > 1 {
		t.Fatalf("reward out of range %v", learned.ComputedReward)
	}
}

func TestRecordOutcomeExecutesOnce(t *testing.T) {
	policy := thompson.NewDefault("a", "b")
	mem := &MemoryEvidenceWriter{}
	rt := newRouterWithFake(policy, mem, nil)
	req := httptest.NewRequest("POST", "/", nil)
	rr := httptest.NewRecorder()
	before := policy.TotalPulls()
	rt.ServeHTTP(rr, req)
	after := policy.TotalPulls()
	if after-before != 1 {
		t.Fatalf("expected 1 pull, got %d", after-before)
	}
	if len(mem.Learned) != 1 {
		t.Fatalf("expected 1 learned")
	}
	// Second request should also record once (different decision_id)
	req2 := httptest.NewRequest("POST", "/", nil)
	rr2 := httptest.NewRecorder()
	rt.ServeHTTP(rr2, req2)
	if policy.TotalPulls() != 2 {
		t.Fatalf("expected 2 pulls after second request, got %d", policy.TotalPulls())
	}
}

func TestPosteriorTransitions(t *testing.T) {
	policy := thompson.NewDefault("a")
	mem := &MemoryEvidenceWriter{}
	fake := NewFakeProvider("a", FakeWithStatus(200))
	reg := NewProviderRegistry()
	reg.Register(fake)
	// Need Bernoulli update; reward derived from latency/success.
	rt, _ := NewRouter(RouterConfig{Policy: policy, Registry: reg, Writer: mem, RNGFactory: func() *rand.Rand { return newTestRNG() }})
	req := httptest.NewRequest("POST", "/", nil)
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	if len(mem.Learned) != 1 {
		t.Fatal("no learned")
	}
	before := mem.Learned[0].PosteriorBefore
	after := mem.Learned[0].PosteriorAfter
	if before.Pulls != 0 {
		t.Fatalf("before pulls expected 0, got %d", before.Pulls)
	}
	// After pulls should be 1 before discount (if discount stationary). Since default discount 0, pulls increment by 1.
	if after.Pulls != 1 {
		t.Fatalf("after pulls expected 1, got %d", after.Pulls)
	}
	// Alpha+Beta should increment by 1 total (Bernoulli)
	if math.Abs((after.Alpha+after.Beta)-(before.Alpha+before.Beta+1)) > 1e-9 {
		t.Fatalf("concentration should increase by 1: before %v after %v", before, after)
	}
	// Ensure after differs from before
	if before.Alpha == after.Alpha && before.Beta == after.Beta {
		t.Fatal("posterior did not change")
	}
}

func TestConcurrentRequestsNoInterleavedJSON(t *testing.T) {
	policy := thompson.NewDefault("a", "b")
	mem := &MemoryEvidenceWriter{}
	rt := newRouterWithFake(policy, mem, nil)
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/", nil)
			rr := httptest.NewRecorder()
			rt.ServeHTTP(rr, req)
		}()
	}
	wg.Wait()
	if len(mem.Lines) != n*3 {
		t.Fatalf("expected %d lines, got %d", n*3, len(mem.Lines))
	}
	for i, line := range mem.Lines {
		var m map[string]interface{}
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("line %d not valid JSON: %v line=%s", i, err, string(line))
		}
		if _, ok := m["event_type"]; !ok {
			t.Fatalf("line %d missing event_type", i)
		}
		if _, ok := m["decision_id"]; !ok {
			t.Fatalf("line %d missing decision_id", i)
		}
	}
}

func TestEvidenceWriterFailureSurfaced(t *testing.T) {
	policy := thompson.NewDefault("a")
	mem := &MemoryEvidenceWriter{FailNext: fmt.Errorf("disk full")}
	reg := NewProviderRegistry()
	reg.Register(NewFakeProvider("a"))
	rt, _ := NewRouter(RouterConfig{Policy: policy, Registry: reg, Writer: mem, RNGFactory: func() *rand.Rand { return newTestRNG() }})
	req := httptest.NewRequest("POST", "/", nil)
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	if rr.Code != 500 {
		t.Fatalf("expected 500 on evidence write failure, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "evidence write failed") {
		t.Fatalf("expected evidence error in body, got %s", rr.Body.String())
	}
	// Ensure provider not invoked and no posterior update (pulls still 0)
	if policy.TotalPulls() != 0 {
		t.Fatalf("expected no pull on write failure, got %d", policy.TotalPulls())
	}
}

func TestRouterHealth(t *testing.T) {
	policy := newTestPolicy()
	mem := &MemoryEvidenceWriter{}
	rt := newRouterWithFake(policy, mem, nil)
	req := httptest.NewRequest("GET", "/health", nil)
	rr := httptest.NewRecorder()
	rt.HealthHandler(rr, req)
	if rr.Code != 200 {
		t.Fatalf("health expected 200, got %d", rr.Code)
	}
	var m map[string]string
	json.Unmarshal(rr.Body.Bytes(), &m)
	if m["status"] != "ok" {
		t.Fatalf("health body %s", rr.Body.String())
	}
}

func TestRouterWithFakeProviderIntegration(t *testing.T) {
	policy := thompson.NewDefault("a")
	mem := &MemoryEvidenceWriter{}
	reg := NewProviderRegistry()
	reg.Register(NewFakeProvider("a", FakeWithTokens(10, 5), FakeWithCost(0.002)))
	rt, _ := NewRouter(RouterConfig{Policy: policy, Registry: reg, Writer: mem, RNGFactory: func() *rand.Rand { return newTestRNG() }})
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"a","prompt":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("expected 200, got %d body %s", rr.Code, rr.Body.String())
	}
	if len(mem.Observed) != 1 || len(mem.Learned) != 1 {
		t.Fatalf("missing evidence")
	}
	if mem.Observed[0].InputTokens == nil || *mem.Observed[0].InputTokens != 10 {
		t.Fatalf("input tokens not preserved")
	}
	if mem.Observed[0].CostUSD == nil || math.Abs(*mem.Observed[0].CostUSD-0.002) > 1e-9 {
		t.Fatalf("cost not preserved")
	}
}

func TestRouterFileLedgerEndToEnd(t *testing.T) {
	policy := thompson.NewDefault("a", "b")
	tmp, err := osCreateTemp()
	if err != nil {
		t.Fatal(err)
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)
	writer, err := NewFileEvidenceWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reg := NewProviderRegistry()
	for _, id := range policy.EligibleArmIDs() {
		reg.Register(NewFakeProvider(id))
	}
	rt, _ := NewRouter(RouterConfig{Policy: policy, Registry: reg, Writer: writer, RNGFactory: func() *rand.Rand { return newTestRNG() }})
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"prompt":"secret-not-in-ledger"}`))
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	writer.Close()
	data, _ := os.ReadFile(path)
	if len(data) == 0 {
		t.Fatal("empty ledger")
	}
	lines := splitLinesFile(data)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d data=%s", len(lines), string(data))
	}
	for i, line := range lines {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("line %d invalid JSON: %v", i, err)
		}
		if _, ok := m["decision_id"]; !ok {
			t.Fatalf("line %d missing decision_id", i)
		}
		if containsBytes(data, []byte("secret-not-in-ledger")) {
			t.Fatal("ledger must not contain raw prompt")
		}
	}
	// All three share same decision_id
	var e1 DecisionStarted
	var e2 ExecutionObserved
	var e3 DecisionLearned
	json.Unmarshal(lines[0], &e1)
	json.Unmarshal(lines[1], &e2)
	json.Unmarshal(lines[2], &e3)
	if e1.DecisionID != e2.DecisionID || e2.DecisionID != e3.DecisionID {
		t.Fatalf("decision_id mismatch %s %s %s", e1.DecisionID, e2.DecisionID, e3.DecisionID)
	}
}

// helpers for file test (avoid import cycle)
func osCreateTemp() (*os.File, error) { return os.CreateTemp("", "rt-file-*.jsonl") }
func splitLinesFile(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i+1 > start {
				out = append(out, b[start:i+1])
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
func containsBytes(b, sub []byte) bool {
	return strings.Contains(string(b), string(sub))
}
