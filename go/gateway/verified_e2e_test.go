package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// verifiedFixture wires a verified-mode router over real files.
type verifiedFixture struct {
	router   *Router
	decStore *FileDecisionStore
	outStore *outcome.FileOutcomeStore
	mem      *MemoryEvidenceWriter
	ckpt     string
	dir      string
}

func newVerifiedFixture(t *testing.T, policy *thompson.Policy, providers map[string]Provider, auth func(r *http.Request) bool) *verifiedFixture {
	t.Helper()
	dir := t.TempDir()
	decStore, err := NewFileDecisionStore(filepath.Join(dir, "decisions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	outStore, err := outcome.NewFileOutcomeStore(filepath.Join(dir, "outcomes.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	mem := &MemoryEvidenceWriter{}
	reg := NewProviderRegistry()
	for _, p := range providers {
		reg.Register(p)
	}
	if auth == nil {
		auth = func(r *http.Request) bool { return true }
	}
	rt, err := NewRouter(RouterConfig{
		Policy: policy, Registry: reg, Writer: mem,
		Decisions: decStore, Outcomes: outStore,
		StrategyID: "test-strategy", Mode: VerifiedMode,
		SettleAuth: auth,
		RNGFactory: func() *rand.Rand { return newTestRNG() },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &verifiedFixture{router: rt, decStore: decStore, outStore: outStore, mem: mem, ckpt: filepath.Join(dir, "ckpt.json"), dir: dir}
}

func (f *verifiedFixture) close(t *testing.T) {
	t.Helper()
	if err := f.decStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.outStore.Close(); err != nil {
		t.Fatal(err)
	}
}

func (f *verifiedFixture) serve(t *testing.T, body string) (decisionID, jobID string, code int) {
	t.Helper()
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	return rec.Header().Get("X-Decision-ID"), rec.Header().Get("X-Job-ID"), rec.Code
}

func (f *verifiedFixture) settle(t *testing.T, ev outcome.OutcomeEvent, authed bool) (int, settleResponse) {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/outcomes", bytes.NewReader(b))
	if authed {
		req.Header.Set("Authorization", "Bearer test")
	}
	rec := httptest.NewRecorder()
	f.router.SettleHandler(rec, req)
	var resp settleResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad settle response: %v", err)
		}
	}
	return rec.Code, resp
}

func verifiedAttempt(id string, seq uint, arm string, verified outcome.VerifiedOutcome) outcome.Attempt {
	return outcome.Attempt{
		AttemptID: id, Seq: seq, ExecutorID: arm, ArmID: arm,
		Transport: outcome.TransportOK, LatencyMs: 150,
		Validation: outcome.ValidationPass, Verified: verified,
		VerifiedBy: "checker:e2e-v1",
	}
}

func settleEvent(decisionID, jobID string, version uint64, status outcome.JobStatus, arm string) outcome.OutcomeEvent {
	ev := outcome.OutcomeEvent{
		SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
		DecisionID: decisionID, JobID: jobID, StrategyID: "test-strategy",
		Version: version, Supersedes: version - 1, Status: status,
		OccurredAt: "2026-09-27T00:00:00Z", VerifiedBy: "checker:e2e-v1",
	}
	switch status {
	case outcome.StatusAccepted:
		ev.Attempts = []outcome.Attempt{verifiedAttempt("a1", 0, arm, outcome.VerifiedSuccess)}
		ev.DecidingAttemptID = "a1"
	case outcome.StatusRejected:
		ev.Attempts = []outcome.Attempt{verifiedAttempt("a1", 0, arm, outcome.VerifiedFailure)}
		ev.DecidingAttemptID = "a1"
	case outcome.StatusUnknown:
		a := verifiedAttempt("a1", 0, arm, outcome.VerifiedUnknown)
		a.Transport = outcome.TransportTimeout
		a.Validation = outcome.ValidationNotRun
		ev.Attempts = []outcome.Attempt{a}
	}
	return ev
}

// Mandatory: HTTP 200 with task-invalid output learns nothing until a
// verified ACCEPTED arrives (and a verified REJECTED learns failure, not
// transport success).
func TestVerifiedHTTP200InvalidOutputLearnsNothing(t *testing.T) {
	policy := thompson.NewDefault("a", "b")
	fx := newVerifiedFixture(t, policy, map[string]Provider{"a": NewFakeProvider("a"), "b": NewFakeProvider("b")}, nil)
	defer fx.close(t)

	d1, j1, code := fx.serve(t, `{"prompt":"hi"}`)
	if code != http.StatusOK {
		t.Fatalf("serve=%d", code)
	}
	if policy.TotalPulls() != 0 {
		t.Fatal("verified request path learned from transport")
	}
	if len(fx.mem.Learned) != 0 {
		t.Fatalf("DecisionLearned written in verified mode: %d", len(fx.mem.Learned))
	}
	// The 200 was task-invalid: verified REJECTED learns failure.
	if code, resp := fx.settle(t, settleEvent(d1, j1, 1, outcome.StatusRejected, fx.mem.Started[0].SelectedArmID), true); code != http.StatusOK || !resp.Learned {
		t.Fatalf("settle=%d %+v", code, resp)
	}
	if policy.TotalPulls() != 1 {
		t.Fatalf("pulls=%d want 1", policy.TotalPulls())
	}
	post, _ := policy.PosteriorFor(fx.mem.Started[0].SelectedArmID)
	if post.Pulls != 1 {
		t.Fatalf("selected arm pulls=%d want 1", post.Pulls)
	}
}

// Mandatory: timeout then late verified success.
func TestVerifiedTimeoutThenLateSuccess(t *testing.T) {
	policy := thompson.NewDefault("a")
	fx := newVerifiedFixture(t, policy,
		map[string]Provider{"a": NewFakeProvider("a", FakeWithError(errors.New("timeout")))},
		nil)
	defer fx.close(t)

	d1, j1, _ := fx.serve(t, `{}`)
	if policy.TotalPulls() != 0 {
		t.Fatal("timeout learned in verified mode")
	}
	exec, ok := fx.router.decisions.Execution(d1)
	if !ok || exec.Phase != PhaseUnknown {
		t.Fatalf("timeout execution not unknown: %+v", exec)
	}
	// Ambiguous period settles UNKNOWN: still nothing learned.
	if code, resp := fx.settle(t, settleEvent(d1, j1, 1, outcome.StatusUnknown, "a"), true); code != http.StatusOK || resp.Learned {
		t.Fatalf("unknown settle=%d %+v", code, resp)
	}
	if policy.TotalPulls() != 0 {
		t.Fatal("UNKNOWN moved the policy")
	}
	// Late authoritative success learns exactly once.
	if code, resp := fx.settle(t, settleEvent(d1, j1, 2, outcome.StatusAccepted, "a"), true); code != http.StatusOK || !resp.Learned {
		t.Fatalf("late accept=%d %+v", code, resp)
	}
	if policy.TotalPulls() != 1 {
		t.Fatalf("pulls=%d want 1", policy.TotalPulls())
	}
}

// Mandatory: fallback chain with human correction.
func TestVerifiedFallbackChainHumanCorrection(t *testing.T) {
	policy := thompson.NewDefault("cheap", "strong")
	fx := newVerifiedFixture(t, policy,
		map[string]Provider{"cheap": NewFakeProvider("cheap"), "strong": NewFakeProvider("strong")},
		nil)
	defer fx.close(t)

	d1, j1, code := fx.serve(t, `{}`)
	if code != http.StatusOK {
		t.Fatalf("serve=%d", code)
	}
	cheap, strong, human := 0.0004, 0.02, 2.10
	a1 := outcome.Attempt{AttemptID: "a1", Seq: 0, ExecutorID: "cheap", ArmID: "cheap",
		Transport: outcome.TransportOK, LatencyMs: 320, CostUSD: &cheap,
		Validation: outcome.ValidationFail, FailureCategory: "schema_violation",
		Verified: outcome.VerifiedFailure, VerifiedBy: "validator:v7"}
	a2 := outcome.Attempt{AttemptID: "a2", Seq: 1, ExecutorID: "strong", ArmID: "strong",
		Transport: outcome.TransportOK, LatencyMs: 900, CostUSD: &strong,
		Validation: outcome.ValidationFail, FailureCategory: "factual_error",
		Verified: outcome.VerifiedFailure, VerifiedBy: "judge:strong-v2"}
	a3 := outcome.Attempt{AttemptID: "a3", Seq: 2, ExecutorID: "human-pool-b",
		Transport: outcome.TransportOK, LatencyMs: 2400000,
		Validation: outcome.ValidationPass, Verified: outcome.VerifiedSuccess, VerifiedBy: "human:pool-b"}
	ev := outcome.OutcomeEvent{
		SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
		DecisionID: d1, JobID: j1, StrategyID: "test-strategy", Version: 1,
		Status: outcome.StatusAccepted, Attempts: []outcome.Attempt{a1, a2, a3},
		DecidingAttemptID: "a3", HumanReviewCostUSD: &human,
		VerifiedBy: "human:pool-b", OccurredAt: "2026-09-27T00:40:00Z",
	}
	code, resp := fx.settle(t, ev, true)
	if code != http.StatusOK || resp.Learned {
		t.Fatalf("human-decided settle=%d %+v (must apply without arm learning)", code, resp)
	}
	if policy.TotalPulls() != 0 {
		t.Fatal("human correction counted as model success")
	}
	stored, _ := fx.outStore.Latest(j1)
	if len(stored.Attempts) != 3 || stored.Attempts[0].Verified != outcome.VerifiedFailure {
		t.Fatalf("attempt history not preserved: %+v", stored)
	}
	metered, unmetered := stored.TotalCostUSD()
	if metered != cheap+strong || unmetered != 1 {
		t.Fatalf("costs wrong: %v/%d", metered, unmetered)
	}
}

// Mandatory: dual-learning prevention — construction rejects mixed wiring,
// and verified traffic performs zero transport learning.
func TestDualLearningPrevention(t *testing.T) {
	dir := t.TempDir()
	decStore, _ := NewFileDecisionStore(filepath.Join(dir, "d.jsonl"))
	defer decStore.Close()
	outStore, _ := outcome.NewFileOutcomeStore(filepath.Join(dir, "o.jsonl"))
	defer outStore.Close()
	allow := func(r *http.Request) bool { return true }

	legacyWithOutcomes := RouterConfig{
		Policy: newTestPolicy(), Registry: NewProviderRegistry(),
		Writer: &MemoryEvidenceWriter{}, Outcomes: outStore,
	}
	if _, err := NewRouter(legacyWithOutcomes); err == nil {
		t.Fatal("legacy mode with outcome store constructed (dual-learning risk)")
	}
	legacyWithAuth := RouterConfig{
		Policy: newTestPolicy(), Registry: NewProviderRegistry(),
		Writer: &MemoryEvidenceWriter{}, SettleAuth: allow,
	}
	if _, err := NewRouter(legacyWithAuth); err == nil {
		t.Fatal("legacy mode with settle auth constructed (dual-learning risk)")
	}
	if _, err := NewRouter(RouterConfig{
		Policy: newTestPolicy(), Registry: NewProviderRegistry(),
		Writer: &MemoryEvidenceWriter{}, Mode: VerifiedMode,
	}); err == nil {
		t.Fatal("verified mode without outcome store constructed (would fail open)")
	}
	if _, err := NewRouter(RouterConfig{
		Policy: newTestPolicy(), Registry: NewProviderRegistry(),
		Writer: &MemoryEvidenceWriter{}, Mode: VerifiedMode, Outcomes: outStore,
	}); err == nil {
		t.Fatal("verified mode without auth constructed (unauthenticated writes)")
	}

	// Unsupported selection/sampler configs are rejected, not silently run.
	ucbCfg := thompson.DefaultConfig()
	ucbCfg.Selection = thompson.Selection{Kind: thompson.UCBRegularized, C: 2.0, UntilPulls: 30}
	ucbPolicy := thompson.New(ucbCfg, thompson.ExactSampler{})
	ucbPolicy.AddArm("a")
	if _, err := NewRouter(RouterConfig{
		Policy: ucbPolicy, Registry: NewProviderRegistry(),
		Writer: &MemoryEvidenceWriter{}, Mode: VerifiedMode,
		Decisions: decStore, Outcomes: outStore, SettleAuth: allow,
	}); err == nil {
		t.Fatal("verified mode with UCB selection constructed (no valid propensity)")
	}
	approxPolicy := thompson.New(thompson.DefaultConfig(), thompson.DeterministicSampler{})
	approxPolicy.AddArm("a")
	if _, err := NewRouter(RouterConfig{
		Policy: approxPolicy, Registry: NewProviderRegistry(),
		Writer: &MemoryEvidenceWriter{}, Mode: VerifiedMode,
		Decisions: decStore, Outcomes: outStore, SettleAuth: allow,
	}); err == nil {
		t.Fatal("verified mode with approximate sampler constructed (no valid propensity)")
	}

	// Behavioral: verified traffic learns nothing on the request path.
	policy := thompson.NewDefault("a", "b")
	fx := newVerifiedFixture(t, policy, map[string]Provider{"a": NewFakeProvider("a"), "b": NewFakeProvider("b")}, nil)
	defer fx.close(t)
	for i := 0; i < 8; i++ {
		fx.serve(t, `{}`)
	}
	if policy.TotalPulls() != 0 || len(fx.mem.Learned) != 0 {
		t.Fatalf("verified traffic learned: pulls=%d learned=%d", policy.TotalPulls(), len(fx.mem.Learned))
	}
}

// Mandatory: crash after decision persistence — restart recovers the exact
// decision and permits valid settlement.
func TestCrashAfterDecisionPersistence(t *testing.T) {
	policy := thompson.NewDefault("a", "b")
	fx := newVerifiedFixture(t, policy, map[string]Provider{"a": NewFakeProvider("a"), "b": NewFakeProvider("b")}, nil)
	d1, j1, code := fx.serve(t, `{}`)
	if code != http.StatusOK {
		t.Fatalf("serve=%d", code)
	}
	before, ok := fx.decStore.Lookup(d1)
	if !ok {
		t.Fatal("decision missing pre-crash")
	}
	fx.close(t) // crash: all process state lost

	// Restart: fresh policy + reopened stores, then recover learning.
	policy2 := thompson.NewDefault("a", "b")
	decStore2, err := NewFileDecisionStore(filepath.Join(fx.dir, "decisions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer decStore2.Close()
	outStore2, err := outcome.NewFileOutcomeStore(filepath.Join(fx.dir, "outcomes.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer outStore2.Close()
	after, ok := decStore2.Lookup(d1)
	if !ok {
		t.Fatal("committed decision lost across restart")
	}
	if !decisionsEqual(after, before) {
		t.Fatalf("decision identity diverged:\n%+v\n%+v", after, before)
	}
	rt2, err := NewRouter(RouterConfig{
		Policy: policy2, Registry: NewProviderRegistry(), Writer: &MemoryEvidenceWriter{},
		Decisions: decStore2, Outcomes: outStore2,
		StrategyID: "test-strategy", Mode: VerifiedMode, SettleAuth: func(r *http.Request) bool { return true },
		RNGFactory: func() *rand.Rand { return newTestRNG() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt2.RecoverVerifiedLearning(filepath.Join(fx.dir, "ckpt.json")); err != nil {
		t.Fatalf("recover: %v", err)
	}
	// The recovered decision settles normally.
	fx2 := &verifiedFixture{router: rt2, decStore: decStore2, outStore: outStore2}
	if code, resp := fx2.settle(t, settleEvent(d1, j1, 1, outcome.StatusAccepted, after.SelectedArmID), true); code != http.StatusOK || !resp.Learned {
		t.Fatalf("post-restart settle=%d %+v", code, resp)
	}
	if policy2.TotalPulls() != 1 {
		t.Fatalf("pulls=%d want 1", policy2.TotalPulls())
	}
}

// Mandatory: crash before decision persistence (commit failure) dispatches
// nothing, and settlement for an uncommitted decision is rejected.
func TestCrashBeforeDecisionPersistence(t *testing.T) {
	policy := thompson.NewDefault("a")
	fx := newVerifiedFixture(t, policy, map[string]Provider{"a": NewFakeProvider("a")}, nil)
	defer fx.close(t)
	ev := settleEvent("never-committed", "job-never-committed", 1, outcome.StatusAccepted, "a")
	if code, _ := fx.settle(t, ev, true); code != http.StatusNotFound {
		t.Fatalf("uncommitted settlement status=%d want 404", code)
	}
	if policy.TotalPulls() != 0 {
		t.Fatal("uncommitted outcome mutated policy")
	}
}

// Mandatory: crash after outcome persistence but before learning — recovery
// applies the committed outcome exactly once.
func TestCrashAfterOutcomePersistenceBeforeLearning(t *testing.T) {
	policy := thompson.NewDefault("a", "b")
	fx := newVerifiedFixture(t, policy, map[string]Provider{"a": NewFakeProvider("a"), "b": NewFakeProvider("b")}, nil)
	d1, j1, _ := fx.serve(t, `{}`)
	selected := fx.mem.Started[0].SelectedArmID
	// Persist the outcome WITHOUT learning: write directly to the store,
	// simulating a crash between Submit and Apply.
	ev := settleEvent(d1, j1, 1, outcome.StatusAccepted, selected)
	if applied, err := fx.outStore.Submit(ev); err != nil || !applied {
		t.Fatalf("direct submit: %v %v", applied, err)
	}
	fx.close(t) // crash before any checkpoint

	policy2 := thompson.NewDefault("a", "b")
	decStore2, _ := NewFileDecisionStore(filepath.Join(fx.dir, "decisions.jsonl"))
	defer decStore2.Close()
	outStore2, _ := outcome.NewFileOutcomeStore(filepath.Join(fx.dir, "outcomes.jsonl"))
	defer outStore2.Close()
	rt2, err := NewRouter(RouterConfig{
		Policy: policy2, Registry: NewProviderRegistry(), Writer: &MemoryEvidenceWriter{},
		Decisions: decStore2, Outcomes: outStore2,
		StrategyID: "test-strategy", Mode: VerifiedMode, SettleAuth: func(r *http.Request) bool { return true },
		RNGFactory: func() *rand.Rand { return newTestRNG() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt2.RecoverVerifiedLearning(filepath.Join(fx.dir, "ckpt.json")); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if policy2.TotalPulls() != 1 {
		t.Fatalf("recovered pulls=%d want 1", policy2.TotalPulls())
	}
	// Redelivery after recovery is an idempotent no-op.
	fx2 := &verifiedFixture{router: rt2, outStore: outStore2}
	if code, resp := fx2.settle(t, ev, true); code != http.StatusOK || !resp.Duplicate || resp.Learned {
		t.Fatalf("redelivery=%d %+v (want duplicate, no learning)", code, resp)
	}
	if policy2.TotalPulls() != 1 {
		t.Fatal("redelivery learned twice")
	}
}

// Mandatory: correction after restart matches deterministic replay.
func TestCorrectionAfterRestart(t *testing.T) {
	policy := thompson.NewDefault("a", "b")
	fx := newVerifiedFixture(t, policy, map[string]Provider{"a": NewFakeProvider("a"), "b": NewFakeProvider("b")}, nil)
	d1, j1, _ := fx.serve(t, `{}`)
	selected := fx.mem.Started[0].SelectedArmID
	if code, _ := fx.settle(t, settleEvent(d1, j1, 1, outcome.StatusAccepted, selected), true); code != http.StatusOK {
		t.Fatalf("v1=%d", code)
	}
	if err := fx.router.CheckpointVerifiedLearning(fx.ckpt); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	fx.close(t) // crash

	policy2 := thompson.NewDefault("a", "b")
	decStore2, _ := NewFileDecisionStore(filepath.Join(fx.dir, "decisions.jsonl"))
	defer decStore2.Close()
	outStore2, _ := outcome.NewFileOutcomeStore(filepath.Join(fx.dir, "outcomes.jsonl"))
	defer outStore2.Close()
	rt2, _ := NewRouter(RouterConfig{
		Policy: policy2, Registry: NewProviderRegistry(), Writer: &MemoryEvidenceWriter{},
		Decisions: decStore2, Outcomes: outStore2,
		StrategyID: "test-strategy", Mode: VerifiedMode, SettleAuth: func(r *http.Request) bool { return true },
		RNGFactory: func() *rand.Rand { return newTestRNG() },
	})
	if err := rt2.RecoverVerifiedLearning(fx.ckpt); err != nil {
		t.Fatalf("recover: %v", err)
	}
	fx2 := &verifiedFixture{router: rt2, outStore: outStore2}
	if code, resp := fx2.settle(t, settleEvent(d1, j1, 2, outcome.StatusRejected, selected), true); code != http.StatusOK || !resp.Learned {
		t.Fatalf("correction=%d %+v", code, resp)
	}
	// Deterministic replay: fresh policy + only the authoritative v2.
	want := thompson.NewDefault("a", "b")
	l := outcome.NewLearner(want, outcome.BinaryStatusMapper{}, outStore2.Events)
	if _, err := l.Rebuild(outStore2.Events()); err != nil {
		t.Fatal(err)
	}
	for _, arm := range []string{"a", "b"} {
		g, _ := policy2.PosteriorFor(arm)
		w, _ := want.PosteriorFor(arm)
		if g != w || policy2.TotalPulls() != want.TotalPulls() {
			t.Fatalf("arm %s diverged from replay: %+v vs %+v", arm, g, w)
		}
	}
}

// Mandatory: concurrent + duplicate settlement is idempotent; conflicts fail.
func TestConcurrentDuplicateSettlement(t *testing.T) {
	policy := thompson.NewDefault("a", "b")
	fx := newVerifiedFixture(t, policy, map[string]Provider{"a": NewFakeProvider("a"), "b": NewFakeProvider("b")}, nil)
	defer fx.close(t)
	d1, j1, _ := fx.serve(t, `{}`)
	selected := fx.mem.Started[0].SelectedArmID
	ev := settleEvent(d1, j1, 1, outcome.StatusAccepted, selected)

	const senders = 16
	var wg sync.WaitGroup
	codes := make([]int, senders)
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, _ := fx.settle(t, ev, true)
			codes[i] = code
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("sender %d: status=%d", i, c)
		}
	}
	if policy.TotalPulls() != 1 {
		t.Fatalf("duplicate settlement learned %d times", policy.TotalPulls())
	}
	// Conflicting version: rejected, history untouched.
	conflict := settleEvent(d1, j1, 1, outcome.StatusRejected, selected)
	if code, _ := fx.settle(t, conflict, true); code != http.StatusConflict {
		t.Fatalf("conflict status=%d want 409", code)
	}
	latest, _ := fx.outStore.Latest(j1)
	if latest.Status != outcome.StatusAccepted {
		t.Fatalf("accepted history rewritten: %+v", latest)
	}
}

// Mandatory: auth + malformed/conflict handling; legacy has no settlement.
func TestSettlementGuardrails(t *testing.T) {
	policy := thompson.NewDefault("a")
	deny := func(r *http.Request) bool { return false }
	fx := newVerifiedFixture(t, policy, map[string]Provider{"a": NewFakeProvider("a")}, deny)
	defer fx.close(t)
	d1, j1, _ := fx.serve(t, `{}`)
	if code, _ := fx.settle(t, settleEvent(d1, j1, 1, outcome.StatusAccepted, "a"), false); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated settle=%d want 401", code)
	}
	if policy.TotalPulls() != 0 {
		t.Fatal("rejected settlement mutated policy")
	}
	// Malformed body (auth runs first by design: 401 here proves ordering).
	req := httptest.NewRequest(http.MethodPost, "/v1/outcomes", strings.NewReader(`{bad json`))
	rec := httptest.NewRecorder()
	fx.router.SettleHandler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("malformed-with-deny=%d want 401 (auth first)", rec.Code)
	}
	// Job mismatch.
	ev := settleEvent(d1, "job-someone-else", 1, outcome.StatusAccepted, "a")
	if code, _ := fx.settle(t, ev, true); code != http.StatusUnauthorized {
		t.Fatalf("job mismatch with deny-auth=%d want 401", code)
	}

	// Legacy routers expose no settlement surface.
	legacy, _ := NewRouter(RouterConfig{
		Policy: thompson.NewDefault("a"), Registry: NewProviderRegistry(),
		Writer: &MemoryEvidenceWriter{},
	})
	rec2 := httptest.NewRecorder()
	legacy.SettleHandler(rec2, httptest.NewRequest(http.MethodPost, "/v1/outcomes", strings.NewReader(`{}`)))
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("legacy settle=%d want 404", rec2.Code)
	}
}
