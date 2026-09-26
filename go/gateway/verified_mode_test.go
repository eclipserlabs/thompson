package gateway

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// failCommitStore refuses every commit: simulates unavailable durable storage.
type failCommitStore struct {
	MemoryDecisionStore
	err     error
	commits int
}

func (s *failCommitStore) Commit(d CommittedDecision) (CommittedDecision, bool, error) {
	s.commits++
	return CommittedDecision{}, false, s.err
}

type invokingProvider struct {
	id      string
	invoked *bool
}

func (p *invokingProvider) ID() string { return p.id }
func (p *invokingProvider) Invoke(ctx context.Context, r *http.Request) (ProviderOutcome, error) {
	*p.invoked = true
	return ProviderOutcome{Success: true, StatusCode: 200}, nil
}

// Commit failure is fail-closed: no provider is dispatched and no decision
// evidence is written.
func TestCommitFailureBlocksExecution(t *testing.T) {
	policy := thompson.NewDefault("a")
	mem := &MemoryEvidenceWriter{}
	store := &failCommitStore{err: errors.New("disk gone")}
	reg := NewProviderRegistry()
	var invoked bool
	reg.Register(&invokingProvider{id: "a", invoked: &invoked})
	rt, err := NewRouter(RouterConfig{
		Policy: policy, Registry: reg, Writer: mem,
		Decisions:  store,
		RNGFactory: func() *rand.Rand { return newTestRNG() },
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
	if invoked {
		t.Fatal("provider dispatched despite commit failure")
	}
	if len(mem.Started) != 0 {
		t.Fatalf("decision evidence written for uncommitted decision: %d", len(mem.Started))
	}
	if store.commits != 1 {
		t.Fatalf("commits=%d want 1", store.commits)
	}
}

// Happy path: the committed decision is retrievable with identical selection
// evidence, and the caller learns decision_id + job_id from headers.
func TestHappyPathPersistsRetrievableDecision(t *testing.T) {
	policy := thompson.NewDefault("a", "b")
	mem := &MemoryEvidenceWriter{}
	store := NewMemoryDecisionStore()
	rt := newRouterWithFake(policy, mem, nil)
	rt.decisions = store
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	decisionID := rec.Header().Get("X-Decision-ID")
	jobID := rec.Header().Get("X-Job-ID")
	if decisionID == "" || jobID != "job-"+decisionID {
		t.Fatalf("headers: decision=%q job=%q", decisionID, jobID)
	}
	got, ok := store.Lookup(decisionID)
	if !ok {
		t.Fatal("committed decision not retrievable")
	}
	if got.JobID != jobID || got.SelectedArmID != mem.Started[0].SelectedArmID ||
		len(got.EligibleArmState) != len(mem.Started[0].EligibleArmState) ||
		got.LoggingPolicyID != mem.Started[0].LoggingPolicyID ||
		got.ConfigHash != mem.Started[0].PolicyConfigHash {
		t.Fatalf("stored decision diverges from evidence:\n%+v\n%+v", got, mem.Started[0])
	}
	exec, ok := store.Execution(decisionID)
	if !ok || exec.Phase != PhaseObserved {
		t.Fatalf("execution marker wrong: %+v", exec)
	}
}
