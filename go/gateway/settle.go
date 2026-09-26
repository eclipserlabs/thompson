package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// settleRequest is the wire body for POST /v1/outcomes: one versioned job
// outcome. It is outcome.OutcomeEvent on the wire; the type alias documents
// the endpoint contract.
type settleRequest = outcome.OutcomeEvent

type settleResponse struct {
	DecisionID string `json:"decision_id"`
	JobID      string `json:"job_id"`
	Version    uint64 `json:"outcome_version"`
	Status     string `json:"status"`
	Applied    bool   `json:"applied"`
	Learned    bool   `json:"learned"`
	Duplicate  bool   `json:"duplicate,omitempty"`
}

// initVerifiedSettlement wires PR 2's outcome store + learner into a verified
// router, failing closed on any ambiguous or unsafe configuration:
//
//   - legacy mode with any settlement wiring is rejected (no dual learning);
//   - verified mode requires an explicit outcome store, an auth hook, and
//     durable (non-memory) decision + outcome stores — otherwise construction
//     fails instead of silently running in-memory or legacy learning.
func (rt *Router) initVerifiedSettlement(cfg RouterConfig) error {
	if cfg.Mode != VerifiedMode {
		if cfg.Outcomes != nil || cfg.Learner != nil || cfg.SettleAuth != nil || cfg.Mapper != nil {
			return fmt.Errorf("router: settlement wiring is only valid in verified mode (legacy mode forbids dual learning)")
		}
		return nil
	}
	if cfg.Outcomes == nil {
		return fmt.Errorf("router: verified mode requires an explicit outcome store (fails closed, never in-memory-by-default)")
	}
	if cfg.SettleAuth == nil {
		return fmt.Errorf("router: verified mode requires a settlement auth hook (no unauthenticated outcome writes)")
	}
	if _, ok := cfg.Decisions.(*MemoryDecisionStore); cfg.Decisions == nil || ok {
		return fmt.Errorf("router: verified mode requires an explicit durable decision store")
	}
	if _, ok := cfg.Outcomes.(*outcome.MemoryOutcomeStore); ok {
		return fmt.Errorf("router: verified mode requires a durable outcome store")
	}
	// Verified decisions must reconstruct exact-Thompson propensities from
	// their persisted eligible_arm_state. Any other selection/sampler has no
	// valid denominator, so enabling it would silently produce unevaluable
	// evidence: refuse at construction instead.
	if kind := cfg.Policy.ConfigSnapshot().Selection.Kind; kind != thompson.ThompsonSelection {
		return fmt.Errorf("router: verified mode requires Thompson selection, got kind %d", kind)
	}
	if name := cfg.Policy.SamplerName(); name != "exact" {
		return fmt.Errorf("router: verified mode requires the exact sampler, got %q", name)
	}
	mapper := cfg.Mapper
	if mapper == nil {
		mapper = outcome.BinaryStatusMapper{}
	}
	learner := cfg.Learner
	if learner == nil {
		learner = outcome.NewLearner(cfg.Policy, mapper, cfg.Outcomes.Events)
	}
	rt.outcomes = cfg.Outcomes
	rt.learner = learner
	rt.mapper = mapper
	rt.settleAuth = cfg.SettleAuth
	return nil
}

