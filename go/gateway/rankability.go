package gateway

import (
	"fmt"
	"math"
	"sort"
)

// Rankability is the verdict on whether a candidate's off-policy value is
// trustworthy enough to be compared with another candidate's.
type Rankability string

const (
	// Rankable: overlap and propensity precision both clear their gates.
	Rankable Rankability = "RANKABLE"
	// NotRankable: at least one gate failed. The point estimate may still be
	// reported, but the candidate may not be named winner or loser.
	NotRankable Rankability = "NOT_RANKABLE"
)

// SupportPrecision separates the two independent ways an off-policy estimate
// fails, because they have different fixes and neither fix helps the other.
type SupportPrecision string

const (
	GoodSupportGoodPrecision          SupportPrecision = "GOOD_SUPPORT_GOOD_PRECISION"
	GoodSupportPropensityUncertain    SupportPrecision = "GOOD_SUPPORT_PROPENSITY_UNCERTAIN"
	PoorSupportGoodPrecision          SupportPrecision = "POOR_SUPPORT_GOOD_PRECISION"
	PoorSupportAndPropensityUncertain SupportPrecision = "POOR_SUPPORT_AND_PROPENSITY_UNCERTAIN"
)

// RankabilityConfig holds the explicit thresholds. Every one is a policy
// choice, not a derivation, so they live in one struct and are echoed into
// every report.
type RankabilityConfig struct {
	// MinESS is the smallest effective sample size accepted. Below it the
	// estimate is driven by a handful of rows regardless of how many were logged.
	MinESS float64
	// MinESSOverN is the smallest accepted ESS as a fraction of usable rows.
	MinESSOverN float64
	// MaxWeight is the largest single importance weight accepted.
	MaxWeight float64
	// MaxLowPrecisionFraction and MaxMCZeroWinsFraction bound the share of
	// eligible rows refused for each reason.
	MaxLowPrecisionFraction float64
	MaxMCZeroWinsFraction   float64
	// MaxUnusableFraction bounds the combined refused share.
	MaxUnusableFraction float64
	// MaxPropensitySensitivity is the largest accepted spread in the candidate's
	// SNIPS value across the accepted propensity estimates. A candidate whose
	// value moves more than this when the denominator estimator changes is being
	// ranked on the estimator, not on the policy.
	MaxPropensitySensitivity float64
}

// DefaultRankabilityConfig returns the gate used by the audit tooling.
//
// MinESS 200: the standard error of a mean over n effective samples is
// ~sigma/sqrt(n), so 200 puts a bounded [0,1] reward within roughly +/-0.035 at
// 95%, which is the resolution any policy comparison here would need.
// MinESSOverN 0.10: below one effective sample per ten logged rows the
// logging and candidate policies barely overlap.
// MaxWeight 50: a single row supplying more than 2% of a 2500-row estimate.
// Refusal fractions are deliberately tight because a refused row is a row whose
// denominator the system could not compute, and a biased subsample is worse
// than a wide interval.
func DefaultRankabilityConfig() RankabilityConfig {
	return RankabilityConfig{
		MinESS:                   200,
		MinESSOverN:              0.10,
		MaxWeight:                50,
		MaxLowPrecisionFraction:  0.02,
		MaxMCZeroWinsFraction:    0.01,
		MaxUnusableFraction:      0.05,
		MaxPropensitySensitivity: 0.02,
	}
}

// RankabilityReport is the gate outcome for one candidate.
type RankabilityReport struct {
	CandidateID string
	Status      Rankability
	Support     SupportPrecision
	// Failures lists every gate that failed, in evaluation order.
	Failures []string
	// Estimate is the OPE result the gate was applied to.
	Estimate OPEEstimate
	// Sensitivity is the observed spread of SNIPS across the accepted propensity
	// estimates, when a sensitivity sweep was supplied.
	Sensitivity *float64
	// SensitivitySources names the estimates the spread was measured over.
	SensitivitySources []string
	Config             RankabilityConfig
}

