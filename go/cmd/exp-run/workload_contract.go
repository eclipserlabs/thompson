// Real-workload integration contract (REAL_WORKLOAD_CONTRACT_V1).
//
// This file defines the minimum general contract for integrating a
// customer's existing AI workflow with Thompson without changing the
// learning algorithm. It reuses the existing outcome vocabulary
// (outcome.Attempt, outcome.JobStatus) so historical records and live
// settlement speak the same language.
//
// The customer retains control over execution, models, fallback,
// verification, thresholds, cost measurement and retention. Thompson
// receives stable identifiers, classifications, eligibility, attempts,
// verified outcomes, observed costs, and corrections with provenance.
//
// No customer-specific integration is implemented here: without an actual
// customer specification only this general contract and the feasibility
// assessment (feasibility.go) exist.
package main

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// WorkloadContractVersion is the version of REAL_WORKLOAD_CONTRACT_V1
// implemented by this package.
const WorkloadContractVersion = "real-workload-contract-v1"

// CustomerRecord is one versioned historical observation of a customer's job.
// A job may contribute several rows (corrections / late verification) chained
// by Version/Supersedes, mirroring outcome.OutcomeEvent versioning. DecisionID
// is optional: historical exports rarely carry gateway decision IDs (the live
// gateway assigns them); everything else needed for feasibility must be
// present or explicitly missing (nil costs, empty verifier).
type CustomerRecord struct {
	JobID      string   `json:"job_id"`
	Strata     string   `json:"strata,omitempty"`
	Eligible   []string `json:"eligible_strategies,omitempty"`
	Executed   string   `json:"executed_strategy,omitempty"`
	AssignProb *float64 `json:"assignment_probability,omitempty"`
	Assigner   string   `json:"assignment_provenance,omitempty"`

	Version    uint64 `json:"version"`
	Supersedes uint64 `json:"supersedes"`

	Status            outcome.JobStatus `json:"status"`
	Attempts          []outcome.Attempt `json:"attempts"`
	DecidingAttemptID string            `json:"deciding_attempt_id,omitempty"`

	HumanReviewCostUSD *float64 `json:"human_review_cost_usd,omitempty"`
	VerifiedBy         string   `json:"verified_by,omitempty"`
	VerifiedAt         string   `json:"verified_at,omitempty"`
	OccurredAt         string   `json:"occurred_at,omitempty"`
	Provenance         string   `json:"provenance,omitempty"`
}

