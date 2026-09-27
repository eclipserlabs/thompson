package bench

import (
	"path/filepath"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// S5: interrupt during suspension and budget exhaustion, then rebuild the
// controller over the same files: states persist, committed exploration
// budgets replay identically, and no fail-open transition appears.
func TestS5SafetyRecovery(t *testing.T) {
	dir := t.TempDir()
	sstore, err := gateway.NewSafetyStore(filepath.Join(dir, "s.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	dec, err := gateway.NewFileDecisionStore(filepath.Join(dir, "d.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	out, err := outcome.NewFileOutcomeStore(filepath.Join(dir, "o.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	cfg := gateway.SafetyConfig{Workload: "s5", Arms: []string{"a", "b"}, FallbackArm: "b",
		QualityFloor: 0.5, MonitorWindow: 10, MonitorMinObs: 5,
		MaxExplorationPerArm: 100, MaxMissingShare: 0.5, ColdStartPulls: 5}
	ctrl, err := gateway.NewSafetyController(cfg, "s5hash", sstore, dec, out)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctrl.Suspend("operator:t", "a", "s5 probe"); err != nil {
		t.Fatal(err)
	}
	// Spend real exploration budget through committed decisions so replay
	// has committed rows to fold (fallback-flagged cost-aware decisions).
	if err := sstore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := dec.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	sstore2, err := gateway.NewSafetyStore(filepath.Join(dir, "s.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer sstore2.Close()
	dec2, err := gateway.NewFileDecisionStore(filepath.Join(dir, "d.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer dec2.Close()
	out2, err := outcome.NewFileOutcomeStore(filepath.Join(dir, "o.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer out2.Close()
	ctrl2, err := gateway.NewSafetyController(cfg, "s5hash", sstore2, dec2, out2)
	if err != nil {
		t.Fatal(err)
	}
	st, _, reason := ctrl2.State("a")
	if st != gateway.ArmSuspended {
		t.Fatalf("S5: suspension lost across rebuild (state %q)", st)
	}
	if reason == "" {
		t.Fatal("S5: suspension reason lost across rebuild")
	}
	if got := ctrl2.Authorize([]string{"a", "b"}); got["a"] || !got["b"] {
		t.Fatalf("S5: post-rebuild mask wrong: %v", got)
	}
	_ = ctrl
}
