package opeval

import (
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
	"github.com/wiramahendra/thompson-sampling/go/propensity"
)

const testSeed = 20240101

// sharedRowSets reconstructs the diagnostic log once for the whole package.
// Reconstruction is the expensive half of every candidate test and does not
// depend on the candidate, so building it per test would triple the suite.
var (
	sharedOnce    sync.Once
	sharedLedger  []*gateway.LedgerDecision
	sharedRowSets []RowSet
)

func diagnosticLog(t *testing.T) ([]*gateway.LedgerDecision, []RowSet) {
	t.Helper()
	sharedOnce.Do(func() {
		sharedLedger = Run(FourArmEnv(), 500, testSeed)
		sharedRowSets = BuildRowSets(sharedLedger, DefaultSweep(testSeed, []int{2000, 20000}), nil)
	})
	return sharedLedger, sharedRowSets
}

func TestSyntheticRunProducesReconstructableRows(t *testing.T) {
	ledger := Run(FourArmEnv(), 300, testSeed)
	if len(ledger) != 300 {
		t.Fatalf("got %d decisions", len(ledger))
	}
	for _, d := range ledger {
		if len(d.Started.EligibleArmState) != 4 {
			t.Fatalf("decision %s has %d arm states", d.Started.DecisionID, len(d.Started.EligibleArmState))
		}
		if d.Started.LoggingPolicyID != "exact-thompson-v1" {
			t.Fatalf("logging policy %q", d.Started.LoggingPolicyID)
		}
		// The snapshot must be the posterior *before* the update, or the
		// denominator would be conditioned on the outcome it weights.
		for _, s := range d.Started.EligibleArmState {
			if s.ArmID == d.Learned.ArmID {
				if s.Alpha != d.Learned.PosteriorBefore.Alpha || s.Beta != d.Learned.PosteriorBefore.Beta {
					t.Fatalf("snapshot for the selected arm is not posterior-before")
				}
			}
		}
	}
}

func TestFourArmEnvHasKnownUniformValue(t *testing.T) {
	if v := FourArmEnv().UniformValue(); math.Abs(v-0.5) > 1e-12 {
		t.Fatalf("uniform value %v, want 0.5", v)
	}
	if v := FourArmEnv().BestValue(); math.Abs(v-0.8) > 1e-12 {
		t.Fatalf("best value %v, want 0.8", v)
	}
}

// TestIndependentCalibrationWeightsConvergeTowardOne is the real Thompson
// self-calibration check: the numerator comes from the numerical reference and
// the denominator from Monte Carlo, so deviation from 1 measures the
// reconstruction error rather than being ruled out by construction.
func TestIndependentCalibrationWeightsConvergeTowardOne(t *testing.T) {
	ledger := Run(FourArmEnv(), 150, testSeed)
	cache := BuildRefCache(ledger, propensity.DefaultReferenceOptions())

	low := CalibrateCached(ledger, cache, 2000, testSeed, false)
	high := CalibrateCached(ledger, cache, 50000, testSeed, false)

	if low.N == 0 || high.N == 0 {
		t.Fatalf("no weights computed: low n=%d high n=%d", low.N, high.N)
	}
	if low.SanityOnly || low.Label != "INDEPENDENT_THOMPSON_CALIBRATION" {
		t.Fatalf("label %q sanityOnly=%v", low.Label, low.SanityOnly)
	}
	if high.MeanAbsDeviation >= low.MeanAbsDeviation {
		t.Fatalf("deviation from 1 did not shrink with draws: 2k=%.6f, 200k=%.6f",
			low.MeanAbsDeviation, high.MeanAbsDeviation)
	}
	if math.Abs(high.MeanWeight-1) > 0.01 {
		t.Fatalf("mean weight at 50k draws is %.6f, expected within 1%% of 1", high.MeanWeight)
	}
	if high.MaxAbsDeviation > 0.25 {
		t.Fatalf("max deviation at 50k draws is %.4f", high.MaxAbsDeviation)
	}
	// At 2k draws the deviation must be visible, or the test would prove nothing.
	if low.MaxAbsDeviation < 0.02 {
		t.Fatalf("2k-draw calibration showed almost no error (%.4f); the check is not sensitive",
			low.MaxAbsDeviation)
	}
	t.Logf("2k: mean |w-1| = %.5f, max %.5f; 50k: mean %.5f, max %.5f",
		low.MeanAbsDeviation, low.MaxAbsDeviation, high.MeanAbsDeviation, high.MaxAbsDeviation)
}

