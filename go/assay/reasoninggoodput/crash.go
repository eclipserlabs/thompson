package reasoninggoodput

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// crash.go: Phase 9 durable records + resume. Every slice completion,
// validation verdict, and commit is appended to a JSONL log as it happens.
// A crash (injected via RunOpts.CrashAfter* ) leaves a durable prefix;
// ResumeFromLog rebuilds slice state from the log and continues, always
// revalidating before any commit. Resume never upgrades an unvalidated
// slice: reuse still requires premise validation against live witnesses.

// LogRec is one durable record.
type LogRec struct {
	Seq    int             `json:"seq"`
	Type   string          `json:"type"` // slice | validate | commit | change
	Key    string          `json:"key,omitempty"`
	Idx    int             `json:"idx,omitempty"`
	Slice  *ReasoningSlice `json:"slice,omitempty"`
	Valid  bool            `json:"valid,omitempty"`
	Accept bool            `json:"accept,omitempty"`
	Final  string          `json:"final,omitempty"`
	Treat  string          `json:"treat,omitempty"`
	Job    string          `json:"job,omitempty"`
}

type durableLog struct {
	mu   sync.Mutex
	file *os.File
	seq  int
}

func openLog(path string) (*durableLog, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &durableLog{file: f}, nil
}

func (l *durableLog) append(rec LogRec) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	rec.Seq = l.seq
	l.seq++
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	line = append(line, '\n')
	_, _ = l.file.Write(line)
	_ = l.file.Sync()
}

func (l *durableLog) close() {
	if l == nil || l.file == nil {
		return
	}
	_ = l.file.Close()
}

// crashTrip counts slice executions and fires once at the configured point.
type crashTrip struct {
	afterExecs int // crash after this many executions; <0 disables... 0 means before first
	beforeAll  bool
	fired      bool
	count      int
	crashed    bool
}

func (c *crashTrip) noteExec() bool {
	if c == nil || c.fired {
		return false
	}
	c.count++
	if !c.beforeAll && c.count >= c.afterExecs && c.afterExecs > 0 {
		c.fired = true
		c.crashed = true
		return true
	}
	return false
}

type crashError struct{}

func (crashError) Error() string { return "injected crash" }

// readLog replays a records file into slice cache + commit outcome +
// already-fired change targets (so resume never re-applies environment moves).
func readLog(path string) (cache map[string]ReasoningSlice, commit *LogRec, fired map[int]bool, err error) {
	cache = map[string]ReasoningSlice{}
	fired = map[int]bool{}
	f, err := os.Open(path)
	if err != nil {
		return cache, nil, fired, nil // no log yet: cold resume
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4<<20), 4<<20)
	for sc.Scan() {
		var r LogRec
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		switch r.Type {
		case "slice":
			if r.Slice != nil {
				cache[r.Key] = *r.Slice
			}
		case "commit":
			cp := r
			commit = &cp
		case "change":
			fired[r.Idx] = true
		}
	}
	return cache, commit, fired, sc.Err()
}

// ResumeFromLog continues a crashed run: rebuilds slice state from the
// durable log, filters already-fired environment changes (never re-applies
// the world), revalidates everything against live witnesses, and resumes.
// A logged commit short-circuits to the recorded outcome (no blind redo).
func ResumeFromLog(logPath string, spec JobSpec, treat TreatID, schedule []Change, applier func(Change), maxRetries int) (TaskMetrics, error) {
	cache, commit, fired, err := readLog(logPath)
	if err != nil {
		return TaskMetrics{}, fmt.Errorf("resume: %w", err)
	}
	if commit != nil {
		m := TaskMetrics{Treatment: string(treat), OracleOK: commit.Accept}
		return m, nil
	}
	var pending []Change
	for i, ch := range schedule {
		if !fired[i] {
			pending = append(pending, ch)
		}
	}
	m := runWithCache(spec, treat, pending, applier, maxRetries, RunOpts{LogPath: logPath}, cache)
	return m, nil
}
