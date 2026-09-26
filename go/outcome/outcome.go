// Package outcome implements the durable outcome lifecycle from
// docs/design/OUTCOME_CONTRACT_V1.md: versioned, append-only job outcomes with
// late corrections, idempotent delivery, and correction-safe learning.
//
// Transport status and verified task outcome are carried separately on every
// attempt and never conflated: only a settled job (ACCEPTED/REJECTED) with an
// independent verification moves the policy, and UNKNOWN/PENDING jobs are
// excluded from confident success/failure learning.
package outcome

import (
	"fmt"
	"math"
	"time"
)

// Outcome schema version for events written by this package.
const SchemaVersion = 1

// JobStatus is the derived terminal state of one job.
type JobStatus string

const (
	// StatusPending means verification is not yet complete. The job is open;
	// it must never move the policy.
	StatusPending JobStatus = "PENDING"
	// StatusUnknown means verification completed without a verdict (ambiguous
	// timeout, validator abstention, contradictory evidence) or never
	// completed. Censored: excluded from learning and from OPE numerators.
	StatusUnknown JobStatus = "UNKNOWN"
	// StatusAccepted means at least one attempt verified success with no
	// later superseding correction revoking it.
	StatusAccepted JobStatus = "ACCEPTED"
	// StatusRejected means verification completed with no verified success.
	StatusRejected JobStatus = "REJECTED"
)

// TransportStatus describes what happened on the wire for one attempt. It is
// a measurement, never a task verdict.
type TransportStatus string

const (
	TransportOK           TransportStatus = "ok"
	TransportTimeout      TransportStatus = "timeout"
	TransportError        TransportStatus = "transport_error"
	TransportBodyTooLarge TransportStatus = "body_too_large"
	TransportCancelled    TransportStatus = "cancelled"
)

// ValidationVerdict is the application validator's verdict on one attempt,
// independent of transport.
type ValidationVerdict string

const (
	ValidationNotRun ValidationVerdict = "not_run"
	ValidationPass   ValidationVerdict = "pass"
	ValidationFail   ValidationVerdict = "fail"
)

// VerifiedOutcome is the independent task verdict on one attempt.
type VerifiedOutcome string

const (
	VerifiedSuccess VerifiedOutcome = "success"
	VerifiedFailure VerifiedOutcome = "failure"
	VerifiedUnknown VerifiedOutcome = "unknown"
)

// FieldCorrection records one validator/human field-level fix. Diagnostic
// only: it never rewrites the attempt's own verdict.
type FieldCorrection struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// Attempt is one execution of a job against one executor. Attempts are
// appended and never mutated; each outcome version carries the cumulative
// history so the full chain (including fallbacks, costs and provenance) is
// preserved.
type Attempt struct {
	AttemptID string `json:"attempt_id"`
	Seq       uint   `json:"seq"`
	// ExecutorID identifies the model/provider/tool/human pool.
	ExecutorID string `json:"executor_id"`
	// ArmID is the bandit arm this attempt maps to. Empty means the attempt
	// maps to no arm (e.g. a human fallback): it contributes to
	// strategy-level accounting but never to an arm posterior.
	ArmID            string            `json:"arm_id,omitempty"`
	Transport        TransportStatus   `json:"transport"`
	LatencyMs        float64           `json:"latency_ms"`
	CostUSD          *float64          `json:"cost_usd,omitempty"`
	InputTokens      *int              `json:"input_tokens,omitempty"`
	OutputTokens     *int              `json:"output_tokens,omitempty"`
	Validation       ValidationVerdict `json:"validation"`
	FailureCategory  string            `json:"failure_category,omitempty"`
	FieldCorrections []FieldCorrection `json:"field_corrections,omitempty"`
	Verified         VerifiedOutcome   `json:"verified"`
	VerifiedBy       string            `json:"verified_by,omitempty"`
	VerifiedAt       string            `json:"verified_at,omitempty"`
}

