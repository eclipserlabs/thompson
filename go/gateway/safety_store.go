package gateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

func hoursBetween(a, b string) float64 {
	t1, err1 := time.Parse(time.RFC3339Nano, a)
	t2, err2 := time.Parse(time.RFC3339Nano, b)
	if err1 != nil || err2 != nil {
		t1, err1 = time.Parse(time.RFC3339, a)
		t2, err2 = time.Parse(time.RFC3339, b)
		if err1 != nil || err2 != nil {
			return -1
		}
	}
	return t2.Sub(t1).Hours()
}

// SafetyStore is the durable, single-writer safety event log. Append-only
// with per-write fsync; a second writer fails loudly via non-blocking flock
// instead of interleaving operator actions. Failed() latches on any write
// error so the controller fails closed instead of running blind.
type SafetyStore struct {
	mu     sync.Mutex
	f      *os.File
	path   string
	failed bool
	seq    uint64
}

// NewSafetyStore opens (creating) the event log and indexes existing events.
func NewSafetyStore(path string) (*SafetyStore, error) {
	if err := os.MkdirAll(dirOf(path), 0o700); err != nil {
		return nil, fmt.Errorf("safety: mkdir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("safety: open: %w", err)
	}
	s := &SafetyStore{f: f, path: path}
	evs, err := s.readAll()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	for _, ev := range evs {
		if ev.Seq >= s.seq {
			s.seq = ev.Seq + 1
		}
	}
	return s, nil
}

// Append persists one event with fsync before returning. Any error latches
// Failed: the controller then admits fallback-or-nothing until restart with
// a healthy store.
func (s *SafetyStore) Append(ev SafetyEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return fmt.Errorf("safety: store already failed")
	}
	ev.Seq = s.seq
	b, err := json.Marshal(ev)
	if err != nil {
		s.failed = true
		return fmt.Errorf("safety: marshal: %w", err)
	}
	b = append(b, '\n')
	if _, err := s.f.Write(b); err != nil {
		s.failed = true
		return fmt.Errorf("safety: write: %w", err)
	}
	if err := s.f.Sync(); err != nil {
		s.failed = true
		return fmt.Errorf("safety: sync: %w", err)
	}
	s.seq++
	return nil
}

// Events replays the whole log (small: operator/monitor actions only).
func (s *SafetyStore) Events() ([]SafetyEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readAll()
}

// Failed reports the latched failure state.
func (s *SafetyStore) Failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failed
}

// Close syncs and closes the log.
func (s *SafetyStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.f.Sync(); err != nil {
		return err
	}
	return s.f.Close()
}

func (s *SafetyStore) readAll() ([]SafetyEvent, error) {
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("safety: read: %w", err)
	}
	defer f.Close()
	var out []SafetyEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev SafetyEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("safety: bad event line: %w", err)
		}
		out = append(out, ev)
	}
	return out, sc.Err()
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}
