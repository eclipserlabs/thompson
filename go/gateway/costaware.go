package gateway

import (
	"fmt"
	"math/rand/v2"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// SafetyGate masks selection eligibility and accounts exploration. It is the
// seam the Phase-3 SafetyController implements; Phase-2 ships a static
// prequalified-set gate. Nil gate means no filtering (tests only — the
// binary always wires a real gate in cost-aware mode).
type SafetyGate interface {
	// Authorize returns the currently permitted subset of eligible arms.
	Authorize(eligible []string) map[string]bool
	// Reserve claims one selection unit for arm. explore=true marks
	// cold/fallback exploration (budgeted); explore=false is accounting
	// only and never fails. ok=false means the budget is exhausted and
	// the caller must exclude the arm and re-pick deterministically.
	Reserve(arm string, explore bool) (ok bool)
}

// StaticGate permits exactly the prequalified set. Budgets are unenforced
// (Reserve always true); Phase-3 replaces it with the durable controller.
type StaticGate struct {
	Approved map[string]bool
}

// NewStaticGate builds a gate over the approved arms. Empty approval fails
// closed at selection time (no eligible arms), never defaults open.
func NewStaticGate(arms []string) *StaticGate {
	m := make(map[string]bool, len(arms))
	for _, a := range arms {
		m[a] = true
	}
	return &StaticGate{Approved: m}
}

func (g *StaticGate) Authorize(eligible []string) map[string]bool {
	out := make(map[string]bool, len(eligible))
	for _, a := range eligible {
		if g.Approved[a] {
			out[a] = true
		}
	}
	return out
}

func (g *StaticGate) Reserve(string, bool) bool { return true }

// CostAwarePolicy implements SelectionPolicy over the validated RuleV2/V3
// path. It never duplicates the algorithm: selection delegates to
// thompson.SelectCostAwareFromSamplesV3 with posterior means from the same
// atomic quality snapshot cost-blind mode draws from.
type CostAwarePolicy struct {
	Quality *thompson.Policy
	Book    *outcome.CostBookV1
	CACfg   thompson.CostAwareConfig
	Safety  SafetyGate
}

// NewCostAwarePolicy validates configuration explicitly. Nil quality/book,
// invalid objective config, or arm-set mismatch between policy and book all
// fail closed at construction, never at first request.
func NewCostAwarePolicy(quality *thompson.Policy, book *outcome.CostBookV1, cfg thompson.CostAwareConfig, safety SafetyGate) (*CostAwarePolicy, error) {
	if quality == nil {
		return nil, fmt.Errorf("gateway: cost-aware policy needs a quality policy")
	}
	if book == nil {
		return nil, fmt.Errorf("gateway: cost-aware policy needs a cost book")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	qa := quality.EligibleArmIDs()
	ba := book.Arms()
	if len(qa) != len(ba) {
		return nil, fmt.Errorf("gateway: quality arms %d != cost-book arms %d", len(qa), len(ba))
	}
	bset := make(map[string]bool, len(ba))
	for _, a := range ba {
		bset[a] = true
	}
	for _, a := range qa {
		if !bset[a] {
			return nil, fmt.Errorf("gateway: quality arm %q missing from cost book", a)
		}
	}
	return &CostAwarePolicy{Quality: quality, Book: book, CACfg: cfg, Safety: safety}, nil
}

// SelectSnapshot draws the atomic quality snapshot (same RNG stream as
// cost-blind mode), overlays validated cost-aware selection with the safety
// mask, and returns the snapshot with the cost-aware result attached.
// Budget exhaustion re-picks deterministically among remaining allowed arms;
// an empty mask fails closed with an error (the Router answers 503 and
// commits nothing).
func (c *CostAwarePolicy) SelectSnapshot(rng *rand.Rand) (thompson.DecisionSnapshot, error) {
	q, err := c.Quality.SelectSnapshot(rng)
	if err != nil {
		return thompson.DecisionSnapshot{}, err
	}
	means, known := c.costMaps()
	pulls := make(map[string]uint64, len(q.Posteriors))
	qmeans := make(map[string]float64, len(q.Posteriors))
	for arm, post := range q.Posteriors {
		pulls[arm] = post.Pulls
		qmeans[arm] = post.Mean()
	}
	var allowed map[string]bool
	if c.Safety != nil {
		allowed = c.Safety.Authorize(q.Eligible)
	}
	for {
		res, err := thompson.SelectCostAwareFromSamplesV3(q.Scores, qmeans, means, known, pulls, allowed, c.CACfg)
		if err != nil {
			return thompson.DecisionSnapshot{}, err
		}
		explore := res.Fallback || pulls[res.ArmID] < c.CACfg.ColdStartPulls
		if c.Safety != nil && !c.Safety.Reserve(res.ArmID, explore) {
			// Budget exhausted mid-flight: exclude and re-pick from the
			// same drawn scores (deterministic, no extra RNG consumed).
			if allowed == nil {
				allowed = make(map[string]bool, len(q.Eligible))
				for _, a := range q.Eligible {
					allowed[a] = true
				}
			}
			delete(allowed, res.ArmID)
			continue
		}
		res.PolicyID = thompson.CostAwarePolicyID
		res.Objective = thompson.CostAwareObjectiveVer
		q.Selected = res.ArmID
		q.CostAware = &res
		q.ConfigHash = thompson.CombinedConfigHash(q.Config, c.CACfg)
		return q, nil
	}
}

func (c *CostAwarePolicy) costMaps() (map[string]float64, map[string]bool) {
	means := map[string]float64{}
	known := map[string]bool{}
	for _, arm := range c.Book.Arms() {
		st, ok := c.Book.Stats(arm)
		if !ok {
			continue
		}
		if st.MeteredN >= c.CACfg.MinMeteredN {
			if m, ok := st.Mean(); ok {
				means[arm] = m
				known[arm] = true
			}
		}
	}
	return means, known
}

// LoggingPolicyID is the experimental identity: downstream OPE gates refuse
// it as a Thompson denominator automatically (never silently evaluable).
func (c *CostAwarePolicy) LoggingPolicyID() string { return thompson.CostAwarePolicyID }

// QualityPolicy exposes the inner concrete policy for learner construction
// and recovery, which must bind to quality state, never to this wrapper.
func (c *CostAwarePolicy) QualityPolicy() *thompson.Policy { return c.Quality }

func (c *CostAwarePolicy) ConfigSnapshot() thompson.Config { return c.Quality.ConfigSnapshot() }
func (c *CostAwarePolicy) ConfigHash() string {
	return thompson.CombinedConfigHash(c.Quality.ConfigSnapshot(), c.CACfg)
}
func (c *CostAwarePolicy) SamplerName() string { return c.Quality.SamplerName() }
func (c *CostAwarePolicy) Record(rng *rand.Rand, id string, reward float64) error {
	return c.Quality.Record(rng, id, reward)
}
func (c *CostAwarePolicy) PosteriorFor(id string) (thompson.Posterior, bool) {
	return c.Quality.PosteriorFor(id)
}
func (c *CostAwarePolicy) TotalPulls() uint64 { return c.Quality.TotalPulls() }

// SettlementObserver receives every durably settled event for monitoring and
// safety state. Implementations must be safe for concurrent use (called under
// the router settlement mutex) and must fail loudly: a returned error fails
// the settlement so monitoring blindness can never hide unsafety.
type SettlementObserver interface {
	ObserveSettlement(ev outcome.OutcomeEvent) error
}
