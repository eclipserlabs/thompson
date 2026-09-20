package propensity

import (
	"fmt"
	"math"
	"sort"
)

// Status is the per-action verdict on whether a reconstructed propensity is
// fit to be an importance-weight denominator.
type Status string

const (
	// Reliable: the propensity is known well enough to divide by.
	Reliable Status = "RELIABLE"
	// LowPrecision: the propensity is positive but its own uncertainty is large
	// enough that the resulting weight is not trustworthy.
	LowPrecision Status = "LOW_PRECISION"
	// MCZeroWins: finite Monte Carlo produced no wins for this arm. This is a
	// statement about the estimator, not about the policy: an eligible arm with
	// alpha>0 and beta>0 always has positive Thompson probability.
	MCZeroWins Status = "MC_ZERO_WINS"
	// NumericalReferenceFailure: the numerical reference did not converge or did
	// not integrate to a partition of unity.
	NumericalReferenceFailure Status = "NUMERICAL_REFERENCE_FAILURE"
	// InvalidPosterior: alpha or beta is not finite and positive.
	InvalidPosterior Status = "INVALID_POSTERIOR"
)

// Usable reports whether a status may supply an IPS/SNIPS denominator.
func (s Status) Usable() bool { return s == Reliable }

// Thresholds define when a reconstructed propensity is precise enough to use.
// The zero value is invalid; use DefaultThresholds.
type Thresholds struct {
	// MinWins is the smallest Monte-Carlo win count accepted. The relative
	// standard error of a binomial proportion is about 1/sqrt(wins), so 100 wins
	// pins the denominator to roughly +/-10% and 400 to +/-5%. Draw count alone
	// says nothing: 1/sqrt(draws) is an absolute bound, and an action with
	// p=1e-5 has 100% relative error at 1e5 draws.
	MinWins int
	// MaxRelCIHalfWidth is the largest accepted (CI half-width / estimate).
	MaxRelCIHalfWidth float64
	// ZeroWinAlpha is the level of the one-sided upper bound reported for
	// zero-win actions.
	ZeroWinAlpha float64
	// CIZ is the two-sided normal quantile for the Wilson interval (1.96 = 95%).
	CIZ float64
	// MaxReferenceRelDisagreement downgrades an otherwise-precise Monte-Carlo
	// estimate that disagrees with the numerical reference by more than this
	// relative amount.
	MaxReferenceRelDisagreement float64
	// MinReferenceProb is the smallest reference probability treated as
	// numerically resolved. Below it the integral is at the noise floor of the
	// quadrature and the implied weight is meaningless.
	MinReferenceProb float64
}

// DefaultThresholds returns the gate used by the audit tooling and the OPE path.
func DefaultThresholds() Thresholds {
	return Thresholds{
		MinWins:                     100,
		MaxRelCIHalfWidth:           0.10,
		ZeroWinAlpha:                0.05,
		CIZ:                         1.959963984540054,
		MaxReferenceRelDisagreement: 0.15,
		MinReferenceProb:            1e-12,
	}
}

// Source names which estimator produced a propensity.
type Source string

const (
	SourceReference  Source = "numerical-reference"
	SourceMonteCarlo Source = "monte-carlo"
)

// Estimate is the full audit record for one action's reconstructed propensity.
type Estimate struct {
	ArmID  string
	Status Status
	Source Source
	Reason string

	// Value is the propensity actually offered as a denominator. It is only
	// meaningful when Status is RELIABLE.
	Value float64

	// Estimated is the Monte-Carlo point estimate, when Monte Carlo was run.
	Estimated *float64
	// Reference is the numerical-reference probability, when available.
	Reference *float64
	// AbsError and RelError compare Estimated against Reference.
	AbsError *float64
	RelError *float64

	// Wins and Draws are the raw Monte-Carlo counts.
	Wins  int
	Draws int
	// CILow/CIHigh is the Wilson interval on Estimated at Thresholds.CIZ.
	CILow  float64
	CIHigh float64
	// ZeroWinUpperBound is the one-sided upper bound on a probability that
	// produced no wins. Non-nil exactly when Status is MC_ZERO_WINS.
	ZeroWinUpperBound *float64
	// IntegrationAbsErr is the quadrature error indicator for Reference.
	IntegrationAbsErr *float64
}

// Reconstruction is the per-decision result: an Estimate for every eligible arm
// plus the shared diagnostics of the run.
type Reconstruction struct {
	Estimates map[string]Estimate
	Reference *ReferenceResult
	MC        *MCResult
	// RawSumError is the pre-normalization partition-of-unity residual of the
	// numerical reference, echoed here so callers need not hold the whole result.
	RawSumError float64
}

// Get returns the Estimate for an arm.
func (r Reconstruction) Get(armID string) (Estimate, bool) {
	e, ok := r.Estimates[armID]
	return e, ok
}

