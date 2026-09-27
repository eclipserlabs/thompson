package outcome

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// CostBookVersion versions the cost-ledger state representation. It is
// independent from the quality posterior and from checkpoint versions: a
// cost-aware learner owns both, and neither is ever reinterpreted as the
// other.
const CostBookVersion = 1

// ArmCostStats is the versioned per-arm cost state: sums over fully-metered
// settled jobs only. Unmetered jobs are counted, never zero-filled.
type ArmCostStats struct {
	MeteredSum float64 `json:"metered_sum"`
	MeteredN   uint64  `json:"metered_n"`
	UnmeteredN uint64  `json:"unmetered_n"`
}

// Mean returns the empirical mean fully-loaded cost and whether the arm has
// any fully-metered observation.
func (s ArmCostStats) Mean() (float64, bool) {
	if s.MeteredN == 0 {
		return 0, false
	}
	return s.MeteredSum / float64(s.MeteredN), true
}

// CostBookV1 tracks per-arm fully-loaded cost means over settled,
// fully-metered jobs. Correction-safe and deterministically replayable:
// same semantics as Learner (idempotent duplicates, gap refusal, rebuild
// on correction, ledger-order rebuild).
type CostBookV1 struct {
	mu      sync.Mutex
	arms    map[string]*ArmCostStats
	applied map[string]uint64
}

// NewCostBookV1 creates an empty cost book for the given arms.
func NewCostBookV1(armIDs []string) *CostBookV1 {
	b := &CostBookV1{arms: make(map[string]*ArmCostStats), applied: make(map[string]uint64)}
	for _, id := range armIDs {
		b.arms[id] = &ArmCostStats{}
	}
	return b
}

// jobFullyLoadedCost implements the SAME counting rule as harness.jobCost:
// every nil attempt CostUSD counts as unmetered — including human-pool
// attempts whose cost lives in HumanReviewCostUSD. This conservative rule is
// deliberately shared (see TestCostBookReportParity): the cost-aware learner
// must optimize over exactly the population the evaluator meters, even though
// a recorded HumanReviewCostUSD arguably accounts the job. Changing the
// evaluator's rule is out of scope (frozen baseline + active work on report
// tests); the experimental side aligns instead. Malformed (NaN/Inf/negative)
// costs are rejected explicitly (stricter than the report, which predates
// cost-aware validation and sums blindly).
func jobFullyLoadedCost(ev OutcomeEvent) (float64, int, error) {
	metered := 0.0
	unmetered := 0
	humanAttempt := false
	for _, a := range ev.Attempts {
		if a.CostUSD == nil {
			unmetered++
			if a.ExecutorID == "human-pool" {
				humanAttempt = true
			}
			continue
		}
		c := *a.CostUSD
		if math.IsNaN(c) || math.IsInf(c, 0) {
			return 0, 0, fmt.Errorf("outcome: non-finite cost %v on attempt %q", c, a.AttemptID)
		}
		if c < 0 {
			return 0, 0, fmt.Errorf("outcome: negative cost %v on attempt %q", c, a.AttemptID)
		}
		metered += c
		if a.ExecutorID == "human-pool" {
			humanAttempt = true
		}
	}
	if ev.HumanReviewCostUSD == nil {
		if humanAttempt {
			unmetered++
		}
	} else {
		hc := *ev.HumanReviewCostUSD
		if math.IsNaN(hc) || math.IsInf(hc, 0) {
			return 0, 0, fmt.Errorf("outcome: non-finite human review cost %v", hc)
		}
		if hc < 0 {
			return 0, 0, fmt.Errorf("outcome: negative human review cost %v", hc)
		}
		metered += hc
	}
	return metered, unmetered, nil
}

// FullyLoadedCost exposes the book's counting rule for cross-package parity
// tests (harness.jobCost must agree on every constructible event).
func FullyLoadedCost(ev OutcomeEvent) (float64, int, error) {
	return jobFullyLoadedCost(ev)
}

