package outcome

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"syscall"
)

// OutcomeStore is the durable, append-only ledger for versioned job outcomes.
// Submit is idempotent per (job, version) and linearizes concurrent writers:
// exactly one outcome version per job is latest, versions increase by exactly
// 1, and a conflicting re-submission of an existing version is rejected.
type OutcomeStore interface {
	// Submit validates, assigns Seq, persists, and returns applied=true when
	// the event is newly committed. An exact duplicate returns applied=false
	// and nil error. Stale or conflicting versions return an error and commit
	// nothing.
	Submit(ev OutcomeEvent) (applied bool, err error)
	// Latest returns the highest committed version for a job.
	Latest(jobID string) (OutcomeEvent, bool)
	// Events returns all committed events in ledger (Seq) order.
	Events() []OutcomeEvent
	// Len returns the number of committed events.
	Len() int
}

// eventsEqual compares two events for duplicate detection, ignoring Seq
// (assigned by the store, not part of the event identity).
func eventsEqual(a, b OutcomeEvent) bool {
	ac, bc := a, b
	ac.Seq, bc.Seq = 0, 0
	ab, err := json.Marshal(ac)
	if err != nil {
		return false
	}
	bb, err := json.Marshal(bc)
	if err != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}

// MemoryOutcomeStore is an in-memory OutcomeStore for tests and harness use.
// It enforces the same version-ordering and idempotency rules as the file
// store.
type MemoryOutcomeStore struct {
	mu     sync.Mutex
	events []OutcomeEvent
	latest map[string]int // jobID -> index into events
}

// NewMemoryOutcomeStore creates an empty in-memory store.
func NewMemoryOutcomeStore() *MemoryOutcomeStore {
	return &MemoryOutcomeStore{latest: make(map[string]int)}
}

// Submit implements OutcomeStore.
func (s *MemoryOutcomeStore) Submit(ev OutcomeEvent) (bool, error) {
	if err := ev.Validate(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.submitLocked(ev)
}

func (s *MemoryOutcomeStore) submitLocked(ev OutcomeEvent) (bool, error) {
	if idx, ok := s.latest[ev.JobID]; ok {
		cur := s.events[idx]
		switch {
		case ev.Version == cur.Version && eventsEqual(ev, cur):
			return false, nil // exact duplicate: idempotent no-op
		case ev.Version == cur.Version:
			return false, fmt.Errorf("outcome: conflicting version %d for job %q", ev.Version, ev.JobID)
		case ev.Version <= cur.Version:
			return false, fmt.Errorf("outcome: stale version %d for job %q (latest %d)", ev.Version, ev.JobID, cur.Version)
		case ev.Version != cur.Version+1:
			return false, fmt.Errorf("outcome: version gap for job %q: latest %d, got %d", ev.JobID, cur.Version, ev.Version)
		}
	} else if ev.Version != 1 {
		return false, fmt.Errorf("outcome: first version for job %q must be 1, got %d", ev.JobID, ev.Version)
	}
	ev.Seq = uint64(len(s.events) + 1)
	s.events = append(s.events, ev)
	s.latest[ev.JobID] = len(s.events) - 1
	return true, nil
}

// Latest implements OutcomeStore.
func (s *MemoryOutcomeStore) Latest(jobID string) (OutcomeEvent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.latest[jobID]
	if !ok {
		return OutcomeEvent{}, false
	}
	return s.events[idx], true
}

// Events implements OutcomeStore.
func (s *MemoryOutcomeStore) Events() []OutcomeEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]OutcomeEvent, len(s.events))
	copy(out, s.events)
	return out
}

// Len implements OutcomeStore.
func (s *MemoryOutcomeStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// FileOutcomeStore is a file-backed, append-only JSONL OutcomeStore. Each
// committed event is one JSON line followed by file.Sync, so a Submit that
// returns applied=true is durable on the local filesystem.
//
// Single-writer operation is enforced with an exclusive, non-blocking flock
// held for the store's lifetime: a second opener fails fast instead of
// silently forking the ledger. Multi-writer coordination is not implemented
// (non-goal); run one writer per ledger file.
type FileOutcomeStore struct {
	mu       sync.Mutex
	file     *os.File
	path     string
	events   []OutcomeEvent
	latest   map[string]int
	tornTail bool
}

// NewFileOutcomeStore opens (or creates) the JSONL ledger at path, takes the
// single-writer lock, and recovers committed events. A torn trailing line
// from a crash mid-append is truncated away and reported via TornTail.
func NewFileOutcomeStore(path string) (*FileOutcomeStore, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("outcome: open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("outcome: ledger %s is already held by another writer (single-writer enforced): %w", path, err)
	}
	s := &FileOutcomeStore{file: f, path: path, latest: make(map[string]int)}
	if err := s.recover(); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		return nil, err
	}
	return s, nil
}

