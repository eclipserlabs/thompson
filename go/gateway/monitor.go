package gateway

import (
	"sort"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// ArmHealth is the honestly-labeled monitoring readout for one arm. Rates
// are OBSERVED fractions over settled, learnable outcomes in the window —
// never guarantees of true rates. Censored (UNKNOWN/PENDING) jobs are
// counted separately and NEVER enter the accept-rate denominator, so thin
// verification cannot inflate apparent quality. AvgDelayH reports feedback
// latency so operators distinguish health from stale data.
type ArmHealth struct {
	Arm        string  `json:"arm"`
	Window     int     `json:"window"`
	Matured    int     `json:"matured"`
	Accepted   int     `json:"accepted"`
	AcceptRate float64 `json:"accept_rate"`
	Censored   int     `json:"censored"`
	Unmetered  int     `json:"unmetered"`
	// MeteredJobs counts fully metered matured jobs; PartialJobs counts
	// matured jobs with SOME metered attempt cost but incomplete totals.
	// Both are accounting visibility: partial costs are preserved for
	// sensitivity analysis, never completed by invention.
	MeteredJobs int `json:"metered_jobs"`
	PartialJobs int `json:"partial_jobs"`
	// HumanFixed counts accepted jobs the arm ATTEMPTED but did not decide:
	// a human (armless) attempt verified success instead. Such jobs move no
	// arm posterior (inherited outcome semantics) and enter no cost mean, so
	// without this counter an arm with total model failure but routine human
	// rescue would look merely quiet. HumanCostSum is the recorded review
	// spend on those jobs. Visibility only: no automatic suspension fires on
	// these (operator judgment + exploration-budget dynamics handle the trap;
	// see the human-trap fixture).
	HumanFixed   int     `json:"human_fixed"`
	HumanCostSum float64 `json:"human_cost_sum"`
	AvgDelayH    float64 `json:"avg_delay_hours"`
	HasEstimate  bool    `json:"has_estimate"`
}

// armHealth folds the authoritative outcome events (the same source the
// learner rebuilds from): latest version per job, newest-first by Seq, last
// Window learnable-or-censored outcomes attributed to the deciding arm.
//
// Implementation: single latest-wins pass storing slice indices (no event
// copies, no full sort), then a Seq-ordered pass over the attributed subset
// only. Output is identical to a full newest-first fold for inputs in any
// order; cost is linear in history with a small constant (see
// monitor_scaling_test.go).
func armHealth(evs []outcome.OutcomeEvent, arm string, window int) ArmHealth {
	h := ArmHealth{Arm: arm, Window: window}
	// Pass 1: latest version per job (max version, Seq breaks ties).
	latest := make(map[string]int, len(evs))
	for i, ev := range evs {
		if j, ok := latest[ev.JobID]; ok {
			cur := evs[j]
			if cur.Version > ev.Version || (cur.Version == ev.Version && cur.Seq >= ev.Seq) {
				continue
			}
		}
		latest[ev.JobID] = i
	}
	// Pass 2: newest-first scan over indices (no event copies). The window
	// break below also bounds HumanFixed crediting to jobs newer than the
	// cutoff — preserved exactly from the full-sort fold.
	byNewest := make([]int, 0, len(latest))
	for _, i := range latest {
		byNewest = append(byNewest, i)
	}
	sort.Slice(byNewest, func(a, b int) bool { return evs[byNewest[a]].Seq > evs[byNewest[b]].Seq })
	var windowed []outcome.OutcomeEvent
	for _, i := range byNewest {
		ev := evs[i]
		dec := ""
		for _, a := range ev.Attempts {
			if a.AttemptID == ev.DecidingAttemptID {
				dec = a.ArmID
				break
			}
		}
		if dec == "" && ev.Status == outcome.StatusAccepted {
			// Human-fixed job: credit correction burden to every model arm
			// on the tape (visibility; moves no estimator). The per-arm
			// window below still only admits decided jobs.
			for _, a := range ev.Attempts {
				if a.ArmID == arm {
					h.HumanFixed++
					if ev.HumanReviewCostUSD != nil {
						h.HumanCostSum += *ev.HumanReviewCostUSD
					}
					break
				}
			}
		}
		if dec != arm {
			continue
		}
		windowed = append(windowed, ev)
		if len(windowed) >= window {
			break
		}
	}
	var delaySum float64
	delayN := 0
	for _, ev := range windowed {
		switch ev.Status {
		case outcome.StatusAccepted:
			h.Matured++
			h.Accepted++
			countMetered(ev, &h)
		case outcome.StatusRejected:
			h.Matured++
			countMetered(ev, &h)
		default:
			h.Censored++
		}
		if d := hoursBetween(ev.OccurredAt, ev.VerifiedAt); d >= 0 {
			delaySum += d
			delayN++
		}
	}
	if h.Matured > 0 {
		h.AcceptRate = float64(h.Accepted) / float64(h.Matured)
		h.HasEstimate = true
	}
	if delayN > 0 {
		h.AvgDelayH = delaySum / float64(delayN)
	}
	return h
}

func countMetered(ev outcome.OutcomeEvent, h *ArmHealth) {
	// Single counting rule shared with the learner and the evaluator
	// (outcome.FullyLoadedCost): no third implementation to diverge.
	// Malformed costs (rejected at settlement, but foldable on replay)
	// count as incomplete, never as observations.
	metered, unmetered, err := outcome.FullyLoadedCost(ev)
	if err != nil || unmetered > 0 {
		h.Unmetered++
		if err == nil && metered > 0 {
			h.PartialJobs++
		}
		return
	}
	h.MeteredJobs++
}

// missingShare is the fraction of matured jobs with incomplete costs.
// Denominator is matured (learnable) jobs; censored jobs are excluded from
// BOTH sides, never silently discarded from a larger denominator.
func missingShare(h ArmHealth) float64 {
	if h.Matured == 0 {
		return 0
	}
	return float64(h.Unmetered) / float64(h.Matured)
}