func TestCalibrationBothDirectionsAgree(t *testing.T) {
	ledger := Run(FourArmEnv(), 120, testSeed)
	cache := BuildRefCache(ledger, propensity.DefaultReferenceOptions())
	fwd := CalibrateCached(ledger, cache, 10000, testSeed, false)
	rev := CalibrateCached(ledger, cache, 10000, testSeed, true)
	if fwd.Numerator != "numerical-reference" || rev.Numerator != "monte-carlo" {
		t.Fatalf("directions not labelled: %q vs %q", fwd.Numerator, rev.Numerator)
	}
	// Reciprocal ratios: the deviations must be of comparable size, so the error
	// is a property of the Monte-Carlo estimate rather than of the ratio's shape.
	if math.Abs(fwd.MeanAbsDeviation-rev.MeanAbsDeviation) > 0.02 {
		t.Fatalf("forward %.5f and reversed %.5f deviations differ too much",
			fwd.MeanAbsDeviation, rev.MeanAbsDeviation)
	}
}

// TestIdenticalEstimatorSelfEvaluationIsLabelledSanityOnly pins the thing this
// work exists to prevent from being read as validation.
func TestIdenticalEstimatorSelfEvaluationIsLabelledSanityOnly(t *testing.T) {
	ledger := Run(FourArmEnv(), 100, testSeed)
	res := SanityOnlySelfEvaluation(ledger, 2000, testSeed)
	if res.Label != "IMPLEMENTATION_SANITY_ONLY" {
		t.Fatalf("label %q", res.Label)
	}
	if !res.SanityOnly {
		t.Fatal("SanityOnly must be set")
	}
	if res.MinWeight != 1 || res.MaxWeight != 1 {
		t.Fatalf("weights are 1 by construction; got [%v, %v]", res.MinWeight, res.MaxWeight)
	}
}

// TestUniformIsNotRankableFromOverlap is the required uniform diagnostic. The
// environment's true uniform value is exactly 0.5, but a Thompson logging
// policy plays the best arm almost always, so uniform has no overlap. It must
// be refused even with the exact numerical-reference denominators, where no
// amount of propensity accuracy is in question.
func TestUniformIsNotRankableFromOverlap(t *testing.T) {
	_, rowSets := diagnosticLog(t)
	results := EvaluateOver(rowSets, gateway.UniformCandidate{}, nil, 0, 0)
	cfg := gateway.DefaultRankabilityConfig()
	sens, used := Sensitivity(results, cfg.MaxUnusableFraction)
	rep := gateway.AssessRankability(results[0].Estimate, cfg, sens, used)

	if results[0].Label != "numerical-reference" {
		t.Fatalf("expected the reference denominators first, got %q", results[0].Label)
	}
	if rep.Status != gateway.NotRankable {
		t.Fatalf("uniform should be NOT_RANKABLE; ESS=%.1f ESS/N=%.4f maxW=%.1f",
			results[0].Estimate.ESS, results[0].Estimate.ESSOverN, results[0].Estimate.MaxWeight)
	}
	// The refusal must be attributable to overlap, not to propensity precision:
	// the reference denominators refuse essentially nothing.
	if results[0].Estimate.UnusablePropensityFraction > cfg.MaxUnusableFraction {
		t.Fatalf("reference denominators refused %.3f of rows; the diagnostic would then be confounded",
			results[0].Estimate.UnusablePropensityFraction)
	}
	if !strings.Contains(strings.Join(rep.Failures, " "), "ESS") {
		t.Fatalf("expected an ESS failure, got %v", rep.Failures)
	}
	res := gateway.Rank([]gateway.RankabilityReport{rep})
	if res.Winner != "" {
		t.Fatalf("a NOT_RANKABLE uniform was named winner: %q", res.Winner)
	}
	t.Logf("uniform under reference denominators: IPS=%.4f SNIPS=%.4f ESS=%.1f ESS/N=%.4f maxW=%.1f (true value 0.5)",
		results[0].Estimate.IPS, results[0].Estimate.SNIPS, results[0].Estimate.ESS,
		results[0].Estimate.ESSOverN, results[0].Estimate.MaxWeight)
}

