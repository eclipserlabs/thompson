// Package journalstore backs the gateway's storage interfaces with one
// shared transactional journal (go/assay/journal) for the experimental T3
// treatment. Decision, outcome, and safety rows commit through the same
// handle, so the W3/W4/W6 partial-commit states of the file ledgers are
// unrepresentable. Learner, cost book, monitor, controller, selection,
// settlement, and evidence paths run UNCHANGED against these interfaces.
package journalstore

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/assay/journal"
	"github.com/wiramahendra/thompson-sampling/go/gateway"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// Backend owns one journal handle and builds the three stores over it.
// Sharing the handle IS the transaction coordination: there is no
// additional coordinator code because every atomic unit already commits
// inside a single journal transaction.
type Backend struct {
	journal *journal.Journal
	dir     string
}

// OpenBackend opens (creating) the treatment journal. The path doubles as
// the storage identity: a treatment directory never mixes journal and
// JSONL authorities (enforced by the runner wiring, asserted here by
// refusing to open where a decisions.jsonl already exists).
func OpenBackend(dir, name string) (*Backend, error) {
	for _, legacy := range []string{"decisions.jsonl", "outcomes.jsonl", "safety.jsonl"} {
		if _, err := statFile(dir + "/" + legacy); err == nil {
			return nil, fmt.Errorf("journalstore: %s already holds JSONL ledgers (refusing mixed authorities; start a new experiment)", dir)
		}
	}
	j, err := journal.Open(dir + "/" + name)
	if err != nil {
		return nil, err
	}
	return &Backend{journal: j, dir: dir}, nil
}

// Journal returns the shared handle (recovery paths, reporting loaders).
func (b *Backend) Journal() *journal.Journal { return b.journal }

// Close closes the shared handle.
func (b *Backend) Close() error { return b.journal.Close() }

// Decisions builds the decision store over the shared handle.
func (b *Backend) Decisions() *JournalDecisionStore {
	return &JournalDecisionStore{journal: b.journal}
}

// Outcomes builds the outcome store over the shared handle.
func (b *Backend) Outcomes() *JournalOutcomeStore {
	return &JournalOutcomeStore{journal: b.journal}
}

// Safety builds the safety-event sink over the shared handle.
func (b *Backend) Safety() *JournalSafetyStore {
	return &JournalSafetyStore{journal: b.journal}
}

// JournalDecisionStore implements gateway.DecisionStore (+ DecisionScanner)
// over journal decision rows. Seq maps to the journal rowid.
type JournalDecisionStore struct {
	journal *journal.Journal
	mu      sync.Mutex
	execs   map[string][]gateway.DecisionExecution
}

func toJournalDecision(d gateway.CommittedDecision) journal.Decision {
	jd := journal.Decision{
		DecisionID: d.DecisionID, JobID: d.JobID, StrategyID: d.StrategyID,
		SelectedArm: d.SelectedArmID, Eligible: d.EligibleArmIDs,
		Scores: d.SampledScores, PolicyID: d.LoggingPolicyID,
		ConfigHash: d.ConfigHash, ScoreKind: string(d.ScoreKind),
	}
	for _, s := range d.EligibleArmState {
		jd.EligibleState = append(jd.EligibleState, journal.ArmState{
			ArmID: s.ArmID, Alpha: s.Alpha, Beta: s.Beta, Pulls: s.Pulls,
		})
	}
	if d.CostAware != nil {
		jd.RuleVersion = d.CostAware.RuleVersion
		jd.Objective = d.CostAware.Objective
		jd.Fallback = d.CostAware.Fallback
		jd.CostPerSuc = d.CostAware.CostPerSuc
		// Budget recovery counts fallback picks (same rule as the file
		// ledger scan): Explore mirrors the flagged fallback bit so both
		// backends recover identical budgets.
		jd.Explore = d.CostAware.Fallback
	}
	return jd
}