// AssessRankability applies the gates to one candidate's OPE estimate.
//
// sensitivity, when non-nil, is the spread of the candidate's SNIPS value
// across accepted propensity estimates; pass nil when no sweep was run, which
// itself fails the gate because propensity sensitivity is then unknown.
func AssessRankability(est OPEEstimate, cfg RankabilityConfig, sensitivity *float64, sensitivitySources []string) RankabilityReport {
	rep := RankabilityReport{
		CandidateID: est.CandidateID, Estimate: est, Config: cfg,
		Sensitivity: sensitivity, SensitivitySources: sensitivitySources,
	}

	// --- overlap gates -------------------------------------------------------
	overlapOK := true
	if est.UsableDecisions == 0 {
		rep.Failures = append(rep.Failures, "no usable rows")
		overlapOK = false
	}
	if est.ESS < cfg.MinESS {
		rep.Failures = append(rep.Failures, fmt.Sprintf("ESS %.1f < MinESS %.1f", est.ESS, cfg.MinESS))
		overlapOK = false
	}
	if est.ESSOverN < cfg.MinESSOverN {
		rep.Failures = append(rep.Failures, fmt.Sprintf("ESS/N %.4f < MinESSOverN %.4f", est.ESSOverN, cfg.MinESSOverN))
		overlapOK = false
	}
	if est.MaxWeight > cfg.MaxWeight {
		rep.Failures = append(rep.Failures, fmt.Sprintf("max weight %.2f > MaxWeight %.2f", est.MaxWeight, cfg.MaxWeight))
		overlapOK = false
	}

	// --- propensity-precision gates -----------------------------------------
	precisionOK := true
	n := float64(est.EligibleDecisions)
	if n > 0 {
		if f := float64(est.LowPrecisionCount) / n; f > cfg.MaxLowPrecisionFraction {
			rep.Failures = append(rep.Failures, fmt.Sprintf("LOW_PRECISION fraction %.4f > %.4f", f, cfg.MaxLowPrecisionFraction))
			precisionOK = false
		}
		if f := float64(est.MCZeroWinsCount) / n; f > cfg.MaxMCZeroWinsFraction {
			rep.Failures = append(rep.Failures, fmt.Sprintf("MC_ZERO_WINS fraction %.4f > %.4f", f, cfg.MaxMCZeroWinsFraction))
			precisionOK = false
		}
	}
	if est.UnusablePropensityFraction > cfg.MaxUnusableFraction {
		rep.Failures = append(rep.Failures, fmt.Sprintf("unusable-propensity fraction %.4f > %.4f", est.UnusablePropensityFraction, cfg.MaxUnusableFraction))
		precisionOK = false
	}
	if est.ReferenceFailureCount > 0 {
		rep.Failures = append(rep.Failures, fmt.Sprintf("%d NUMERICAL_REFERENCE_FAILURE row(s)", est.ReferenceFailureCount))
		precisionOK = false
	}
	if est.InvalidPosteriorCount > 0 {
		rep.Failures = append(rep.Failures, fmt.Sprintf("%d INVALID_POSTERIOR row(s)", est.InvalidPosteriorCount))
		precisionOK = false
	}
	switch {
	case sensitivity == nil:
		rep.Failures = append(rep.Failures, "no propensity-sensitivity sweep supplied: value stability across estimators is unknown")
		precisionOK = false
	case math.IsNaN(*sensitivity) || *sensitivity > cfg.MaxPropensitySensitivity:
		rep.Failures = append(rep.Failures, fmt.Sprintf("propensity sensitivity %.4f > %.4f (value moves when the denominator estimator changes)", *sensitivity, cfg.MaxPropensitySensitivity))
		precisionOK = false
	}

	switch {
	case overlapOK && precisionOK:
		rep.Support = GoodSupportGoodPrecision
	case overlapOK && !precisionOK:
		rep.Support = GoodSupportPropensityUncertain
	case !overlapOK && precisionOK:
		rep.Support = PoorSupportGoodPrecision
	default:
		rep.Support = PoorSupportAndPropensityUncertain
	}
	if overlapOK && precisionOK {
		rep.Status = Rankable
	} else {
		rep.Status = NotRankable
	}
	return rep
}

