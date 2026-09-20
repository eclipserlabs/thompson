package opeval

import (
	"math"
	"runtime"
	"sort"
	"sync"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
	"github.com/wiramahendra/thompson-sampling/go/propensity"
)

// CalibrationResult summarises the Thompson self-calibration weights
// pi_numerator(a) / pi_denominator(a) over a bandit log, where the two
// probabilities come from *different* estimators.
//
// If both came from the same estimator every weight would be exactly 1 and the
// summary would be vacuous; that configuration is reported separately as
// IMPLEMENTATION_SANITY_ONLY.
type CalibrationResult struct {
	Numerator   string
	Denominator string
	Draws       int
	N           int
	// Skipped counts rows where the denominator estimate was zero, which is the
	// zero-win case: the weight is undefined, not infinite.
	Skipped      int
	MeanWeight   float64
	MedianWeight float64
	MinWeight    float64
	MaxWeight    float64
	P95Weight    float64
	// MeanAbsDeviation and MaxAbsDeviation measure |w - 1|: the reconstruction
	// error expressed directly in importance-weight units.
	MeanAbsDeviation float64
	MaxAbsDeviation  float64
	P95AbsDeviation  float64
	// SanityOnly marks a configuration whose numerator and denominator share an
	// estimator, so the weights are 1 by construction.
	SanityOnly bool
	Label      string
}

// Calibrate computes pi_reference / pi_MC(draws) for the logged action of every
// decision.
//
// This is the actual Thompson self-calibration check. The numerator is the
// numerical reference and the denominator is the Monte-Carlo reconstruction, so
// the weights are 1 only to the extent that the Monte-Carlo reconstruction is
// correct. Deviation from 1 at low draw counts is the propensity reconstruction
// error, measured rather than assumed.
func Calibrate(decisions []*gateway.LedgerDecision, draws int, seed uint64, opts propensity.ReferenceOptions) CalibrationResult {
	return calibrate(decisions, draws, seed, opts, false)
}

// CalibrateReversed computes pi_MC(draws) / pi_reference, the same check with
// the estimators swapped. Running both directions shows the deviation is a
// property of the Monte-Carlo estimate rather than of which side of the ratio
// it sits on.
func CalibrateReversed(decisions []*gateway.LedgerDecision, draws int, seed uint64, opts propensity.ReferenceOptions) CalibrationResult {
	return calibrate(decisions, draws, seed, opts, true)
}

// RefCache holds the numerical-reference action distribution for every decision
// in a log, computed once.
//
// The reference costs a few milliseconds per decision, and calibration sweeps
// re-ask for the same distributions at every draw count and in both directions;
// without this the reference would be recomputed a dozen times per row.
type RefCache struct {
	Probs []map[string]float64 // nil entry means the reference failed for that row
}

// BuildRefCache computes the numerical reference for every decision, in
// parallel. Entries are nil where the reference failed or the snapshot is
// missing.
func BuildRefCache(decisions []*gateway.LedgerDecision, opts propensity.ReferenceOptions) *RefCache {
	c := &RefCache{Probs: make([]map[string]float64, len(decisions))}
	workers := runtime.GOMAXPROCS(0)
	ch := make(chan int, workers*4)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				d := decisions[i]
				if d.Started == nil || len(d.Started.EligibleArmState) == 0 {
					continue
				}
				ref, err := propensity.Reference(gateway.ArmsFromState(d.Started.EligibleArmState), opts)
				if err != nil {
					continue
				}
				c.Probs[i] = ref.Probs
			}
		}()
	}
	for i := range decisions {
		ch <- i
	}
	close(ch)
	wg.Wait()
	return c
}

// CalibrateCached is Calibrate with a prebuilt reference cache.
func CalibrateCached(decisions []*gateway.LedgerDecision, cache *RefCache, draws int, seed uint64, reversed bool) CalibrationResult {
	return calibrateWith(decisions, cache, draws, seed, reversed)
}