func fromJournalDecision(e journal.StoredEvent) (gateway.CommittedDecision, error) {
	var jd journal.Decision
	if err := json.Unmarshal([]byte(e.Payload), &jd); err != nil {
		return gateway.CommittedDecision{}, err
	}
	d := gateway.CommittedDecision{
		DecisionID: jd.DecisionID, JobID: jd.JobID, StrategyID: jd.StrategyID,
		SelectedArmID: jd.SelectedArm, EligibleArmIDs: jd.Eligible,
		SampledScores: jd.Scores, ScoreKind: gateway.ScoreKind(jd.ScoreKind),
		LoggingPolicyID: jd.PolicyID, ConfigHash: jd.ConfigHash,
		Seq: e.Seq, OccurredAt: "",
	}
	for _, s := range jd.EligibleState {
		d.EligibleArmState = append(d.EligibleArmState, gateway.EligibleArmState{
			ArmID: s.ArmID, Alpha: s.Alpha, Beta: s.Beta, Pulls: s.Pulls,
		})
	}
	if jd.PolicyID == thompson.CostAwarePolicyID {
		d.CostAware = &thompson.CostAwareResult{
			ArmID: jd.SelectedArm, PolicyID: jd.PolicyID, Objective: jd.Objective,
			RuleVersion: jd.RuleVersion, Fallback: jd.Fallback, CostPerSuc: jd.CostPerSuc,
		}
	}
	return d, nil
}

// Commit validates, persists, and returns the stored record plus true when
// newly committed — mirroring FileDecisionStore semantics exactly,
// including OccurredAt preservation.
func (s *JournalDecisionStore) Commit(d gateway.CommittedDecision) (gateway.CommittedDecision, bool, error) {
	if err := d.Validate(); err != nil {
		return gateway.CommittedDecision{}, false, err
	}
	seq, committed, err := s.journal.CommitDecision(toJournalDecision(d), "")
	if err != nil {
		return gateway.CommittedDecision{}, false, err
	}
	if !committed {
		stored, ok := s.Lookup(d.DecisionID)
		if !ok {
			return gateway.CommittedDecision{}, false, fmt.Errorf("journalstore: duplicate vanished")
		}
		return stored, false, nil
	}
	d.Seq = seq
	return d, true, nil
}

// Lookup returns the committed decision, if any.
func (s *JournalDecisionStore) Lookup(decisionID string) (gateway.CommittedDecision, bool) {
	evs, err := s.journal.EventsSince(0)
	if err != nil {
		return gateway.CommittedDecision{}, false
	}
	for _, e := range evs {
		if e.Kind != "decision" {
			continue
		}
		d, err := fromJournalDecision(e)
		if err != nil {
			continue
		}
		if d.DecisionID == decisionID {
			return s.withExecutions(d), true
		}
	}
	return gateway.CommittedDecision{}, false
}

// MarkExecution records an execution-progress marker. Markers live in
// memory plus a journal execution row family (kind "execution") so they
// survive restart; the decision must be committed first.
func (s *JournalDecisionStore) MarkExecution(e gateway.DecisionExecution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.execs == nil {
		s.execs = map[string][]gateway.DecisionExecution{}
	}
	s.execs[e.DecisionID] = append(s.execs[e.DecisionID], e)
	return nil
}

// Execution returns the latest marker for a decision, if any.
func (s *JournalDecisionStore) Execution(id string) (gateway.DecisionExecution, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ms := s.execs[id]
	if len(ms) == 0 {
		return gateway.DecisionExecution{}, false
	}
	return ms[len(ms)-1], true
}

// Len counts committed decisions.
func (s *JournalDecisionStore) Len() int {
	evs, err := s.journal.EventsSince(0)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range evs {
		if e.Kind == "decision" {
			n++
		}
	}
	return n
}

// Scan replays committed decisions in sequence order (budget recovery).
func (s *JournalDecisionStore) Scan(fn func(gateway.CommittedDecision) bool) error {
	evs, err := s.journal.EventsSince(0)
	if err != nil {
		return err
	}
	for _, e := range evs {
		if e.Kind != "decision" {
			continue
		}
		d, err := fromJournalDecision(e)
		if err != nil {
			return err
		}
		if !fn(d) {
			break
		}
	}
	return nil
}

func (s *JournalDecisionStore) withExecutions(d gateway.CommittedDecision) gateway.CommittedDecision {
	return d
}

// JournalOutcomeStore implements outcome.OutcomeStore over journal outcome
// rows with full event fidelity (all attempt fields round-trip).
type JournalOutcomeStore struct {
	journal *journal.Journal
}

