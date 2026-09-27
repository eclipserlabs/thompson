package thompson

import (
	"fmt"
	"math/rand/v2"
	"sort"
)

// Cost-aware v1 identities. Distinct from every cost-blind LoggingPolicyID
// ("exact-thompson-v1", "ucb-regularized-v1", "phased-v1"): a decision bound
// to this identity was produced by quality-constrained cost-aware selection,
// never by silent cost-blind fallback.
const (
	CostAwarePolicyID     = "thompson-costaware-v1"
	CostAwareObjectiveVer = "cost-aware-v1"
	CostAwareStateVersion = 1
	// CostAwareRuleV1 qualified on the Thompson SAMPLE crossing the floor.
	// CostAwareRuleV2 (current) qualifies post-cold arms on the posterior
	// MEAN reaching the floor. Rule version is bound to every decision so
	// ledgers distinguish which guarantee produced each pick.
	CostAwareRuleV1 = 1
	CostAwareRuleV2 = 2
)

// CostAwareConfig freezes the v1 objective parameters. Every field is a
// charter item: changing one restarts the experiment.
type CostAwareConfig struct {
	// QualityFloor is the pre-registered minimum acceptable verified success
	// rate. Arms sampling below it are disqualified (unless cold).
	QualityFloor float64 `json:"quality_floor"`
	// MinMeteredN is the minimum fully-metered jobs before an arm's cost mean
	// counts as known. Below it the arm is explorable but never cost-optimal.
	MinMeteredN uint64 `json:"min_metered_n"`
	// ColdStartPulls grants exploration qualification to arms with fewer
	// quality pulls, regardless of sample. Zero disables cold qualification.
	ColdStartPulls uint64 `json:"cold_start_pulls"`
	// Epsilon floors the success sample in the cost-per-success divisor.
	Epsilon float64 `json:"epsilon"`
}

// DefaultCostAwareConfig returns the v1 charter defaults.
func DefaultCostAwareConfig() CostAwareConfig {
	return CostAwareConfig{QualityFloor: 0.3, MinMeteredN: 3, ColdStartPulls: 5, Epsilon: 1e-3}
}

// Validate rejects malformed configs explicitly.
func (c CostAwareConfig) Validate() error {
	if !(c.QualityFloor >= 0 && c.QualityFloor <= 1) {
		return fmt.Errorf("thompson: cost-aware quality floor %v out of [0,1]", c.QualityFloor)
	}
	if !(c.Epsilon > 0 && c.Epsilon <= 1) {
		return fmt.Errorf("thompson: cost-aware epsilon %v out of (0,1]", c.Epsilon)
	}
	return nil
}

// CostAwareInputs are the per-arm observations for one selection. Scores are
// the Thompson quality samples from the audited sampler path
// (SelectWithScores); Means/Known come from the independent CostBookV1;
// Pulls come from the quality posterior. Maps are read-only.
type CostAwareInputs struct {
	Scores map[string]float64 // Thompson success samples per arm
	Means  map[string]float64 // mean fully-loaded cost per arm (only when Known)
	Known  map[string]bool    // cost mean known (metered_n >= MinMeteredN)
	Pulls  map[string]uint64  // quality pulls per arm
}

// CostAwareResult is one bound decision: the chosen arm, whether it is a
// genuine cost-aware optimum or an explicit quality-fallback, and the
// per-arm cost-per-success figures used.
type CostAwareResult struct {
	ArmID       string             `json:"arm_id"`
	PolicyID    string             `json:"policy_id"`
	Objective   string             `json:"objective_version"`
	RuleVersion int                `json:"rule_version"`
	Fallback    bool               `json:"fallback"`
	Reason      string             `json:"reason"`
	CostPerSuc  map[string]float64 `json:"cost_per_success,omitempty"`
}

// SelectCostAware implements quality-constrained cost-aware selection (v1):
// qualify by floor-or-cold, then minimize mean_cost / max(sample, eps) among
// qualified arms with known costs. Arms without known costs are explorable
// but never optimal; when no qualified arm has a known cost, the max-sample
// qualified arm is returned with Fallback=true (explicit, counted, never
// mislabeled optimal). When nothing qualifies, the global max-sample arm is
// returned with Fallback=true. Deterministic tie-breaks by sorted arm ID.
func SelectCostAware(rng *rand.Rand, policy *Policy, means map[string]float64, known map[string]bool, cfg CostAwareConfig) (CostAwareResult, error) {
	if rng == nil {
		return CostAwareResult{}, ErrNilRNG
	}
	if policy == nil {
		return CostAwareResult{}, fmt.Errorf("thompson: cost-aware nil policy")
	}
	if err := cfg.Validate(); err != nil {
		return CostAwareResult{}, err
	}
	choice, scores, err := policy.SelectWithScores(rng)
	if err != nil {
		return CostAwareResult{}, err
	}
	_ = choice // recomputed under the cost-aware rule; kept for RNG parity
	pulls := make(map[string]uint64, len(scores))
	qmeans := make(map[string]float64, len(scores))
	for arm := range scores {
		if post, ok := policy.PosteriorFor(arm); ok {
			pulls[arm] = post.Pulls
			qmeans[arm] = post.Mean()
		}
	}
	return SelectCostAwareFromSamplesV2(scores, qmeans, means, known, pulls, cfg)
}

