package gateway

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

type safetyFixture struct {
	router   *Router
	safety   *SafetyController
	book     *outcome.CostBookV1
	quality  *thompson.Policy
	outStore *outcome.FileOutcomeStore
	decStore *FileDecisionStore
	sstore   *SafetyStore
	dir      string
}

func safetyTestConfig() SafetyConfig {
	return SafetyConfig{
		Workload: "pilot-test", Arms: []string{"cheap", "strong"},
		FallbackArm: "strong", QualityFloor: 0.5,
		MonitorWindow: 20, MonitorMinObs: 10,
		MaxExplorationPerArm: 8, MaxMissingShare: 0.2, ColdStartPulls: 5,
	}
}

func newSafetyFixture(t *testing.T, cfg SafetyConfig) *safetyFixture {
	t.Helper()
	dir := t.TempDir()
	decStore, err := NewFileDecisionStore(filepath.Join(dir, "d.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	outStore, err := outcome.NewFileOutcomeStore(filepath.Join(dir, "o.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sstore, err := NewSafetyStore(filepath.Join(dir, "safety.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	safety, err := NewSafetyController(cfg, "test-hash", sstore, decStore, outStore)
	if err != nil {
		t.Fatal(err)
	}
	q := thompson.NewDefault(cfg.Arms...)
	book := outcome.NewCostBookV1(cfg.Arms)
	cacfg := thompson.CostAwareConfig{QualityFloor: cfg.QualityFloor, MinMeteredN: 1, ColdStartPulls: cfg.ColdStartPulls, Epsilon: 1e-3}
	cp, err := NewCostAwarePolicy(q, book, cacfg, safety)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewProviderRegistry()
	for _, a := range cfg.Arms {
		reg.Register(NewFakeProvider(a))
	}
	rt, err := NewRouter(RouterConfig{
		Policy: cp, Registry: reg, Writer: &MemoryEvidenceWriter{},
		Decisions: decStore, Outcomes: outStore, StrategyID: "t3", Mode: VerifiedMode,
		SettleAuth: func(r *http.Request) bool { return true },
		CostBook:   book, Safety: safety,
		OperatorAuth: func(r *http.Request) (string, bool) {
			if r.Header.Get("Authorization") == "Bearer op:alice" {
				return "alice", true
			}
			return "", false
		},
		RNGFactory: advancingTestRNG(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &safetyFixture{router: rt, safety: safety, book: book, quality: q, outStore: outStore, decStore: decStore, sstore: sstore, dir: dir}
}

func (f *safetyFixture) close(t *testing.T) {
	t.Helper()
	if err := f.decStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.outStore.Close(); err != nil {
		t.Fatal(err)
	}
}

func (f *safetyFixture) serve(t *testing.T) (did, jid, arm string, code int) {
	t.Helper()
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
	return rec.Header().Get("X-Decision-ID"), rec.Header().Get("X-Job-ID"), rec.Header().Get("X-Selected-Arm"), rec.Code
}

func (f *safetyFixture) settleMetered(t *testing.T, did, jid, arm string, cost float64, ok bool) int {
	t.Helper()
	ver := outcome.VerifiedSuccess
	st := outcome.StatusAccepted
	if !ok {
		ver, st = outcome.VerifiedFailure, outcome.StatusRejected
	}
	ev := outcome.OutcomeEvent{SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
		DecisionID: did, JobID: jid, StrategyID: "t3", Version: 1,
		Status: st, Attempts: []outcome.Attempt{meteredAttempt(jid+"-a0", 0, arm, cost, ver)},
		DecidingAttemptID: jid + "-a0", VerifiedBy: "checker:safety", OccurredAt: "2026-01-05T00:00:00Z"}
	b, _ := json.Marshal(ev)
	req := httptest.NewRequest(http.MethodPost, "/v1/outcomes", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	f.router.SettleHandler(rec, req)
	return rec.Code
}

func (f *safetyFixture) operator(t *testing.T, path, arm, reason, auth string) int {
	t.Helper()
	b, _ := json.Marshal(operatorRequest{Arm: arm, Reason: reason})
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	rec := httptest.NewRecorder()
	if strings.HasSuffix(path, "suspend") {
		f.router.SuspendHandler(rec, req)
	} else {
		f.router.ResumeHandler(rec, req)
	}
	return rec.Code
}

// Deterioration suspends the arm; subsequent jobs avoid it; no auto-reenable.
func TestSafetyDeteriorationSuspends(t *testing.T) {
	cfg := safetyTestConfig()
	cfg.MonitorWindow = 10
	cfg.MonitorMinObs = 6
	f := newSafetyFixture(t, cfg)
	defer f.close(t)
	// Establish STRONG as the preferred arm (it wins early traffic and
	// becomes the cost-known optimum), then collapse it. The collapsing arm
	// must be the traffic-dominant one: cost-unknown arms are starved by
	// design once any arm is known, so breaking a starved arm proves nothing.
	// Strong costs $0.002, cheap $0.05: T3 rides strong until it collapses.
	step := func(i int, strongOK bool) string {
		did, jid, arm, code := f.serve(t)
		if code != 200 {
			t.Fatalf("serve %d", code)
		}
		c := 0.05
		ok := true
		if arm == "strong" {
			c = 0.002
			ok = strongOK
		}
		if code := f.settleMetered(t, did, jid, arm, c, ok); code != 200 {
			t.Fatalf("settle %d", code)
		}
		return arm
	}
	for i := 0; i < 12; i++ {
		step(i, true)
	}
	suspended := false
	for i := 12; i < 100; i++ {
		step(i, false)
		if st, _, _ := f.safety.State("strong"); st == ArmSuspended {
			suspended = true
			t.Logf("deterioration detected after %d post-collapse jobs", i-12)
			break
		}
	}
	if !suspended {
		t.Fatal("deteriorated arm was never suspended")
	}
	// After suspension, strong receives no traffic (cheap takes over until
	// ITS exploration budget (8) is spent, then it suspends too and the
	// gateway fails closed: fallback strong is down, nothing safe remains.
	cheapServed := 0
	terminal := 0
	for i := 0; i < 12; i++ {
		_, _, arm, code := f.serve(t)
		if code != 200 {
			terminal = code
			break
		}
		if arm == "strong" {
			t.Fatal("suspended arm still selected")
		}
		cheapServed++
	}
	if cheapServed == 0 || cheapServed > 8 {
		t.Fatalf("budget miscount: cheap served %d", cheapServed)
	}
	if terminal != 503 {
		t.Fatalf("double-suspension must fail closed 503, got %d", terminal)
	}
	if st, _, reason := f.safety.State("cheap"); st != ArmSuspended {
		t.Fatalf("cheap budget exhaustion must suspend, got %q", st)
	} else {
		t.Logf("cheap suspended: %s", reason)
	}
	// No automatic re-enablement over continued traffic.
	if st, _, _ := f.safety.State("strong"); st != ArmSuspended {
		t.Fatal("suspension lifted without operator action")
	}
}

// Unauthorized resume is rejected; authorized resume with reason works.
func TestSafetyOperatorResumeAuth(t *testing.T) {
	f := newSafetyFixture(t, safetyTestConfig())
	defer f.close(t)
	if code := f.operator(t, "/v1/operator/suspend", "cheap", "test", "op:alice"); code != 200 {
		t.Fatalf("suspend %d", code)
	}
	if code := f.operator(t, "/v1/operator/resume", "cheap", "fixed", ""); code != 401 {
		t.Fatalf("anonymous resume must be 401, got %d", code)
	}
	if code := f.operator(t, "/v1/operator/resume", "cheap", "fixed", "op:bob"); code != 401 {
		t.Fatalf("unknown operator must be 401, got %d", code)
	}
	if code := f.operator(t, "/v1/operator/resume", "cheap", "", "op:alice"); code != 400 {
		t.Fatalf("reasonless resume must be 400, got %d", code)
	}
	if code := f.operator(t, "/v1/operator/resume", "cheap", "verified fix", "op:alice"); code != 200 {
		t.Fatalf("authorized resume %d", code)
	}
	if st, _, _ := f.safety.State("cheap"); st != ArmPrequalified {
		t.Fatalf("resume did not re-admit: %q", st)
	}
}

// Emergency stop routes everything to fallback and survives restart.
func TestSafetyEmergencyStopDurable(t *testing.T) {
	f := newSafetyFixture(t, safetyTestConfig())
	defer f.close(t)
	if code := f.operator(t, "/v1/operator/suspend", "", "incident-7", "op:alice"); code != 200 {
		t.Fatalf("stop %d", code)
	}
	for i := 0; i < 5; i++ {
		_, _, arm, code := f.serve(t)
		if code != 200 || arm != "strong" {
			t.Fatalf("emergency must route fallback, got %d %q", code, arm)
		}
	}
	// Restart durability: close (releasing the writer lock, exactly as
	// process death would) and rebuild the controller over the same files.
	if err := f.sstore.Close(); err != nil {
		t.Fatal(err)
	}
	sstore, err := NewSafetyStore(f.dir + "/safety.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	safety2, err := NewSafetyController(safetyTestConfig(), "test-hash", sstore, f.decStore, f.outStore)
	if err != nil {
		t.Fatal(err)
	}
	if !safety2.Emergency() {
		t.Fatal("emergency stop did not survive restart")
	}
	allowed := safety2.Authorize([]string{"cheap", "strong"})
	if len(allowed) != 1 || !allowed["strong"] {
		t.Fatalf("post-restart mask wrong: %v", allowed)
	}
	if code := func() int {
		// Release through the REBUILT controller: the pre-restart router's
		// store is closed, so its handlers correctly refuse with 409.
		// (HTTP release path is covered by TestSafetyOperatorResumeAuth.)
		if err := safety2.EmergencyRelease("operator:alice", "all-clear"); err != nil {
			t.Fatalf("release: %v", err)
		}
		return 200
	}(); code != 200 {
		t.Fatalf("release %d", code)
	}
	if safety2.Emergency() {
		t.Fatal("release did not lift the emergency")
	}
}

// Missing-cost budget breach suspends the dark arm. Strong is established
// as the metered optimum, then goes dark (unmetered) while cheap stays
// metered: T3 keeps riding strong on stale known costs until the missing
// share trips its budget.
func TestSafetyMissingCostSuspends(t *testing.T) {
	cfg := safetyTestConfig()
	cfg.MaxMissingShare = 0.2
	cfg.MonitorWindow = 12
	cfg.MonitorMinObs = 6
	f := newSafetyFixture(t, cfg)
	defer f.close(t)
	unmetered := func(did, jid, arm string) int {
		ev := outcome.OutcomeEvent{SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
			DecisionID: did, JobID: jid, StrategyID: "t3", Version: 1, Status: outcome.StatusAccepted,
			Attempts: []outcome.Attempt{{
				AttemptID: jid + "-a0", Seq: 0, ExecutorID: arm, ArmID: arm,
				Transport: outcome.TransportOK, LatencyMs: 10,
				Validation: outcome.ValidationPass, Verified: outcome.VerifiedSuccess, VerifiedBy: "checker:safety"}},
			DecidingAttemptID: jid + "-a0", VerifiedBy: "checker:safety", OccurredAt: "2026-01-05T00:00:00Z"}
		b, _ := json.Marshal(ev)
		req := httptest.NewRequest(http.MethodPost, "/v1/outcomes", bytes.NewReader(b))
		rec := httptest.NewRecorder()
		f.router.SettleHandler(rec, req)
		return rec.Code
	}
	// Phase 1: both metered, strong cheaper — T3 rides strong.
	for i := 0; i < 10; i++ {
		did, jid, arm, code := f.serve(t)
		if code != 200 {
			t.Fatalf("serve %d", code)
		}
		c := 0.05
		if arm == "strong" {
			c = 0.002
		}
		if code := f.settleMetered(t, did, jid, arm, c, true); code != 200 {
			t.Fatalf("settle %d", code)
		}
	}
	// Phase 2: strong goes dark. T3 keeps riding it on stale known costs.
	suspended := false
	for i := 0; i < 40 && !suspended; i++ {
		did, jid, arm, code := f.serve(t)
		if code != 200 {
			t.Fatalf("serve %d", code)
		}
		if arm == "strong" {
			if code := unmetered(did, jid, arm); code != 200 {
				t.Fatalf("settle %d", code)
			}
		} else if code := f.settleMetered(t, did, jid, arm, 0.05, true); code != 200 {
			t.Fatalf("settle %d", code)
		}
		if st, _, _ := f.safety.State("strong"); st == ArmSuspended {
			suspended = true
			t.Logf("missing-cost breach detected")
		}
	}
	if !suspended {
		t.Fatal("dark arm never breached its missing-cost budget")
	}
	// Fully metered cheap is not disadvantaged by strong's darkness: it
	// remains selectable (the suspension targets the blind arm only).
	if st, _, _ := f.safety.State("cheap"); st == ArmSuspended {
		t.Fatal("metered arm incorrectly suspended")
	}
}

// Exploration budget is enforced, including the cold path; suspension sticks.
func TestSafetyExplorationBudgetEnforced(t *testing.T) {
	cfg := safetyTestConfig()
	cfg.MaxExplorationPerArm = 5
	cfg.ColdStartPulls = 5
	f := newSafetyFixture(t, cfg)
	defer f.close(t)
	// Never settle: arms stay cold+unknown, every pick is exploration.
	served := 0
	for i := 0; i < 40; i++ {
		_, _, _, code := f.serve(t)
		if code != 200 {
			break
		}
		served++
	}
	// Budgets: 5/arm × 2 arms = 10 exploration picks, then both suspended
	// (no genuine-optimum path exists with zero observations) → fail closed.
	if served > 12 {
		t.Fatalf("exploration exceeded budget: %d served", served)
	}
	if code := func() int {
		_, _, _, c := f.serve(t)
		return c
	}(); code != 503 {
		t.Fatalf("exhausted budgets must fail closed 503, got %d", code)
	}
}

// advancingTestRNG returns an RNG factory with a deterministic but advancing
// stream (unlike newTestRNG, which replays the identical stream per request
// and would pin every selection to one arm).
func advancingTestRNG() func() *rand.Rand {
	var n atomic.Uint64
	return func() *rand.Rand {
		k := n.Add(1)
		return rand.New(rand.NewPCG(42+k*101, k))
	}
}