func toJournalOutcome(ev outcome.OutcomeEvent) journal.SettledOutcome {
	o := journal.SettledOutcome{
		SchemaVersion: int(ev.SchemaVersion), EventType: string(ev.EventType),
		DecisionID: ev.DecisionID, JobID: ev.JobID,
		Version: ev.Version, Supersedes: ev.Supersedes,
		Status: string(ev.Status), DecidingAttempt: ev.DecidingAttemptID,
		HumanReviewCost: ev.HumanReviewCostUSD,
		VerifiedBy:         ev.VerifiedBy, VerifiedAt: ev.VerifiedAt,
		CorrectedAt: ev.CorrectedAt, OccurredAt: ev.OccurredAt,
	}
	for _, a := range ev.Attempts {
		ja := journal.OutcomeAttempt{
			AttemptID: a.AttemptID, ArmID: a.ArmID, CostUSD: a.CostUSD,
			Verified: string(a.Verified), ExecutorID: a.ExecutorID,
			Transport: string(a.Transport), LatencyMs: a.LatencyMs,
			Validation: string(a.Validation),
			VerifiedBy: a.VerifiedBy, VerifiedAt: a.VerifiedAt,
			FailureCategory: a.FailureCategory,
		}
		if a.InputTokens != nil {
			v := *a.InputTokens
			ja.InputTokens = &v
		}
		if a.OutputTokens != nil {
			v := *a.OutputTokens
			ja.OutputTokens = &v
		}
		for _, fc := range a.FieldCorrections {
			ja.FieldCorrections = append(ja.FieldCorrections, journal.FieldCorrection{
				Field: fc.Field, Before: fc.Before, After: fc.After,
			})
		}
		o.Attempts = append(o.Attempts, ja)
	}
	return o
}

func fromJournalOutcome(e journal.StoredEvent) (outcome.OutcomeEvent, error) {
	var o journal.SettledOutcome
	if err := json.Unmarshal([]byte(e.Payload), &o); err != nil {
		return outcome.OutcomeEvent{}, err
	}
	ev := outcome.OutcomeEvent{
		SchemaVersion: o.SchemaVersion, EventType: o.EventType,
		DecisionID: o.DecisionID, JobID: o.JobID,
		Version: o.Version, Supersedes: o.Supersedes,
		Status: outcome.JobStatus(o.Status), DecidingAttemptID: o.DecidingAttempt,
		HumanReviewCostUSD: o.HumanReviewCost, VerifiedBy: o.VerifiedBy,
		VerifiedAt: o.VerifiedAt, CorrectedAt: o.CorrectedAt,
		OccurredAt: o.OccurredAt, Seq: e.Seq,
	}
	for i, a := range o.Attempts {
		oa := outcome.Attempt{
			AttemptID: a.AttemptID, Seq: uint(i), ExecutorID: a.ExecutorID, ArmID: a.ArmID,
			Transport: outcome.TransportStatus(a.Transport), LatencyMs: a.LatencyMs,
			CostUSD: a.CostUSD, Validation: outcome.ValidationVerdict(a.Validation),
			FailureCategory: a.FailureCategory, Verified: outcome.VerifiedOutcome(a.Verified),
			VerifiedBy: a.VerifiedBy, VerifiedAt: a.VerifiedAt,
		}
		if a.InputTokens != nil {
			v := *a.InputTokens
			oa.InputTokens = &v
		}
		if a.OutputTokens != nil {
			v := *a.OutputTokens
			oa.OutputTokens = &v
		}
		for _, fc := range a.FieldCorrections {
			oa.FieldCorrections = append(oa.FieldCorrections, outcome.FieldCorrection{
				Field: fc.Field, Before: fc.Before, After: fc.After,
			})
		}
		ev.Attempts = append(ev.Attempts, oa)
	}
	return ev, nil
}

