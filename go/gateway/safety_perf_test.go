package gateway

import (
		"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// Operational measurements for the supervised single-writer deployment
// (darwin arm64 here; Linux numbers go in the ops doc when measured there).
// Run with: go test -run TestSafetyPerf -v ./gateway/
func TestSafetyPerf(t *testing.T) {
	if testing.Short() {
		t.Skip("perf measurement skipped in short mode")
	}
	f := newSafetyFixture(t, safetyTestConfig())
	defer f.close(t)

	// Warm up: settle 20 metered jobs so estimators + monitor have state.
	for i := 0; i < 20; i++ {
		did, jid, arm, code := f.serve(t)
		if code != 200 {
			t.Fatalf("serve %d", code)
		}
		c := 0.05
		if arm == "cheap" {
			c = 0.002
		}
		if code := f.settleMetered(t, did, jid, arm, c, true); code != 200 {
			t.Fatalf("settle %d", code)
		}
	}

	// 1. Decision latency (selection + safety mask + budget reserve).
	start := time.Now()
	const selIters = 500
	for i := 0; i < selIters; i++ {
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
		if rec.Code != 200 {
			t.Fatalf("serve %d", rec.Code)
		}
	}
	decLat := time.Since(start) / selIters

	// 2. Monitoring overhead: one ObserveSettlement fold over the ledger.
	evs := f.outStore.Events()
	mstart := time.Now()
	const monIters = 50
	for i := 0; i < monIters; i++ {
		_ = armHealth(evs, "cheap", 20)
		_ = armHealth(evs, "strong", 20)
	}
	monLat := time.Since(mstart) / monIters

	// 3. Checkpoint duration (quality + cost sidecar).
	cstart := time.Now()
	if err := f.router.CheckpointVerifiedLearning(filepath.Join(f.dir, "ck.json")); err != nil {
		t.Fatal(err)
	}
	cpLat := time.Since(cstart)

	// 4. Recovery time (quality resume + cost rebuild + safety refold).
	rstart := time.Now()
	if err := f.router.RecoverVerifiedLearning(filepath.Join(f.dir, "ck.json")); err != nil {
		t.Fatal(err)
	}
	recLat := time.Since(rstart)

	// 5. Memory delta across selections.
	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	for i := 0; i < 200; i++ {
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
	}
	runtime.ReadMemStats(&m1)

	// 6. Disk growth: per-decision and per-outcome ledger costs.
	// (Evidence uses an in-memory writer in this fixture; production adds
	// evidence.jsonl rows of similar size to decisions.)
	decSize := fileSize(t, filepath.Join(f.dir, "d.jsonl"))
	outSize := fileSize(t, filepath.Join(f.dir, "o.jsonl"))
	safSize := fileSize(t, filepath.Join(f.dir, "safety.jsonl"))
	nDec := float64(f.decStore.Len())
	nOut := float64(len(f.outStore.Events()))
	_ = thompson.CostAwarePolicyID
	t.Logf("decision=%s monitor_fold=%s checkpoint=%s recovery=%s mem_per_200sel=%dKB per_decision=%.0fB per_outcome=%.0fB safety_total=%.0fB",
		decLat, monLat, cpLat, recLat, (m1.Alloc-m0.Alloc)/1024,
		float64(decSize)/nDec, float64(outSize)/nOut, float64(safSize))
	if decLat > 50*time.Millisecond {
		t.Fatalf("decision latency too high: %s", decLat)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}
