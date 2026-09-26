// Package thompson implements Thompson Sampling over a mutable set of arms,
// with pluggable Beta samplers and warm-start priors.
//
// It is a port of the Rust crate in this repository and deliberately keeps the
// same structure, so results from the simulation harness carry across.
//
// The package has no dependencies outside the standard library and does no I/O:
// persistence is the caller's business, via [Policy.Snapshot] and [Restore].
package thompson

import (
	"fmt"
	"math/rand/v2"
)

// UpdateKind selects how a reward in [0, 1] is folded into a Beta posterior.
type UpdateKind int

const (
	// Binarize counts reward > Threshold as one success, otherwise one failure.
	//
	// This is what most production routers do and it is the only rule here that
	// discards information: two rewards on the same side of the threshold
	// become the same observation. It also carries no regret guarantee for
	// non-Bernoulli rewards.
	Binarize UpdateKind = iota

	// Bernoulli draws u ~ U(0,1) and counts a success when u < reward.
	//
	// The rule from Agrawal & Goyal (2012), which extends the Beta-Bernoulli
	// regret bound to arbitrary [0, 1] rewards.
	Bernoulli

	// Fractional adds reward to Alpha and 1-reward to Beta. Cheap and
	// low-variance, but not a Bayesian update for any standard likelihood.
	Fractional
)

// UpdateRule is an UpdateKind plus the parameter Binarize needs.
type UpdateRule struct {
	Kind      UpdateKind
	Threshold float64
}

// DefaultUpdateRule returns the Bernoulli rule.
func DefaultUpdateRule() UpdateRule { return UpdateRule{Kind: Bernoulli} }

// Posterior is a Beta(Alpha, Beta) distribution over an arm's success
// probability, plus the number of real observations behind it.
//
// Pulls is tracked separately from Alpha+Beta because a warm-started arm begins
// with a concentrated posterior and zero observations.
type Posterior struct {
	Alpha float64 `json:"alpha"`
	Beta  float64 `json:"beta"`
	Pulls uint64  `json:"pulls"`
}

// Uninformative returns the uniform prior, Beta(1, 1).
func Uninformative() Posterior { return Posterior{Alpha: 1, Beta: 1} }

// NewPosterior validates and returns a posterior.
func NewPosterior(alpha, beta float64) (Posterior, error) {
	if !isFinitePositive(alpha) || !isFinitePositive(beta) {
		return Posterior{}, fmt.Errorf(
			"thompson: Beta parameters must be finite and > 0, got alpha=%v beta=%v", alpha, beta)
	}
	return Posterior{Alpha: alpha, Beta: beta}, nil
}

func isFinitePositive(v float64) bool {
	return v > 0 && v-v == 0 // rejects NaN and +Inf
}

// Concentration returns Alpha + Beta.
func (p Posterior) Concentration() float64 { return p.Alpha + p.Beta }

// Mean returns the posterior mean.
func (p Posterior) Mean() float64 { return p.Alpha / p.Concentration() }

// Variance returns the posterior variance.
func (p Posterior) Variance() float64 {
	n := p.Concentration()
	return (p.Alpha * p.Beta) / (n * n * (n + 1))
}

// StdDev returns the posterior standard deviation.
func (p Posterior) StdDev() float64 { return sqrt(p.Variance()) }

// CredibleWidth returns an approximate 95% credible interval width.
func (p Posterior) CredibleWidth() float64 { return 1.96 * p.StdDev() }

// Observe folds one reward in [0, 1] into the posterior under rule.
func (p *Posterior) Observe(rng *rand.Rand, reward float64, rule UpdateRule) error {
	if reward < 0 || reward > 1 || reward != reward {
		return fmt.Errorf("thompson: reward must lie in [0, 1], got %v", reward)
	}

	switch rule.Kind {
	case Binarize:
		if rule.Threshold != rule.Threshold || rule.Threshold < 0 || rule.Threshold >= 1 {
			// A NaN threshold makes every comparison false (all failures);
			// out-of-range thresholds silently binarize everything one way.
			return fmt.Errorf("thompson: binarize threshold must lie in [0, 1), got %v", rule.Threshold)
		}
		if reward > rule.Threshold {
			p.Alpha++
		} else {
			p.Beta++
		}
	case Bernoulli:
		if rng == nil {
			return ErrNilRNG
		}
		if rng.Float64() < reward {
			p.Alpha++
		} else {
			p.Beta++
		}
	case Fractional:
		p.Alpha += reward
		p.Beta += 1 - reward
	default:
		return fmt.Errorf("thompson: unknown update rule %d", rule.Kind)
	}

	p.Pulls++
	return nil
}

// Discount scales the posterior toward the uniform prior by factor in (0, 1].
//
// Applied every round, this gives the posterior an effective memory of roughly
// 1/(1-factor) observations, so an arm that degrades is unlearned instead of
// being pinned by stale successes.
func (p *Posterior) Discount(factor float64) {
	if factor <= 0 || factor > 1 {
		return
	}
	p.Alpha = 1 + (p.Alpha-1)*factor
	p.Beta = 1 + (p.Beta-1)*factor
}
