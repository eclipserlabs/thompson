package outcome

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func testAttempt(id string, seq uint, arm string, verified VerifiedOutcome) Attempt {
	return Attempt{
		AttemptID: id, Seq: seq, ExecutorID: arm,
		ArmID: arm, Transport: TransportOK, LatencyMs: 120,
		Validation: ValidationPass, Verified: verified,
		VerifiedBy: "checker:test-v1", VerifiedAt: "2026-09-27T00:00:00Z",
	}
}

func settledJob(job, decision, arm string, status JobStatus, version uint64) OutcomeEvent {
	ev := OutcomeEvent{
		SchemaVersion: SchemaVersion, EventType: EventJobSettled,
		DecisionID: decision, JobID: job, StrategyID: "thompson-adaptive-v1",
		Version: version, Supersedes: version - 1, Status: status,
		OccurredAt: "2026-09-27T00:00:00Z",
		VerifiedBy: "checker:test-v1", VerifiedAt: "2026-09-27T00:00:01Z",
	}
	if version > 1 {
		ev.CorrectedAt = "2026-09-27T00:05:00Z"
	}
	switch status {
	case StatusAccepted:
		ev.Attempts = []Attempt{testAttempt("a1", 0, arm, VerifiedSuccess)}
		ev.DecidingAttemptID = "a1"
	case StatusRejected:
		ev.Attempts = []Attempt{testAttempt("a1", 0, arm, VerifiedFailure)}
		ev.DecidingAttemptID = "a1"
	case StatusUnknown:
		a := testAttempt("a1", 0, arm, VerifiedUnknown)
		a.Transport = TransportTimeout
		a.Validation = ValidationNotRun
		ev.Attempts = []Attempt{a}
	case StatusPending:
		a := testAttempt("a1", 0, arm, VerifiedUnknown)
		a.Validation = ValidationNotRun
		ev.Attempts = []Attempt{a}
	}
	return ev
}