// SettleHandler serves POST /v1/outcomes: externally verified, versioned job
// settlement. It acknowledges (200) only after the outcome event is durable.
// Unknown decisions, job mismatches, and conflicting versions are rejected
// without changing policy state.
func (rt *Router) SettleHandler(w http.ResponseWriter, r *http.Request) {
	if rt.mode != VerifiedMode || rt.learner == nil {
		http.Error(w, "verified settlement not enabled", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if rt.settleAuth != nil && !rt.settleAuth(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var ev outcome.OutcomeEvent
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		http.Error(w, fmt.Sprintf("malformed outcome: %v", err), http.StatusBadRequest)
		return
	}
	if err := ev.Validate(); err != nil {
		http.Error(w, fmt.Sprintf("invalid outcome: %v", err), http.StatusBadRequest)
		return
	}
	dec, ok := rt.decisions.Lookup(ev.DecisionID)
	if !ok {
		http.Error(w, "unknown decision_id: no committed decision", http.StatusNotFound)
		return
	}
	if ev.JobID != dec.JobID {
		http.Error(w, fmt.Sprintf("job mismatch: event %q vs committed %q", ev.JobID, dec.JobID), http.StatusConflict)
		return
	}
	if ev.StrategyID != "" && dec.StrategyID != "" && ev.StrategyID != dec.StrategyID {
		http.Error(w, fmt.Sprintf("strategy mismatch: event %q vs committed %q", ev.StrategyID, dec.StrategyID), http.StatusConflict)
		return
	}
	if err := checkAttribution(dec, ev); err != nil {
		// Forged or inconsistent arm attribution: the ledger and the policy
		// are untouched (rejected before Submit).
		http.Error(w, fmt.Sprintf("attribution rejected: %v", err), http.StatusConflict)
		return
	}
	// The already-latest check runs inside the settlement critical section
	// so the applied/duplicate flags describe the operation that actually
	// occurred: a redelivery of the latest version succeeds idempotently
	// when its content matches and fails as a conflict otherwise.
	rt.settleMu.Lock()
	alreadyLatest := isAlreadyLatest(rt.outcomes, ev)
	learned, err := outcome.Settle(rt.outcomes, rt.learner, ev)
	rt.settleMu.Unlock()
	if err != nil {
		// Store-level conflict/stale/gap or learner error: accepted history
		// is untouched (Submit/Apply commit nothing on error).
		http.Error(w, fmt.Sprintf("settlement rejected: %v", err), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(settleResponse{
		DecisionID: ev.DecisionID,
		JobID:      ev.JobID,
		Version:    ev.Version,
		Status:     string(ev.Status),
		Applied:    !alreadyLatest,
		Learned:    learned,
		Duplicate:  alreadyLatest,
	})
}

// checkAttribution verifies that a settled outcome is attributable to the
// execution path authorized by its committed decision:
//
//   - a single-attempt outcome must name the selected arm: anything else is
//     forged attribution (decision A settling arm B);
//   - multi-attempt outcomes must name only arms from the decision's eligible
//     set. This is necessary but explicitly not sufficient proof of
//     execution: per-attempt execution records do not exist in the current
//     evidence, so fallback arms are sanity-checked, not proven. An empty
//     ArmID (non-arm executor, e.g. human review) is always allowed and
//     learns nothing at the arm level.
//
// Rejection happens before Submit: neither ledger nor policy is touched.
func checkAttribution(dec CommittedDecision, ev outcome.OutcomeEvent) error {
	eligible := make(map[string]bool, len(dec.EligibleArmIDs))
	for _, id := range dec.EligibleArmIDs {
		eligible[id] = true
	}
	if len(ev.Attempts) == 1 {
		only := ev.Attempts[0]
		if only.ArmID != "" && only.ArmID != dec.SelectedArmID {
			return fmt.Errorf("single-attempt outcome names arm %q but decision selected %q", only.ArmID, dec.SelectedArmID)
		}
		return nil
	}
	for _, a := range ev.Attempts {
		if a.ArmID == "" {
			continue
		}
		if !eligible[a.ArmID] {
			return fmt.Errorf("attempt %q names arm %q outside the decision eligible set", a.AttemptID, a.ArmID)
		}
	}
	return nil
}

// isAlreadyLatest reports whether ev's version is already the latest
// committed version for its job.
func isAlreadyLatest(store outcome.OutcomeStore, ev outcome.OutcomeEvent) bool {
	latest, ok := store.Latest(ev.JobID)
	return ok && latest.Version == ev.Version
}

// RecoverVerifiedLearning resumes verified learning after a restart: restore
// the checkpointed policy + cursor, then replay every outcome committed past
// the checkpoint's high-watermark exactly once. Call before serving traffic.
func (rt *Router) RecoverVerifiedLearning(ckptPath string) error {
	if rt.mode != VerifiedMode || rt.learner == nil {
		return fmt.Errorf("router: recovery is only valid in verified mode with settlement wired")
	}
	rt.settleMu.Lock()
	defer rt.settleMu.Unlock()
	cp, err := outcome.LoadCheckpoint(ckptPath)
	if err != nil {
		return err
	}
	restored, err := outcome.Resume(rt.policy, rt.outcomes.Events(), cp, rt.mapper, rt.outcomes.Events)
	if err != nil {
		return err
	}
	rt.learner = restored
	return nil
}

// CheckpointVerifiedLearning persists the current learning checkpoint.
func (rt *Router) CheckpointVerifiedLearning(ckptPath string) error {
	if rt.mode != VerifiedMode || rt.learner == nil {
		return fmt.Errorf("router: checkpointing is only valid in verified mode with settlement wired")
	}
	rt.settleMu.Lock()
	defer rt.settleMu.Unlock()
	return outcome.SaveCheckpoint(ckptPath, rt.learner.CheckpointOf(rt.outcomes.Len()))
}
