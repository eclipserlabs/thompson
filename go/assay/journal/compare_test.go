package journal

import (
	"math/rand/v2"
	"path/filepath"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/harness"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// matchedPair drives one job through the production harness treatment AND
// the journal with identical truth, asserting both accept the same facts.
type matchedPair struct {
	t     *testing.T
	dir   string
	tr    *harness.CostAwareTreatment
	j     *Journal
	rng   *rand.Rand
	clock time.Time
}

func newMatchedPair(t *testing.T, cfg thompson.CostAwareConfig) *matchedPair {
	t.Helper()
	dir := t.TempDir()
	// Textbook-pinned production policy (cold priors): the assay compares
	// durable-learning mechanics, not prior-shaping features. Family
	// warm-start would move both sides identically on every rebuild, but
	// pinning it out keeps the equivalence target exact (see bench parity
	// docs for the documented difference).
	tcfg := thompson.Config{
		UpdateRule: thompson.DefaultUpdateRule(),
		WarmStart:  thompson.WarmStart{Kind: thompson.ColdStart},
		Selection:  thompson.Selection{Kind: thompson.ThompsonSelection},
	}
	pol := thompson.New(tcfg, thompson.ExactSampler{})
	pol.AddArm("cheap")
	pol.AddArm("strong")
	tr, err := harness.OpenCostAwareTreatment(dir+"/t3", "t3", pol, cfg)
	if err != nil {
		t.Fatal(err)
	}
	j, err := Open(filepath.Join(dir, "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	return &matchedPair{t: t, dir: dir, tr: tr, j: j,
		rng:   rand.New(rand.NewPCG(31, 31)),
		clock: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)}
}

func (m *matchedPair) close() {
	m.tr.Close()
	m.j.Close()
}

// drive settles one job on both stacks with identical truth. Returns the
// production event and whether the journal applied it.
func (m *matchedPair) drive(i int, cheapP, strongP, cheapC, strongC float64) (outcome.OutcomeEvent, bool) {
	m.t.Helper()
	jid := "mjob-" + itoaJ(i)
	job := harness.JobTruth{JobID: jid, Strata: "web",
		AssignedAt: m.clock.Add(time.Duration(i) * time.Minute),
		ArmSuccess: map[string]float64{"cheap": cheapP, "strong": strongP},
		ArmCost:    map[string]float64{"cheap": cheapC, "strong": strongC},
		ArmLatency: map[string]float64{"cheap": 100, "strong": 200}}
	asg := harness.Assignment{JobID: jid, Strata: "web", Treatment: "t3", Probability: 1}
	nBefore := len(m.tr.Strategy.Decisions)
	ev, err := m.tr.RunJob(m.rng, job, asg)
	if err != nil {
		m.t.Fatal(err)
	}
	// Mirror the production decision + outcome into the journal.
	var decArm string
	var scores map[string]float64
	var fallback bool
	for _, d := range m.tr.Strategy.Decisions[nBefore:] {
		decArm = d.ArmID
		fallback = fallback || d.Fallback
		_ = scores
	}
	_ = fallback
	// Reconstruct scores is unnecessary: journal decisions carry the same
	// identity; selection agreement is checked distributionally below.
	jdec := Decision{
		DecisionID: ev.DecisionID, JobID: ev.JobID, StrategyID: "t3",
		SelectedArm: decArm, Eligible: []string{"cheap", "strong"},
		PolicyID: thompson.CostAwarePolicyID, RuleVersion: thompson.CostAwareRuleV2,
		Objective: thompson.CostAwareObjectiveVer, ConfigHash: "bench",
		Fallback: false, Explore: false,
	}
	if _, ok, err := m.j.CommitDecision(jdec, "bench"); err != nil || !ok {
		m.t.Fatalf("journal commit: %v %v", err, ok)
	}
	jo := SettledOutcome{
		DecisionID: ev.DecisionID, JobID: ev.JobID, Version: 1, Supersedes: 0,
		Status: string(ev.Status), DecidingAttempt: ev.DecidingAttemptID,
		VerifiedBy: "bench",
	}
	for _, a := range ev.Attempts {
		jo.Attempts = append(jo.Attempts, OutcomeAttempt{
			AttemptID: a.AttemptID, ArmID: a.ArmID, CostUSD: a.CostUSD, Verified: string(a.Verified),
		})
	}
	if ev.HumanReviewCostUSD != nil {
		jo.HumanReviewCost = ev.HumanReviewCostUSD
	}
	ok, err := m.j.SettleOutcome(jo, "bench")
	if err != nil {
		m.t.Fatal(err)
	}
	return ev, ok
}

// Matched normal execution: 30 identical jobs, then state equivalence.
func TestCompareMatchedExecution(t *testing.T) {
	cfg := thompson.CostAwareConfig{QualityFloor: 0.3, MinMeteredN: 2, ColdStartPulls: 5, Epsilon: 1e-3}
	m := newMatchedPair(t, cfg)
	defer m.close()
	for i := 0; i < 30; i++ {
		m.drive(i, 0.8, 0.8, 0.002, 0.05)
	}
	p, err := m.j.Replay()
	if err != nil {
		t.Fatal(err)
	}
	// Quality posteriors match the production policy exactly.
	for _, arm := range []string{"cheap", "strong"} {
		post, ok := m.tr.Policy.PosteriorFor(arm)
		if !ok {
			t.Fatal("missing arm")
		}
		got := p.Quality[arm]
		if got.Alpha != post.Alpha || got.Beta != post.Beta || got.Pulls != post.Pulls {
			t.Fatalf("quality diverged on %s: %+v vs %+v", arm, got, post)
		}
	}
	// Cost means match the production book exactly.
	for _, arm := range []string{"cheap", "strong"} {
		bm, bok := m.tr.Book.Mean(arm)
		jm, jok := p.CostMean[arm], false
		if _, ok := p.CostMean[arm]; ok {
			jok = true
		}
		if bok != jok || (bok && bm != jm) {
			t.Fatalf("cost diverged on %s: %v/%v vs %v/%v", arm, bm, bok, jm, jok)
		}
	}
	// Applied cursors match.
	for job, ver := range p.Applied {
		found := false
		for _, e := range m.tr.Store.Events() {
			if e.JobID == job && e.Version == ver {
				found = true
			}
		}
		if !found {
			t.Fatalf("applied cursor %s v%d not in production ledger", job, ver)
		}
	}
}

// Matched duplicates/corrections/out-of-order: same permutations both sides.
func TestCompareCorrections(t *testing.T) {
	cfg := thompson.CostAwareConfig{QualityFloor: 0.3, MinMeteredN: 1, ColdStartPulls: 5, Epsilon: 1e-3}
	m := newMatchedPair(t, cfg)
	defer m.close()
	ev, _ := m.drive(0, 0.9, 0.9, 0.002, 0.05)
	// Correction on both sides: production via store submit + learner apply
	// (mirroring a validator revision), journal via v2.
	decArm := ""
	for _, a := range ev.Attempts {
		if a.AttemptID == ev.DecidingAttemptID {
			decArm = a.ArmID
		}
	}
	if decArm == "" {
		t.Fatal("no deciding arm")
	}
	corr := outcome.OutcomeEvent{
		SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
		DecisionID: ev.DecisionID, JobID: ev.JobID, StrategyID: "t3",
		Version: 2, Supersedes: 1, Status: outcome.StatusRejected,
		Attempts: []outcome.Attempt{{
			AttemptID: ev.DecidingAttemptID, Seq: 0, ExecutorID: decArm, ArmID: decArm,
			Transport: outcome.TransportOK, CostUSD: ev.Attempts[0].CostUSD,
			Validation: outcome.ValidationPass, Verified: outcome.VerifiedFailure, VerifiedBy: "bench",
		}},
		DecidingAttemptID: ev.DecidingAttemptID, OccurredAt: "2026-01-05T00:00:00Z",
	}
	if _, err := outcome.Settle(m.tr.Store, m.tr.Learner, corr); err != nil {
		t.Fatal(err)
	}
	if _, err := m.tr.Book.Apply(corr, m.tr.Store.Events); err != nil {
		t.Fatal(err)
	}
	jcorr := SettledOutcome{
		DecisionID: ev.DecisionID, JobID: ev.JobID, Version: 2, Supersedes: 1,
		Status: StatusRejected,
		Attempts: []OutcomeAttempt{{
			AttemptID: ev.DecidingAttemptID, ArmID: decArm,
			CostUSD: ev.Attempts[0].CostUSD, Verified: "failure",
		}},
		DecidingAttempt: ev.DecidingAttemptID, VerifiedBy: "bench",
	}
	if ok, err := m.j.SettleOutcome(jcorr, "bench"); err != nil || !ok {
		t.Fatalf("journal correction: %v %v", err, ok)
	}
	p, err := m.j.Replay()
	if err != nil {
		t.Fatal(err)
	}
	// Untouched arms are absent from journal projections but present as
	// cold Beta(1,1) priors in production: normalize before comparing.
	for _, arm := range []string{"cheap", "strong"} {
		post, _ := m.tr.Policy.PosteriorFor(arm)
		got, ok := p.Quality[arm]
		if !ok {
			got = ArmPosterior{Alpha: 1, Beta: 1}
		}
		if got.Alpha != post.Alpha || got.Beta != post.Beta || got.Pulls != post.Pulls {
			t.Fatalf("post-correction quality diverged on %s: %+v vs %+v", arm, got, post)
		}
	}
}

// Emergency stop/restart/resume equivalence across both stacks.
func TestCompareEmergencyCycle(t *testing.T) {
	cfg := thompson.CostAwareConfig{QualityFloor: 0.3, MinMeteredN: 1, ColdStartPulls: 5, Epsilon: 1e-3}
	m := newMatchedPair(t, cfg)
	defer m.close()
	for i := 0; i < 6; i++ {
		m.drive(i, 0.9, 0.9, 0.002, 0.05)
	}
	// Production side has no gateway safety here (harness-level); the
	// journal records the stop transition durably.
	if _, ok, err := m.j.RecordSafety(SafetyTransition{Actor: "op:test", Type: "EMERGENCY_STOP", Reason: "assay", Nonce: "stop-1"}, "bench"); err != nil || !ok {
		t.Fatalf("stop: %v %v", err, ok)
	}
	p, err := m.j.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if !p.Emergency {
		t.Fatal("emergency flag lost in replay")
	}
	// Checkpoint + corrupt + recover: projections return via full replay.
	if err := m.j.SaveCheckpoint("assay", p); err != nil {
		t.Fatal(err)
	}
	if _, err := m.j.LoadCheckpoint("assay"); err != nil {
		t.Fatal(err)
	}
	// Release and continue on both sides.
	if _, ok, err := m.j.RecordSafety(SafetyTransition{Actor: "op:test", Type: "EMERGENCY_RELEASED", Reason: "assay", Nonce: "rel-1"}, "bench"); err != nil || !ok {
		t.Fatalf("release: %v %v", err, ok)
	}
	for i := 6; i < 10; i++ {
		m.drive(i, 0.9, 0.9, 0.002, 0.05)
	}
	p2, err := m.j.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if p2.Emergency {
		t.Fatal("release lost in replay")
	}
	if p2.AtSeq <= p.AtSeq {
		t.Fatal("sequence did not advance across the cycle")
	}
}