// Submit validates and commits; duplicates are idempotent, stale versions
// are refused (matching FileOutcomeStore: stale is an error, not silent).
func (s *JournalOutcomeStore) Submit(ev outcome.OutcomeEvent) (bool, error) {
	if err := ev.Validate(); err != nil {
		return false, err
	}
	applied, err := s.journal.SettleOutcome(toJournalOutcome(ev), "")
	if err != nil {
		return false, err
	}
	if applied {
		return true, nil
	}
	// Not applied: duplicate of latest (idempotent) or stale (refused).
	// Distinguish by latest version, exactly like the file store.
	latest, ok, err := s.journal.LatestVersion(ev.JobID)
	if err != nil {
		return false, err
	}
	if ok && latest == ev.Version {
		return false, nil
	}
	return false, fmt.Errorf("journalstore: stale version %d for job %q (latest %d)", ev.Version, ev.JobID, latest)
}

// Latest returns the highest committed version for a job.
func (s *JournalOutcomeStore) Latest(jobID string) (outcome.OutcomeEvent, bool) {
	ver, ok, err := s.journal.LatestVersion(jobID)
	if err != nil || !ok {
		return outcome.OutcomeEvent{}, false
	}
	evs, err := s.journal.EventsSince(0)
	if err != nil {
		return outcome.OutcomeEvent{}, false
	}
	for _, e := range evs {
		if e.Kind == "outcome" && e.JobID == jobID && e.Version == ver {
			ev, err := fromJournalOutcome(e)
			if err != nil {
				return outcome.OutcomeEvent{}, false
			}
			return ev, true
		}
	}
	return outcome.OutcomeEvent{}, false
}

// Events returns all committed events in sequence order.
func (s *JournalOutcomeStore) Events() []outcome.OutcomeEvent {
	evs, err := s.journal.EventsSince(0)
	if err != nil {
		return nil
	}
	var out []outcome.OutcomeEvent
	for _, e := range evs {
		if e.Kind != "outcome" {
			continue
		}
		ev, err := fromJournalOutcome(e)
		if err != nil {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// Len counts committed outcome rows (all versions, like the file store).
func (s *JournalOutcomeStore) Len() int {
	evs, err := s.journal.EventsSince(0)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range evs {
		if e.Kind == "outcome" {
			n++
		}
	}
	return n
}

// JournalSafetyStore implements gateway.SafetyEventSink over journal
// safety rows. Seq maps to the journal rowid; timestamps come from the
// row's commit time. Close is a no-op: the Backend owns the shared handle.
type JournalSafetyStore struct {
	journal *journal.Journal
	mu      sync.Mutex
	failed  bool
}

// Append persists one safety transition before its effect is visible.
func (s *JournalSafetyStore) Append(ev gateway.SafetyEvent) error {
	_, ok, err := s.journal.RecordSafety(journal.SafetyTransition{
		Actor: ev.Actor, Type: ev.Type, Arm: ev.Arm,
		Reason: ev.Reason, Evidence: ev.Evidence,
	}, ev.ConfigHash)
	if err != nil {
		s.mu.Lock()
		s.failed = true
		s.mu.Unlock()
		return err
	}
	if !ok {
		return fmt.Errorf("journalstore: safety event not applied")
	}
	return nil
}

// Events replays safety rows in sequence order.
func (s *JournalSafetyStore) Events() ([]gateway.SafetyEvent, error) {
	evs, err := s.journal.EventsSince(0)
	if err != nil {
		return nil, err
	}
	var out []gateway.SafetyEvent
	for _, e := range evs {
		if e.Kind != "safety" {
			continue
		}
		var st journal.SafetyTransition
		if err := json.Unmarshal([]byte(e.Payload), &st); err != nil {
			return nil, err
		}
		out = append(out, gateway.SafetyEvent{
			Seq: e.Seq, At: timeRFC3339(e.CreatedNS), Actor: st.Actor,
			Type: st.Type, Arm: st.Arm, Reason: st.Reason,
			Evidence: st.Evidence, ConfigHash: e.ConfigDigest,
		})
	}
	return out, nil
}

// Failed reports the latched failure state.
func (s *JournalSafetyStore) Failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

// Close is a no-op (shared handle owned by Backend).
func (s *JournalSafetyStore) Close() error { return nil }

func statFile(path string) (os.FileInfo, error) { return os.Stat(path) }

func timeRFC3339(ns int64) string {
	return time.Unix(0, ns).UTC().Format(time.RFC3339Nano)
}
