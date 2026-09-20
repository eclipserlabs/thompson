package opeval

import (
	"fmt"
	"math"
	"sort"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
	"github.com/wiramahendra/thompson-sampling/go/propensity"
)

// State is one named posterior configuration in the test matrix.
type State struct {
	Name string
	Kind string // shape family, for grouping in reports
	Arms []propensity.Arm
}

func armID(i int) string { return fmt.Sprintf("arm%02d", i) }

// Matrix builds the posterior test matrix: every shape family at 2, 4, 8 and 16
// arms, plus states lifted from an actual Thompson run at 10, 100, 1000 and
// 10000 decisions.
//
// The hand-built families cover the regimes the estimator is asked about; the
// harness-sampled ones cover the regimes it is actually given.
func Matrix(env SyntheticEnv, harnessSeed uint64) []State {
	counts := []int{2, 4, 8, 16}
	var out []State

	shape := func(name, kind string, f func(i, k int) (float64, float64)) {
		for _, k := range counts {
			arms := make([]propensity.Arm, k)
			for i := 0; i < k; i++ {
				a, b := f(i, k)
				arms[i] = propensity.Arm{ID: armID(i), Alpha: a, Beta: b}
			}
			out = append(out, State{Name: fmt.Sprintf("%s/K=%d", name, k), Kind: kind, Arms: arms})
		}
	}

	shape("all-beta(1,1)", "uniform-prior", func(i, k int) (float64, float64) { return 1, 1 })
	shape("warm-start-beta(4,1)", "warm-start", func(i, k int) (float64, float64) { return 4, 1 })
	// Near-tied with moderate evidence: means within one percentage point after
	// ~200 observations each, which is where propensities are most evenly spread.
	shape("near-tied-moderate", "near-tied", func(i, k int) (float64, float64) {
		return 100 + float64(i), 100 - float64(i)
	})
	// One arm ahead by a margin a few hundred observations can resolve.
	shape("one-moderately-dominant", "dominant", func(i, k int) (float64, float64) {
		if i == 0 {
			return 70, 30
		}
		return 50, 50
	})
	// One arm so far ahead that the rest have propensities far below any
	// achievable Monte-Carlo resolution.
	shape("one-extremely-dominant", "extreme", func(i, k int) (float64, float64) {
		if i == 0 {
			return 900, 100
		}
		return 100, 900
	})
	// Late-stage: thousands of observations per arm, posteriors very narrow.
	shape("late-stage-concentrated", "late-stage", func(i, k int) (float64, float64) {
		return 4000 + 40*float64(i), 6000 - 40*float64(i)
	})
	// Highly asymmetric alpha/beta, including sub-unit shapes reachable through
	// discounted warm-start priors.
	shape("highly-asymmetric", "asymmetric", func(i, k int) (float64, float64) {
		switch i % 4 {
		case 0:
			return 0.5, 250
		case 1:
			return 250, 0.5
		case 2:
			return 1, 4000
		default:
			return 4000, 1
		}
	})

	for _, n := range []int{10, 100, 1000, 10000} {
		states := StatesAfter(env, []int{n}, harnessSeed)
		if arms, ok := states[n]; ok {
			out = append(out, State{
				Name: fmt.Sprintf("harness-after-%d-decisions/K=%d", n, len(arms)),
				Kind: "harness", Arms: arms,
			})
		}
	}
	return out
}

// ArmError is the Monte-Carlo error for one arm of one state at one draw count.
type ArmError struct {
	StateName string
	Kind      string
	ArmID     string
	K         int
	Draws     int
	Reference float64
	Estimated float64
	AbsError  float64
	RelError  float64 // NaN when Reference is 0
	Wins      int
	Status    propensity.Status
	// ZeroWinButPositiveReference marks the case the whole gate exists for: the
	// estimator saw nothing, the reference says the action was reachable.
	ZeroWinButPositiveReference bool
	// ReferenceResolved reports whether the reference probability is above the
	// numerical resolution floor. Below it the reference is a subnormal artefact
	// of an integral whose true value underflowed, and neither estimator knows
	// anything useful about the action.
	ReferenceResolved bool
}

