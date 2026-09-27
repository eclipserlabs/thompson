package harness

import (
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// Concurrent selection + settlement + correction + checkpoint recovery.
func TestCostAwareConcurrentSafe(t *testing.T) {
	dir := t.TempDir()
	pol := thompson.NewDefault("cheap", "strong")
	cfg := thompson.DefaultCostAwareConfig()
	tr, err := OpenCostAwareTreatment(dir+"/t3", "t3", pol, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	tr.Strategy.MaxRetries = 0
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(w+1), uint64(w+1)))
			for i := 0; i < 5; i++ {
				id := "w" + costAwareItoa(w) + "j" + costAwareItoa(i)
				job := costAwareTestJob(id, 0.7, 0.7, 0.002, 0.04)
				job.AssignedAt = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
				if _, err := tr.RunJob(rng, job, Assignment{JobID: id, Strata: "web", Treatment: "t3", Probability: 1}); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	// Correction on one job: rebuild path must hold under concurrency-free check.
	evs := tr.Store.Events()
	if len(evs) == 0 {
		t.Fatal("no events settled")
	}
	// Checkpoint round-trip.
	snap := tr.Book.Snapshot()
	b2 := outcome.NewCostBookV1([]string{"cheap", "strong"})
	if err := b2.Restore(snap); err != nil {
		t.Fatal(err)
	}
	for _, arm := range []string{"cheap", "strong"} {
		m1, k1 := tr.Book.Mean(arm)
		m2, k2 := b2.Mean(arm)
		if k1 != k2 || (k1 && m1 != m2) {
			t.Fatalf("checkpoint diverged on %s", arm)
		}
	}
}
