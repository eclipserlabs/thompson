package harness

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"sync"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// CostAwareDecision binds one selection to the policy identity, objective
// version, and configuration hash that actually produced it. Persisted
// append-only per treatment; replay compares these records.
type CostAwareDecision struct {
	JobID      string                   `json:"job_id"`
	AttemptSeq int                      `json:"attempt_seq"`
	ArmID      string                   `json:"arm_id"`
	PolicyID   string                   `json:"policy_id"`
	Objective  string                   `json:"objective_version"`
	ConfigHash string                   `json:"config_hash"`
	Fallback   bool                     `json:"fallback"`
	Reason     string                   `json:"reason"`
	Result     thompson.CostAwareResult `json:"result"`
}

// CostAwareStrategy selects arms via quality-constrained cost-aware selection.
// Learning happens once at settlement through the treatment's quality learner
// plus cost book, never inside Execute. Single-attempt-dominant v1: each
// Execute performs up to MaxRetries+1 cost-aware selections (one per attempt)
// and records every decision with its true identity.
type CostAwareStrategy struct {
	StrategyID string
	Policy     *thompson.Policy
	Book       *outcome.CostBookV1
	Cfg        thompson.CostAwareConfig
	ConfigHash string
	MaxRetries int
	Verifier   string

	mu        sync.Mutex
	Decisions []CostAwareDecision
	Fallbacks int
}

// ID returns the strategy identity: the cost-aware policy ID, never the
// cost-blind one.
func (s *CostAwareStrategy) ID() string {
	if s.StrategyID != "" {
		return s.StrategyID
	}
	return thompson.CostAwarePolicyID
}

func (s *CostAwareStrategy) record(d CostAwareDecision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Decisions = append(s.Decisions, d)
	if d.Fallback {
		s.Fallbacks++
	}
}

// Execute runs one job against its truth with per-attempt cost-aware selection.
func (s *CostAwareStrategy) Execute(rng *rand.Rand, job JobTruth) []outcome.Attempt {
	var attempts []outcome.Attempt
	for i := 0; i <= s.MaxRetries; i++ {
		means, known := s.costMaps()
		res, err := thompson.SelectCostAware(rng, s.Policy, means, known, s.Cfg)
		if err != nil {
			break
		}
		arm := res.ArmID
		s.record(CostAwareDecision{
			JobID: job.JobID, AttemptSeq: i, ArmID: arm,
			PolicyID: res.PolicyID, Objective: res.Objective,
			ConfigHash: s.ConfigHash, Fallback: res.Fallback,
			Reason: res.Reason, Result: res,
		})
		p := job.ArmSuccess[arm]
		ok := rng.Float64() < p
		verified := outcome.VerifiedFailure
		if ok {
			verified = outcome.VerifiedSuccess
		}
		cost := job.ArmCost[arm]
		attempts = append(attempts, outcome.Attempt{
			AttemptID: fmt.Sprintf("%s-a%d", job.JobID, i), Seq: uint(i),
			ExecutorID: arm, ArmID: arm,
			Transport: outcome.TransportOK, LatencyMs: job.ArmLatency[arm],
			CostUSD: &cost, Validation: outcome.ValidationPass, Verified: verified,
			VerifiedBy: s.Verifier,
		})
		if ok {
			return attempts
		}
	}
	if job.HumanFallback {
		attempts = append(attempts, outcome.Attempt{
			AttemptID: fmt.Sprintf("%s-ah", job.JobID), Seq: uint(len(attempts)),
			ExecutorID: "human-pool", Transport: outcome.TransportOK,
			LatencyMs: 600000, Validation: outcome.ValidationPass,
			Verified: outcome.VerifiedSuccess, VerifiedBy: "human:pool",
		})
	}
	return attempts
}

func (s *CostAwareStrategy) costMaps() (map[string]float64, map[string]bool) {
	means := map[string]float64{}
	known := map[string]bool{}
	for _, arm := range s.Book.Arms() {
		st, ok := s.Book.Stats(arm)
		if !ok {
			continue
		}
		if st.MeteredN >= s.Cfg.MinMeteredN {
			if m, ok := st.Mean(); ok {
				means[arm] = m
				known[arm] = true
			}
		}
	}
	return means, known
}

// CostAwareTreatment is one T3 treatment's fully isolated state: its own
// quality policy + learner, cost book, outcome ledger, assignment log, and
// decision log. Nothing is shared with sibling treatments.
type CostAwareTreatment struct {
	ID         string
	Dir        string
	Policy     *thompson.Policy
	Learner    *outcome.Learner
	Book       *outcome.CostBookV1
	Strategy   *CostAwareStrategy
	Cfg        thompson.CostAwareConfig
	Store      *outcome.FileOutcomeStore
	assignFile *os.File
	decFile    *os.File
	mu         sync.Mutex
}

