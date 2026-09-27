package journal

import (
	"fmt"
	"math"
)

// Outcome statuses carried by the journal (same vocabulary as the outcome
// contract; the assay reuses the words, not the package, to stay standalone).
const (
	StatusPending  = "PENDING"
	StatusUnknown  = "UNKNOWN"
	StatusAccepted = "ACCEPTED"
	StatusRejected = "REJECTED"
)

func validateOutcome(o SettledOutcome) error {
	if o.DecisionID == "" || o.JobID == "" {
		return fmt.Errorf("journal: decision_id and job_id are required")
	}
	if o.Version < 1 {
		return fmt.Errorf("journal: version must be >= 1")
	}
	if o.Supersedes != o.Version-1 {
		return fmt.Errorf("journal: version %d must supersede %d", o.Version, o.Version-1)
	}
	switch o.Status {
	case StatusPending, StatusUnknown, StatusAccepted, StatusRejected:
	default:
		return fmt.Errorf("journal: unknown status %q", o.Status)
	}
	seen := map[string]bool{}
	for i, a := range o.Attempts {
		if a.AttemptID == "" {
			return fmt.Errorf("journal: attempt %d missing id", i)
		}
		if seen[a.AttemptID] {
			return fmt.Errorf("journal: duplicate attempt_id %q", a.AttemptID)
		}
		seen[a.AttemptID] = true
		switch a.Verified {
		case "success", "failure", "unknown":
		default:
			return fmt.Errorf("journal: attempt %q has unknown verdict %q", a.AttemptID, a.Verified)
		}
		// Malformed costs are refused, never stored: NaN/Inf/negative.
		if a.CostUSD != nil {
			c := *a.CostUSD
			if math.IsNaN(c) || math.IsInf(c, 0) {
				return fmt.Errorf("journal: non-finite cost on attempt %q", a.AttemptID)
			}
			if c < 0 {
				return fmt.Errorf("journal: negative cost on attempt %q", a.AttemptID)
			}
		}
	}
	if o.HumanReviewCost != nil {
		hc := *o.HumanReviewCost
		if math.IsNaN(hc) || math.IsInf(hc, 0) || hc < 0 {
			return fmt.Errorf("journal: bad human review cost")
		}
	}
	if o.Status == StatusAccepted || o.Status == StatusRejected {
		if o.DecidingAttempt == "" {
			return fmt.Errorf("journal: %s requires a deciding attempt", o.Status)
		}
		if !seen[o.DecidingAttempt] {
			return fmt.Errorf("journal: deciding attempt %q not in tape", o.DecidingAttempt)
		}
	}
	return nil
}

func checkOutcomeAttribution(dec Decision, o SettledOutcome) error {
	eligible := map[string]bool{}
	for _, id := range dec.Eligible {
		eligible[id] = true
	}
	if len(o.Attempts) == 1 {
		only := o.Attempts[0]
		if only.ArmID != "" && only.ArmID != dec.SelectedArm {
			return fmt.Errorf("journal: single-attempt arm %q != selected %q", only.ArmID, dec.SelectedArm)
		}
		return nil
	}
	for _, a := range o.Attempts {
		if a.ArmID == "" {
			continue
		}
		if !eligible[a.ArmID] {
			return fmt.Errorf("journal: attempt %q arm %q outside eligible set", a.AttemptID, a.ArmID)
		}
	}
	return nil
}