func TestValidateRejectsBadEvents(t *testing.T) {
	base := settledJob("j", "d", "a", StatusAccepted, 1)
	cases := map[string]func(*OutcomeEvent){
		"zero version":        func(e *OutcomeEvent) { e.Version, e.Supersedes = 0, 0 },
		"supersedes mismatch": func(e *OutcomeEvent) { e.Supersedes = 5 },
		"accepted no decider": func(e *OutcomeEvent) { e.DecidingAttemptID = "" },
		"rejected no decider": func(e *OutcomeEvent) {
			*e = settledJob("j", "d", "a", StatusRejected, 1)
			e.DecidingAttemptID = ""
		},
		"decider not in chain": func(e *OutcomeEvent) { e.DecidingAttemptID = "ghost" },
		"NaN latency":          func(e *OutcomeEvent) { e.Attempts[0].LatencyMs = nan() },
		"empty job":            func(e *OutcomeEvent) { e.JobID = "" },
	}
	for name, mut := range cases {
		ev := base
		ev.Attempts = append([]Attempt(nil), base.Attempts...)
		mut(&ev)
		if err := ev.Validate(); err == nil {
			t.Fatalf("%s: expected validation error", name)
		}
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
}

func nan() float64 {
	z := 0.0
	return z / z
}

func TestMemoryStoreOrderingAndIdempotency(t *testing.T) {
	s := NewMemoryOutcomeStore()
	v1 := settledJob("j1", "d1", "a", StatusAccepted, 1)

	applied, err := s.Submit(v1)
	if err != nil || !applied {
		t.Fatalf("first submit: applied=%v err=%v", applied, err)
	}
	// Exact duplicate: idempotent no-op, no new Seq.
	applied, err = s.Submit(v1)
	if err != nil || applied {
		t.Fatalf("duplicate: applied=%v err=%v", applied, err)
	}
	if s.Len() != 1 {
		t.Fatalf("dup grew ledger: len=%d", s.Len())
	}
	// Same version, different content: conflict.
	conflict := settledJob("j1", "d1", "a", StatusRejected, 1)
	if _, err := s.Submit(conflict); err == nil {
		t.Fatal("conflicting version accepted")
	}
	// Gap: v3 before v2.
	if _, err := s.Submit(settledJob("j1", "d1", "a", StatusRejected, 3)); err == nil {
		t.Fatal("version gap accepted")
	}
	// Stale redelivery after correction.
	v2 := settledJob("j1", "d1", "a", StatusRejected, 2)
	if ok, err := s.Submit(v2); err != nil || !ok {
		t.Fatalf("correction: ok=%v err=%v", ok, err)
	}
	if _, err := s.Submit(v1); err == nil {
		t.Fatal("stale v1 accepted after v2")
	}
	// New job must start at 1.
	if _, err := s.Submit(settledJob("j2", "d2", "a", StatusAccepted, 2)); err == nil {
		t.Fatal("new job starting at v2 accepted")
	}
	latest, ok := s.Latest("j1")
	if !ok || latest.Version != 2 || latest.Status != StatusRejected {
		t.Fatalf("latest wrong: %+v", latest)
	}
	// Seq order preserved.
	evs := s.Events()
	if len(evs) != 2 || evs[0].Seq != 1 || evs[1].Seq != 2 {
		t.Fatalf("bad seq order: %+v", evs)
	}
}

func TestFileStoreRoundTripAndTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outcomes.jsonl")
	s, err := NewFileOutcomeStore(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.Submit(settledJob("j1", "d1", "a", StatusAccepted, 1)); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Simulate a crash mid-append: torn bytes with no trailing newline.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"schema_version":1,"event_type":"JobSettled","trunca`)
	_ = f.Close()

	r, err := NewFileOutcomeStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer r.Close()
	if !r.TornTail() {
		t.Fatal("torn tail not reported")
	}
	if r.Len() != 1 {
		t.Fatalf("prefix not salvaged: len=%d", r.Len())
	}
	latest, ok := r.Latest("j1")
	if !ok || latest.Status != StatusAccepted {
		t.Fatalf("recovered event wrong: %+v", latest)
	}
	// Ledger usable after salvage: next submit appends cleanly.
	if _, err := r.Submit(settledJob("j1", "d1", "a", StatusRejected, 2)); err != nil {
		t.Fatalf("post-salvage submit: %v", err)
	}
	if r.Len() != 2 {
		t.Fatalf("len=%d want 2", r.Len())
	}
}

func TestFileStoreSingleWriterEnforced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outcomes.jsonl")
	a, err := NewFileOutcomeStore(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	defer a.Close()
	if _, err := NewFileOutcomeStore(path); err == nil {
		t.Fatal("second writer opened without error")
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	b, err := NewFileOutcomeStore(path)
	if err != nil {
		t.Fatalf("open after close: %v", err)
	}
	_ = b.Close()
}

func TestSubmitFailurePersistsNothing(t *testing.T) {
	// Crash-before-persistence model: a Submit that cannot persist must
	// commit nothing, so a later recovery never sees a phantom event.
	// Rejected (invalid) submissions and un-openable paths are the
	// observable instances of "the write never happened".
	s := NewMemoryOutcomeStore()
	bad := settledJob("j1", "d1", "a", StatusAccepted, 1)
	bad.DecidingAttemptID = ""
	if _, err := s.Submit(bad); err == nil {
		t.Fatal("invalid event accepted")
	}
	if s.Len() != 0 {
		t.Fatalf("phantom events: len=%d", s.Len())
	}
	if _, ok := s.Latest("j1"); ok {
		t.Fatal("phantom latest")
	}
	// A ledger path that cannot be opened (a directory) fails loudly.
	if _, err := NewFileOutcomeStore(t.TempDir()); err == nil {
		t.Fatal("expected open of directory to fail")
	}
}

// TestConcurrentSubmitVersionOrdering: scrambled + duplicated version
// submissions linearize to exactly versions 1..K, each committed once.
func TestConcurrentSubmitVersionOrdering(t *testing.T) {
	s := NewMemoryOutcomeStore()
	const versions = 12
	var wg sync.WaitGroup
	errs := make(chan error, versions*4)
	// Every version submitted twice, from interleaved goroutines, with
	// retry-on-gap so scrambled arrival still converges.
	for v := uint64(1); v <= versions; v++ {
		for dup := 0; dup < 2; dup++ {
			wg.Add(1)
			go func(v uint64) {
				defer wg.Done()
				for tries := 0; tries < 200; tries++ {
					_, err := s.Submit(settledJob("hot", "d", "a", StatusAccepted, v))
					if err == nil {
						return
					}
					if isConflict(err) {
						errs <- fmt.Errorf("v%d: %w", v, err)
						return
					}
					// gap/stale: spin briefly; stale-after-commit is fine.
					if isStale(err) {
						return
					}
				}
				errs <- fmt.Errorf("v%d never committed", v)
			}(v)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	evs := s.Events()
	if len(evs) != versions {
		t.Fatalf("ledger has %d events, want %d", len(evs), versions)
	}
	for i, ev := range evs {
		if ev.Version != uint64(i+1) || ev.Seq != uint64(i+1) {
			t.Fatalf("event %d has version=%d seq=%d", i, ev.Version, ev.Seq)
		}
	}
	latest, _ := s.Latest("hot")
	if latest.Version != versions {
		t.Fatalf("latest=%d want %d", latest.Version, versions)
	}
}

func isConflict(err error) bool {
	return err != nil && contains(err.Error(), "conflicting")
}

func isStale(err error) bool {
	return err != nil && contains(err.Error(), "stale")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
