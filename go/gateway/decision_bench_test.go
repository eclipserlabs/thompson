package gateway

import (
	"fmt"
	"path/filepath"
	"testing"
)

func BenchmarkDecisionCommit(b *testing.B) {
	s, err := NewFileDecisionStore(filepath.Join(b.TempDir(), "bench.jsonl"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	base := testCommittedDecision("base")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d := base
		d.DecisionID = fmt.Sprintf("d-%d", i)
		d.JobID = "job-" + d.DecisionID
		if _, _, err := s.Commit(d); err != nil {
			b.Fatal(err)
		}
	}
}
