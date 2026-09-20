package propensity

import (
	"math"
	"testing"
)

// TestHighDrawMonteCarloAgreesWithReference is the core agreement test. The
// tolerance is set from the binomial standard error of the draw count, not
// picked to make the test pass: at 200k draws and p near 0.25 the standard
// error is about 1e-3, so 6e-3 is roughly six sigma.
func TestHighDrawMonteCarloAgreesWithReference(t *testing.T) {
	states := [][]Arm{
		identical(2, 1, 1),
		identical(4, 1, 1),
		identical(8, 4, 1),
		{{"a", 70, 30}, {"b", 50, 50}, {"c", 50, 50}, {"d", 50, 50}},
		{{"a", 101, 99}, {"b", 100, 100}, {"c", 102, 98}},
		{{"a", 12, 4}, {"b", 6, 6}, {"c", 3, 9}},
	}
	const draws = 200000
	for _, arms := range states {
		ref := mustReference(t, arms)
		mc := MonteCarlo(arms, draws, 20240101)
		for _, a := range arms {
			p, q := ref.Probs[a.ID], mc.Probs[a.ID]
			se := math.Sqrt(p * (1 - p) / draws)
			tol := 6*se + 1e-6
			if math.Abs(p-q) > tol {
				t.Fatalf("arm %s: reference %.6f, %d-draw Monte Carlo %.6f, |diff| %.6f > 6 sigma %.6f",
					a.ID, p, draws, q, math.Abs(p-q), tol)
			}
		}
	}
}

// TestMonteCarloConvergesTowardReference checks the estimate actually improves
// with draws rather than merely being close once.
func TestMonteCarloConvergesTowardReference(t *testing.T) {
	arms := []Arm{{"a", 70, 30}, {"b", 50, 50}, {"c", 45, 55}, {"d", 40, 60}}
	ref := mustReference(t, arms)
	errAt := func(draws int) float64 {
		mc := MonteCarlo(arms, draws, 7)
		worst := 0.0
		for _, a := range arms {
			if d := math.Abs(mc.Probs[a.ID] - ref.Probs[a.ID]); d > worst {
				worst = d
			}
		}
		return worst
	}
	lo, hi := errAt(2000), errAt(200000)
	if hi >= lo {
		t.Fatalf("error did not shrink with draws: 2k=%.6f, 200k=%.6f", lo, hi)
	}
}

func TestMonteCarloProbabilitiesSumToOne(t *testing.T) {
	arms := []Arm{{"a", 5, 1}, {"b", 2, 2}, {"c", 1, 5}}
	mc := MonteCarlo(arms, 10000, 123)
	sum, wins := 0.0, 0
	for _, a := range arms {
		sum += mc.Probs[a.ID]
		wins += mc.Wins[a.ID]
	}
	if math.Abs(sum-1) > 1e-12 {
		t.Fatalf("probabilities sum to %v", sum)
	}
	if wins != mc.Draws {
		t.Fatalf("win counts sum to %d, want %d", wins, mc.Draws)
	}
}

func TestMonteCarloIsDeterministic(t *testing.T) {
	arms := []Arm{{"a", 2, 3}, {"b", 4, 1}}
	a := MonteCarlo(arms, 10000, 99)
	b := MonteCarlo(arms, 10000, 99)
	for _, arm := range arms {
		if a.Wins[arm.ID] != b.Wins[arm.ID] {
			t.Fatalf("not deterministic for %s: %d vs %d", arm.ID, a.Wins[arm.ID], b.Wins[arm.ID])
		}
	}
}

// TestZeroWinsIsLabelledNotTreatedAsZeroProbability is the central semantic
// test. The arm is genuinely reachable -- the reference says so -- and a finite
// Monte-Carlo run happens not to see it. The status must say that, and no
// propensity may be offered.
func TestZeroWinsIsLabelledNotTreatedAsZeroProbability(t *testing.T) {
	// arm b is behind by enough that no achievable Monte-Carlo budget samples it,
	// yet its probability (~4e-9) is far above the numerical resolution floor.
	arms := []Arm{{"a", 70, 30}, {"b", 30, 70}}
	ref := mustReference(t, arms)
	if ref.Probs["b"] <= 0 {
		t.Fatalf("test premise broken: reference probability for b is %v, expected > 0", ref.Probs["b"])
	}

	th := DefaultThresholds()
	rec := ReconstructMonteCarlo(arms, 2000, 42, true, ReferenceOptions{}, th)
	e := rec.Estimates["b"]
	if e.Wins != 0 {
		t.Skipf("seed produced %d wins; the zero-win path needs a state Monte Carlo actually misses", e.Wins)
	}
	if e.Status != MCZeroWins {
		t.Fatalf("status %s, want %s", e.Status, MCZeroWins)
	}
	if e.Status.Usable() {
		t.Fatal("MC_ZERO_WINS must not be usable as a denominator")
	}
	if e.ZeroWinUpperBound == nil {
		t.Fatal("zero-win case must report an upper bound")
	}
	if ub := *e.ZeroWinUpperBound; ub <= 0 || ub > 0.01 {
		t.Fatalf("upper bound %v is not a plausible rule-of-three bound for 2000 draws", ub)
	}
	if *e.Reference <= 0 {
		t.Fatal("reference must remain positive: this is MC_ZERO_WINS, not TRUE_ZERO_PROPENSITY")
	}
	if *e.ZeroWinUpperBound < *e.Reference {
		t.Fatalf("upper bound %g is below the true probability %g", *e.ZeroWinUpperBound, *e.Reference)
	}
}

