package outcome

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

func newTestPolicy() *thompson.Policy {
	return thompson.NewDefault("cheap", "strong")
}

func newLearnerOver(p *thompson.Policy, store OutcomeStore) *Learner {
	return NewLearner(p, BinaryStatusMapper{}, store.Events)
}

func posteriorOf(p *thompson.Policy, arm string) thompson.Posterior {
	post, ok := p.PosteriorFor(arm)
	if !ok {
		panic("missing arm " + arm)
	}
	return post
}

// referencePolicy folds the same (job, version, arm, reward) stream through
// direct Record calls with the same deterministic RNG, mirroring foldLocked.
func referencePolicy(t *testing.T, jobs []settledRef) *thompson.Policy {
	t.Helper()
	p := newTestPolicy()
	for _, j := range jobs {
		if err := p.Record(rngFor(j.job, j.version), j.arm, j.reward); err != nil {
			t.Fatalf("reference record: %v", err)
		}
	}
	return p
}

type settledRef struct {
	job     string
	version uint64
	arm     string
	reward  float64
}

func assertSameLearnedState(t *testing.T, got, want *thompson.Policy, arms ...string) {
	t.Helper()
	if got.TotalPulls() != want.TotalPulls() {
		t.Fatalf("totalPulls %d != %d", got.TotalPulls(), want.TotalPulls())
	}
	for _, arm := range arms {
		g, w := posteriorOf(got, arm), posteriorOf(want, arm)
		if g != w {
			t.Fatalf("arm %q: %+v != %+v", arm, g, w)
		}
	}
}

// Mandatory 1: the same outcome delivered 100 times changes the policy once.
func TestDuplicateDelivered100TimesLearnsOnce(t *testing.T) {
	store := NewMemoryOutcomeStore()
	p := newTestPolicy()
	l := newLearnerOver(p, store)
	ev := settledJob("j1", "d1", "cheap", StatusAccepted, 1)

	learned := 0
	for i := 0; i < 100; i++ {
		got, err := Settle(store, l, ev)
		if err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
		if got {
			learned++
		}
	}
	if learned != 1 {
		t.Fatalf("learned %d times, want 1", learned)
	}
	want := referencePolicy(t, []settledRef{{"j1", 1, "cheap", 1.0}})
	assertSameLearnedState(t, p, want, "cheap", "strong")
}

// Mandatory 2: UNKNOWN followed by ACCEPTED produces exactly one accepted
// contribution — the ambiguous period teaches nothing.
func TestUnknownThenAcceptedLearnsOnce(t *testing.T) {
	store := NewMemoryOutcomeStore()
	p := newTestPolicy()
	l := newLearnerOver(p, store)

	before := p.Snapshot()
	if learned, err := Settle(store, l, settledJob("j1", "d1", "cheap", StatusUnknown, 1)); err != nil || learned {
		t.Fatalf("unknown: learned=%v err=%v", learned, err)
	}
	afterUnknown := p.Snapshot()
	if afterUnknown.Arms[0] != before.Arms[0] || afterUnknown.Arms[1] != before.Arms[1] ||
		afterUnknown.TotalPulls != before.TotalPulls {
		t.Fatal("UNKNOWN moved the policy")
	}
	if learned, err := Settle(store, l, settledJob("j1", "d1", "cheap", StatusAccepted, 2)); err != nil || !learned {
		t.Fatalf("accepted: learned=%v err=%v", learned, err)
	}
	// Exactly the v2 contribution: rebuild folds latest (v2) only.
	want := referencePolicy(t, []settledRef{{"j1", 2, "cheap", 1.0}})
	assertSameLearnedState(t, p, want, "cheap", "strong")
}

