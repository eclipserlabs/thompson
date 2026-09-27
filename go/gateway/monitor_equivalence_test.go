package gateway

import (
	"math/rand/v2"
	"sort"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// referenceArmHealth is the pre-optimization fold (full copy + newest-first
// sort), kept as the equivalence oracle for the index-based rewrite.
func referenceArmHealth(evs []outcome.OutcomeEvent, arm string, window int) ArmHealth {
	h := ArmHealth{Arm: arm, Window: window}
	latest := map[string]outcome.OutcomeEvent{}
	var order []outcome.OutcomeEvent
	seenJob := map[string]bool{}
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
		if dec == "" && ev.Status == outcome.StatusAccepted {
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

// Differential proof: optimized fold equals the reference on randomized
// ledgers with corrections, duplicates, unknowns, human legs, missing
// costs, and shuffled input order.
func TestArmHealthEquivalence(t *testing.T) {
	rng := rand.New(rand.NewPCG(1234, 5678))
	var evs []outcome.OutcomeEvent
	seq := uint64(0)
	emit := func(job string, ver uint64, st outcome.JobStatus, arm string, cost *float64, human bool) {
		dec := job + "-a0"
		atts := []outcome.Attempt{{
			AttemptID: dec, Seq: 0, ExecutorID: arm, ArmID: arm,
			Transport: outcome.TransportOK, LatencyMs: 10, CostUSD: cost,
			Validation: outcome.ValidationPass, Verified: outcome.VerifiedFailure, VerifiedBy: "m",
		}}
		if st == outcome.StatusAccepted && !human {
			atts[0].Verified = outcome.VerifiedSuccess
		}
		var hc *float64
		if human {
			hc = cost
			atts = append(atts, outcome.Attempt{AttemptID: job + "-ah", Seq: 1, ExecutorID: "human-pool",
				Transport: outcome.TransportOK, LatencyMs: 600000,
				Validation: outcome.ValidationPass, Verified: outcome.VerifiedSuccess, VerifiedBy: "human:pool"})
			dec = job + "-ah"
		}
		evs = append(evs, outcome.OutcomeEvent{
			SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
			DecisionID: "dec-" + job, JobID: job, Version: ver, Supersedes: ver - 1,
			Status: st, Attempts: atts, DecidingAttemptID: dec,
			HumanReviewCostUSD: hc, VerifiedBy: "m",
			OccurredAt: "2026-01-05T00:00:00Z", VerifiedAt: "2026-01-05T01:00:00Z", Seq: seq,
		})
		seq++
	}
	f := func(v float64) *float64 { c := v; return &c }
	for j := 0; j < 60; j++ {
		job := "job-" + itoaEq(j)
		arm := "cheap"
		if j%3 == 0 {
			arm = "strong"
		}
		switch j % 7 {
		case 0:
			emit(job, 1, outcome.StatusAccepted, arm, f(0.02), false)
			emit(job, 2, outcome.StatusRejected, arm, f(0.02), false) // correction
			evs = append(evs, evs[len(evs)-2])                        // duplicate redelivery
		case 1:
			emit(job, 1, outcome.StatusUnknown, arm, nil, false)
		case 2:
			emit(job, 1, outcome.StatusAccepted, arm, nil, false) // missing cost
		case 3:
			emit(job, 1, outcome.StatusAccepted, arm, f(0.01), true) // human-fixed
		default:
			st := outcome.StatusAccepted
			if rng.Float64() < 0.3 {
				st = outcome.StatusRejected
			}
			emit(job, 1, st, arm, f(0.02), false)
		}
		_ = rng
	}
	// Shuffled input order must not matter.
	shuffled := make([]outcome.OutcomeEvent, len(evs))
	perm := rng.Perm(len(evs))
	for i, p := range perm {
		shuffled[i] = evs[p]
	}
	for _, input := range [][]outcome.OutcomeEvent{evs, shuffled} {
		for _, arm := range []string{"cheap", "strong"} {
			for _, w := range []int{1, 5, 20} {
				got := armHealth(input, arm, w)
				want := referenceArmHealth(input, arm, w)
				if got != want {
					t.Fatalf("arm=%s window=%d:\n got %+v\nwant %+v", arm, w, got, want)
				}
			}
		}
	}
}

func itoaEq(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}