// OpenCostAwareTreatment creates the isolated directory layout.
func OpenCostAwareTreatment(dir, id string, policy *thompson.Policy, cfg thompson.CostAwareConfig) (*CostAwareTreatment, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if policy == nil {
		return nil, fmt.Errorf("harness: cost-aware treatment %s needs a policy", id)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	store, err := outcome.NewFileOutcomeStore(dir + "/outcomes.jsonl")
	if err != nil {
		return nil, err
	}
	af, err := os.OpenFile(dir+"/assignments.jsonl", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	df, err := os.OpenFile(dir+"/decisions.jsonl", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		_ = store.Close()
		_ = af.Close()
		return nil, err
	}
	arms := policy.EligibleArmIDs()
	book := outcome.NewCostBookV1(arms)
	learner := outcome.NewLearner(policy, outcome.BinaryStatusMapper{}, store.Events)
	cfgHash := configHashForCostAware(cfg)
	strategy := &CostAwareStrategy{
		StrategyID: thompson.CostAwarePolicyID, Policy: policy, Book: book,
		Cfg: cfg, ConfigHash: cfgHash, Verifier: "harness:synthetic-v1",
	}
	return &CostAwareTreatment{
		ID: id, Dir: dir, Policy: policy, Learner: learner, Book: book,
		Strategy: strategy, Cfg: cfg, Store: store, assignFile: af, decFile: df,
	}, nil
}

func configHashForCostAware(cfg thompson.CostAwareConfig) string {
	b, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	// Short stable hash; full config persisted alongside decisions.
	h := 0
	for _, c := range b {
		h = h*31 + int(c)
	}
	return fmt.Sprintf("costaware-v1-%08x", uint32(h))
}

// Close syncs and closes files.
func (t *CostAwareTreatment) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	var first error
	for _, f := range []*os.File{t.assignFile, t.decFile} {
		if f == nil {
			continue
		}
		if err := f.Sync(); err != nil && first == nil {
			first = err
		}
		if err := f.Close(); err != nil && first == nil {
			first = err
		}
	}
	t.assignFile, t.decFile = nil, nil
	if err := t.Store.Close(); err != nil && first == nil {
		first = err
	}
	return first
}

// RunJob persists the assignment BEFORE execution, runs cost-aware selection,
// settles through the quality learner AND the cost book, and persists every
// decision with its true identity. Assignment RNG (caller rng for Assign is
// separate) stays independent from policy RNG (rng here drives selection and
// truth draws only).
func (t *CostAwareTreatment) RunJob(rng *rand.Rand, job JobTruth, a Assignment) (outcome.OutcomeEvent, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	a.AssignedAt = job.AssignedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	ab, err := json.Marshal(a)
	if err != nil {
		return outcome.OutcomeEvent{}, err
	}
	ab = append(ab, '\n')
	if _, err := t.assignFile.Write(ab); err != nil {
		return outcome.OutcomeEvent{}, err
	}
	if err := t.assignFile.Sync(); err != nil {
		return outcome.OutcomeEvent{}, err
	}
	nBefore := len(t.Strategy.Decisions)
	attempts := t.Strategy.Execute(rng, job)
	decider, status := decide(attempts)
	var humanCost *float64
	for _, at := range attempts {
		if at.ExecutorID == "human-pool" {
			hc := job.HumanCost
			humanCost = &hc
		}
	}
	ev := outcome.OutcomeEvent{
		SchemaVersion: outcome.SchemaVersion, EventType: outcome.EventJobSettled,
		DecisionID: "dec-" + job.JobID, JobID: job.JobID, StrategyID: t.ID,
		Version: 1, Supersedes: 0, Status: status, Attempts: attempts,
		DecidingAttemptID: decider, HumanReviewCostUSD: humanCost,
		VerifiedBy: "harness:synthetic-v1",
		VerifiedAt: job.AssignedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		OccurredAt: job.AssignedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	}
	if _, err := outcome.Settle(t.Store, t.Learner, ev); err != nil {
		return outcome.OutcomeEvent{}, err
	}
	if _, err := t.Book.Apply(ev, t.Store.Events); err != nil {
		return outcome.OutcomeEvent{}, err
	}
	for _, d := range t.Strategy.Decisions[nBefore:] {
		db, err := json.Marshal(d)
		if err != nil {
			return outcome.OutcomeEvent{}, err
		}
		db = append(db, '\n')
		if _, err := t.decFile.Write(db); err != nil {
			return outcome.OutcomeEvent{}, err
		}
	}
	if err := t.decFile.Sync(); err != nil {
		return outcome.OutcomeEvent{}, err
	}
	return ev, nil
}
