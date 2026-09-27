package gateway

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

type costAwareFixture struct {
	router   *Router
	policy   *CostAwarePolicy
	quality  *thompson.Policy
	book     *outcome.CostBookV1
	outStore *outcome.FileOutcomeStore
	decStore *FileDecisionStore
	dir      string
}

func newCostAwareFixture(t *testing.T, arms []string) *costAwareFixture {
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
	q := thompson.NewDefault(arms...)
	book := outcome.NewCostBookV1(arms)
	cfg := thompson.CostAwareConfig{QualityFloor: 0.3, MinMeteredN: 1, ColdStartPulls: 5, Epsilon: 1e-3}
	cp, err := NewCostAwarePolicy(q, book, cfg, NewStaticGate(arms))
	if err != nil {
		t.Fatal(err)
	}
	reg := NewProviderRegistry()
	for _, a := range arms {
		reg.Register(NewFakeProvider(a))
	}
	rt, err := NewRouter(RouterConfig{
		Policy: cp, Registry: reg, Writer: &MemoryEvidenceWriter{},
		Decisions: decStore, Outcomes: outStore,
		StrategyID: "t3", Mode: VerifiedMode,
		SettleAuth: func(r *http.Request) bool { return true },
		CostBook:   book,
		RNGFactory: func() *rand.Rand { return newTestRNG() },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &costAwareFixture{router: rt, policy: cp, quality: q, book: book, outStore: outStore, decStore: decStore, dir: dir}
}

func (f *costAwareFixture) close(t *testing.T) {
	t.Helper()
	if err := f.decStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.outStore.Close(); err != nil {
		t.Fatal(err)
	}
}

func (f *costAwareFixture) serve(t *testing.T) (decisionID, jobID, arm string, code int) {
	t.Helper()
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
	return rec.Header().Get("X-Decision-ID"), rec.Header().Get("X-Job-ID"), rec.Header().Get("X-Selected-Arm"), rec.Code
}

func meteredAttempt(id string, seq uint, arm string, cost float64, verified outcome.VerifiedOutcome) outcome.Attempt {
	return outcome.Attempt{
		AttemptID: id, Seq: seq, ExecutorID: arm, ArmID: arm,
		Transport: outcome.TransportOK, LatencyMs: 150, CostUSD: &cost,
		Validation: outcome.ValidationPass, Verified: verified,
		VerifiedBy:  "checker:ca-v1",
	}
}

func (f *costAwareFixture) settle(t *testing.T, decisionID, jobID string, version uint64, status outcome.JobStatus, attempts []outcome.Attempt, deciding string) (int, settleResponse) {
	t.Helper()
	ev := outcome.OutcomeEvent{
		SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
		DecisionID: decisionID, JobID: jobID, StrategyID: "t3",
		Version: version, Supersedes: version - 1, Status: status,
		Attempts: attempts, DecidingAttemptID: deciding,
		VerifiedBy: "checker:ca-v1", OccurredAt: "2026-01-05T00:00:00Z",
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/outcomes", bytes.NewReader(b))
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

// The actual gateway selects through validated RuleV2 and persists the real
// cost-aware identity on the committed decision.
func TestGatewayCostAwareSelectsRuleV2(t *testing.T) {
	f := newCostAwareFixture(t, []string{"cheap", "strong"})
	defer f.close(t)
	// Seed: cheap good+cheap, strong good+expensive (metered settlement).
	for i := 0; i < 6; i++ {
		did, jid, arm, code := f.serve(t)
		if code != 200 {
			t.Fatalf("serve %d", code)
		}
		c := 0.002
		if arm == "strong" {
			c = 0.05
		}
		st := outcome.StatusAccepted
		if i%3 == 2 {
			st = outcome.StatusRejected
		}
		ver := outcome.VerifiedSuccess
		if st == outcome.StatusRejected {
			ver = outcome.VerifiedFailure
		}
		if code, _ := f.settle(t, did, jid, 1, st, []outcome.Attempt{meteredAttempt(jid+"-a0", 0, arm, c, ver)}, jid+"-a0"); code != 200 {
			t.Fatalf("settle %d", code)
		}
	}
	did, _, arm, code := f.serve(t)
	if code != 200 {
		t.Fatalf("serve %d", code)
	}
	dec, ok := f.decStore.Lookup(did)
	if !ok {
		t.Fatal("decision not committed")
	}
	if dec.LoggingPolicyID != thompson.CostAwarePolicyID {
		t.Fatalf("wrong policy identity: %q", dec.LoggingPolicyID)
	}
	if dec.CostAware == nil {
		t.Fatal("committed decision missing cost-aware result")
	}
	// The gateway always authorizes through a safety mask, so live decisions
	// report RuleV3 (V2 algorithm + eligibility mask); RuleV2 is the
	// harness/exact-replay path.
	if dec.CostAware.RuleVersion != thompson.CostAwareRuleV3 {
		t.Fatalf("wrong rule version: %+v", dec.CostAware)
	}
	if dec.SelectedArmID != arm || dec.CostAware.ArmID != arm {
		t.Fatalf("identity/selection mismatch: %q vs %q", dec.SelectedArmID, arm)
	}
	if dec.CostAware.Objective != thompson.CostAwareObjectiveVer {
		t.Fatalf("wrong objective: %+v", dec.CostAware)
	}
}

// Bidirectional binding: mismatched policy/book fail closed at construction.
func TestGatewayCostAwareBindingFailsClosed(t *testing.T) {
	dir := t.TempDir()
	mk := func() (*FileDecisionStore, *outcome.FileOutcomeStore) {
		d, _ := NewFileDecisionStore(filepath.Join(dir, "d.jsonl"))
		o, _ := outcome.NewFileOutcomeStore(filepath.Join(dir, "o.jsonl"))
		return d, o
	}
	// Cost book + cost-blind policy.
	d, o := mk()
	_, err := NewRouter(RouterConfig{
		Policy: thompson.NewDefault("a", "b"), Registry: NewProviderRegistry(),
		Writer: &MemoryEvidenceWriter{}, Decisions: d, Outcomes: o,
		Mode: VerifiedMode, SettleAuth: func(r *http.Request) bool { return true },
		CostBook: outcome.NewCostBookV1([]string{"a", "b"}),
	})
	if err == nil {
		t.Fatal("book without cost-aware policy must fail closed")
	}
	// Cost-aware policy without book.
	d2, o2 := mk()
	q := thompson.NewDefault("a", "b")
	cp, err := NewCostAwarePolicy(q, outcome.NewCostBookV1([]string{"a", "b"}), thompson.DefaultCostAwareConfig(), NewStaticGate([]string{"a", "b"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRouter(RouterConfig{
		Policy: cp, Registry: NewProviderRegistry(),
		Writer: &MemoryEvidenceWriter{}, Decisions: d2, Outcomes: o2,
		Mode: VerifiedMode, SettleAuth: func(r *http.Request) bool { return true },
	})
	if err == nil {
		t.Fatal("cost-aware policy without book must fail closed (no silent cost-blind)")
	}
}

// Duplicate settlement is idempotent across quality AND cost state.
func TestGatewayCostAwareDuplicateIdempotent(t *testing.T) {
	f := newCostAwareFixture(t, []string{"cheap", "strong"})
	defer f.close(t)
	did, jid, arm, code := f.serve(t)
	if code != 200 {
		t.Fatalf("serve %d", code)
	}
	atts := []outcome.Attempt{meteredAttempt(jid+"-a0", 0, arm, 0.01, outcome.VerifiedSuccess)}
	if code, _ := f.settle(t, did, jid, 1, outcome.StatusAccepted, atts, jid+"-a0"); code != 200 {
		t.Fatalf("settle1 %d", code)
	}
	pulls1, m1 := f.quality.TotalPulls(), mustMean(t, f.book, arm)
	if code, resp := f.settle(t, did, jid, 1, outcome.StatusAccepted, atts, jid+"-a0"); code != 200 || !resp.Duplicate {
		t.Fatalf("duplicate not acknowledged: %d %+v", code, resp)
	}
	if f.quality.TotalPulls() != pulls1 {
		t.Fatal("duplicate moved quality state")
	}
	if m2 := mustMean(t, f.book, arm); m2 != m1 {
		t.Fatal("duplicate moved cost state")
	}
}

// Correction converges to deterministic replay.
func TestGatewayCostAwareCorrectionReplays(t *testing.T) {
	f := newCostAwareFixture(t, []string{"cheap", "strong"})
	defer f.close(t)
	did, jid, arm, code := f.serve(t)
	if code != 200 {
		t.Fatalf("serve %d", code)
	}
	okAtt := []outcome.Attempt{meteredAttempt(jid+"-a0", 0, arm, 0.01, outcome.VerifiedSuccess)}
	if code, _ := f.settle(t, did, jid, 1, outcome.StatusAccepted, okAtt, jid+"-a0"); code != 200 {
		t.Fatalf("v1 %d", code)
	}
	badAtt := []outcome.Attempt{meteredAttempt(jid+"-a0", 0, arm, 0.01, outcome.VerifiedFailure)}
	if code, _ := f.settle(t, did, jid, 2, outcome.StatusRejected, badAtt, jid+"-a0"); code != 200 {
		t.Fatalf("v2 %d", code)
	}
	// Fresh rebuild over the same ledger must match live state.
	evs := f.outStore.Events()
	q2 := thompson.NewDefault("cheap", "strong")
	l2 := outcome.NewLearner(q2, outcome.BinaryStatusMapper{}, func() []outcome.OutcomeEvent { return evs })
	if _, err := l2.Rebuild(evs); err != nil {
		t.Fatal(err)
	}
	b2 := outcome.NewCostBookV1([]string{"cheap", "strong"})
	if _, err := b2.Rebuild(evs); err != nil {
		t.Fatal(err)
	}
	j1, _ := json.Marshal(f.quality.Snapshot())
	j2, _ := json.Marshal(q2.Snapshot())
	if string(j1) != string(j2) {
		t.Fatal("quality diverged from replay after correction")
	}
	for _, a := range []string{"cheap", "strong"} {
		m1, k1 := f.book.Mean(a)
		m2, k2 := b2.Mean(a)
		if k1 != k2 || (k1 && m1 != m2) {
			t.Fatalf("cost diverged from replay on %s", a)
		}
	}
}

// Restart restores both estimators with identity intact.
func TestGatewayCostAwareRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	build := func() (*Router, *CostAwarePolicy, *outcome.CostBookV1, *FileDecisionStore, *outcome.FileOutcomeStore) {
		d, _ := NewFileDecisionStore(filepath.Join(dir, "d.jsonl"))
		o, _ := outcome.NewFileOutcomeStore(filepath.Join(dir, "o.jsonl"))
		q := thompson.NewDefault("cheap", "strong")
		b := outcome.NewCostBookV1([]string{"cheap", "strong"})
		cp, _ := NewCostAwarePolicy(q, b, thompson.CostAwareConfig{QualityFloor: 0.3, MinMeteredN: 1, ColdStartPulls: 5, Epsilon: 1e-3}, NewStaticGate([]string{"cheap", "strong"}))
		reg := NewProviderRegistry()
		reg.Register(NewFakeProvider("cheap"))
		reg.Register(NewFakeProvider("strong"))
		rt, err := NewRouter(RouterConfig{
			Policy: cp, Registry: reg, Writer: &MemoryEvidenceWriter{},
			Decisions: d, Outcomes: o, StrategyID: "t3", Mode: VerifiedMode,
			SettleAuth: func(r *http.Request) bool { return true }, CostBook: b,
			RNGFactory: func() *rand.Rand { return newTestRNG() },
		})
		if err != nil {
			t.Fatal(err)
		}
		return rt, cp, b, d, o
	}
	rt, cp, b, d, o := build()
	_ = cp
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
	did, jid, arm := rec.Header().Get("X-Decision-ID"), rec.Header().Get("X-Job-ID"), rec.Header().Get("X-Selected-Arm")
	ev := outcome.OutcomeEvent{SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
		DecisionID: did, JobID: jid, StrategyID: "t3", Version: 1,
		Status:     outcome.StatusAccepted,
		Attempts:   []outcome.Attempt{meteredAttempt(jid+"-a0", 0, arm, 0.02, outcome.VerifiedSuccess)},
		DecidingAttemptID: jid + "-a0", VerifiedBy: "checker:ca-v1", OccurredAt: "2026-01-05T00:00:00Z"}
	bb, _ := json.Marshal(ev)
	srec := httptest.NewRecorder()
	rt.SettleHandler(srec, httptest.NewRequest(http.MethodPost, "/v1/outcomes", bytes.NewReader(bb)))
	if srec.Code != 200 {
		t.Fatalf("settle %d", srec.Code)
	}
	before, _ := b.Mean(arm)
	ck := filepath.Join(dir, "ckpt.json")
	if err := rt.CheckpointVerifiedLearning(ck); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	_ = o.Close()
	// Restart: fresh process objects over the same files.
	rt2, cp2, b2, d2, o2 := build()
	defer d2.Close()
	defer o2.Close()
	if err := rt2.RecoverVerifiedLearning(ck); err != nil {
		t.Fatal(err)
	}
	if after, _ := b2.Mean(arm); after != before {
		t.Fatalf("cost not restored: %v vs %v", after, before)
	}
	if rt2.policy.LoggingPolicyID() != thompson.CostAwarePolicyID {
		t.Fatal("policy identity changed across restart")
	}
	_ = cp2
}

// Missing costs never become favorable; malformed costs are refused before
// anything moves. Cost-unknown arms surface as flagged fallback, never as
// silent cost-blind optima.
func TestGatewayCostAwareMissingCostExplicit(t *testing.T) {
	f := newCostAwareFixture(t, []string{"cheap", "strong"})
	defer f.close(t)
	did, jid, arm, code := f.serve(t)
	if code != 200 {
		t.Fatalf("serve %d", code)
	}
	dec, _ := f.decStore.Lookup(did)
	if dec.CostAware == nil || dec.CostAware.PolicyID != thompson.CostAwarePolicyID {
		t.Fatalf("fresh-arm decision must carry cost-aware identity: %+v", dec.CostAware)
	}
	// Unmetered (nil cost) settlement is accepted but learns no cost.
	atts := []outcome.Attempt{{
		AttemptID: jid + "-a0", Seq: 0, ExecutorID: arm, ArmID: arm,
		Transport: outcome.TransportOK, LatencyMs: 10,
		Validation: outcome.ValidationPass, Verified: outcome.VerifiedSuccess, VerifiedBy: "checker:ca-v1",
	}}
	if code, _ := f.settle(t, did, jid, 1, outcome.StatusAccepted, atts, jid+"-a0"); code != 200 {
		t.Fatalf("unmetered settle %d", code)
	}
	if _, ok := f.book.Mean(arm); ok {
		t.Fatal("missing cost became a cost observation")
	}
	// Malformed (negative) cost is refused with nothing moved. (NaN cannot
	// cross the JSON wire at all; in-process NaN refusal is covered by
	// outcome unit tests.)
	pulls := f.quality.TotalPulls()
	did2, jid2, arm2, code := f.serve(t)
	if code != 200 {
		t.Fatalf("serve2 %d", code)
	}
	neg := -0.5
	bad := []outcome.Attempt{{
		AttemptID: jid2 + "-a0", Seq: 0, ExecutorID: arm2, ArmID: arm2,
		Transport: outcome.TransportOK, LatencyMs: 10, CostUSD: &neg,
		Validation: outcome.ValidationPass, Verified: outcome.VerifiedSuccess, VerifiedBy: "checker:ca-v1",
	}}
	if code, _ := f.settle(t, did2, jid2, 1, outcome.StatusAccepted, bad, jid2+"-a0"); code != 400 {
		t.Fatalf("malformed cost must be refused 400, got %d", code)
	}
	if f.quality.TotalPulls() != pulls {
		t.Fatal("refused settlement moved quality state")
	}
	_ = arm
}

func mustMean(t *testing.T, b *outcome.CostBookV1, arm string) float64 {
	t.Helper()
	m, ok := b.Mean(arm)
	if !ok {
		t.Fatalf("no mean for %s", arm)
	}
	return m
}
