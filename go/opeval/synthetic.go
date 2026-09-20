// Package opeval holds the offline propensity-validation harness: synthetic
// bandit-log generation, the posterior test matrix, Monte-Carlo-versus-reference
// error measurement, Thompson self-calibration, and importance-weight
// sensitivity analysis.
//
// Nothing here runs online. It exists so the claims in the OPE report are
// reproducible from a seed rather than asserted.
package opeval

import (
	"fmt"
	"math/rand/v2"
	"sort"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
	"github.com/wiramahendra/thompson-sampling/go/propensity"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// SyntheticEnv is a stationary Bernoulli bandit with known arm means.
type SyntheticEnv struct {
	Means map[string]float64
}

// FourArmEnv is the 0.8/0.6/0.4/0.2 environment used throughout the report.
// Its uniform-policy value is exactly the mean of the arm means, 0.5.
func FourArmEnv() SyntheticEnv {
	return SyntheticEnv{Means: map[string]float64{"a": 0.8, "b": 0.6, "c": 0.4, "d": 0.2}}
}

// UniformValue is the exact value of the uniform policy in this environment.
func (e SyntheticEnv) UniformValue() float64 {
	s := 0.0
	for _, m := range e.Means {
		s += m
	}
	return s / float64(len(e.Means))
}

// BestValue is the exact value of always playing the best arm.
func (e SyntheticEnv) BestValue() float64 {
	best := 0.0
	for _, m := range e.Means {
		if m > best {
			best = m
		}
	}
	return best
}

// ArmIDs returns the arm identifiers in sorted order.
func (e SyntheticEnv) ArmIDs() []string {
	ids := make([]string, 0, len(e.Means))
	for id := range e.Means {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Run logs n decisions under exact Thompson Sampling in env and returns them in
// the same LedgerDecision shape the gateway replays from, including the
// decision-time eligible_arm_state snapshot the propensity reconstruction needs.
//
// This is the logging policy. Nothing here evaluates a candidate; the log is
// produced once and every estimator is then applied to the same rows.
func Run(env SyntheticEnv, n int, seed uint64) []*gateway.LedgerDecision {
	policy := thompson.New(thompson.DefaultConfig(), thompson.ExactSampler{})
	for _, id := range env.ArmIDs() {
		policy.AddArm(id)
	}
	rng := rand.New(rand.NewPCG(seed, seed>>1))
	out := make([]*gateway.LedgerDecision, 0, n)

	for i := 0; i < n; i++ {
		eligible := policy.EligibleArmIDs()
		state := make([]gateway.EligibleArmState, 0, len(eligible))
		for _, id := range eligible {
			if p, ok := policy.PosteriorFor(id); ok {
				state = append(state, gateway.EligibleArmState{ArmID: id, Alpha: p.Alpha, Beta: p.Beta, Pulls: p.Pulls})
			}
		}
		chosen, scores, err := policy.SelectWithScores(rng)
		if err != nil {
			break
		}
		before, _ := policy.PosteriorFor(chosen)
		hash := policy.ConfigHash()
		id := fmt.Sprintf("syn-%06d", i)

		reward := 0.0
		if rng.Float64() < env.Means[chosen] {
			reward = 1
		}
		started := &gateway.DecisionStarted{
			SchemaVersion: 1, EventType: "DecisionStarted", DecisionID: id,
			EligibleArmIDs: eligible, SelectedArmID: chosen, SampledScores: scores,
			PolicyConfigHash: hash, EligibleArmState: state,
			PosteriorBefore:         gateway.PosteriorSnapshot{Alpha: before.Alpha, Beta: before.Beta, Pulls: before.Pulls},
			LoggingPolicyID:         "exact-thompson-v1",
			LoggingPolicyConfigHash: hash,
		}
		observed := &gateway.ExecutionObserved{
			SchemaVersion: 1, EventType: "ExecutionObserved", DecisionID: id,
			ArmID: chosen, LatencyMs: 10, Success: reward == 1,
		}
		policy.Record(rng, chosen, reward)
		after, _ := policy.PosteriorFor(chosen)
		learned := &gateway.DecisionLearned{
			SchemaVersion: 1, EventType: "DecisionLearned", DecisionID: id,
			ArmID: chosen, ComputedReward: reward,
			PosteriorBefore: gateway.PosteriorSnapshot{Alpha: before.Alpha, Beta: before.Beta, Pulls: before.Pulls},
			PosteriorAfter:  gateway.PosteriorSnapshot{Alpha: after.Alpha, Beta: after.Beta, Pulls: after.Pulls},
			TotalPullsAfter: policy.TotalPulls(),
		}
		out = append(out, &gateway.LedgerDecision{
			Started: started, Primary: observed, Learned: learned, Eligible: eligible,
		})
	}
	return out
}

// StatesAfter replays the same logging policy and captures the posterior state
// at the given decision indices. These are the states the estimator actually
// meets in this repository, as opposed to hand-written ones.
func StatesAfter(env SyntheticEnv, at []int, seed uint64) map[int][]propensity.Arm {
	maxN := 0
	want := map[int]bool{}
	for _, n := range at {
		want[n] = true
		if n > maxN {
			maxN = n
		}
	}
	out := map[int][]propensity.Arm{}
	decisions := Run(env, maxN+1, seed)
	for _, n := range at {
		if n < len(decisions) {
			out[n] = gateway.ArmsFromState(decisions[n].Started.EligibleArmState)
		}
	}
	return out
}