// Remedy states what would actually move a report's support/precision class,
// and refuses to suggest the fix that does not apply.
func (r RankabilityReport) Remedy() string {
	switch r.Support {
	case GoodSupportGoodPrecision:
		return "no remedy required"
	case GoodSupportPropensityUncertain:
		return "raise Monte-Carlo draws or switch to the numerical-reference denominator; more logged data will not help, the overlap is already adequate"
	case PoorSupportGoodPrecision:
		return "log under a policy that actually plays the candidate's actions; the propensities are already accurate, so raising draws changes nothing"
	default:
		return "both overlap and propensity precision are inadequate; they are independent failures and must be fixed separately"
	}
}

// RankingResult is the outcome of comparing candidates.
type RankingResult struct {
	// Winner and Loser are only ever set to RANKABLE candidates. Empty means the
	// comparison could not be made.
	Winner string
	Loser  string
	// Ranked lists the rankable candidates, best SNIPS first.
	Ranked []RankabilityReport
	// Refused lists the NOT_RANKABLE candidates with their gate failures. Their
	// point estimates are reported but they hold no position in the ranking.
	Refused []RankabilityReport
	// Notes explains any refusal to name a winner or loser.
	Notes []string
}

// Rank orders candidates by SNIPS and names a winner and loser.
//
// It refuses to name a NOT_RANKABLE candidate in either position. That is the
// whole point of the gate: a candidate with a flattering point estimate and no
// overlap must not be able to win, and a candidate whose denominators could not
// be resolved must not be able to lose.
func Rank(reports []RankabilityReport) RankingResult {
	var out RankingResult
	for _, r := range reports {
		if r.Status == Rankable {
			out.Ranked = append(out.Ranked, r)
		} else {
			out.Refused = append(out.Refused, r)
		}
	}
	sort.SliceStable(out.Ranked, func(i, j int) bool {
		return out.Ranked[i].Estimate.SNIPS > out.Ranked[j].Estimate.SNIPS
	})
	for _, r := range out.Refused {
		out.Notes = append(out.Notes, fmt.Sprintf("%s: NOT_RANKABLE (%s) - excluded from ranking; %s",
			r.CandidateID, r.Support, joinFailures(r.Failures)))
	}
	switch len(out.Ranked) {
	case 0:
		out.Notes = append(out.Notes, "no rankable candidate: no winner or loser named")
	case 1:
		out.Winner = out.Ranked[0].CandidateID
		out.Notes = append(out.Notes, "only one rankable candidate: named as winner, no loser named (nothing rankable to compare against)")
	default:
		out.Winner = out.Ranked[0].CandidateID
		out.Loser = out.Ranked[len(out.Ranked)-1].CandidateID
		out.Notes = append(out.Notes, separationNote(out.Ranked[0], out.Ranked[len(out.Ranked)-1])...)
	}
	return out
}

// separationNote reports when the winner and loser are not separated by more
// than their own bootstrap uncertainty.
//
// Passing the rankability gates means the estimates are trustworthy enough to
// compare. It does not mean the comparison found a difference, and an ordering
// inside the error bars is an ordering of noise. The gate does not refuse this
// case -- refusing it would conflate "unrankable" with "tied" -- but the report
// must not let it read as a result.
func separationNote(best, worst RankabilityReport) []string {
	bSE, wSE := best.Estimate.BootstrapSE, worst.Estimate.BootstrapSE
	if bSE == nil || wSE == nil {
		return []string{"no bootstrap interval available: the ordering is a point-estimate ordering only"}
	}
	gap := math.Abs(best.Estimate.SNIPS - worst.Estimate.SNIPS)
	combined := 2 * math.Sqrt(*bSE**bSE+*wSE**wSE)
	if gap < combined {
		return []string{fmt.Sprintf(
			"winner and loser differ by %.4f, inside the combined bootstrap uncertainty %.4f: the ordering is not evidence of a difference",
			gap, combined)}
	}
	return nil
}

func joinFailures(f []string) string {
	if len(f) == 0 {
		return "no recorded failure"
	}
	s := f[0]
	for _, x := range f[1:] {
		s += "; " + x
	}
	return s
}