// StateComparison is every arm's error for one state at one draw count, plus
// the shared numerical diagnostics.
type StateComparison struct {
	StateName    string
	Kind         string
	K            int
	Draws        int
	Errors       []ArmError
	RefRawSum    float64
	RefSumError  float64
	RefConverged bool
	RefFailed    bool
	RefErr       string
}

// Compare measures Monte Carlo against the numerical reference for one state at
// one draw count.
//
// The reference is computed once and reused across draw counts by the caller
// via CompareAll; it is recomputed here only when called directly.
func Compare(st State, draws int, seed uint64, ref *propensity.ReferenceResult, refErr error, th propensity.Thresholds) StateComparison {
	out := StateComparison{StateName: st.Name, Kind: st.Kind, K: len(st.Arms), Draws: draws}
	if ref != nil {
		out.RefRawSum, out.RefSumError, out.RefConverged = ref.RawSum, ref.SumError, ref.Converged
	}
	if refErr != nil {
		out.RefFailed, out.RefErr = true, refErr.Error()
	}
	mc := propensity.MonteCarlo(st.Arms, draws, seed)
	for _, a := range st.Arms {
		e := ArmError{
			StateName: st.Name, Kind: st.Kind, ArmID: a.ID, K: len(st.Arms), Draws: draws,
			Estimated: mc.Probs[a.ID], Wins: mc.Wins[a.ID], RelError: math.NaN(),
		}
		if ref != nil && refErr == nil {
			e.Reference = ref.Probs[a.ID]
			e.AbsError = math.Abs(e.Estimated - e.Reference)
			if e.Reference > 0 {
				e.RelError = e.AbsError / e.Reference
			}
			e.ZeroWinButPositiveReference = e.Wins == 0 && e.Reference > 0
			e.ReferenceResolved = e.Reference >= th.MinReferenceProb
		}
		switch {
		case e.Wins == 0:
			e.Status = propensity.MCZeroWins
		case e.Wins < th.MinWins:
			e.Status = propensity.LowPrecision
		default:
			e.Status = propensity.Reliable
		}
		out.Errors = append(out.Errors, e)
	}
	return out
}

// CompareAll runs the whole matrix against every draw count, computing each
// state's numerical reference exactly once.
func CompareAll(states []State, drawCounts []int, seed uint64, opts propensity.ReferenceOptions, th propensity.Thresholds) []StateComparison {
	var out []StateComparison
	for _, st := range states {
		r, err := propensity.Reference(st.Arms, opts)
		for _, d := range drawCounts {
			out = append(out, Compare(st, d, seed, &r, err, th))
		}
	}
	return out
}

// SmallProbBuckets are the rare-action thresholds the report breaks errors down
// by. Below each of these, a fixed draw budget stops resolving the action at all.
var SmallProbBuckets = []float64{1e-2, 1e-3, 1e-4, 1e-5}

// BucketSummary aggregates errors for reference probabilities under a threshold.
type BucketSummary struct {
	Threshold    float64
	Draws        int
	N            int
	ZeroWins     int
	MeanRelError float64
	MaxRelError  float64
	MedianRelErr float64
}

