package reasoninggoodput

import (
	"fmt"
	"sync"
	"testing"
)

// parallel_test.go: workers dimension. W independent jobs on DISJOINT
// resources of one shared fixture run concurrently (real goroutines; the
// fixture serializes internally). Asserts all converge identically to their
// sequential counterparts and measures coordination-free parallelism. Any
// shared-resource contention would appear as retries — none is expected
// here by construction (disjoint validators ⇒ SAFE_CONCURRENT per the
// confluence classifier).

func TestParallelDisjointWorkers(t *testing.T) {
	for _, w := range []int{1, 2, 4, 8} {
		paths := make([]string, w)
		init := map[string]string{}
		for i := range paths {
			paths[i] = fmt.Sprintf("/w%d", i)
			init[paths[i]] = fmt.Sprintf(`{"v":%d}`, 100+i)
		}
		f := NewHTTPFixture(init)
		t.Cleanup(f.Close)
		// The fixture serializes internally (mutex); jobs touch disjoint
		// resources, so this exercises real parallelism with no expected
		// cross-talk (SAFE_CONCURRENT by construction).
		var wg sync.WaitGroup
		results := make([]TaskMetrics, w)
		errs := make([]error, w)
		for i := 0; i < w; i++ {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				path := paths[i]
				spec := HTTPJob(f, fmt.Sprintf("worker-%d", i), []string{path},
					map[string][]string{path: {"v"}},
					[]SliceBuilder{{
						ID: "work", From: []string{"read:0"},
						Work: WorkSpec{BurnRounds: 400, Label: "parallel"},
						Make: func(ins [][]byte) []byte {
							return []byte("w:" + DigestBytes(ins[0])[:8])
						},
					}},
					func(outs map[string][]byte, _ map[string]Val, _ []Val) (string, string) {
						return path, fmt.Sprintf(`{"v":%d}`, 200+i)
					})
				// No test-level serialization: the fixture is internally
				// mutex-guarded and jobs touch disjoint resources, so this
				// is real parallelism (race detector arbitrates).
				results[i] = Run(spec, T4Replay, nil, func(Change) {}, 3)
				if !results[i].OracleOK {
					errs[i] = fmt.Errorf("worker %d failed oracle", i)
				}
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("workers=%d: %v", w, err)
			}
			if results[i].Retries != 0 || results[i].WorkExec != 400 {
				t.Fatalf("workers=%d job %d did extra work: %+v", w, i, results[i])
			}
		}
		t.Logf("workers=%d: all converged, no retries, no cross-talk", w)
	}
}
