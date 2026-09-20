package propensity

import (
	"math"
	"math/rand/v2"
	"sort"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// MCResult is one Monte-Carlo reconstruction of the Thompson action
// distribution, keeping the raw win counts rather than only the ratios. The
// counts are what make "zero wins" distinguishable from "probability zero".
type MCResult struct {
	Probs map[string]float64
	Wins  map[string]int
	Draws int
	Seed  uint64
}

// MonteCarlo estimates P(arm wins one Thompson round) by simulating draws
// rounds of "one exact Beta sample per arm, take the argmax".
//
// It is the reference *implementation of the estimator under audit*: it must
// stay bit-identical to what the offline OPE path uses, which is why
// gateway.EstimateThompsonPropensities delegates here rather than keeping its
// own copy. Arms are iterated in sorted ID order so the RNG stream, and hence
// the result, is reproducible for a given seed.
func MonteCarlo(arms []Arm, draws int, seed uint64) MCResult {
	if draws <= 0 {
		draws = 10000
	}
	ids := make([]string, len(arms))
	post := make([]thompson.Posterior, len(arms))
	idx := make([]int, len(arms))
	for i, a := range arms {
		ids[i] = a.ID
		idx[i] = i
	}
	sort.Slice(idx, func(i, j int) bool { return ids[idx[i]] < ids[idx[j]] })
	sortedIDs := make([]string, len(arms))
	for k, i := range idx {
		sortedIDs[k] = arms[i].ID
		post[k] = thompson.Posterior{Alpha: arms[i].Alpha, Beta: arms[i].Beta}
	}

	rng := rand.New(rand.NewPCG(seed, seed>>1))
	sampler := thompson.ExactSampler{}
	wins := make([]int, len(arms))
	for d := 0; d < draws; d++ {
		best, bestScore := -1, math.Inf(-1)
		for k := range sortedIDs {
			s := sampler.Sample(rng, post[k])
			if best < 0 || s > bestScore {
				best, bestScore = k, s
			}
		}
		wins[best]++
	}

	out := MCResult{
		Probs: make(map[string]float64, len(arms)),
		Wins:  make(map[string]int, len(arms)),
		Draws: draws,
		Seed:  seed,
	}
	for k, id := range sortedIDs {
		out.Wins[id] = wins[k]
		out.Probs[id] = float64(wins[k]) / float64(draws)
	}
	return out
}

// wilsonInterval returns the Wilson score interval for k successes in n trials
// at the given two-sided level. It is used instead of the normal approximation
// because propensities near zero are exactly where the normal interval fails.
func wilsonInterval(k, n int, z float64) (lo, hi float64) {
	if n <= 0 {
		return 0, 1
	}
	nf := float64(n)
	p := float64(k) / nf
	den := 1 + z*z/nf
	center := (p + z*z/(2*nf)) / den
	half := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf)) / den
	lo, hi = center-half, center+half
	if lo < 0 {
		lo = 0
	}
	if hi > 1 {
		hi = 1
	}
	return lo, hi
}

// zeroWinUpperBound is the exact Clopper-Pearson one-sided upper confidence
// bound for a probability that produced zero wins in n draws: 1 - alpha^(1/n),
// the "rule of three" (~3/n at alpha=0.05) without the approximation.
//
// It is an upper bound on an unknown positive probability. It is never a floor
// and must never be substituted into an IPS denominator.
func zeroWinUpperBound(n int, alpha float64) float64 {
	if n <= 0 {
		return 1
	}
	if alpha <= 0 || alpha >= 1 {
		alpha = 0.05
	}
	return 1 - math.Pow(alpha, 1/float64(n))
}
