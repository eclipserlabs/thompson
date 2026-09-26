package main

import (
	"fmt"
	"sync"
	"testing"
)

// Regression for the cluster.go test-scaffolding race: SpawnGateway handed a
// plain bytes.Buffer to os/exec as Cmd.Stderr while the health-poll loop
// called stderr.String() on failure. The exec writer goroutines and the
// reader raced (TestE2EMissingStorageFailsClosed under -race).
// syncBuffer must tolerate concurrent Write/String without racing and must
// preserve every written byte.
func TestSyncBufferConcurrentUse(t *testing.T) {
	var b syncBuffer
	const writers = 8
	const perWriter = 200
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				fmt.Fprintf(&b, "w%d-%d;", w, i)
			}
		}(w)
	}
	// Concurrent readers mimic the SpawnGateway failure path sampling
	// stderr while the child is still writing.
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				_ = b.String()
			}
		}
	}()
	wg.Wait()
	close(done)
	got := b.String()
	want := writers * perWriter
	count := 0
	for i := 0; i < len(got); i++ {
		if got[i] == ';' {
			count++
		}
	}
	if count != want {
		t.Fatalf("syncBuffer lost writes: got %d records want %d", count, want)
	}
}