// OutcomeEvent is one immutable, versioned job outcome. Version starts at 1
// and increases by exactly 1 per correction; each version carries the full
// attempt history to date plus the derived job status.
type OutcomeEvent struct {
	SchemaVersion int    `json:"schema_version"`
	EventType     string `json:"event_type"` // "JobSettled"
	DecisionID    string `json:"decision_id"`
	JobID         string `json:"job_id"`
	StrategyID    string `json:"strategy_id"`
	// Version is the monotonically increasing outcome version for this job.
	Version uint64 `json:"outcome_version"`
	// Supersedes is Version-1 (0 for the first version).
	Supersedes uint64    `json:"supersedes"`
	Status     JobStatus `json:"status"`
	Attempts   []Attempt `json:"attempts"`
	// DecidingAttemptID names the attempt whose verification determined the
	// status. Required for ACCEPTED; may be empty for REJECTED/UNKNOWN.
	DecidingAttemptID string `json:"deciding_attempt_id,omitempty"`
	// HumanReviewCostUSD is recorded when a human touched the job, never imputed.
	HumanReviewCostUSD *float64 `json:"human_review_cost_usd,omitempty"`
	VerifiedBy         string   `json:"verified_by,omitempty"`
	VerifiedAt         string   `json:"verified_at,omitempty"`
	// CorrectedAt is set when Supersedes > 0.
	CorrectedAt string `json:"corrected_at,omitempty"`
	OccurredAt  string `json:"occurred_at"`
	// Seq is assigned by the store on append: ledger order for replay. It is
	// not part of the event identity.
	Seq uint64 `json:"seq"`
}

// Validate checks structural invariants. It does not interpret policy.
func (e OutcomeEvent) Validate() error {
	if e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("outcome: unsupported schema version %d", e.SchemaVersion)
	}
	if e.EventType != EventJobSettled {
		return fmt.Errorf("outcome: unknown event type %q", e.EventType)
	}
	if e.DecisionID == "" || e.JobID == "" {
		return fmt.Errorf("outcome: decision_id and job_id are required")
	}
	if e.Version < 1 {
		return fmt.Errorf("outcome: version must be >= 1, got %d", e.Version)
	}
	if e.Supersedes != e.Version-1 {
		return fmt.Errorf("outcome: version %d must supersede %d, got %d", e.Version, e.Version-1, e.Supersedes)
	}
	switch e.Status {
	case StatusPending, StatusUnknown, StatusAccepted, StatusRejected:
	default:
		return fmt.Errorf("outcome: unknown status %q", e.Status)
	}
	seen := make(map[string]bool, len(e.Attempts))
	for i, a := range e.Attempts {
		if a.AttemptID == "" {
			return fmt.Errorf("outcome: attempt %d missing attempt_id", i)
		}
		if seen[a.AttemptID] {
			return fmt.Errorf("outcome: duplicate attempt_id %q", a.AttemptID)
		}
		seen[a.AttemptID] = true
		if a.Seq != uint(i) {
			return fmt.Errorf("outcome: attempt %q has seq %d, want %d", a.AttemptID, a.Seq, i)
		}
		if math.IsNaN(a.LatencyMs) {
			return fmt.Errorf("outcome: attempt %q has NaN latency", a.AttemptID)
		}
		switch a.Transport {
		case TransportOK, TransportTimeout, TransportError, TransportBodyTooLarge, TransportCancelled:
		default:
			return fmt.Errorf("outcome: attempt %q has unknown transport %q", a.AttemptID, a.Transport)
		}
		switch a.Validation {
		case ValidationNotRun, ValidationPass, ValidationFail:
		default:
			return fmt.Errorf("outcome: attempt %q has unknown validation %q", a.AttemptID, a.Validation)
		}
		switch a.Verified {
		case VerifiedSuccess, VerifiedFailure, VerifiedUnknown:
		default:
			return fmt.Errorf("outcome: attempt %q has unknown verified outcome %q", a.AttemptID, a.Verified)
		}
	}
	if e.Status == StatusAccepted || e.Status == StatusRejected {
		if e.DecidingAttemptID == "" {
			return fmt.Errorf("outcome: %s requires deciding_attempt_id", e.Status)
		}
		if !seen[e.DecidingAttemptID] {
			return fmt.Errorf("outcome: deciding attempt %q not in attempts", e.DecidingAttemptID)
		}
	}
	return nil
}

// EventJobSettled is the event type for versioned job outcomes.
const EventJobSettled = "JobSettled"

// TotalCostUSD sums attempt costs. Null-unaware inputs propagate: it returns
// the sum over metered attempts and the count of unmetered ones; callers must
// not zero-fill the unmetered remainder.
func (e OutcomeEvent) TotalCostUSD() (metered float64, unmetered int) {
	for _, a := range e.Attempts {
		if a.CostUSD == nil {
			unmetered++
			continue
		}
		metered += *a.CostUSD
	}
	return metered, unmetered
}

func nowRFC3339Nano() string { return time.Now().UTC().Format(time.RFC3339Nano) }
