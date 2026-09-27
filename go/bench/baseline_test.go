package bench

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// Corpus determinism + digests: same seed regenerates byte-identical
// fixtures; different seeds differ; digests verify content.
func TestCorpusDeterministic(t *testing.T) {
	a := Generate("equal-cost-gap", 101, 50)
	b := Generate("equal-cost-gap", 101, 50)
	if a.Digest != b.Digest {
		t.Fatal("same seed must reproduce digest")
	}
	if len(a.Jobs) != 50 {
		t.Fatalf("want 50 jobs, got %d", len(a.Jobs))
	}
	c := Generate("equal-cost-gap", 102, 50)
	if c.Digest == a.Digest {
		t.Fatal("different seeds must differ")
	}
	for _, name := range []string{"quality-gap", "static-matches", "deterioration", "drift",
		"delayed-missing", "corrections", "fallback-chains", "human-trap",
		"missing-costs", "heavy-tail", "difficulty-split", "intervention"} {
		sc := Generate(name, 101, 20)
		if sc.Digest == "" || len(sc.Jobs) != 20 {
			t.Fatalf("scenario %s malformed", name)
		}
		if got := Digest(sc); got != sc.Digest {
			t.Fatalf("scenario %s digest mismatch", name)
		}
	}
}

// Dev/eval seed sets are disjoint (tuning on eval is detectable).
func TestCorpusDevEvalDisjoint(t *testing.T) {
	dev := map[uint64]bool{}
	for _, s := range DevSeeds {
		dev[s] = true
	}
	for _, s := range EvalSeeds {
		if dev[s] {
			t.Fatalf("eval seed %d in dev set", s)
		}
	}
}

// textbookPolicy builds the production policy pinned to textbook assumptions
// (cold Beta(1,1) priors, Bernoulli update, exact Thompson sampling).
// Production DEFAULTS differ deliberately: family-similarity warm start lets
// related arms borrow strength. The research baseline excludes warm start by
// design (it measures the textbook rule); the difference is documented, not
// hidden — see TestWarmStartDifference.
func textbookPolicy(arms ...string) *thompson.Policy {
	cfg := thompson.Config{
		UpdateRule: thompson.DefaultUpdateRule(),
		WarmStart:  thompson.WarmStart{Kind: thompson.ColdStart},
		Selection:  thompson.Selection{Kind: thompson.ThompsonSelection},
	}
	p := thompson.New(cfg, thompson.ExactSampler{})
	for _, a := range arms {
		p.AddArm(a)
	}
	return p
}

// Parity 1: identical posteriors given identical reward streams.
func TestMinimalPosteriorParity(t *testing.T) {
	prod := textbookPolicy("a", "b")
	mine := NewMinimalPolicy("a", "b")
	rng := rand.New(rand.NewPCG(7, 7))
	stream := []struct {
		arm string
		win bool
	}{{"a", true}, {"a", false}, {"b", true}, {"b", true}, {"a", true}}
	for _, s := range stream {
		var rew float64
		if s.win {
			rew = 1.0
		}
		// Production default update rule on binary rewards.
		if err := prod.Record(rng, s.arm, rew); err != nil {
			t.Fatal(err)
		}
		if err := mine.Observe(s.arm, s.win); err != nil {
			t.Fatal(err)
		}
	}
	for _, arm := range []string{"a", "b"} {
		post, ok := prod.PosteriorFor(arm)
		if !ok {
			t.Fatal("missing arm")
		}
		mean, pulls, ok := mine.Mean(arm)
		if !ok {
			t.Fatal("missing arm")
		}
		if math.Abs(post.Mean()-mean) > 1e-12 {
			t.Fatalf("arm %s posterior mean %v != %v", arm, post.Mean(), mean)
		}
		if post.Pulls != pulls {
			t.Fatalf("arm %s pulls %d != %d", arm, post.Pulls, pulls)
		}
	}
}