// ReconstructReference builds propensities from the numerical reference alone.
// This is the denominator the audit treats as ground truth.
func ReconstructReference(arms []Arm, opts ReferenceOptions, th Thresholds) Reconstruction {
	out := Reconstruction{Estimates: make(map[string]Estimate, len(arms))}
	if bad := firstInvalid(arms); bad != nil {
		for _, a := range arms {
			out.Estimates[a.ID] = Estimate{
				ArmID: a.ID, Status: InvalidPosterior, Source: SourceReference,
				Reason: fmt.Sprintf("arm %q has alpha=%v beta=%v", bad.ID, bad.Alpha, bad.Beta),
			}
		}
		return out
	}
	ref, err := Reference(arms, opts)
	out.Reference = &ref
	out.RawSumError = ref.SumError
	for _, a := range arms {
		p := ref.Probs[a.ID]
		ie := ref.IntegrationAbsErr[a.ID]
		e := Estimate{
			ArmID: a.ID, Source: SourceReference, Value: p,
			Reference: fptr(p), IntegrationAbsErr: fptr(ie),
		}
		switch {
		case err != nil:
			e.Status = NumericalReferenceFailure
			e.Reason = err.Error()
		case math.IsNaN(p) || p < 0:
			e.Status = NumericalReferenceFailure
			e.Reason = "non-finite integral"
		case !ref.Converged:
			// The local |K15-G7| indicator understates the true error at an
			// integrable endpoint singularity (alpha or beta below 1, reachable
			// via discounted warm-start priors), which is exactly when the
			// budget runs out. Report reduced precision rather than a number
			// whose own error bar cannot be trusted.
			e.Status = LowPrecision
			e.Reason = "adaptive quadrature exhausted its subdivision budget; the error indicator may understate the true error"
		case p < th.MinReferenceProb:
			e.Status = LowPrecision
			e.Reason = fmt.Sprintf("reference probability %.3g below numerical resolution %.3g", p, th.MinReferenceProb)
		case ie > 0 && p > 0 && ie/p > th.MaxRelCIHalfWidth:
			e.Status = LowPrecision
			e.Reason = fmt.Sprintf("quadrature error %.3g is %.1f%% of the integral", ie, 100*ie/p)
		default:
			e.Status = Reliable
		}
		out.Estimates[a.ID] = e
	}
	return out
}

// ReconstructMonteCarlo builds propensities from Monte Carlo, and -- when
// withReference is true -- also computes the numerical reference so that each
// Monte-Carlo estimate can be scored against it.
//
// The reference is only ever used here to *classify* the Monte-Carlo estimate.
// Estimate.Value stays the Monte-Carlo number, so a caller that asks for
// Monte-Carlo denominators gets Monte-Carlo denominators.
func ReconstructMonteCarlo(arms []Arm, draws int, seed uint64, withReference bool, opts ReferenceOptions, th Thresholds) Reconstruction {
	out := Reconstruction{Estimates: make(map[string]Estimate, len(arms))}
	if bad := firstInvalid(arms); bad != nil {
		for _, a := range arms {
			out.Estimates[a.ID] = Estimate{
				ArmID: a.ID, Status: InvalidPosterior, Source: SourceMonteCarlo,
				Reason: fmt.Sprintf("arm %q has alpha=%v beta=%v", bad.ID, bad.Alpha, bad.Beta),
			}
		}
		return out
	}
	mc := MonteCarlo(arms, draws, seed)
	out.MC = &mc

	var ref *ReferenceResult
	var refErr error
	if withReference {
		r, err := Reference(arms, opts)
		ref, refErr = &r, err
		out.Reference = ref
		out.RawSumError = r.SumError
	}

	for _, a := range arms {
		p := mc.Probs[a.ID]
		wins := mc.Wins[a.ID]
		lo, hi := wilsonInterval(wins, mc.Draws, th.CIZ)
		e := Estimate{
			ArmID: a.ID, Source: SourceMonteCarlo, Value: p, Estimated: fptr(p),
			Wins: wins, Draws: mc.Draws, CILow: lo, CIHigh: hi,
		}
		if ref != nil && refErr == nil && ref.Converged {
			rp := ref.Probs[a.ID]
			ie := ref.IntegrationAbsErr[a.ID]
			e.Reference = fptr(rp)
			e.IntegrationAbsErr = fptr(ie)
			ae := math.Abs(p - rp)
			e.AbsError = fptr(ae)
			if rp > 0 {
				e.RelError = fptr(ae / rp)
			}
		}

		switch {
		case wins == 0:
			ub := zeroWinUpperBound(mc.Draws, th.ZeroWinAlpha)
			e.ZeroWinUpperBound = fptr(ub)
			e.Status = MCZeroWins
			e.Reason = fmt.Sprintf("0 wins in %d draws; true probability is positive but unresolved, one-sided %.0f%% upper bound %.3g",
				mc.Draws, 100*(1-th.ZeroWinAlpha), ub)
		case wins < th.MinWins:
			e.Status = LowPrecision
			e.Reason = fmt.Sprintf("%d wins in %d draws is below MinWins=%d (relative standard error ~%.1f%%)",
				wins, mc.Draws, th.MinWins, 100/math.Sqrt(float64(wins)))
		case p > 0 && (hi-lo)/2/p > th.MaxRelCIHalfWidth:
			e.Status = LowPrecision
			e.Reason = fmt.Sprintf("Wilson half-width is %.1f%% of the estimate, above %.1f%%",
				100*(hi-lo)/2/p, 100*th.MaxRelCIHalfWidth)
		case e.RelError != nil && *e.RelError > th.MaxReferenceRelDisagreement:
			e.Status = LowPrecision
			e.Reason = fmt.Sprintf("disagrees with numerical reference by %.1f%% (est %.6g vs ref %.6g)",
				100**e.RelError, p, *e.Reference)
		default:
			e.Status = Reliable
		}
		out.Estimates[a.ID] = e
	}
	return out
}

func firstInvalid(arms []Arm) *Arm {
	for i := range arms {
		if !isFinitePositive(arms[i].Alpha) || !isFinitePositive(arms[i].Beta) {
			return &arms[i]
		}
	}
	return nil
}

func fptr(v float64) *float64 { return &v }

// SortedArmIDs returns arm IDs in the deterministic order used everywhere here.
func SortedArmIDs(arms []Arm) []string {
	ids := make([]string, len(arms))
	for i, a := range arms {
		ids[i] = a.ID
	}
	sort.Strings(ids)
	return ids
}