// ValidateCosts rejects malformed (NaN/Inf/negative) costs explicitly without
// touching any learner state. Callers must invoke it BEFORE Submit/Settle so
// a refusal is atomic: the quality learner never moves on a job the cost
// ledger refuses.
func ValidateCosts(ev OutcomeEvent) error {
	if err := ev.Validate(); err != nil {
		return err
	}
	_, _, err := jobFullyLoadedCost(ev)
	return err
}

// decidingArm returns the deciding attempt's arm, or "" when none.
func decidingArm(ev OutcomeEvent) string {
	for _, a := range ev.Attempts {
		if a.AttemptID == ev.DecidingAttemptID {
			return a.ArmID
		}
	}
	return ""
}

// Apply folds one event into the cost book. UNKNOWN/PENDING advance the
// cursor without touching estimates. Fully-metered ACCEPTED/REJECTED jobs
// add to the deciding arm's mean; partially-metered jobs increment
// UnmeteredN only. Duplicates are idempotent; version gaps are refused;
// corrections rebuild from history.
func (b *CostBookV1) Apply(ev OutcomeEvent, history func() []OutcomeEvent) (bool, error) {
	if err := ev.Validate(); err != nil {
		return false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	cur, known := b.applied[ev.JobID]
	switch {
	case known && ev.Version <= cur:
		return false, nil
	case known && ev.Version > cur+1:
		return false, fmt.Errorf("outcome: costbook version gap for job %q: applied %d, got %d", ev.JobID, cur, ev.Version)
	case known:
		return b.rebuildLocked(append(history(), ev))
	default:
		if ev.Version != 1 {
			return false, fmt.Errorf("outcome: costbook first version for job %q must be 1, got %d", ev.JobID, ev.Version)
		}
		return b.foldLocked(ev)
	}
}

// foldLocked maps one event to at most one cost observation on the deciding arm.
func (b *CostBookV1) foldLocked(ev OutcomeEvent) (bool, error) {
	// Only learnable statuses move the book; UNKNOWN/PENDING advance cursor.
	switch ev.Status {
	case StatusAccepted, StatusRejected:
	default:
		b.applied[ev.JobID] = ev.Version
		return false, nil
	}
	armID := decidingArm(ev)
	if armID == "" {
		b.applied[ev.JobID] = ev.Version
		return false, nil
	}
	st, ok := b.arms[armID]
	if !ok {
		return false, fmt.Errorf("outcome: costbook arm %q not in book", armID)
	}
	metered, unmetered, err := jobFullyLoadedCost(ev)
	if err != nil {
		return false, err
	}
	if unmetered > 0 {
		st.UnmeteredN++
		b.applied[ev.JobID] = ev.Version
		return false, nil
	}
	st.MeteredSum += metered
	st.MeteredN++
	b.applied[ev.JobID] = ev.Version
	return true, nil
}

// Rebuild restores empty stats and folds the latest version per job in ledger order.
func (b *CostBookV1) Rebuild(evs []OutcomeEvent) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rebuildLocked(evs)
}

func (b *CostBookV1) rebuildLocked(evs []OutcomeEvent) (bool, error) {
	for _, st := range b.arms {
		st.MeteredSum = 0
		st.MeteredN = 0
		st.UnmeteredN = 0
	}
	latest := make(map[string]OutcomeEvent)
	firstSeq := make(map[string]uint64)
	seen := make(map[string]map[uint64]bool)
	for _, ev := range evs {
		if err := ev.Validate(); err != nil {
			return false, err
		}
		if seen[ev.JobID] == nil {
			seen[ev.JobID] = make(map[uint64]bool)
		}
		if seen[ev.JobID][ev.Version] {
			continue
		}
		seen[ev.JobID][ev.Version] = true
		if cur, ok := latest[ev.JobID]; !ok || ev.Version > cur.Version {
			latest[ev.JobID] = ev
		}
		if s, ok := firstSeq[ev.JobID]; !ok || ev.Seq < s {
			firstSeq[ev.JobID] = ev.Seq
		}
	}
	ordered := make([]OutcomeEvent, 0, len(latest))
	for _, ev := range latest {
		ordered = append(ordered, ev)
	}
	sort.Slice(ordered, func(i, j int) bool {
		si, sj := firstSeq[ordered[i].JobID], firstSeq[ordered[j].JobID]
		if si != sj {
			return si < sj
		}
		return ordered[i].JobID < ordered[j].JobID
	})
	moved := false
	b.applied = make(map[string]uint64, len(ordered))
	for _, ev := range ordered {
		m, err := b.foldLocked(ev)
		if err != nil {
			return false, fmt.Errorf("outcome: costbook rebuild fold job %q v%d: %w", ev.JobID, ev.Version, err)
		}
		moved = moved || m
	}
	return moved, nil
}

