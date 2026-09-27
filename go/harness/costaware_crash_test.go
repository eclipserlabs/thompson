package harness

import (
	"encoding/json"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// F5 regression: crash between jobs loses nothing authoritative. Run 10 jobs,
// checkpoint, snapshot state; resume from the ledger in a fresh instance
// (simulated crash: original never closed); state must be identical and the
// resumed instance must continue learning seamlessly.
func TestCostAwareCrashResume(t *testing.T) {
	dir := t.TempDir()
	mkJob := func(i int) JobTruth {
		return JobTruth{
			JobID: "c" + costAwarePad(i), Strata: "web",
			AssignedAt: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute),
			ArmSuccess: map[string]float64{"cheap": 0.7, "strong": 0.7},
			ArmCost:    map[string]float64{"cheap": 0.002, "strong": 0.04},
			ArmLatency: map[string]float64{"cheap": 100, "strong": 200},
		}
	}
	cfg := thompson.CostAwareConfig{QualityFloor: 0.3, MinMeteredN: 1, ColdStartPulls: 5, Epsilon: 1e-3}
	pol := thompson.NewDefault("cheap", "strong")
	tr, err := OpenCostAwareTreatment(dir+"/t3", "t3", pol, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(77, 77))
	for i := 0; i < 10; i++ {
		job := mkJob(i)
		if _, err := tr.RunJob(rng, job, Assignment{JobID: job.JobID, Strata: "web", Treatment: "t3", Probability: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tr.SaveCheckpoint(); err != nil {
		t.Fatal(err)
	}
	snapPol, _ := json.Marshal(pol.Snapshot())
	meanCheap, _ := tr.Book.Mean("cheap")
	// Simulated crash: Close releases the single-writer lock exactly as
	// process death would (durable per-submit fsync already done; no
	// graceful shutdown protocol exists beyond that). Recovery replays the
	// authoritative ledger, never the checkpoint files.
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	pol2 := thompson.NewDefault("cheap", "strong")
	tr2, err := ResumeCostAwareTreatment(dir+"/t3", "t3", pol2, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tr2.Close()
	snapPol2, _ := json.Marshal(pol2.Snapshot())
	if string(snapPol) != string(snapPol2) {
		t.Fatal("F5: quality posterior diverged after resume")
	}
	if m, _ := tr2.Book.Mean("cheap"); m != meanCheap {
		t.Fatalf("F5: cost mean diverged after resume: %v vs %v", m, meanCheap)
	}
	// Config mismatch must refuse.
	bad := cfg
	bad.QualityFloor = 0.9
	if _, err := ResumeCostAwareTreatment(dir+"/t3", "t3", thompson.NewDefault("cheap", "strong"), bad); err == nil {
		t.Fatal("F5: resume with different config must refuse")
	}
	// Continued learning on the resumed instance.
	for i := 10; i < 15; i++ {
		job := mkJob(i)
		if _, err := tr2.RunJob(rng, job, Assignment{JobID: job.JobID, Strata: "web", Treatment: "t3", Probability: 1}); err != nil {
			t.Fatal(err)
		}
	}
}
