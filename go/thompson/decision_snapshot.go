package thompson

import (
	"math/rand/v2"
)

// DecisionSnapshot is a coherent, immutable capture of one selection and the
// exact policy state it was drawn from.
//
// All fields are populated under a single policy lock by SelectSnapshot, so
// selection, sampled scores, per-arm posteriors, total pulls and config can
// never observe different policy versions under concurrency. Callers must
// treat the snapshot as read-only evidence: maps are freshly allocated per
// call and posteriors are copies, never internal pointers.
type DecisionSnapshot struct {
	// Eligible is the sorted arm IDs the selection ran over.
	Eligible []string
	// Selected is the chosen arm; always an element of Eligible.
	Selected string
	// Scores maps every eligible arm to the value that produced the decision:
	// true Beta samples (Thompson), sample+bonus composites (UCBRegularized),
	// or posterior means when PhasedSelection forced the choice. Observability
	// only — never a propensity source.
	Scores map[string]float64
	// Posteriors maps every eligible arm to its posterior copy at selection
	// time. Posteriors[Selected] is the posterior_before of this decision.
	Posteriors map[string]Posterior
	// TotalPulls is the observation count at selection time.
	TotalPulls uint64
	// Config is the policy config in force at selection time.
	Config Config
	// ConfigHash is hashConfig(Config): the evidence identifier for Config.
	ConfigHash string
}

// SelectSnapshot atomically selects an arm and captures the full policy state
// the decision was drawn from. It is SelectWithScores plus coherence: eligible
// set, scores, posteriors, pulls and config come from one locked instant, so a
// concurrent Record/AddArm/RemoveArm cannot tear the evidence apart.
//
// The selection algorithm is unchanged: this delegates to the same locked
// argmax helpers as SelectWithScores for an identical RNG stream.
func (p *Policy) SelectSnapshot(rng *rand.Rand) (DecisionSnapshot, error) {
	p.mu.Lock()
	if len(p.arms) == 0 {
		p.mu.Unlock()
		return DecisionSnapshot{}, ErrNoArms
	}
	var snap DecisionSnapshot
	snap.Eligible = make([]string, len(p.order))
	copy(snap.Eligible, p.order)
	snap.Scores = make(map[string]float64, len(p.order))
	switch p.config.Selection.Kind {
	case UCBRegularized:
		snap.Selected, snap.Scores = p.argmaxUCBWithScoresLocked(rng, snap.Scores)
	case PhasedSelection:
		quota := p.config.Selection.Bootstrap
		if p.config.Selection.MinPullsForExploit > quota {
			quota = p.config.Selection.MinPullsForExploit
		}
		if id, ok := p.leastPulledBelowLocked(quota); ok {
			snap.Selected = id
			for _, pid := range p.order {
				snap.Scores[pid] = p.arms[pid].Posterior.Mean()
			}
		} else {
			snap.Selected, snap.Scores = p.argmaxSampledWithScoresLocked(rng, snap.Scores)
		}
	default:
		snap.Selected, snap.Scores = p.argmaxSampledWithScoresLocked(rng, snap.Scores)
	}
	snap.Posteriors = make(map[string]Posterior, len(p.order))
	for _, id := range p.order {
		snap.Posteriors[id] = p.arms[id].Posterior
	}
	snap.TotalPulls = p.totalPulls
	snap.Config = p.config
	if p.observer != nil {
		// Same single notification point as SelectWithScores.
		p.observer.OnSelect(snap.Selected, snap.Scores)
	}
	p.mu.Unlock()

	snap.ConfigHash = hashConfig(snap.Config)
	return snap, nil
}