// SummarizeBuckets aggregates relative error by reference-probability magnitude
// and draw count. Actions with a zero reference probability are excluded from
// the relative-error statistics and counted separately.
func SummarizeBuckets(cmps []StateComparison) []BucketSummary {
	type key struct {
		t float64
		d int
	}
	acc := map[key]*[]float64{}
	zeros := map[key]int{}
	counts := map[key]int{}
	for _, c := range cmps {
		if c.RefFailed {
			continue
		}
		for _, e := range c.Errors {
			for _, t := range SmallProbBuckets {
				if e.Reference > 0 && e.Reference < t {
					k := key{t, e.Draws}
					counts[k]++
					if e.Wins == 0 {
						zeros[k]++
					}
					if acc[k] == nil {
						acc[k] = &[]float64{}
					}
					*acc[k] = append(*acc[k], e.RelError)
				}
			}
		}
	}
	var out []BucketSummary
	for _, t := range SmallProbBuckets {
		for _, d := range drawCountsSeen(cmps) {
			k := key{t, d}
			vals := acc[k]
			s := BucketSummary{Threshold: t, Draws: d, N: counts[k], ZeroWins: zeros[k]}
			if vals != nil && len(*vals) > 0 {
				s.MeanRelError, s.MaxRelError, s.MedianRelErr = meanMaxMedian(*vals)
			}
			out = append(out, s)
		}
	}
	return out
}

func drawCountsSeen(cmps []StateComparison) []int {
	seen := map[int]bool{}
	var out []int
	for _, c := range cmps {
		if !seen[c.Draws] {
			seen[c.Draws] = true
			out = append(out, c.Draws)
		}
	}
	sortInts(out)
	return out
}

// SpreadEnv builds a stationary environment with k arms whose means are spread
// evenly across [0.2, 0.8], matching the shape of the four-arm environment used
// elsewhere in the report.
func SpreadEnv(k int) SyntheticEnv {
	means := make(map[string]float64, k)
	for i := 0; i < k; i++ {
		p := 0.8
		if k > 1 {
			p = 0.8 - 0.6*float64(i)/float64(k-1)
		}
		means[armID(i)] = p
	}
	return SyntheticEnv{Means: means}
}

// HarnessStates samples posterior states from real Thompson runs at 2, 4, 8 and
// 16 arms, taken at log-spaced decision indices so that early diffuse states and
// late concentrated states are both represented.
//
// These are the states the estimator is actually handed in this repository,
// which is a different population from any hand-written matrix.
func HarnessStates(perArmCount int, horizon int, seed uint64) []State {
	var out []State
	for _, k := range []int{2, 4, 8, 16} {
		decisions := Run(SpreadEnv(k), horizon, seed+uint64(k))
		if len(decisions) == 0 {
			continue
		}
		for _, idx := range sampleIndices(len(decisions), perArmCount) {
			d := decisions[idx]
			if d.Started == nil || len(d.Started.EligibleArmState) == 0 {
				continue
			}
			out = append(out, State{
				Name: fmt.Sprintf("harness/K=%d/@%d", k, idx),
				Kind: "harness-sampled",
				Arms: gateway.ArmsFromState(d.Started.EligibleArmState),
			})
		}
	}
	return out
}

// sampleIndices returns want distinct indices into a run of length n, weighted
// toward the start.
//
// Log spacing alone collapses at low i -- floor(9999^(i/25)) is 1 for both i=0
// and i=1 -- which would silently sample the same posterior twice and overstate
// coverage. Log-spaced indices are taken first and deduplicated, then linear
// spacing tops the set up to the requested count.
func sampleIndices(n, want int) []int {
	if n <= 0 || want <= 0 {
		return nil
	}
	if want > n {
		want = n
	}
	seen := make(map[int]bool, want)
	out := make([]int, 0, want)
	add := func(idx int) bool {
		if idx < 0 {
			idx = 0
		}
		if idx >= n {
			idx = n - 1
		}
		if seen[idx] {
			return false
		}
		seen[idx] = true
		out = append(out, idx)
		return len(out) >= want
	}
	for i := 0; i < want; i++ {
		if add(int(math.Pow(float64(n-1), float64(i)/float64(want)))) {
			return out
		}
	}
	for i := 0; i < n && len(out) < want; i++ {
		if add(i * n / want) {
			return out
		}
	}
	for i := 0; i < n && len(out) < want; i++ {
		if add(i) {
			return out
		}
	}
	sort.Ints(out)
	return out
}
