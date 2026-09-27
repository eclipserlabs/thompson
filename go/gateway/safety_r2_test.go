package gateway

import (
	"math/rand/v2"
	"path/filepath"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// R2: exploration accounting across the reservation/commit boundary.
// Contract under test: only COMMITTED exploration consumes budget across
// recovery (replay folds the decisions ledger). A reservation abandoned
// before commit is forgiven on recovery — safe direction (availability),
// never silent over-exploration.
func TestR2UncommittedReservationForgiven(t *testing.T) {
	dir := t.TempDir()
	decStore, err := NewFileDecisionStore(filepath.Join(dir, "d.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer decStore.Close()
	outStore, err := outcome.NewFileOutcomeStore(filepath.Join(dir, "o.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer outStore.Close()
	sstore, err := NewSafetyStore(filepath.Join(dir, "s.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer sstore.Close()
	cfg := SafetyConfig{Workload: "r2", Arms: []string{"a", "b"}, FallbackArm: "b",
		QualityFloor: 0.3, MonitorWindow: 10, MonitorMinObs: 5,
		MaxExplorationPerArm: 100, MaxMissingShare: 0.5, ColdStartPulls: 5}
	ctrl, err := NewSafetyController(cfg, "r2hash", sstore, decStore, outStore)
	if err != nil {
		t.Fatal(err)
	}
	q := thompson.NewDefault("a", "b")
	book := outcome.NewCostBookV1([]string{"a", "b"})
	cp, err := NewCostAwarePolicy(q, book, thompson.DefaultCostAwareConfig(), ctrl)
	if err != nil {
		t.Fatal(err)
	}
	// Reserve (and consume budget) but NEVER commit: simulated crash between
	// reservation and decision persistence.
	rng := rand.New(rand.NewPCG(9, 9))
	snap, err := cp.SelectSnapshot(rng)
	if err != nil {
		t.Fatal(err)
	}
	liveUsed := exploredOf(t, ctrl, snap.Selected)
	if liveUsed != 1 {
		t.Fatalf("live budget should count the reservation, got %d", liveUsed)
	}
	// Recovery over the same ledgers: the abandoned reservation is absent.
	sstore2, err := NewSafetyStore(filepath.Join(dir, "s2.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer sstore2.Close()
	ctrl2, err := NewSafetyController(cfg, "r2hash", sstore2, decStore, outStore)
	if err != nil {
		t.Fatal(err)
	}
	if got := exploredOf(t, ctrl2, snap.Selected); got != 0 {
		t.Fatalf("R2: recovery counted an uncommitted reservation: %d", got)
	}
}

// R2 equivalence: committed exploration decisions replay to identical
// budget counters after restart.
func TestR2CommittedBudgetRecoveryEquivalent(t *testing.T) {
	dir := t.TempDir()
	decStore, err := NewFileDecisionStore(filepath.Join(dir, "d.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	outStore, err := outcome.NewFileOutcomeStore(filepath.Join(dir, "o.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sstore, err := NewSafetyStore(filepath.Join(dir, "s.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := SafetyConfig{Workload: "r2b", Arms: []string{"a", "b"}, FallbackArm: "b",
		QualityFloor: 0.3, MonitorWindow: 10, MonitorMinObs: 5,
		MaxExplorationPerArm: 100, MaxMissingShare: 0.5, ColdStartPulls: 5}
	ctrl, err := NewSafetyController(cfg, "r2hash", sstore, decStore, outStore)
	if err != nil {
		t.Fatal(err)
	}
	q := thompson.NewDefault("a", "b")
	book := outcome.NewCostBookV1([]string{"a", "b"})
	cp, err := NewCostAwarePolicy(q, book, thompson.DefaultCostAwareConfig(), ctrl)
	if err != nil {
		t.Fatal(err)
	}
	// Commit 7 exploration-heavy decisions through the real commit path.
	committed := map[string]int{}
	for i := 0; i < 7; i++ {
		rng := rand.New(rand.NewPCG(uint64(i+1), 7))
		snap, err := cp.SelectSnapshot(rng)
		if err != nil {
			t.Fatal(err)
		}
		var armState []EligibleArmState
		for _, id := range snap.Eligible {
			post := snap.Posteriors[id]
			armState = append(armState, EligibleArmState{ArmID: id, Alpha: post.Alpha, Beta: post.Beta, Pulls: post.Pulls})
		}
		d := CommittedDecision{
			DecisionID: decID("r2", i), JobID: jobID("r2", i), StrategyID: "t3",
			SelectedArmID: snap.Selected, EligibleArmIDs: snap.Eligible,
			EligibleArmState: armState,
			SampledScores:    snap.Scores, ScoreKind: scoreKindFor(snap.Config.Selection.Kind, snap.Forced),
			LoggingPolicyID: cp.LoggingPolicyID(),
			ConfigHash:      snap.ConfigHash, OccurredAt: "2026-01-05T00:00:00Z",
		}
		if snap.CostAware != nil {
			d.CostAware = snap.CostAware
			if snap.CostAware.Fallback {
				committed[snap.Selected]++
			}
		}
		if _, _, err := decStore.Commit(d); err != nil {
			t.Fatal(err)
		}
	}
	// Fresh controller over the same files replays identical counters.
	if err := sstore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := decStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := outStore.Close(); err != nil {
		t.Fatal(err)
	}
	sstoreB, err := NewSafetyStore(filepath.Join(dir, "s.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer sstoreB.Close()
	decB, err := NewFileDecisionStore(filepath.Join(dir, "d.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer decB.Close()
	outB, err := outcome.NewFileOutcomeStore(filepath.Join(dir, "o.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer outB.Close()
	ctrlB, err := NewSafetyController(cfg, "r2hash", sstoreB, decB, outB)
	if err != nil {
		t.Fatal(err)
	}
	for arm, want := range committed {
		if got := exploredOf(t, ctrlB, arm); got != uint64(want) {
			t.Fatalf("R2: arm %s budget %d != committed %d", arm, got, want)
		}
	}
}

func exploredOf(t *testing.T, c *SafetyController, arm string) uint64 {
	t.Helper()
	_, used, _ := c.State(arm)
	return used
}

func decID(p string, i int) string { return p + "-dec-" + itoaR2(i) }
func jobID(p string, i int) string { return p + "-job-" + itoaR2(i) }

func itoaR2(i int) string {
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
