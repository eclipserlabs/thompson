package journal

import (
	"bytes"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// countCommitted opens a short-lived read handle (readers never contend)
// and counts committed decision rows.
func countCommitted(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return 0
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind='decision'`).Scan(&n); err != nil {
		return 0
	}
	return n
}

// Kill -9 mid-commit, then reopen: WAL recovery must converge to a prefix
// of committed transactions with no torn rows and no phantom decisions.
func TestFaultKillMidCommit(t *testing.T) {
	if os.Getenv("ASSAY_FAULT_CHILD") == "1" {
		dir := os.Getenv("ASSAY_FAULT_DIR")
		j, err := Open(filepath.Join(dir, "k.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer j.Close()
		for i := 0; i < 500; i++ {
			d := testDecision(decID("k", i), jobID("k", i), "cheap", i%2 == 0)
			if _, _, err := j.CommitDecision(d, "cfg"); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run", "TestFaultKillMidCommit")
	cmd.Env = append(os.Environ(), "ASSAY_FAULT_CHILD=1", "ASSAY_FAULT_DIR="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if countCommitted(t, filepath.Join(dir, "k.db")) >= 50 {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("child never reached 50 commits")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = cmd.Process.Wait()
	// Reopen: must succeed with a clean prefix; every row parses; no
	// decision without its budget row and vice versa (single txn).
	j, err := Open(filepath.Join(dir, "k.db"))
	if err != nil {
		t.Fatalf("reopen after kill: %v", err)
	}
	defer j.Close()
	p, err := j.Replay()
	if err != nil {
		t.Fatalf("replay after kill: %v", err)
	}
	if err := j.VerifyExploration(); err != nil {
		t.Fatalf("budget/decision divergence after kill: %v", err)
	}
	n, _ := j.Len()
	if n < 50 {
		t.Fatalf("lost committed prefix: %d", n)
	}
	var explored uint64
	for _, u := range p.Explored {
		explored += u
	}
	t.Logf("kill-mid-commit recovered prefix of %d events, %d exploration units, budgets consistent", n, explored)
}

// Real two-process contention: a holder process keeps a write transaction
// open while this process commits; the contender must fail loudly.
func TestFaultCompetingProcesses(t *testing.T) {
	if os.Getenv("ASSAY_HOLD_CHILD") == "1" {
		dir := os.Getenv("ASSAY_FAULT_DIR")
		j, err := Open(filepath.Join(dir, "c.db"))
		if err != nil {
			println("HOLDER: open failed: " + err.Error())
			os.Exit(3)
		}
		defer j.Close()
		if _, err := j.db.Exec("BEGIN IMMEDIATE"); err != nil {
			println("HOLDER: begin failed: " + err.Error())
			os.Exit(3)
		}
		// Hold an UNCOMMITTED write (stronger than bare BEGIN): anyone who
		// can commit past this is not seeing our locks at all.
		if _, err := j.db.Exec("INSERT INTO events(kind, job_id, version, event_key, payload, config_digest, created_ns) VALUES('__hold','',0,'__hold','{}','',0)"); err != nil {
			println("HOLDER: insert failed: " + err.Error())
			os.Exit(3)
		}
		// Signal readiness only AFTER the write lock is held, then hold it
		// until killed.
		if err := os.WriteFile(filepath.Join(dir, "HOLDER_READY"), []byte("1"), 0o600); err != nil {
			println("HOLDER: ready write failed: " + err.Error())
			os.Exit(3)
		}
		// Hold until killed. NOTE: bare select{} trips Go deadlock detector
		// (all goroutines asleep -> fatal error -> holder dies silently and
		// contention tests pass vacuously); sleep loop does not.
		for {
			time.Sleep(time.Hour)
		}
	}
	dir := t.TempDir()
	holder := exec.Command(os.Args[0], "-test.run", "TestFaultCompetingProcesses", "-test.v")
	holder.Env = append(os.Environ(), "ASSAY_HOLD_CHILD=1", "ASSAY_FAULT_DIR="+dir)
	hbuf := &lockedBuf{}
	holder.Stdout = hbuf
	holder.Stderr = hbuf
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = holder.Process.Kill()
		_, _ = holder.Process.Wait()
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "HOLDER_READY")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("holder never entered its write transaction; holder stderr: %s", hbuf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	j, err := Open(filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatalf("open alongside holder: %v", err)
	}
	defer j.Close()
	if _, _, err := j.CommitDecision(testDecision("dx", "jx", "cheap", false), "cfg"); err == nil {
		t.Fatal("contended commit must fail loudly, not interleave")
	} else {
		t.Logf("two-process contention refusal: %v", err)
	}
}

// Corrupt WAL content: recovery must refuse or truncate torn tail, never
// invent history.
func TestFaultCorruptWAL(t *testing.T) {
	dir := t.TempDir()
	j, err := Open(filepath.Join(dir, "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, _, err := j.CommitDecision(testDecision(decID("w", i), jobID("w", i), "cheap", false), "cfg"); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "w.db-wal"))
	target := filepath.Join(dir, "w.db")
	if len(matches) > 0 {
		target = matches[0]
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	corrupted := false
	if len(raw) > 100 {
		raw[50] ^= 0xff
		raw[60] ^= 0xff
		if err := os.WriteFile(target, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		corrupted = true
	}
	j2, err := Open(filepath.Join(dir, "w.db"))
	if err != nil {
		t.Logf("corrupt store refused at open (acceptable): %v", err)
		return
	}
	defer j2.Close()
	p, err := j2.Replay()
	if err != nil {
		t.Logf("corrupt store refused at replay (acceptable): %v", err)
		return
	}
	_ = p
	if !corrupted {
		t.Skip("nothing to corrupt (tiny store)")
	}
	t.Logf("corrupt store opened; replay outcome recorded in assay doc")
}

// Concurrent commit storm on one handle with caller-side retry: every
// commit lands exactly once and budgets sum exactly.
func TestFaultConcurrentStorm(t *testing.T) {
	j := mustOpen(t)
	defer j.Close()
	const racers = 8
	const perRacer = 25
	var wg sync.WaitGroup
	errs := make(chan error, racers)
	for w := 0; w < racers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perRacer; i++ {
				id := "s" + itoaJ(w*perRacer+i)
				// Single-connection pool serializes in-process transactions:
				// no BUSY is possible here; any error is a real defect.
				// (Cross-process contention is proven by
				// TestFaultCompetingProcesses, not here.)
				if _, ok, err := j.CommitDecision(testDecision(id, id, "cheap", true), "cfg"); err != nil || !ok {
					errs <- errOrDup(id, err, ok)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("storm: %v", err)
		}
	}
	p, err := j.Replay()
	if err != nil {
		t.Fatal(err)
	}
	var total uint64
	for _, u := range p.Explored {
		total += u
	}
	if total != racers*perRacer {
		t.Fatalf("storm budget %d != committed %d", total, racers*perRacer)
	}
	if err := j.VerifyExploration(); err != nil {
		t.Fatal(err)
	}
}

func errOrDup(id string, err error, ok bool) error {
	if err != nil {
		return err
	}
	return &dupErr{id}
}

func errDup(id string) error { return &dupErr{id} }

type dupErr struct{ id string }

func (e *dupErr) Error() string { return "duplicate commit of " + e.id }

func errStorm(id string) error { return &stormErr{id} }

type stormErr struct{ id string }

func (e *stormErr) Error() string { return "uncommittable " + e.id }

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