func calibrate(decisions []*gateway.LedgerDecision, draws int, seed uint64, opts propensity.ReferenceOptions, reversed bool) CalibrationResult {
	return calibrateWith(decisions, BuildRefCache(decisions, opts), draws, seed, reversed)
}

func calibrateWith(decisions []*gateway.LedgerDecision, cache *RefCache, draws int, seed uint64, reversed bool) CalibrationResult {
	res := CalibrationResult{Draws: draws}
	if reversed {
		res.Numerator, res.Denominator = "monte-carlo", "numerical-reference"
	} else {
		res.Numerator, res.Denominator = "numerical-reference", "monte-carlo"
	}
	res.Label = "INDEPENDENT_THOMPSON_CALIBRATION"

	var ws []float64
	for i, d := range decisions {
		if d.Started == nil || len(d.Started.EligibleArmState) == 0 || cache.Probs[i] == nil {
			continue
		}
		arms := gateway.ArmsFromState(d.Started.EligibleArmState)
		mc := propensity.MonteCarlo(arms, draws, seed)
		a := d.Started.SelectedArmID
		num, den := cache.Probs[i][a], mc.Probs[a]
		if reversed {
			num, den = den, num
		}
		if den <= 0 {
			res.Skipped++
			continue
		}
		ws = append(ws, num/den)
	}
	res.N = len(ws)
	if len(ws) == 0 {
		return res
	}
	sort.Float64s(ws)
	sum, maxDev, sumDev := 0.0, 0.0, 0.0
	devs := make([]float64, len(ws))
	for i, w := range ws {
		sum += w
		dev := math.Abs(w - 1)
		devs[i] = dev
		sumDev += dev
		if dev > maxDev {
			maxDev = dev
		}
	}
	sort.Float64s(devs)
	res.MeanWeight = sum / float64(len(ws))
	res.MedianWeight = quantile(ws, 0.5)
	res.MinWeight, res.MaxWeight = ws[0], ws[len(ws)-1]
	res.P95Weight = quantile(ws, 0.95)
	res.MeanAbsDeviation = sumDev / float64(len(devs))
	res.MaxAbsDeviation = maxDev
	res.P95AbsDeviation = quantile(devs, 0.95)
	return res
}

// SanityOnlySelfEvaluation runs the configuration this work exists to discredit:
// Monte-Carlo Thompson candidate against Monte-Carlo Thompson logging
// propensities with the same draws and seed.
//
// The returned weights are exactly 1 and the result is labelled
// IMPLEMENTATION_SANITY_ONLY. It confirms the plumbing joins numerator to
// denominator on matching rows and nothing else.
func SanityOnlySelfEvaluation(decisions []*gateway.LedgerDecision, draws int, seed uint64) CalibrationResult {
	res := CalibrationResult{
		Numerator: "monte-carlo", Denominator: "monte-carlo(identical draws and seed)",
		Draws: draws, SanityOnly: true, Label: "IMPLEMENTATION_SANITY_ONLY",
	}
	var ws []float64
	for _, d := range decisions {
		if d.Started == nil || len(d.Started.EligibleArmState) == 0 {
			continue
		}
		arms := gateway.ArmsFromState(d.Started.EligibleArmState)
		mc := propensity.MonteCarlo(arms, draws, seed)
		p := mc.Probs[d.Started.SelectedArmID]
		if p <= 0 {
			res.Skipped++
			continue
		}
		ws = append(ws, p/p)
	}
	res.N = len(ws)
	if len(ws) == 0 {
		return res
	}
	sort.Float64s(ws)
	res.MeanWeight, res.MedianWeight = 1, 1
	res.MinWeight, res.MaxWeight, res.P95Weight = ws[0], ws[len(ws)-1], 1
	return res
}