// Parity 2: selection distributions statistically indistinguishable.
// 20000 draws each; same-arm selection frequency within 3 points.
func TestMinimalSelectionDistribution(t *testing.T) {
	prod := textbookPolicy("a", "b")
	mine := NewMinimalPolicy("a", "b")
	rng := rand.New(rand.NewPCG(1, 1))
	for i := 0; i < 30; i++ {
		arm := "a"
		if i%3 == 0 {
			arm = "b"
		}
		win := i%2 == 0
		var rew float64
		if win {
			rew = 1.0
		}
		if err := prod.Record(rng, arm, rew); err != nil {
			t.Fatal(err)
		}
		if err := mine.Observe(arm, win); err != nil {
			t.Fatal(err)
		}
	}
	r1 := rand.New(rand.NewPCG(99, 99))
	r2 := rand.New(rand.NewPCG(99, 99))
	pA, mA, n := 0, 0, 20000
	for i := 0; i < n; i++ {
		s1, _, err := prod.SelectWithScores(r1)
		if err != nil {
			t.Fatal(err)
		}
		if s1 == "a" {
			pA++
		}
		s2, _, err := mine.Select(r2)
		if err != nil {
			t.Fatal(err)
		}
		if s2 == "a" {
			mA++
		}
	}
	if d := math.Abs(float64(pA-mA)) / float64(n); d > 0.03 {
		t.Fatalf("selection distributions differ: prod %.3f mine %.3f", float64(pA)/float64(n), float64(mA)/float64(n))
	}
}

// LoggedPolicy: replay reconstructs state; limitation documented by test:
// redelivery double-learns (no duplicate suppression by design).
func TestLoggedPolicyReplayAndLimitation(t *testing.T) {
	dir := t.TempDir()
	lp, err := OpenLoggedPolicy(dir+"/learn.jsonl", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if err := lp.Observe("j1", "a", true); err != nil {
		t.Fatal(err)
	}
	if err := lp.Observe("j2", "b", false); err != nil {
		t.Fatal(err)
	}
	if err := lp.Close(); err != nil {
		t.Fatal(err)
	}
	lp2, err := OpenLoggedPolicy(dir+"/learn.jsonl", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	defer lp2.Close()
	m1, p1, _ := lp2.Mean("a")
	if m1 != 2.0/3.0 || p1 != 1 {
		t.Fatalf("replay diverged: %v %d", m1, p1)
	}
	// Documented limitation: redelivering j1 learns twice.
	if err := lp2.Observe("j1", "a", true); err != nil {
		t.Fatal(err)
	}
	m2, _, _ := lp2.Mean("a")
	if m2 != 3.0/4.0 {
		t.Fatalf("expected double-learn mean 0.75, got %v", m2)
	}
}

// Documents the deliberate baseline exclusion: production defaults apply
// family-similarity warm start, so a fresh arm in a known family does NOT
// start at Beta(1,1). The research baseline measures the textbook rule;
// any comparison against production defaults must account for this head
// start (it favors arms in families with early winners).
func TestWarmStartDifference(t *testing.T) {
	prod := thompson.NewDefault("fam/model-a", "fam/model-b")
	rng := rand.New(rand.NewPCG(3, 3))
	for i := 0; i < 10; i++ {
		if err := prod.Record(rng, "fam/model-a", 1.0); err != nil {
			t.Fatal(err)
		}
	}
	postB, ok := prod.PosteriorFor("fam/model-b")
	if !ok {
		t.Fatal("missing arm")
	}
	// b never pulled: textbook prior would be exactly (1,1).
	if postB.Alpha == 1 && postB.Beta == 1 {
		t.Fatal("expected family warm-start to move the unpulled arm")
	}
	t.Logf("unpulled arm posterior under family warm start: alpha=%.3f beta=%.3f pulls=%d",
		postB.Alpha, postB.Beta, postB.Pulls)
}
