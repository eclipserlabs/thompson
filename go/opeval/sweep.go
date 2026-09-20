package opeval

import (
	"fmt"
	"math"
	"runtime"
	"sort"
	"sync"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
)

// BuildRecords converts a ledger into bandit-log rows using est for the logging
// propensity, in parallel across decisions.
//
// Parallelism is safe because every estimator here is a pure function of the
// decision's own posterior snapshot and a fixed seed: no shared RNG, no shared
// accumulator, and the same rows come out in the same order regardless of
// scheduling.
func BuildRecords(decisions []*gateway.LedgerDecision, est gateway.PropensityEstimator) []gateway.BanditLogRecord {
	out := make([]gateway.BanditLogRecord, len(decisions))
	keep := make([]bool, len(decisions))
	workers := runtime.GOMAXPROCS(0)
	var wg sync.WaitGroup
	ch := make(chan int, workers*4)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				d := decisions[i]
				if d.Started == nil || d.Learned == nil {
					continue
				}
				out[i] = gateway.ToBanditLogWith(d, est)
				keep[i] = true
			}
		}()
	}
	for i := range decisions {
		ch <- i
	}
	close(ch)
	wg.Wait()

	res := make([]gateway.BanditLogRecord, 0, len(decisions))
	for i := range out {
		if keep[i] {
			res = append(res, out[i])
		}
	}
	return res
}

// PropensitySpec names one propensity estimator in a sweep.
type PropensitySpec struct {
	Label string
	Est   gateway.PropensityEstimator
}

// DefaultSweep is the estimator ladder: the numerical reference plus Monte Carlo
// at 2k, 20k, 200k and 1M draws.
func DefaultSweep(seed uint64, drawCounts []int) []PropensitySpec {
	specs := []PropensitySpec{{Label: "numerical-reference", Est: gateway.NewReferenceEstimator()}}
	for _, d := range drawCounts {
		specs = append(specs, PropensitySpec{
			Label: fmt.Sprintf("mc-%s", humanDraws(d)),
			Est:   gateway.NewMCEstimator(d, seed).WithReferenceScoring(),
		})
	}
	return specs
}

func humanDraws(d int) string {
	switch {
	case d >= 1_000_000 && d%1_000_000 == 0:
		return fmt.Sprintf("%dM", d/1_000_000)
	case d >= 1000 && d%1000 == 0:
		return fmt.Sprintf("%dk", d/1000)
	default:
		return fmt.Sprintf("%d", d)
	}
}

// SweepResult holds one candidate's OPE estimate under one propensity source.
type SweepResult struct {
	Label    string
	Estimate gateway.OPEEstimate
}

// RowSet is one propensity source's reconstruction of a whole bandit log.
type RowSet struct {
	Label   string
	Records []gateway.BanditLogRecord
}

// BuildRowSets reconstructs the log once per propensity source.
//
// Reconstruction is by far the expensive half of a sweep -- Monte Carlo at 1e6
// draws costs about 0.4s per decision -- and it does not depend on the candidate
// being evaluated, so it is done once and shared across candidates.
func BuildRowSets(decisions []*gateway.LedgerDecision, specs []PropensitySpec, progress func(label string)) []RowSet {
	out := make([]RowSet, 0, len(specs))
	for _, s := range specs {
		if progress != nil {
			progress(s.Label)
		}
		out = append(out, RowSet{Label: s.Label, Records: BuildRecords(decisions, s.Est)})
	}
	return out
}

// EvaluateOver evaluates one candidate against every prebuilt row set.
//
// The denominator changes from row set to row set; the numerator (the
// candidate's action probability) is untouched. That is what makes the spread
// across labels a measurement of denominator sensitivity rather than of
// anything else.
func EvaluateOver(rowSets []RowSet, candidate gateway.CandidatePolicy, clip *float64, bootstrap int, bootstrapSeed uint64) []SweepResult {
	out := make([]SweepResult, 0, len(rowSets))
	for _, rs := range rowSets {
		est := gateway.EvaluateOPE(rs.Records, candidate, clip, bootstrap, bootstrapSeed, 0, 0)
		out = append(out, SweepResult{Label: rs.Label, Estimate: est})
	}
	return out
}

// SweepCandidate evaluates one candidate against every propensity source,
// reconstructing the rows as it goes. Prefer BuildRowSets + EvaluateOver when
// more than one candidate is being swept.
func SweepCandidate(decisions []*gateway.LedgerDecision, candidate gateway.CandidatePolicy, specs []PropensitySpec, clip *float64, bootstrap int, bootstrapSeed uint64) []SweepResult {
	return EvaluateOver(BuildRowSets(decisions, specs, nil), candidate, clip, bootstrap, bootstrapSeed)
}

// Sensitivity is the spread of a candidate's SNIPS value across the propensity
// sources whose denominators were themselves acceptable.
//
// A source is admitted when it produced a usable denominator for at least
// (1 - maxUnusableFraction) of eligible rows. A source that refused most of its
// rows tells us nothing about the candidate's value and must not be allowed to
// widen or narrow the spread.
//
// It returns nil when fewer than two sources qualify: with one source the
// candidate's stability across estimators is unmeasured, which is not the same
// as stable.
func Sensitivity(results []SweepResult, maxUnusableFraction float64) (*float64, []string) {
	var vals []float64
	var used []string
	for _, r := range results {
		if r.Estimate.UsableDecisions == 0 {
			continue
		}
		if r.Estimate.UnusablePropensityFraction > maxUnusableFraction {
			continue
		}
		if math.IsNaN(r.Estimate.SNIPS) {
			continue
		}
		vals = append(vals, r.Estimate.SNIPS)
		used = append(used, r.Label)
	}
	if len(vals) < 2 {
		return nil, used
	}
	sort.Float64s(vals)
	spread := vals[len(vals)-1] - vals[0]
	return &spread, used
}