// Mandatory 3: ACCEPTED corrected to REJECTED removes the original
// contribution — the final state equals "only v2 ever happened".
func TestAcceptedCorrectedToRejected(t *testing.T) {
	for _, rule := range []thompson.UpdateKind{thompson.Bernoulli, thompson.Fractional} {
		p := thompson.New(thompson.Config{
			UpdateRule: thompson.UpdateRule{Kind: rule},
			Reward:     thompson.DefaultRewardPolicy(),
			WarmStart:  thompson.DefaultWarmStart(),
			Selection:  thompson.Selection{Kind: thompson.ThompsonSelection},
		}, thompson.ExactSampler{})
		p.AddArm("cheap")
		p.AddArm("strong")
		store := NewMemoryOutcomeStore()
		l := newLearnerOver(p, store)

		if _, err := Settle(store, l, settledJob("j1", "d1", "cheap", StatusAccepted, 1)); err != nil {
			t.Fatalf("rule %v v1: %v", rule, err)
		}
		mid := posteriorOf(p, "cheap")
		if mid.Pulls != 1 {
			t.Fatalf("rule %v: v1 not applied", rule)
		}
		if _, err := Settle(store, l, settledJob("j1", "d1", "cheap", StatusRejected, 2)); err != nil {
			t.Fatalf("rule %v v2: %v", rule, err)
		}
		want := thompson.New(p.ConfigSnapshot(), thompson.ExactSampler{})
		want.AddArm("cheap")
		want.AddArm("strong")
		if err := want.Record(rngFor("j1", 2), "cheap", 0.0); err != nil {
			t.Fatal(err)
		}
		assertSameLearnedState(t, p, want, "cheap", "strong")
	}
}