// TestGreedyOverlapIsGenuineUnderReferencePropensities is the greedy
// diagnostic: its high ESS must survive replacing the Monte-Carlo denominators
// with the numerical reference.
func TestGreedyOverlapIsGenuineUnderReferencePropensities(t *testing.T) {
	_, rowSets := diagnosticLog(t)
	results := EvaluateOver(rowSets, gateway.GreedyCandidate{}, nil, 0, 0)

	refEst := results[0].Estimate
	if refEst.ESSOverN < 0.5 {
		t.Fatalf("greedy ESS/N under reference denominators is only %.4f", refEst.ESSOverN)
	}
	// The Monte-Carlo denominators must give substantially the same answer, or
	// the high ESS would be an artefact of the estimator rather than a fact.
	for _, r := range results[1:] {
		if math.Abs(r.Estimate.SNIPS-refEst.SNIPS) > 0.02 {
			t.Fatalf("%s SNIPS %.4f differs from reference %.4f", r.Label, r.Estimate.SNIPS, refEst.SNIPS)
		}
	}
	cfg := gateway.DefaultRankabilityConfig()
	sens, used := Sensitivity(results, cfg.MaxUnusableFraction)
	rep := gateway.AssessRankability(refEst, cfg, sens, used)
	if rep.Status != gateway.Rankable {
		t.Fatalf("greedy should be RANKABLE, failures %v", rep.Failures)
	}
	t.Logf("greedy under reference denominators: IPS=%.4f SNIPS=%.4f ESS=%.1f maxW=%.2f sensitivity=%v",
		refEst.IPS, refEst.SNIPS, refEst.ESS, refEst.MaxWeight, derefOr(sens))
}

// TestUniformStaysNotRankableEvenWithPerfectPropensities separates the two
// failure modes explicitly, which is the whole point of the decomposition.
func TestUniformStaysNotRankableEvenWithPerfectPropensities(t *testing.T) {
	_, rowSets := diagnosticLog(t)
	est := gateway.EvaluateOPE(rowSets[0].Records, gateway.UniformCandidate{}, nil, 0, 0, 0, 0)
	cfg := gateway.DefaultRankabilityConfig()
	// Hand it a perfect sensitivity result: even so, overlap alone must refuse it.
	rep := gateway.AssessRankability(est, cfg, ptrf(0.0), []string{"numerical-reference", "hypothetical-perfect"})
	if rep.Status != gateway.NotRankable {
		t.Fatalf("status %s", rep.Status)
	}
	if rep.Support != gateway.PoorSupportGoodPrecision {
		t.Fatalf("support %s: with exact propensities the only remaining failure is overlap", rep.Support)
	}
	if strings.Contains(rep.Remedy(), "raise Monte-Carlo draws") {
		t.Fatalf("remedy must not blame the estimator: %q", rep.Remedy())
	}
}

func TestWeightSensitivityIsWorseForSmallPropensities(t *testing.T) {
	ledger := Run(FourArmEnv(), 400, testSeed)
	cache := BuildRefCache(ledger, propensity.DefaultReferenceOptions())
	regimes := WeightSensitivityCached(ledger, cache, gateway.UniformCandidate{}, 2000, testSeed)

	var big, small *WeightRegime
	for i := range regimes {
		switch regimes[i].Label {
		case "p >= 0.1":
			big = &regimes[i]
		case "0.01 <= p < 0.1":
			small = &regimes[i]
		}
	}
	if big == nil || small == nil || big.N == 0 || small.N == 0 {
		t.Skip("this log did not populate both regimes")
	}
	if small.MeanRelWeightError <= big.MeanRelWeightError {
		t.Fatalf("relative weight error should grow as the propensity shrinks: %.4f at p>=0.1 vs %.4f at 0.01<=p<0.1",
			big.MeanRelWeightError, small.MeanRelWeightError)
	}
	if small.MaxTrueWeight <= big.MaxTrueWeight {
		t.Fatalf("weights should be larger where propensities are smaller: %.2f vs %.2f",
			big.MaxTrueWeight, small.MaxTrueWeight)
	}
}

func TestMatrixCoversRequiredArmCountsAndShapes(t *testing.T) {
	states := Matrix(FourArmEnv(), testSeed)
	counts := map[int]bool{}
	kinds := map[string]bool{}
	for _, s := range states {
		counts[len(s.Arms)] = true
		kinds[s.Kind] = true
	}
	for _, k := range []int{2, 4, 8, 16} {
		if !counts[k] {
			t.Fatalf("missing arm count %d", k)
		}
	}
	for _, kind := range []string{"uniform-prior", "warm-start", "near-tied", "dominant", "extreme", "late-stage", "asymmetric", "harness"} {
		if !kinds[kind] {
			t.Fatalf("missing shape family %q", kind)
		}
	}
	if len(states) < 30 {
		t.Fatalf("matrix has only %d states", len(states))
	}
}

