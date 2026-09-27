package bench

import (
	"encoding/json"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/harness"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// benchOutcome builds a settled event for the systems battery.
func benchOutcome(job, arm string, ver uint64, ok bool, cost float64) outcome.OutcomeEvent {
	st := outcome.StatusAccepted
	vered := outcome.VerifiedSuccess
	if !ok {
		st, vered = outcome.StatusRejected, outcome.VerifiedFailure
	}
	return outcome.OutcomeEvent{
		SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
		DecisionID: "dec-" + job, JobID: job, StrategyID: "bench",
		Version: ver, Supersedes: ver - 1, Status: st,
		Attempts: []outcome.Attempt{{
			AttemptID: job + "-a0", Seq: 0, ExecutorID: arm, ArmID: arm,
			Transport: outcome.TransportOK, LatencyMs: 10, CostUSD: &cost,
			Validation: outcome.ValidationPass, Verified: vered, VerifiedBy: "bench",
		}},
		DecidingAttemptID: job + "-a0", VerifiedBy: "bench",
		OccurredAt: "2026-01-05T00:00:00Z",
	}
}

func benchLearner(pol *thompson.Policy, store *outcome.MemoryOutcomeStore) *outcome.Learner {
	return outcome.NewLearner(pol, outcome.BinaryStatusMapper{}, store.Events)
}

// S1: uninterrupted vs interrupted+resume vs fresh rebuild produce identical
// authoritative state, identity, config, and subsequent decisions.
func TestS1ReplayEquivalence(t *testing.T) {
	mkpolicy := func() *thompson.Policy { return textbookPolicy("a", "b") }
	var evs []outcome.OutcomeEvent
	for i := 0; i < 20; i++ {
		arm := "a"
		if i%3 == 0 {
			arm = "b"
		}
		evs = append(evs, benchOutcome(jobName(i), arm, 1, i%4 != 3, 0.01))
	}
	// (a) Uninterrupted.
	sA := outcome.NewMemoryOutcomeStore()
	pA := mkpolicy()
	lA := benchLearner(pA, sA)
	for _, ev := range evs {
		if _, err := outcome.Settle(sA, lA, ev); err != nil {
			t.Fatal(err)
		}
	}
	// (b) Interrupted at 12 + resume (no checkpoint: full replay).
	sB := outcome.NewMemoryOutcomeStore()
	pB := mkpolicy()
	lB := benchLearner(pB, sB)
	for _, ev := range evs[:12] {
		if _, err := outcome.Settle(sB, lB, ev); err != nil {
			t.Fatal(err)
		}
	}
	pB2 := mkpolicy()
	lB2, err := outcome.Resume(pB2, sB.Events(), nil, outcome.BinaryStatusMapper{}, sB.Events)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs[12:] {
		if _, err := outcome.Settle(sB, lB2, ev); err != nil {
			t.Fatal(err)
		}
	}
	// (c) Fresh rebuild over the full history.
	pC := mkpolicy()
	lC := benchLearner(pC, sA)
	if _, err := lC.Rebuild(sA.Events()); err != nil {
		t.Fatal(err)
	}
	jA, _ := json.Marshal(lA.CheckpointOf(len(sA.Events())))
	jB, _ := json.Marshal(lB2.CheckpointOf(len(sB.Events())))
	jC, _ := json.Marshal(lC.CheckpointOf(len(sA.Events())))
	if string(jA) != string(jB) || string(jA) != string(jC) {
		t.Fatal("S1: recovery-state divergence across uninterrupted/resume/rebuild")
	}
	// Subsequent decisions identical under controlled RNG.
	for _, seed := range []uint64{1, 2, 3} {
		r1 := rand.New(rand.NewPCG(seed, seed))
		r2 := rand.New(rand.NewPCG(seed, seed))
		r3 := rand.New(rand.NewPCG(seed, seed))
		s1, _, err1 := pA.SelectWithScores(r1)
		s2, _, err2 := pB2.SelectWithScores(r2)
		s3, _, err3 := pC.SelectWithScores(r3)
		if err1 != nil || err2 != nil || err3 != nil {
			t.Fatal(err1, err2, err3)
		}
		if s1 != s2 || s1 != s3 {
			t.Fatalf("S1: post-recovery decisions diverge: %s %s %s", s1, s2, s3)
		}
	}
}

// S3: every delivery permutation converges to the authoritative history
// with no double learning.
func TestS3CorrectionPermutations(t *testing.T) {
	v1 := benchOutcome("job-x", "a", 1, true, 0.01)
	v2 := benchOutcome("job-x", "a", 2, false, 0.01)
	v2.Attempts[0].Verified = outcome.VerifiedFailure
	authoritative := []outcome.OutcomeEvent{v2}
	perms := [][]outcome.OutcomeEvent{
		{v1, v2},
		{v1, v1, v2}, // duplicate redelivery
		{v2},         // correction first (gap refusal expected on v1-after)
		{v1, v2, v2}, // duplicate correction
		{v1, v2, v1}, // stale redelivery after correction
		{v2, v1},     // reversed (v1 stale)
		{v1, v1, v1, v2, v2},
	}
	for pi, perm := range perms {
		s := outcome.NewMemoryOutcomeStore()
		p := textbookPolicy("a", "b")
		l := benchLearner(p, s)
		for _, ev := range perm {
			_, _ = outcome.Settle(s, l, ev) // gaps/duplicates may refuse; that is correct
		}
		// Rebuild from authoritative history must equal live state whenever
		// the live store actually holds the authoritative versions.
		hasV2 := false
		for _, e := range s.Events() {
			if e.JobID == "job-x" && e.Version == 2 {
				hasV2 = true
			}
		}
		if !hasV2 {
			continue // permutation never delivered v2: nothing to compare
		}
		pAuth := textbookPolicy("a", "b")
		lAuth := benchLearner(pAuth, s)
		if _, err := lAuth.Rebuild(authoritative); err != nil {
			t.Fatal(err)
		}
		jLive, _ := json.Marshal(p.Snapshot())
		jAuth, _ := json.Marshal(pAuth.Snapshot())
		if string(jLive) != string(jAuth) {
			t.Fatalf("S3 perm %d: live posteriors diverge from authoritative", pi)
		}
	}
}

// S2 boundary probe: harness-level re-execution of the same job duplicates
// assignment rows (the exp-run runner dedups via assigned-set; the harness
// TreatmentInstance does not). Recorded as a scoped finding, not a product
// defect: harness fixtures are single-pass by contract.
func TestS2HarnessReexecutionDuplicates(t *testing.T) {
	dir := t.TempDir()
	tr, err := harness.OpenTreatment(dir+"/t", "t", thompson.NewDefault("a"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	rng := rand.New(rand.NewPCG(5, 5))
	job := harness.JobTruth{JobID: "j1", Strata: "s",
		AssignedAt: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
		ArmSuccess: map[string]float64{"a": 1.0}, ArmCost: map[string]float64{"a": 0.01},
		ArmLatency: map[string]float64{"a": 10}}
	asg := harness.Assignment{JobID: "j1", Strata: "s", Treatment: "t", Probability: 1}
	strat := harness.StaticStrategy{StrategyID: "t", Arms: []string{"a"}, MaxRetries: 0, Verifier: "v"}
	if _, err := tr.RunJob(rng, strat, job, asg); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.RunJob(rng, strat, job, asg); err != nil {
		t.Fatal(err)
	}
	asgRows, _, err := harness.LoadTreatmentDir(dir + "/t")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, a := range asgRows {
		if a.JobID == "j1" {
			count++
		}
	}
	t.Logf("S2: harness re-execution produced %d assignment rows for one job", count)
	if count != 2 {
		t.Fatalf("S2 probe assumption changed: got %d rows", count)
	}
}

func jobName(i int) string { return "bench-job-" + itoaBench(i) }

func itoaBench(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}
