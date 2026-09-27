package gateway

import (
	"path/filepath"
	"sync"
	"testing"
)

// R1 repro: two SafetyStore opens on the same path must not both succeed.
// flock(LOCK_EX|LOCK_NB) is enforced by the kernel per open-file-description,
// so an in-process double-open exercises the exact same mechanism as two
// gateway processes (cf. TestFileStoreSingleWriterEnforced precedent).
func TestSafetyStoreSecondWriterRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "safety.jsonl")
	a, err := NewSafetyStore(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	defer a.Close()
	if _, err := NewSafetyStore(path); err == nil {
		t.Fatal("R1: second safety writer opened without error (no OS lock)")
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	b, err := NewSafetyStore(path)
	if err != nil {
		t.Fatalf("R1: open after close: %v", err)
	}
	_ = b.Close()
}

// R1 corruption demo: without a writer lock, two concurrent appenders assign
// duplicate sequence numbers, so recovery folds a different history than the
// operators actually produced.
func TestSafetyStoreNoDuplicateSeqUnderConcurrency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "safety.jsonl")
	a, err := NewSafetyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewSafetyStore(path)
	if err != nil {
		// With the lock fixed, the second open fails and there is nothing
		// left to prove here.
		t.Skip("second writer correctly rejected")
	}
	defer b.Close()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(s *SafetyStore) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				_ = s.Append(SafetyEvent{Actor: "race", Type: SafetyEmergencyStop, Reason: "r"})
			}
		}([]*SafetyStore{a, b}[i])
	}
	wg.Wait()
	evs, err := a.Events()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uint64]int{}
	for _, ev := range evs {
		seen[ev.Seq]++
	}
	for seq, n := range seen {
		if n > 1 {
			t.Fatalf("R1: seq %d assigned %d times across writers", seq, n)
		}
	}
	if len(evs) != 50 {
		t.Fatalf("R1: lost events under concurrent writers: %d/50", len(evs))
	}
}