func TestHarnessStatesCoverAtLeastOneHundred(t *testing.T) {
	states := HarnessStates(25, 2000, testSeed)
	if len(states) < 100 {
		t.Fatalf("got %d harness states, want at least 100", len(states))
	}
	for _, s := range states {
		if len(s.Arms) < 2 {
			t.Fatalf("state %s has %d arms", s.Name, len(s.Arms))
		}
		for _, a := range s.Arms {
			if a.Alpha <= 0 || a.Beta <= 0 {
				t.Fatalf("state %s arm %s has alpha=%v beta=%v", s.Name, a.ID, a.Alpha, a.Beta)
			}
		}
	}
}

func TestCompareFlagsZeroWinsAgainstPositiveReference(t *testing.T) {
	st := State{Name: "dominant-pair", Kind: "test", Arms: []propensity.Arm{{ID: "a", Alpha: 70, Beta: 30}, {ID: "b", Alpha: 30, Beta: 70}}}
	ref, err := propensity.Reference(st.Arms, propensity.DefaultReferenceOptions())
	if err != nil {
		t.Fatal(err)
	}
	c := Compare(st, 2000, testSeed, &ref, nil, propensity.DefaultThresholds())
	var b ArmError
	for _, e := range c.Errors {
		if e.ArmID == "b" {
			b = e
		}
	}
	if b.Wins != 0 {
		t.Skipf("seed sampled the rare arm %d times", b.Wins)
	}
	if !b.ZeroWinButPositiveReference {
		t.Fatal("zero wins against a positive reference must be flagged")
	}
	if !b.ReferenceResolved {
		t.Fatalf("reference %g should be above the resolution floor", b.Reference)
	}
	if b.Status != propensity.MCZeroWins {
		t.Fatalf("status %s", b.Status)
	}
}

func TestSensitivityNeedsTwoAcceptedSources(t *testing.T) {
	one := []SweepResult{{Label: "a", Estimate: gateway.OPEEstimate{UsableDecisions: 10, SNIPS: 0.5}}}
	if s, _ := Sensitivity(one, 0.05); s != nil {
		t.Fatalf("one source cannot measure sensitivity, got %v", *s)
	}
	two := append(one, SweepResult{Label: "b", Estimate: gateway.OPEEstimate{UsableDecisions: 10, SNIPS: 0.53}})
	s, used := Sensitivity(two, 0.05)
	if s == nil {
		t.Fatal("two sources should yield a spread")
	}
	if math.Abs(*s-0.03) > 1e-9 {
		t.Fatalf("spread %v, want 0.03", *s)
	}
	if len(used) != 2 {
		t.Fatalf("used %v", used)
	}
	// A source that refused most of its rows must not enter the spread.
	noisy := append(two, SweepResult{Label: "c", Estimate: gateway.OPEEstimate{UsableDecisions: 10, SNIPS: 9, UnusablePropensityFraction: 0.9}})
	s2, used2 := Sensitivity(noisy, 0.05)
	if math.Abs(*s2-0.03) > 1e-9 {
		t.Fatalf("a source that refused 90%% of rows widened the spread to %v", *s2)
	}
	if len(used2) != 2 {
		t.Fatalf("used %v", used2)
	}
}

func ptrf(v float64) *float64 { return &v }

func derefOr(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

func TestSampleIndicesAreDistinct(t *testing.T) {
	for _, c := range []struct{ n, want int }{{10000, 25}, {100, 25}, {25, 25}, {10, 25}, {1, 5}} {
		got := sampleIndices(c.n, c.want)
		seen := map[int]bool{}
		for _, i := range got {
			if seen[i] {
				t.Fatalf("n=%d want=%d: index %d repeated in %v", c.n, c.want, i, got)
			}
			if i < 0 || i >= c.n {
				t.Fatalf("n=%d: index %d out of range", c.n, i)
			}
			seen[i] = true
		}
		expect := c.want
		if c.n < expect {
			expect = c.n
		}
		if len(got) != expect {
			t.Fatalf("n=%d want=%d: got %d indices %v", c.n, c.want, len(got), got)
		}
	}
}

func TestHarnessStateNamesAreDistinct(t *testing.T) {
	states := HarnessStates(25, 2000, testSeed)
	seen := map[string]bool{}
	for _, s := range states {
		if seen[s.Name] {
			t.Fatalf("duplicate state %q: the matrix would double-count it", s.Name)
		}
		seen[s.Name] = true
	}
	if len(states) != 100 {
		t.Fatalf("expected 100 distinct harness states (25 x 4 arm counts), got %d", len(states))
	}
}