// recover replays the file into memory, truncating a torn tail.
func (s *FileOutcomeStore) recover() error {
	st, err := s.file.Stat()
	if err != nil {
		return fmt.Errorf("outcome: stat %s: %w", s.path, err)
	}
	if st.Size() == 0 {
		return nil
	}
	if _, err := s.file.Seek(0, 0); err != nil {
		return fmt.Errorf("outcome: seek %s: %w", s.path, err)
	}
	scanner := bufio.NewScanner(s.file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)
	var offset int64
	torn := false
	for scanner.Scan() {
		line := scanner.Bytes()
		offset += int64(len(line)) + 1 // + newline
		if len(line) == 0 {
			continue
		}
		var ev OutcomeEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			torn = true
			break
		}
		if err := ev.Validate(); err != nil {
			return fmt.Errorf("outcome: ledger %s holds invalid event at ~offset %d: %w", s.path, offset, err)
		}
		// Recovery replays history verbatim: Seq is reassigned in file order
		// so a torn tail never shifts the sequence of surviving events.
		ev.Seq = uint64(len(s.events) + 1)
		s.events = append(s.events, ev)
		s.latest[ev.JobID] = len(s.events) - 1
	}
	if err := scanner.Err(); err != nil {
		// bufio.ErrTooLong or I/O: a torn tail, not a valid prefix break.
		torn = true
	}
	if torn {
		if err := s.file.Truncate(offset); err != nil {
			return fmt.Errorf("outcome: truncate torn tail %s: %w", s.path, err)
		}
		if _, err := s.file.Seek(0, 2); err != nil {
			return fmt.Errorf("outcome: seek end %s: %w", s.path, err)
		}
		if err := s.file.Sync(); err != nil {
			return fmt.Errorf("outcome: sync %s: %w", s.path, err)
		}
		s.tornTail = true
	}
	return nil
}

func (s *FileOutcomeStore) submitLocked(ev OutcomeEvent) (bool, error) {
	// Same ordering rules as memory; kept as a separate method so the
	// in-memory state and the file can never disagree on admission.
	if idx, ok := s.latest[ev.JobID]; ok {
		cur := s.events[idx]
		switch {
		case ev.Version == cur.Version && eventsEqual(ev, cur):
			return false, nil
		case ev.Version == cur.Version:
			return false, fmt.Errorf("outcome: conflicting version %d for job %q", ev.Version, ev.JobID)
		case ev.Version <= cur.Version:
			return false, fmt.Errorf("outcome: stale version %d for job %q (latest %d)", ev.Version, ev.JobID, cur.Version)
		case ev.Version != cur.Version+1:
			return false, fmt.Errorf("outcome: version gap for job %q: latest %d, got %d", ev.JobID, cur.Version, ev.Version)
		}
	} else if ev.Version != 1 {
		return false, fmt.Errorf("outcome: first version for job %q must be 1, got %d", ev.JobID, ev.Version)
	}
	ev.Seq = uint64(len(s.events) + 1)
	b, err := json.Marshal(ev)
	if err != nil {
		return false, fmt.Errorf("outcome: marshal: %w", err)
	}
	b = append(b, '\n')
	if _, err := s.file.Write(b); err != nil {
		return false, fmt.Errorf("outcome: write %s: %w", s.path, err)
	}
	if err := s.file.Sync(); err != nil {
		return false, fmt.Errorf("outcome: sync %s: %w", s.path, err)
	}
	s.events = append(s.events, ev)
	s.latest[ev.JobID] = len(s.events) - 1
	return true, nil
}

// Submit implements OutcomeStore: validate, persist, then index. The event is
// indexed in memory only after the write+sync succeeds, so a crash before
// persistence leaves no trace to replay.
func (s *FileOutcomeStore) Submit(ev OutcomeEvent) (bool, error) {
	if err := ev.Validate(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.submitLocked(ev)
}

// Latest implements OutcomeStore.
func (s *FileOutcomeStore) Latest(jobID string) (OutcomeEvent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.latest[jobID]
	if !ok {
		return OutcomeEvent{}, false
	}
	return s.events[idx], true
}

// Events implements OutcomeStore.
func (s *FileOutcomeStore) Events() []OutcomeEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]OutcomeEvent, len(s.events))
	copy(out, s.events)
	return out
}

// Len implements OutcomeStore.
func (s *FileOutcomeStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// TornTail reports whether recovery truncated a torn trailing write.
func (s *FileOutcomeStore) TornTail() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tornTail
}

// Close releases the single-writer lock and closes the ledger.
func (s *FileOutcomeStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	_ = syscall.Flock(int(s.file.Fd()), syscall.LOCK_UN)
	err := s.file.Close()
	s.file = nil
	return err
}