// Mean returns the arm's mean fully-loaded cost and whether any fully-metered
// observation exists.
func (b *CostBookV1) Mean(armID string) (float64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.arms[armID]
	if !ok {
		return 0, false
	}
	return st.Mean()
}

// Stats returns a copy of one arm's stats.
func (b *CostBookV1) Stats(armID string) (ArmCostStats, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.arms[armID]
	if !ok {
		return ArmCostStats{}, false
	}
	return *st, true
}

// Arms returns sorted arm IDs for deterministic iteration.
func (b *CostBookV1) Arms() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.arms))
	for id := range b.arms {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// CostBookSnapshot is the versioned serializable cost state.
type CostBookSnapshot struct {
	Version int                     `json:"version"`
	Arms    map[string]ArmCostStats `json:"arms"`
	Applied map[string]uint64       `json:"applied"`
}

// Snapshot captures the versioned state.
func (b *CostBookV1) Snapshot() CostBookSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	arms := make(map[string]ArmCostStats, len(b.arms))
	for id, st := range b.arms {
		arms[id] = *st
	}
	applied := make(map[string]uint64, len(b.applied))
	for k, v := range b.applied {
		applied[k] = v
	}
	return CostBookSnapshot{Version: CostBookVersion, Arms: arms, Applied: applied}
}

// Restore replaces state from a snapshot of the same version.
func (b *CostBookV1) Restore(snap CostBookSnapshot) error {
	if snap.Version != CostBookVersion {
		return fmt.Errorf("outcome: costbook snapshot version %d, want %d", snap.Version, CostBookVersion)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.arms = make(map[string]*ArmCostStats, len(snap.Arms))
	for id, st := range snap.Arms {
		c := st
		b.arms[id] = &c
	}
	b.applied = make(map[string]uint64, len(snap.Applied))
	for k, v := range snap.Applied {
		b.applied[k] = v
	}
	return nil
}

// SaveCostCheckpoint persists a cost snapshot atomically.
func SaveCostCheckpoint(path string, snap CostBookSnapshot) error {
	if snap.Version != CostBookVersion {
		return fmt.Errorf("outcome: cost checkpoint version %d, want %d", snap.Version, CostBookVersion)
	}
	bb, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("outcome: marshal cost checkpoint: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("outcome: mkdir cost checkpoint dir: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("outcome: open cost checkpoint tmp: %w", err)
	}
	if _, err := f.Write(bb); err != nil {
		_ = f.Close()
		return fmt.Errorf("outcome: write cost checkpoint: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("outcome: sync cost checkpoint: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("outcome: close cost checkpoint: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("outcome: rename cost checkpoint: %w", err)
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// LoadCostCheckpoint reads a cost snapshot, returning (nil,nil) when absent.
func LoadCostCheckpoint(path string) (*CostBookSnapshot, error) {
	bb, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("outcome: read cost checkpoint: %w", err)
	}
	var snap CostBookSnapshot
	if err := json.Unmarshal(bb, &snap); err != nil {
		return nil, fmt.Errorf("outcome: unmarshal cost checkpoint: %w", err)
	}
	if snap.Version != CostBookVersion {
		return nil, fmt.Errorf("outcome: cost checkpoint version %d, want %d", snap.Version, CostBookVersion)
	}
	return &snap, nil
}