// SelectCostAwareFromSamples is the pure rule (deterministic given inputs);
// rng-free so unit tests and replay are exact. Quality means (qmeans) are the
// posterior MEAN success estimates per arm; when nil, samples double as means
// (legacy RuleV1 behavior, RuleVersion=1 in the result).
func SelectCostAwareFromSamples(scores, means map[string]float64, known map[string]bool, pulls map[string]uint64, cfg CostAwareConfig) (CostAwareResult, error) {
	return SelectCostAwareFromSamplesV2(scores, nil, means, known, pulls, cfg)
}

// SelectCostAwareFromSamplesV2 is the safeguarded rule: post-cold arms
// qualify on posterior MEAN reaching the floor (RuleV2). A nil qmeans map
// selects the legacy sample rule (RuleV1) for replay of old ledgers.
func SelectCostAwareFromSamplesV2(scores, qmeans, means map[string]float64, known map[string]bool, pulls map[string]uint64, cfg CostAwareConfig) (CostAwareResult, error) {
	if err := cfg.Validate(); err != nil {
		return CostAwareResult{}, err
	}
	if len(scores) == 0 {
		return CostAwareResult{}, ErrNoArms
	}
	arms := make([]string, 0, len(scores))
	for a := range scores {
		arms = append(arms, a)
	}
	sort.Strings(arms)
	eps := cfg.Epsilon
	cps := make(map[string]float64, len(arms))
	for _, a := range arms {
		q := scores[a]
		if q < eps {
			q = eps
		}
		if m, ok := means[a]; ok && known[a] {
			cps[a] = m / q
		}
	}
	rule := CostAwareRuleV2
	if qmeans == nil {
		rule = CostAwareRuleV1
	}
	qualified := func(a string) bool {
		if pulls[a] < cfg.ColdStartPulls {
			return true
		}
		if rule == CostAwareRuleV2 {
			qm, ok := qmeans[a]
			if !ok {
				return false
			}
			return qm >= cfg.QualityFloor
		}
		return scores[a] >= cfg.QualityFloor
	}
	var qualKnown []string
	var qualUnknown []string
	for _, a := range arms {
		if !qualified(a) {
			continue
		}
		if _, ok := means[a]; ok && known[a] {
			qualKnown = append(qualKnown, a)
		} else {
			// F7: known-without-mean (or unknown) is explorable, never
			// dropped: it joins the flagged fallback pool, never the
			// optimum pool.
			qualUnknown = append(qualUnknown, a)
		}
	}
	mk := func() CostAwareResult {
		return CostAwareResult{PolicyID: CostAwarePolicyID, Objective: CostAwareObjectiveVer, RuleVersion: rule, CostPerSuc: cps}
	}
	if len(qualKnown) > 0 {
		best := qualKnown[0]
		for _, a := range qualKnown[1:] {
			if cps[a] < cps[best] {
				best = a
			}
		}
		r := mk()
		r.ArmID = best
		r.Reason = "min-cost-per-success among quality-qualified arms with known costs"
		return r, nil
	}
	// Explicit fallback: qualified but cost-unknown, or nothing qualified.
	// Never mislabeled optimal; caller must log and count Fallback=true.
	pool := qualUnknown
	reason := "no qualified arm with known cost: max-sample among qualified (exploration fallback)"
	if len(pool) == 0 {
		pool = arms
		reason = "no arm meets quality floor: max-sample global (quality fallback)"
		// Cold-start disabled and everything below floor: still fail
		// explicitly when the caller requires strict gating? v1 returns the
		// max-sample arm flagged fallback so the experiment stays live and
		// the report's quality gate refuses unsupported conclusions.
	}
	best := pool[0]
	for _, a := range pool[1:] {
		if scores[a] > scores[best] {
			best = a
		}
	}
	r := mk()
	r.ArmID = best
	r.Fallback = true
	r.Reason = reason
	return r, nil
}