// ValidateRecord rejects structurally unusable rows. Missing measurements
// (nil costs, empty strata/eligibility/verifier) are NOT validation errors:
// they flow into the feasibility report as explicitly missing data. Rejected
// here means the row cannot be interpreted at all.
func ValidateRecord(r CustomerRecord) error {
	if strings.TrimSpace(r.JobID) == "" {
		return fmt.Errorf("job_id required")
	}
	if r.Version < 1 {
		return fmt.Errorf("job %q: version must be >= 1, got %d", r.JobID, r.Version)
	}
	if r.Supersedes != r.Version-1 {
		return fmt.Errorf("job %q: version %d must supersede %d, got %d",
			r.JobID, r.Version, r.Version-1, r.Supersedes)
	}
	switch r.Status {
	case outcome.StatusPending, outcome.StatusUnknown,
		outcome.StatusAccepted, outcome.StatusRejected:
	default:
		return fmt.Errorf("job %q: unknown status %q", r.JobID, r.Status)
	}
	seen := make(map[string]bool, len(r.Attempts))
	for i, a := range r.Attempts {
		if a.AttemptID == "" {
			return fmt.Errorf("job %q: attempt %d missing attempt_id", r.JobID, i)
		}
		if seen[a.AttemptID] {
			return fmt.Errorf("job %q: duplicate attempt_id %q", r.JobID, a.AttemptID)
		}
		seen[a.AttemptID] = true
		if a.Seq != uint(i) {
			return fmt.Errorf("job %q: attempt %q has seq %d, want %d",
				r.JobID, a.AttemptID, a.Seq, i)
		}
		switch a.Transport {
		case outcome.TransportOK, outcome.TransportTimeout, outcome.TransportError,
			outcome.TransportBodyTooLarge, outcome.TransportCancelled:
		default:
			return fmt.Errorf("job %q: attempt %q has unknown transport %q",
				r.JobID, a.AttemptID, a.Transport)
		}
		switch a.Validation {
		case outcome.ValidationNotRun, outcome.ValidationPass, outcome.ValidationFail:
		default:
			return fmt.Errorf("job %q: attempt %q has unknown validation %q",
				r.JobID, a.AttemptID, a.Validation)
		}
		switch a.Verified {
		case outcome.VerifiedSuccess, outcome.VerifiedFailure, outcome.VerifiedUnknown:
		default:
			return fmt.Errorf("job %q: attempt %q has unknown verified outcome %q",
				r.JobID, a.AttemptID, a.Verified)
		}
		if math.IsNaN(a.LatencyMs) {
			return fmt.Errorf("job %q: attempt %q has NaN latency", r.JobID, a.AttemptID)
		}
		if a.CostUSD != nil && (*a.CostUSD < 0 || math.IsNaN(*a.CostUSD)) {
			return fmt.Errorf("job %q: attempt %q has invalid cost", r.JobID, a.AttemptID)
		}
	}
	if r.Status == outcome.StatusAccepted || r.Status == outcome.StatusRejected {
		if r.DecidingAttemptID == "" {
			return fmt.Errorf("job %q: %s requires deciding_attempt_id", r.JobID, r.Status)
		}
		if !seen[r.DecidingAttemptID] {
			return fmt.Errorf("job %q: deciding attempt %q not in attempts",
				r.JobID, r.DecidingAttemptID)
		}
		if strings.TrimSpace(r.VerifiedBy) == "" {
			return fmt.Errorf("job %q: %s requires verified_by provenance", r.JobID, r.Status)
		}
	}
	if r.HumanReviewCostUSD != nil && *r.HumanReviewCostUSD < 0 {
		return fmt.Errorf("job %q: invalid human review cost", r.JobID)
	}
	if r.AssignProb != nil && (*r.AssignProb < 0 || *r.AssignProb > 1) {
		return fmt.Errorf("job %q: assignment_probability out of range", r.JobID)
	}
	return nil
}

// FullyLoadedCost sums attempt costs plus human-review cost. The second
// return counts unmetered components; callers must not zero-fill them.
// A nil-cost attempt keeps the job out of fully-metered analysis.
func FullyLoadedCost(r CustomerRecord) (metered float64, unmetered int) {
	humanAttempt := false
	for _, a := range r.Attempts {
		if a.CostUSD == nil {
			unmetered++
		} else {
			metered += *a.CostUSD
		}
		if a.ExecutorID == "human-pool" {
			humanAttempt = true
		}
	}
	if r.HumanReviewCostUSD == nil {
		if humanAttempt {
			unmetered++
		}
	} else {
		metered += *r.HumanReviewCostUSD
	}
	return metered, unmetered
}

// HasFallback reports whether the tape shows more than one executor (retry
// across models/providers) or a human-pool step.
func HasFallback(r CustomerRecord) bool {
	executors := map[string]bool{}
	for _, a := range r.Attempts {
		executors[a.ExecutorID] = true
		if a.ExecutorID == "human-pool" {
			return true
		}
	}
	return len(executors) > 1
}

// SelfVerified reports whether the verifier provenance equals the deciding
// attempt's executor: verification that is not independent.
func SelfVerified(r CustomerRecord) bool {
	if r.VerifiedBy == "" || r.DecidingAttemptID == "" {
		return false
	}
	for _, a := range r.Attempts {
		if a.AttemptID == r.DecidingAttemptID && a.ExecutorID != "" {
			return r.VerifiedBy == a.ExecutorID
		}
	}
	return false
}

// StrataMix tallies task classifications over accepted records; empty strata
// counts as "unclassified" rather than an error.
func StrataMix(recs []CustomerRecord) map[string]int {
	out := map[string]int{}
	for _, r := range recs {
		s := r.Strata
		if strings.TrimSpace(s) == "" {
			s = "unclassified"
		}
		out[s]++
	}
	return out
}

// SortedKeys renders a tally deterministically for reports.
func SortedKeys(tally map[string]int) []string {
	keys := make([]string, 0, len(tally))
	for k := range tally {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
