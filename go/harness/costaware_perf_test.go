package harness

import (
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// Operational measurements (single-writer experimental deployment).
// Run with: go test -run TestCostAwarePerf -v ./harness/
func TestCostAwarePerf(t *testing.T) {
	if testing.Short() {
		t.Skip("perf measurement skipped in short mode")
	}
	dir := t.TempDir()
	pol := thompson.NewDefault("cheap", "strong")
	cfg := thompson.DefaultCostAwareConfig()
	tr, err := OpenCostAwareTreatment(dir+"/t3", "t3", pol, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	tr.Strategy.MaxRetries = 0
	rng := rand.New(rand.NewPCG(1, 1))

	// Warm up cost book.
	for i := 0; i < 20; i++ {
		job := costAwareTestJob("w"+costAwarePad(i), 0.7, 0.7, 0.002, 0.04)
		if _, err := tr.RunJob(rng, job, Assignment{JobID: job.JobID, Strata: "web", Treatment: "t3", Probability: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// Decision latency (selection only).
	means, known := tr.Strategy.costMaps()
	start := time.Now()
	iters := 1000
	for i := 0; i < iters; i++ {
		if _, err := thompson.SelectCostAware(rng, pol, means, known, cfg); err != nil {
			t.Fatal(err)
		}
	}
	decLat := time.Since(start) / time.Duration(iters)
	// Settlement latency (RunJob end-to-end, fsync included).
	start = time.Now()
	for i := 0; i < 50; i++ {
		job := costAwareTestJob("s"+costAwarePad(i), 0.7, 0.7, 0.002, 0.04)
		if _, err := tr.RunJob(rng, job, Assignment{JobID: job.JobID, Strata: "web", Treatment: "t3", Probability: 1}); err != nil {
			t.Fatal(err)
		}
	}
	settleLat := time.Since(start) / 50
	// Checkpoint overhead (cost snapshot save).
	snap := tr.Book.Snapshot()
	start = time.Now()
	if err := outcome.SaveCostCheckpoint(dir+"/cost.json", snap); err != nil {
		t.Fatal(err)
	}
	cpLat := time.Since(start)
	// Recovery time (restore + rebuild from ledger).
	start = time.Now()
	if err := loadCostSnap(dir + "/cost.json"); err != nil {
		t.Fatal(err)
	}
	recLat := time.Since(start)
	// Storage per job (outcomes + assignments + decisions).
	var total int64
	for _, f := range []string{"/t3/outcomes.jsonl", "/t3/assignments.jsonl", "/t3/decisions.jsonl"} {
		fi, err := os.Stat(dir + f)
		if err != nil {
			t.Fatal(err)
		}
		total += fi.Size()
	}
	perJob := float64(total) / 70.0
	t.Logf("decision_latency=%s settlement_latency=%s checkpoint_save=%s recovery=%s storage_per_job=%.0fB",
		decLat, settleLat, cpLat, recLat, perJob)
	// Operational limits (single-writer experimental): decision must stay
	// well under interactive budgets; settlement dominated by fsync.
	if decLat > 5*time.Millisecond {
		t.Fatalf("decision latency too high: %s", decLat)
	}
}

func loadCostSnap(path string) error {
	snap, err := outcome.LoadCostCheckpoint(path)
	if err != nil {
		return err
	}
	if snap == nil {
		return nil
	}
	b := outcome.NewCostBookV1(nil)
	return b.Restore(*snap)
}
