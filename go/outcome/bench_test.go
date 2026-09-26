package outcome

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

func BenchmarkFileSubmit(b *testing.B) {
	s, err := NewFileOutcomeStore(filepath.Join(b.TempDir(), "bench.jsonl"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	ev := settledJob("job", "d", "cheap", StatusAccepted, 1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e := ev
		e.JobID = fmt.Sprintf("job-%d", i)
		if _, err := s.Submit(e); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRebuild5k(b *testing.B) {
	p := thompson.NewDefault("cheap", "strong")
	store := NewMemoryOutcomeStore()
	const n = 5000
	for i := 0; i < n; i++ {
		ev := settledJob(fmt.Sprintf("job-%d", i), fmt.Sprintf("d-%d", i), "cheap", StatusAccepted, 1)
		if _, err := store.Submit(ev); err != nil {
			b.Fatal(err)
		}
	}
	l := NewLearner(p, BinaryStatusMapper{}, store.Events)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := l.Rebuild(store.Events()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCheckpointSave(b *testing.B) {
	p := thompson.NewDefault("cheap", "strong")
	l := NewLearner(p, BinaryStatusMapper{}, nil)
	path := filepath.Join(b.TempDir(), "ckpt.json")
	cp := l.CheckpointOf(0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := SaveCheckpoint(path, cp); err != nil {
			b.Fatal(err)
		}
	}
}
