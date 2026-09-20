package propensity

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"gonum.org/v1/gonum/mathext"
)

// Arm is one eligible arm's Beta posterior as it stood at decision time.
type Arm struct {
	ID    string
	Alpha float64
	Beta  float64
}

// ErrInvalidPosterior reports a posterior that is not a usable Beta.
var ErrInvalidPosterior = errors.New("propensity: invalid Beta posterior")

// ReferenceOptions tunes the numerical reference. The zero value is valid and
// gives DefaultReferenceOptions.
type ReferenceOptions struct {
	// AbsTol is the absolute quadrature budget for a single arm's integral.
	AbsTol float64
	// RelTol is the relative quadrature budget for a single arm's integral.
	RelTol float64
	// MaxDepth bounds bisection depth per seed panel.
	MaxDepth int
	// MaxSumError is the largest |sum(P_i) - 1| accepted before the whole
	// reconstruction is declared a numerical failure.
	MaxSumError float64
	// NormalizeAbove: normalize only when |sum-1| exceeds this. Below it the raw
	// integrals are already a partition of unity to working precision and are
	// returned untouched.
	NormalizeAbove float64
}

// DefaultReferenceOptions returns the tolerances used by the audit tooling.
func DefaultReferenceOptions() ReferenceOptions {
	return ReferenceOptions{
		AbsTol:         1e-15,
		RelTol:         1e-9,
		MaxDepth:       24,
		MaxSumError:    1e-6,
		NormalizeAbove: 1e-15,
	}
}

func (o ReferenceOptions) withDefaults() ReferenceOptions {
	d := DefaultReferenceOptions()
	if o.AbsTol <= 0 {
		o.AbsTol = d.AbsTol
	}
	if o.RelTol <= 0 {
		o.RelTol = d.RelTol
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = d.MaxDepth
	}
	if o.MaxSumError <= 0 {
		o.MaxSumError = d.MaxSumError
	}
	if o.NormalizeAbove <= 0 {
		o.NormalizeAbove = d.NormalizeAbove
	}
	return o
}

// ReferenceResult is the numerical action distribution plus everything needed
// to judge whether to believe it.
type ReferenceResult struct {
	// Probs is the reported probability per arm, after the optional final
	// normalization described by Normalized.
	Probs map[string]float64
	// Raw is the probability per arm exactly as integrated, before any
	// normalization. Reported so the partition-of-unity residual stays visible.
	Raw map[string]float64
	// IntegrationAbsErr is the conservative per-arm quadrature error indicator
	// (summed |K15-G7| over accepted panels).
	IntegrationAbsErr map[string]float64
	// RawSum is sum(Raw). SumError is RawSum-1: the pre-normalization residual.
	RawSum   float64
	SumError float64
	// Normalized reports whether Probs differs from Raw.
	Normalized bool
	// Converged is false if any arm's integral hit the subdivision budget.
	Converged bool
	// Evals is the total integrand evaluation count.
	Evals int
	// Method names the algorithm, for the audit record.
	Method string
	// Underflows counts integrand evaluations whose log-space value fell below
	// the double-precision floor and were therefore returned as exact zero.
	Underflows int
}

// Reference computes P(arm i wins one round of exact Thompson Sampling) for
// every arm by numerical integration of
//
//	P_i = int_0^1 f_i(x) * prod_{j != i} F_j(x) dx.
//
// The integrand is formed in log space -- log f_i(x) + sum_j!=i log F_j(x) --
// so that a product of many small CDF values cannot underflow before the
// density has been applied. Each arm is integrated independently over a shared
// partition seeded at every arm's Beta quantiles, which is what keeps the
// routine accurate for posteriors concentrated in a window far narrower than
// any fixed grid would resolve.
//
// It returns ErrInvalidPosterior if any alpha or beta is not finite and
// positive, and an error if the pre-normalization probabilities do not sum to 1
// within opts.MaxSumError.
func Reference(arms []Arm, opts ReferenceOptions) (ReferenceResult, error) {
	opts = opts.withDefaults()
	res := ReferenceResult{
		Probs:             make(map[string]float64, len(arms)),
		Raw:               make(map[string]float64, len(arms)),
		IntegrationAbsErr: make(map[string]float64, len(arms)),
		Converged:         true,
		Method:            "adaptive Gauss-Kronrod (G7,K15) over quantile-seeded partition; cephes regularized incomplete beta",
	}
	if len(arms) == 0 {
		return res, fmt.Errorf("propensity: no arms")
	}
	for _, a := range arms {
		if !isFinitePositive(a.Alpha) || !isFinitePositive(a.Beta) {
			return res, fmt.Errorf("%w: arm %q alpha=%v beta=%v", ErrInvalidPosterior, a.ID, a.Alpha, a.Beta)
		}
	}
	if len(arms) == 1 {
		res.Probs[arms[0].ID] = 1
		res.Raw[arms[0].ID] = 1
		res.IntegrationAbsErr[arms[0].ID] = 0
		res.RawSum, res.SumError = 1, 0
		return res, nil
	}

	logB := make([]float64, len(arms))
	for i, a := range arms {
		logB[i] = logBeta(a.Alpha, a.Beta)
	}
	panels := seedPartition(arms)

	for i, a := range arms {
		integrand := func(x float64) float64 {
			if x <= 0 || x >= 1 {
				return 0
			}
			lg := betaLogPDF(x, a.Alpha, a.Beta, logB[i])
			if math.IsInf(lg, -1) {
				return 0
			}
			for j, o := range arms {
				if j == i {
					continue
				}
				cdf := regIncBeta(o.Alpha, o.Beta, x)
				if cdf <= 0 {
					return 0 // some rival is certain to sit above x
				}
				lg += math.Log(cdf)
				if lg < logUnderflow {
					res.Underflows++
					return 0
				}
			}
			if lg < logUnderflow {
				res.Underflows++
				return 0
			}
			return math.Exp(lg)
		}

		var sum, errSum float64
		for _, p := range panels {
			q := adaptiveGK(integrand, p[0], p[1], opts.AbsTol*(p[1]-p[0]), opts.RelTol, opts.MaxDepth)
			sum += q.Value
			errSum += q.AbsErr
			res.Evals += q.Evals
			if !q.Converged {
				res.Converged = false
			}
		}
		if sum < 0 { // only reachable from rounding on an all-but-zero integrand
			sum = 0
		}
		res.Raw[a.ID] = sum
		res.IntegrationAbsErr[a.ID] = errSum
		res.RawSum += sum
	}

	res.SumError = res.RawSum - 1
	if math.Abs(res.SumError) > opts.MaxSumError || math.IsNaN(res.RawSum) {
		for _, a := range arms {
			res.Probs[a.ID] = res.Raw[a.ID]
		}
		return res, fmt.Errorf("propensity: reference probabilities sum to %.12g (residual %.3g) exceeding MaxSumError %.3g", res.RawSum, res.SumError, opts.MaxSumError)
	}
	if math.Abs(res.SumError) > opts.NormalizeAbove && res.RawSum > 0 {
		res.Normalized = true
		for _, a := range arms {
			res.Probs[a.ID] = res.Raw[a.ID] / res.RawSum
		}
	} else {
		for _, a := range arms {
			res.Probs[a.ID] = res.Raw[a.ID]
		}
	}
	return res, nil
}

