package bench

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// S4 durability ablation: minimal (memory only) vs logged (append+fsync
// rows) vs full (outcome store + learner + checkpoint). Measures per-settle
// cost and records the recovery capability of each layer.
func TestS4DurabilityAblation(t *testing.T) {
	if testing.Short() {
		t.Skip("ablation timing skipped in short mode")
	}
	const n = 200
	rng := rand.New(rand.NewPCG(21, 21))
	jobs := make([]struct {
		arm string
		win bool
	}, n)
	for i := range jobs {
		if rng.Float64() < 0.5 {
			jobs[i] = struct {
				arm string
				win bool
			}{"a", rng.Float64() < 0.7}
		} else {
			jobs[i] = struct {
				arm string
				win bool
			}{"b", rng.Float64() < 0.6}
		}
	}
	// Minimal: memory only.
	minimal := NewMinimalPolicy("a", "b")
	t0 := time.Now()
	for i, j := range jobs {
		_ = i
		if err := minimal.Observe(j.arm, j.win); err != nil {
			t.Fatal(err)
		}
	}
	minPer := time.Since(t0) / n
	// Logged: file + fsync per row.
	dir := t.TempDir()
	logged, err := OpenLoggedPolicy(filepath.Join(dir, "learn.jsonl"), "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	t1 := time.Now()
	for i, j := range jobs {
		if err := logged.Observe("job-"+itoaBench(i), j.arm, j.win); err != nil {
			t.Fatal(err)
		}
	}
	logPer := time.Since(t1) / n
	// Full: outcome store + learner + checkpoint.
	full, err := outcome.NewFileOutcomeStore(filepath.Join(dir, "o.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer full.Close()
	fpol := textbookPolicy("a", "b")
	full_learner := outcome.NewLearner(fpol, outcome.BinaryStatusMapper{}, full.Events)
	t2 := time.Now()
	for i, j := range jobs {
		ev := benchOutcome("fjob-"+itoaBench(i), j.arm, 1, j.win, 0.01)
		if _, err := outcome.Settle(full, full_learner, ev); err != nil {
			t.Fatal(err)
		}
	}
	fullPer := time.Since(t2) / n
	cpStart := time.Now()
	cp := full_learner.CheckpointOf(full.Len())
	cpSize := len(mustJSON(t, cp))
	cpLat := time.Since(cpStart)
	// Storage per settled job per layer.
	fi, _ := os.Stat(filepath.Join(dir, "learn.jsonl"))
	logBytes := float64(fi.Size()) / n
	fi2, _ := os.Stat(filepath.Join(dir, "o.jsonl"))
	fullBytes := float64(fi2.Size()) / n
	t.Logf("S4 per-settle: minimal=%s logged=%s full=%s checkpoint=%s (%dB)",
		minPer, logPer, fullPer, cpLat, cpSize)
	t.Logf("S4 bytes/job: logged=%.0f full=%.0f", logBytes, fullBytes)
	t.Logf("S4 recovery: minimal=NONE (state lost) logged=replay-only (no corrections) full=checkpoint+replay+corrections")
	if logPer < minPer {
		t.Fatal("logged cannot be cheaper than minimal")
	}
	_ = logged.Close()
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
