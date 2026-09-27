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
	Arm         string  `json:"arm"`
	Window      int     `json:"window"`
	Matured     int     `json:"matured"`
	Accepted    int     `json:"accepted"`
	AcceptRate  float64 `json:"accept_rate"`
	Censored    int     `json:"censored"`
	Unmetered   int     `json:"unmetered"`
	AvgDelayH   float64 `json:"avg_delay_hours"`
	HasEstimate bool    `json:"has_estimate"`
}

// armHealth folds the authoritative outcome events (the same source the
// learner rebuilds from): latest version per job, newest-first by Seq, last
// Window learnable-or-censored outcomes attributed to the deciding arm.
func armHealth(evs []outcome.OutcomeEvent, arm string, window int) ArmHealth {
	h := ArmHealth{Arm: arm, Window: window}
	latest := map[string]outcome.OutcomeEvent{}
	var order []outcome.OutcomeEvent
	seenJob := map[string]bool{}
	// Newest first: events carry Seq ledger order.
	cp := make([]outcome.OutcomeEvent, len(evs))
	copy(cp, evs)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Seq > cp[j].Seq })
	for _, ev := range cp {
		if prev, ok := latest[ev.JobID]; ok && prev.Version >= ev.Version {
			continue
		}
		latest[ev.JobID] = ev
	}
	for _, ev := range cp {
		if latest[ev.JobID].Version != ev.Version || latest[ev.JobID].Seq != ev.Seq {
			continue
		}
		if seenJob[ev.JobID] {
			continue
		}
		seenJob[ev.JobID] = true
		dec := ""
		for _, a := range ev.Attempts {
			if a.AttemptID == ev.DecidingAttemptID {
				dec = a.ArmID
				break
			}
		}
		if dec != arm {
			continue
		}
		order = append(order, ev)
		if len(order) >= window {
			break
		}
	}
	var delaySum float64
	delayN := 0
	for _, ev := range order {
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
	for _, a := range ev.Attempts {
		if a.CostUSD == nil {
			h.Unmetered++
			return
		}
	}
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