// WeightRegime is the importance-weight error analysis for one band of logging
// propensity.
type WeightRegime struct {
	Label string
	Lo    float64
	Hi    float64
	N     int
	// MeanRelWeightError is the mean of |c/p_hat - c/p| / (c/p) = |p - p_hat|/p_hat,
	// which is independent of the candidate probability c and so characterises
	// the denominator alone.
	MeanRelWeightError float64
	MaxRelWeightError  float64
	P95RelWeightError  float64
	// MeanTrueWeight and MaxTrueWeight are the reference weights c/p in this band
	// for the candidate supplied, showing where instability actually bites.
	MeanTrueWeight float64
	MaxTrueWeight  float64
	ZeroWins       int
}

// WeightSensitivity quantifies how logging-propensity error propagates into
// importance weights for one candidate.
//
// For each logged action it forms the reference weight c/p and the estimated
// weight c/p_hat and reports their relative discrepancy, bucketed by the size of
// the reference propensity. The bucketing matters because the same absolute
// error in p_hat is harmless at p=0.3 and catastrophic at p=1e-4.
func WeightSensitivity(decisions []*gateway.LedgerDecision, candidate gateway.CandidatePolicy, draws int, seed uint64, opts propensity.ReferenceOptions) []WeightRegime {
	return WeightSensitivityCached(decisions, BuildRefCache(decisions, opts), candidate, draws, seed)
}

// WeightSensitivityCached is WeightSensitivity with a prebuilt reference cache.
func WeightSensitivityCached(decisions []*gateway.LedgerDecision, cache *RefCache, candidate gateway.CandidatePolicy, draws int, seed uint64) []WeightRegime {
	bounds := []struct {
		label  string
		lo, hi float64
	}{
		{"p >= 0.1", 0.1, 1.0000001},
		{"0.01 <= p < 0.1", 0.01, 0.1},
		{"1e-3 <= p < 0.01", 1e-3, 0.01},
		{"1e-4 <= p < 1e-3", 1e-4, 1e-3},
		{"1e-5 <= p < 1e-4", 1e-5, 1e-4},
		{"p < 1e-5", 0, 1e-5},
	}
	type acc struct {
		rel  []float64
		wref []float64
		zero int
	}
	accs := make([]acc, len(bounds))

	for i, d := range decisions {
		if d.Started == nil || len(d.Started.EligibleArmState) == 0 || cache.Probs[i] == nil {
			continue
		}
		arms := gateway.ArmsFromState(d.Started.EligibleArmState)
		mc := propensity.MonteCarlo(arms, draws, seed)
		a := d.Started.SelectedArmID
		p, phat := cache.Probs[i][a], mc.Probs[a]
		c := candidate.ActionProbability(d.Started.EligibleArmIDs, d.Started.EligibleArmState, a)
		if p <= 0 {
			continue
		}
		bi := -1
		for i, b := range bounds {
			if p >= b.lo && p < b.hi {
				bi = i
				break
			}
		}
		if bi < 0 {
			continue
		}
		if phat <= 0 {
			accs[bi].zero++
			continue
		}
		// |c/p_hat - c/p| / (c/p) simplifies to |p - p_hat| / p_hat.
		accs[bi].rel = append(accs[bi].rel, math.Abs(p-phat)/phat)
		accs[bi].wref = append(accs[bi].wref, c/p)
	}

	out := make([]WeightRegime, 0, len(bounds))
	for i, b := range bounds {
		r := WeightRegime{Label: b.label, Lo: b.lo, Hi: b.hi, N: len(accs[i].rel), ZeroWins: accs[i].zero}
		if len(accs[i].rel) > 0 {
			sort.Float64s(accs[i].rel)
			r.MeanRelWeightError, r.MaxRelWeightError, _ = meanMaxMedian(accs[i].rel)
			r.P95RelWeightError = quantile(accs[i].rel, 0.95)
			r.MeanTrueWeight, r.MaxTrueWeight, _ = meanMaxMedian(accs[i].wref)
		}
		out = append(out, r)
	}
	return out
}
