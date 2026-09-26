package main

import (
	"context"
	"os"
	"testing"
)

func TestDbgRunOne(t *testing.T) {
	dir := t.TempDir()
	arms := map[string]ArmTruth{"fixed": truthArm(1.0, 0.01, 100)}
	job := sJob("jinv", "s", BehaviorInvalidOutput, arms)
	job.Eligible = []string{"t0"}
	mPath := scenarioManifest(t, dir, []ManifestJob{job})
	r := openTestRunner(t, mPath, dir, 18781, 18791)
	defer r.Shutdown()
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	raw, _ := os.ReadFile(dir + "/t0/outcomes.jsonl")
	t.Logf("t0 outcomes:\n%s", raw)
	raw2, _ := os.ReadFile(dir + "/progress.jsonl")
	t.Logf("progress:\n%s", raw2)
}