// logUnderflow is the point below which exp() is zero in float64.
const logUnderflow = -745.2

func isFinitePositive(v float64) bool { return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v) }

// logBeta returns log B(a,b) via the log-gamma function, which is the stable
// form: B(a,b) itself underflows for concentrated posteriors long before the
// density does.
func logBeta(a, b float64) float64 {
	la, _ := math.Lgamma(a)
	lb, _ := math.Lgamma(b)
	lab, _ := math.Lgamma(a + b)
	return la + lb - lab
}

// betaLogPDF returns log f(x; a, b) for x in (0,1), given a precomputed log B.
func betaLogPDF(x, a, b, logBab float64) float64 {
	if x <= 0 || x >= 1 {
		return math.Inf(-1)
	}
	return (a-1)*math.Log(x) + (b-1)*math.Log1p(-x) - logBab
}

// regIncBeta is the regularized incomplete beta function I_x(a,b), i.e. the
// Beta CDF. It delegates to gonum's cephes port; see DEPENDENCY.md for why an
// external implementation is used rather than a hand-rolled one.
func regIncBeta(a, b, x float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	return mathext.RegIncBeta(a, b, x)
}

// betaQuantile inverts the Beta CDF, used only to seed the integration
// partition. A wrong quantile costs accuracy, never correctness: the panels are
// still integrated adaptively.
func betaQuantile(a, b, p float64) float64 {
	if p <= 0 {
		return 0
	}
	if p >= 1 {
		return 1
	}
	return mathext.InvRegIncBeta(a, b, p)
}

// seedQuantiles are the probability levels at which every arm contributes a
// breakpoint. The extreme levels matter: a Beta(9000,1000) posterior puts all
// its mass in a window of width ~0.03, and a uniform grid would step straight
// over it.
var seedQuantiles = []float64{
	1e-10, 1e-7, 1e-5, 1e-3, 0.01, 0.05, 0.15, 0.3, 0.5,
	0.7, 0.85, 0.95, 0.99, 1 - 1e-3, 1 - 1e-5, 1 - 1e-7, 1 - 1e-10,
}

// endpointLadder puts panel edges on a geometric ladder toward 0 and 1.
//
// Quantile seeding alone is not enough at the ends. Two integrands force this:
// a Beta(0.5, ...) density has an integrable x^-1/2 singularity at 0, and the
// CDF product against a rival like Beta(1e6, 1) is flat zero until x is within
// 1e-6 of 1. Plain bisection from a panel of width 1 needs ~50 levels to reach
// either scale; the ladder starts it there.
var endpointLadder = func() []float64 {
	var out []float64
	for e := 1.0; e <= 16; e++ {
		d := math.Pow(10, -e)
		out = append(out, d, 1-d)
	}
	return out
}()

// seedPartition builds the initial panel list: every arm's quantiles plus the
// endpoint ladder, deduped and sorted, spanning [0,1].
func seedPartition(arms []Arm) [][2]float64 {
	pts := make([]float64, 0, len(arms)*len(seedQuantiles)+len(endpointLadder)+2)
	pts = append(pts, 0, 1)
	pts = append(pts, endpointLadder...)
	for _, a := range arms {
		for _, q := range seedQuantiles {
			x := betaQuantile(a.Alpha, a.Beta, q)
			if x > 0 && x < 1 && !math.IsNaN(x) {
				pts = append(pts, x)
			}
		}
	}
	sort.Float64s(pts)
	panels := make([][2]float64, 0, len(pts))
	prev := pts[0]
	for _, x := range pts[1:] {
		if x-prev > 1e-15 {
			panels = append(panels, [2]float64{prev, x})
			prev = x
		}
	}
	if len(panels) == 0 {
		panels = append(panels, [2]float64{0, 1})
	}
	// Extend the last panel to 1 in case dedup dropped the endpoint.
	if last := &panels[len(panels)-1]; last[1] < 1 {
		last[1] = 1
	}
	return panels
}