func TestZeroWinUpperBoundIsRuleOfThree(t *testing.T) {
	for _, n := range []int{100, 1000, 10000, 1000000} {
		got := zeroWinUpperBound(n, 0.05)
		want := 3.0 / float64(n)
		if rel := math.Abs(got-want) / want; rel > 0.02 {
			t.Fatalf("n=%d: exact bound %.6g differs from 3/n %.6g by %.1f%%", n, got, want, 100*rel)
		}
		if got <= 0 || got >= 1 {
			t.Fatalf("n=%d: bound %v out of range", n, got)
		}
	}
}

func TestLowPrecisionBelowMinWins(t *testing.T) {
	// A 16-arm uniform-prior state at 200 draws gives ~12 wins per arm.
	arms := identical(16, 1, 1)
	rec := ReconstructMonteCarlo(arms, 200, 5, false, ReferenceOptions{}, DefaultThresholds())
	for _, a := range arms {
		e := rec.Estimates[a.ID]
		if e.Status == Reliable {
			t.Fatalf("arm %s with %d wins in 200 draws should not be RELIABLE", a.ID, e.Wins)
		}
		if e.Status.Usable() {
			t.Fatalf("arm %s status %s must not be usable", a.ID, e.Status)
		}
	}
}

func TestReliableAtHighDraws(t *testing.T) {
	arms := identical(4, 1, 1)
	rec := ReconstructMonteCarlo(arms, 200000, 5, true, ReferenceOptions{}, DefaultThresholds())
	for _, a := range arms {
		e := rec.Estimates[a.ID]
		if e.Status != Reliable {
			t.Fatalf("arm %s: status %s (%s)", a.ID, e.Status, e.Reason)
		}
		if e.CILow > *e.Reference || e.CIHigh < *e.Reference {
			t.Fatalf("arm %s: Wilson interval [%g, %g] excludes the reference %g", a.ID, e.CILow, e.CIHigh, *e.Reference)
		}
	}
}

func TestInvalidPosteriorClassified(t *testing.T) {
	arms := []Arm{{"a", 0, 1}, {"b", 1, 1}}
	for _, rec := range []Reconstruction{
		ReconstructMonteCarlo(arms, 1000, 1, false, ReferenceOptions{}, DefaultThresholds()),
		ReconstructReference(arms, ReferenceOptions{}, DefaultThresholds()),
	} {
		for _, a := range arms {
			e := rec.Estimates[a.ID]
			if e.Status != InvalidPosterior {
				t.Fatalf("arm %s: status %s, want %s", a.ID, e.Status, InvalidPosterior)
			}
			if e.Status.Usable() {
				t.Fatal("INVALID_POSTERIOR must not be usable")
			}
		}
	}
}

func TestReferenceReconstructionIsReliableAndExact(t *testing.T) {
	arms := []Arm{{"a", 4, 1}, {"b", 1, 4}, {"c", 2, 2}}
	rec := ReconstructReference(arms, ReferenceOptions{}, DefaultThresholds())
	sum := 0.0
	for _, a := range arms {
		e := rec.Estimates[a.ID]
		if e.Status != Reliable {
			t.Fatalf("arm %s: %s (%s)", a.ID, e.Status, e.Reason)
		}
		if e.Source != SourceReference {
			t.Fatalf("arm %s: source %s", a.ID, e.Source)
		}
		sum += e.Value
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Fatalf("reference values sum to %v", sum)
	}
}

func TestWilsonIntervalContainsPointEstimate(t *testing.T) {
	for _, c := range []struct{ k, n int }{{0, 100}, {1, 100}, {50, 100}, {99, 100}, {100, 100}, {3, 1000000}} {
		lo, hi := wilsonInterval(c.k, c.n, 1.96)
		p := float64(c.k) / float64(c.n)
		if lo < 0 || hi > 1 || lo > hi {
			t.Fatalf("k=%d n=%d: interval [%g,%g] malformed", c.k, c.n, lo, hi)
		}
		if p < lo-1e-12 || p > hi+1e-12 {
			t.Fatalf("k=%d n=%d: point estimate %g outside [%g,%g]", c.k, c.n, p, lo, hi)
		}
	}
}

// TestDrawCountAloneIsNotAnAccuracyBound demonstrates why MinWins exists: at
// 1e5 draws the 1/sqrt(draws) figure is about 0.3%, yet a rare action's estimate
// is wrong by 100% because it was never sampled.
func TestDrawCountAloneIsNotAnAccuracyBound(t *testing.T) {
	arms := []Arm{{"a", 1, 1}, {"b", 200000, 1}}
	ref := mustReference(t, arms)
	const draws = 100000
	mc := MonteCarlo(arms, draws, 11)
	nominal := 1 / math.Sqrt(draws)
	trueP := ref.Probs["a"]
	if trueP <= 0 {
		t.Fatal("premise broken: reference probability for a should be positive")
	}
	relErr := math.Abs(mc.Probs["a"]-trueP) / trueP
	if mc.Wins["a"] != 0 {
		t.Skipf("seed happened to sample the rare arm %d times", mc.Wins["a"])
	}
	if relErr < 0.5 {
		t.Fatalf("expected a large relative error for the unsampled arm, got %.3f", relErr)
	}
	t.Logf("nominal 1/sqrt(draws) = %.5f, actual relative error for p=%.3g is %.1f%%", nominal, trueP, 100*relErr)
}