// Mandatory 4: restart + replay reproduce the uninterrupted state, with no
// double learning across the checkpoint boundary.
func TestRestartReplayEqualsUninterrupted(t *testing.T) {
	dir := t.TempDir()
	ledgerPath := filepath.Join(dir, "outcomes.jsonl")
	ckptPath := filepath.Join(dir, "ckpt.json")

	stream := []OutcomeEvent{
		settledJob("j1", "d1", "cheap", StatusAccepted, 1),
		settledJob("j2", "d2", "strong", StatusRejected, 1),
		settledJob("j3", "d3", "cheap", StatusUnknown, 1),
		settledJob("j1", "d1", "cheap", StatusRejected, 2), // correction
		settledJob("j3", "d3", "cheap", StatusAccepted, 2), // late verification
		settledJob("j4", "d4", "strong", StatusAccepted, 1),
		settledJob("j4", "d4", "strong", StatusAccepted, 1), // duplicate
	}

	// Uninterrupted run with a mid-stream checkpoint.
	store, err := NewFileOutcomeStore(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	p := newTestPolicy()
	l := NewLearner(p, BinaryStatusMapper{}, store.Events)
	for i, ev := range stream {
		if _, err := Settle(store, l, ev); err != nil {
			t.Fatalf("settle %d: %v", i, err)
		}
		if i == 2 {
			if err := SaveCheckpoint(ckptPath, l.CheckpointOf(store.Len())); err != nil {
				t.Fatalf("checkpoint: %v", err)
			}
		}
	}
	if err := SaveCheckpoint(ckptPath, l.CheckpointOf(store.Len())); err != nil {
		t.Fatalf("final checkpoint: %v", err)
	}
	wantSnap := p.Snapshot()
	wantApplied := l.Applied()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulated restart: fresh process state, same files.
	store2, err := NewFileOutcomeStore(ledgerPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()
	cp, err := LoadCheckpoint(ckptPath)
	if err != nil || cp == nil {
		t.Fatalf("load checkpoint: %+v %v", cp, err)
	}
	p2 := newTestPolicy()
	l2, err := Resume(p2, store2.Events(), cp, BinaryStatusMapper{}, store2.Events)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	gotSnap := p2.Snapshot()
	if len(gotSnap.Arms) != len(wantSnap.Arms) || gotSnap.TotalPulls != wantSnap.TotalPulls {
		t.Fatalf("state diverged: %+v vs %+v", gotSnap, wantSnap)
	}
	for i := range gotSnap.Arms {
		if gotSnap.Arms[i] != wantSnap.Arms[i] {
			t.Fatalf("arm %d diverged: %+v vs %+v", i, gotSnap.Arms[i], wantSnap.Arms[i])
		}
	}
	gotApplied := l2.Applied()
	if len(gotApplied) != len(wantApplied) {
		t.Fatalf("cursor diverged: %v vs %v", gotApplied, wantApplied)
	}
	for k, v := range wantApplied {
		if gotApplied[k] != v {
			t.Fatalf("cursor %q: %d != %d", k, gotApplied[k], v)
		}
	}

	// Restart with NO checkpoint (full rebuild from ledger) agrees too.
	p3 := newTestPolicy()
	l3, err := Resume(p3, store2.Events(), nil, BinaryStatusMapper{}, store2.Events)
	if err != nil {
		t.Fatalf("resume without checkpoint: %v", err)
	}
	_ = l3
	assertSameLearnedState(t, p3, p, "cheap", "strong")
}

// Mandatory 5: a fallback chain records every attempt but learns only the
// deciding attempt's arm mapping. A human fallback (no arm) moves no
// posterior while the job still counts as ACCEPTED with full costs.
func TestFallbackChainLearnsDecidingArmOnly(t *testing.T) {
	cheap, strong, human := 0.0004, 0.02, 2.10
	a1 := Attempt{AttemptID: "a1", Seq: 0, ExecutorID: "cheap", ArmID: "cheap",
		Transport: TransportOK, LatencyMs: 320, CostUSD: &cheap,
		Validation: ValidationFail, FailureCategory: "schema_violation",
		Verified: VerifiedFailure, VerifiedBy: "validator:v7"}
	a2 := Attempt{AttemptID: "a2", Seq: 1, ExecutorID: "strong", ArmID: "strong",
		Transport: TransportOK, LatencyMs: 900, CostUSD: &strong,
		Validation: ValidationFail, FailureCategory: "factual_error",
		Verified: VerifiedFailure, VerifiedBy: "judge:strong-v2"}
	a3 := Attempt{AttemptID: "a3", Seq: 2, ExecutorID: "human-pool-b",
		Transport: TransportOK, LatencyMs: 2400000,
		Validation: ValidationPass, Verified: VerifiedSuccess, VerifiedBy: "human:pool-b"}
	ev := OutcomeEvent{
		SchemaVersion: SchemaVersion, EventType: EventJobSettled,
		DecisionID: "d2", JobID: "j2", StrategyID: "cheap-then-strong-then-human",
		Version: 1, Status: StatusAccepted,
		Attempts: []Attempt{a1, a2, a3}, DecidingAttemptID: "a3",
		HumanReviewCostUSD: &human,
		VerifiedBy:         "human:pool-b", OccurredAt: "2026-09-27T00:40:00Z",
	}
	if err := ev.Validate(); err != nil {
		t.Fatalf("valid chain rejected: %v", err)
	}
	metered, unmetered := ev.TotalCostUSD()
	if unmetered != 1 { // human attempt unmetered at attempt level
		t.Fatalf("unmetered=%d want 1", unmetered)
	}
	if metered != cheap+strong {
		t.Fatalf("metered=%v want %v", metered, cheap+strong)
	}

	store := NewMemoryOutcomeStore()
	p := newTestPolicy()
	l := newLearnerOver(p, store)
	learned, err := Settle(store, l, ev)
	if err != nil {
		t.Fatal(err)
	}
	if learned {
		t.Fatal("human-decided job must not move any arm posterior")
	}
	if p.TotalPulls() != 0 {
		t.Fatalf("pulls=%d want 0", p.TotalPulls())
	}
	// Failed attempts keep their verdicts: nothing rewrites them.
	latest, _ := store.Latest("j2")
	for _, a := range latest.Attempts[:2] {
		if a.Verified != VerifiedFailure {
			t.Fatalf("attempt %s verdict rewritten", a.AttemptID)
		}
	}

	// Variant: deciding attempt maps to an arm — only that arm learns.
	ev2 := ev
	ev2.JobID = "j3"
	ev2.DecisionID = "d3"
	a2b := a2
	a2b.Verified = VerifiedSuccess
	a2b.Validation = ValidationPass
	ev2.Attempts = []Attempt{a1, a2b}
	ev2.DecidingAttemptID = "a2"
	ev2.HumanReviewCostUSD = nil
	if learned, err := Settle(store, l, ev2); err != nil || !learned {
		t.Fatalf("arm-decided: learned=%v err=%v", learned, err)
	}
	want := referencePolicy(t, []settledRef{{"j3", 1, "strong", 1.0}})
	assertSameLearnedState(t, p, want, "cheap", "strong")
	if posteriorOf(p, "cheap").Pulls != 0 {
		t.Fatal("failed first attempt leaked into arm learning")
	}
}

// Mandatory 6 (learner half): concurrent Apply of committed events converges
// to the sequential reference. (Store-level linearization is covered by
// TestConcurrentSubmitVersionOrdering.)
func TestConcurrentApplyConverges(t *testing.T) {
	store := NewMemoryOutcomeStore()
	const jobs = 8
	for i := 0; i < jobs; i++ {
		job := string(rune('a' + i))
		if _, err := store.Submit(settledJob(job, "d"+job, "cheap", StatusAccepted, 1)); err != nil {
			t.Fatal(err)
		}
	}
	committed := store.Events()
	p := newTestPolicy()
	l := NewLearner(p, BinaryStatusMapper{}, store.Events)
	var wg sync.WaitGroup
	for _, ev := range committed {
		wg.Add(1)
		go func(ev OutcomeEvent) {
			defer wg.Done()
			if _, err := l.Apply(ev); err != nil {
				t.Errorf("apply %s: %v", ev.JobID, err)
			}
		}(ev)
	}
	wg.Wait()
	var refs []settledRef
	for i := 0; i < jobs; i++ {
		job := string(rune('a' + i))
		refs = append(refs, settledRef{job, 1, "cheap", 1.0})
	}
	assertSameLearnedState(t, p, referencePolicy(t, refs), "cheap", "strong")
}

// Corrected learning must hold under every selection configuration: the
// learner only calls Record, so selection kind changes sampling, never the
// fold. Each config's corrected state must equal its own direct-Record
// reference.
func TestCorrectedLearningAcrossSelectionKinds(t *testing.T) {
	kinds := map[string]thompson.Selection{
		"thompson": {Kind: thompson.ThompsonSelection},
		"ucb":      {Kind: thompson.UCBRegularized, C: 2.0, UntilPulls: 30},
		"phased":   {Kind: thompson.PhasedSelection, Bootstrap: 5, MinPullsForExploit: 5},
	}
	for name, sel := range kinds {
		t.Run(name, func(t *testing.T) {
			build := func() *thompson.Policy {
				p := thompson.New(thompson.Config{
					UpdateRule: thompson.DefaultUpdateRule(),
					Reward:     thompson.DefaultRewardPolicy(),
					WarmStart:  thompson.DefaultWarmStart(),
					Selection:  sel,
				}, thompson.ExactSampler{})
				p.AddArm("cheap")
				p.AddArm("strong")
				return p
			}
			store := NewMemoryOutcomeStore()
			p := build()
			l := NewLearner(p, BinaryStatusMapper{}, store.Events)
			if _, err := Settle(store, l, settledJob("j1", "d1", "cheap", StatusAccepted, 1)); err != nil {
				t.Fatal(err)
			}
			if _, err := Settle(store, l, settledJob("j1", "d1", "cheap", StatusRejected, 2)); err != nil {
				t.Fatal(err)
			}
			want := build()
			if err := want.Record(rngFor("j1", 2), "cheap", 0.0); err != nil {
				t.Fatal(err)
			}
			assertSameLearnedState(t, p, want, "cheap", "strong")
		})
	}
}

func TestPendingNeverLearns(t *testing.T) {
	store := NewMemoryOutcomeStore()
	p := newTestPolicy()
	l := newLearnerOver(p, store)
	if learned, err := Settle(store, l, settledJob("j1", "d1", "cheap", StatusPending, 1)); err != nil || learned {
		t.Fatalf("pending: learned=%v err=%v", learned, err)
	}
	if p.TotalPulls() != 0 {
		t.Fatal("PENDING moved the policy")
	}
}

// B7: rebuild failure rolls back to the pre-rebuild state instead of
// leaving a half-folded policy. An event naming an unknown arm fails the
// fold after genesis was already restored.
func TestRebuildFailureRollsBack(t *testing.T) {
	store := NewMemoryOutcomeStore()
	p := newTestPolicy()
	l := newLearnerOver(p, store)
	if _, err := Settle(store, l, settledJob("j1", "d1", "cheap", StatusAccepted, 1)); err != nil {
		t.Fatal(err)
	}
	before := p.Snapshot()
	beforeApplied := l.Applied()
	// Bypass the store (which would reject the unknown arm only at fold
	// time): rebuild directly over history plus a foreign event.
	bad := settledJob("j2", "d2", "ghost", StatusAccepted, 1)
	_, err := l.Rebuild(append(store.Events(), bad))
	if err == nil {
		t.Fatal("rebuild with unknown arm succeeded")
	}
	after := p.Snapshot()
	if len(after.Arms) != len(before.Arms) || after.TotalPulls != before.TotalPulls {
		t.Fatalf("policy left half-folded: %+v vs %+v", after, before)
	}
	for i := range after.Arms {
		if after.Arms[i] != before.Arms[i] {
			t.Fatalf("arm %d changed by failed rebuild", i)
		}
	}
	got := l.Applied()
	if len(got) != len(beforeApplied) {
		t.Fatalf("cursor changed by failed rebuild: %v", got)
	}
	for k, v := range beforeApplied {
		if got[k] != v {
			t.Fatalf("cursor %q changed by failed rebuild", k)
		}
	}
}

// B7: arms added under a live learner are refused by rebuilds until an
// explicit Rebase adopts them.
func TestArmSetChangeRequiresRebase(t *testing.T) {
	store := NewMemoryOutcomeStore()
	p := newTestPolicy()
	l := newLearnerOver(p, store)
	if _, err := Settle(store, l, settledJob("j1", "d1", "cheap", StatusAccepted, 1)); err != nil {
		t.Fatal(err)
	}
	p.AddArm("fresh")
	ev := settledJob("j2", "d2", "fresh", StatusAccepted, 1)
	if _, err := Settle(store, l, ev); err == nil {
		t.Fatal("settlement into un-rebased arm set succeeded (would wipe the arm)")
	}
	if p.HasArm("fresh") != true {
		t.Fatal("arm unexpectedly gone")
	}
	l.Rebase()
	// The refused v1 was still committed by Submit (ordering layer), so a
	// fresh job proves post-Rebase learning works.
	ev2 := settledJob("j3", "d3", "fresh", StatusAccepted, 1)
	if _, err := Settle(store, l, ev2); err != nil {
		t.Fatalf("settlement after Rebase failed: %v", err)
	}
	if posteriorOf(p, "fresh").Pulls != 1 {
		t.Fatal("rebased arm did not learn")
	}
}

// B7: incremental application and deterministic rebuild agree, including
// with discounting enabled (order-dependent updates replayed in order).
func TestIncrementalEqualsRebuildWithDiscount(t *testing.T) {
	mkpolicy := func() *thompson.Policy {
		cfg := thompson.DefaultConfig()
		cfg.Discount = 0.99
		p := thompson.New(cfg, thompson.ExactSampler{})
		p.AddArm("cheap")
		p.AddArm("strong")
		return p
	}
	stream := []OutcomeEvent{
		settledJob("j1", "d1", "cheap", StatusAccepted, 1),
		settledJob("j2", "d2", "strong", StatusRejected, 1),
		settledJob("j3", "d3", "cheap", StatusAccepted, 1),
		settledJob("j1", "d1", "cheap", StatusRejected, 2),
	}
	store := NewMemoryOutcomeStore()
	p := mkpolicy()
	l := NewLearner(p, BinaryStatusMapper{}, store.Events)
	for _, ev := range stream {
		if _, err := Settle(store, l, ev); err != nil {
			t.Fatalf("settle: %v", err)
		}
	}
	q := mkpolicy()
	l2 := NewLearner(q, BinaryStatusMapper{}, store.Events)
	moved, err := l2.Rebuild(store.Events())
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if !moved {
		t.Fatal("rebuild reported no movement over real history")
	}
	assertSameLearnedState(t, p, q, "cheap", "strong")
}

// B7: concurrent distinct-job applies each land exactly once; cursor is
// complete afterwards.
func TestConcurrentDistinctApplies(t *testing.T) {
	store := NewMemoryOutcomeStore()
	p := newTestPolicy()
	l := newLearnerOver(p, store)
	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			job := "job-" + itoa(i)
			if _, err := Settle(store, l, settledJob(job, "d", "cheap", StatusAccepted, 1)); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent settle: %v", err)
	}
	if got := p.TotalPulls(); got != n {
		t.Fatalf("pulls=%d want %d (lost or duplicated update)", got, n)
	}
	if len(l.Applied()) != n {
		t.Fatalf("cursor covers %d jobs want %d", len(l.Applied()), n)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

// B5: rebuilds preserve the logging-policy identity. The only config
// mutation path is RestoreSnapshot during rebuild; a drift there aborts
// instead of learning under a swapped configuration.
func TestRebuildPreservesLoggingPolicyID(t *testing.T) {
	store := NewMemoryOutcomeStore()
	p := newTestPolicy()
	before := p.LoggingPolicyID()
	l := newLearnerOver(p, store)
	if _, err := Settle(store, l, settledJob("j1", "d1", "cheap", StatusAccepted, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := Settle(store, l, settledJob("j1", "d1", "cheap", StatusRejected, 2)); err != nil {
		t.Fatal(err)
	}
	if got := p.LoggingPolicyID(); got != before {
		t.Fatalf("identity drifted across rebuild: %q vs %q", got, before)
	}
	if got := p.SamplerName(); got != "exact" {
		t.Fatalf("sampler changed across rebuild: %q", got)
	}
}
