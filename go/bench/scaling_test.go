package bench

import (
	"math/rand/v2"
	"runtime"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// Scaling: minimal/logged/full learners across workload sizes. fsync-bound
// variants stop at 10k (100k x ~5ms fsync would take ~8 minutes; the scaling
// law is already linear and labeled). Memory via MemStats deltas.
func TestScalingLearners(t *testing.T) {
	if testing.Short() {
		t.Skip("scaling skipped in short mode")
	}
	for _, n := range []int{1000, 10000, 100000, 1000000} {
		rng := rand.New(rand.NewPCG(77, 77))
		p := NewMinimalPolicy("a", "b")
		t0 := time.Now()
		for i := 0; i < n; i++ {
			arm := "a"
			if rng.Float64() < 0.5 {
				arm = "b"
			}
			if _, _, err := p.Select(rng); err != nil {
				t.Fatal(err)
			}
			if err := p.Observe(arm, rng.Float64() < 0.7); err != nil {
				t.Fatal(err)
			}
		}
		sel := time.Since(t0) / time.Duration(2*n)
		var m0, m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m0)
		p2 := NewMinimalPolicy("a", "b")
		for i := 0; i < n; i++ {
			_ = p2.Observe("a", true)
		}
		runtime.ReadMemStats(&m1)
		t.Logf("n=%d minimal/op=%s mem=%dKB", n, sel, (m1.Alloc-m0.Alloc)/1024)
		if n > 10000 {
			continue // fsync-bound variants stop here; law already linear
		}
		dir := t.TempDir()
		lp, err := OpenLoggedPolicy(dir+"/l.jsonl", "a", "b")
		if err != nil {
			t.Fatal(err)
		}
		t1 := time.Now()
		for i := 0; i < n; i++ {
			if err := lp.Observe("job", "a", true); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("n=%d logged/settle=%s", n, time.Since(t1)/time.Duration(n))
		lp.Close()
		store, err := outcome.NewFileOutcomeStore(dir + "/o.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		fpol := textbookPolicy("a", "b")
		fl := outcome.NewLearner(fpol, outcome.BinaryStatusMapper{}, store.Events)
		t2 := time.Now()
		for i := 0; i < n; i++ {
			if _, err := outcome.Settle(store, fl, benchOutcome("s"+itoaBench(i), "a", 1, true, 0.01)); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("n=%d full/settle=%s", n, time.Since(t2)/time.Duration(n))
		store.Close()
	}
}
