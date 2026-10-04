package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/harness"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// Supervised experiment integrity under intervention + real crash: phase 1
// runs in a SUBPROCESS (so --crash-after SIGKILLs gateways and exits 3
// without killing the test); the test suspends cheap mid-run by polling
// progress, then resumes in-process to completion.
func TestSupervisedIntegrityUnderIntervention(t *testing.T) {
	dir := t.TempDir()
	m := generateSupervisedManifest(20260901, 48)
	mPath := filepath.Join(dir, "manifest.json")
	writeJSON(t, mPath, m)
	pubBase, settleBase := nextPortBases()
	ports := func(base int) string {
		return fmt.Sprintf("%d,%d,%d,%d", base, base+1, base+2, base+3)
	}
	scb, _ := json.Marshal(supervisedSafetyFor([]string{"cheap", "strong"}, "strong"))
	opToken := "integrity-op"
	mkRunner := func() *Runner {
		r, err := OpenRunner(RunnerConfig{
			Manifest: m, Root: dir, RouterBin: routerBin(t),
			PubPorts:    []int{pubBase, pubBase + 1, pubBase + 2, pubBase + 3},
			SettlePorts: []int{settleBase, settleBase + 1, settleBase + 2, settleBase + 3},
			Token:       "e2e-token",
			Timeout:     20 * time.Second,
			T0Clock:     time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), Step: 60 * time.Second,
			SelectionSeed: 31337, SafetyConfigs: map[string][]byte{"t3": scb}, OperatorToken: opToken,
		})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	// Phase 1: real subprocess crash after 34 jobs. The binary is built, not
	// 'go run': go run masks the program exit code, and the test must
	// observe exactly exit 3.
	expBin := filepath.Join(dir, "exp-run-bin")
	build := exec.Command("go", "build", "-o", expBin, "./cmd/exp-run")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build exp-run: %v %s", err, out)
	}
	cmd := exec.Command(expBin, "supervised",
		"--manifest", mPath, "--root", dir, "--router-bin", routerBin(t),
		"--pub-ports", ports(pubBase), "--settle-ports", ports(settleBase),
		"--token", "e2e-token", "--selection-seed", "31337",
		"--operator-token", opToken,
		"--seed", "20260901", "--n", "48",
		"--t0", "2026-01-05T00:00:00Z", "--step-seconds", "60",
		"--crash-after", "34", "--demo-stop=false")
	var cmdErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		out, err := cmd.CombinedOutput()
		cmdErr = err
		t.Logf("phase1 output tail: %s", tailLines(string(out), 3))
	}()
	// Suspend cheap once 18 jobs complete (deterioration third active).
	suspended := false
	deadline := time.Now().Add(5 * time.Minute)
	for !suspended && time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		n := countTerminalOrProgress(dir)
		if n >= 18 {
			code, err := operatorCall(
				fmt.Sprintf("http://127.0.0.1:%d/health", pubBase+3),
				fmt.Sprintf("http://127.0.0.1:%d/v1/operator/suspend", settleBase+3),
				"cheap", "integrity: deterioration watch", opToken, "e2e-op")
			if err != nil || code != 200 {
				t.Fatalf("suspend: %v code=%d", err, code)
			}
			suspended = true
		}
		select {
		case <-done:
		default:
		}
	}
	if !suspended {
		t.Fatal("subprocess finished before suspension point")
	}
	<-done
	if cmdErr == nil {
		t.Fatal("phase 1 should have crashed (exit 3)")
	}
	if exitErr, ok := cmdErr.(*exec.ExitError); !ok || exitErr.ExitCode() != 3 {
		t.Fatalf("phase 1 exit: %v", cmdErr)
	}
	// Phase 2: reboot fresh processes and resume in-process to completion.
	r2 := mkRunner()
	defer r2.Shutdown()
	if err := r2.Boot(); err != nil {
		t.Fatalf("reboot: %v", err)
	}
	if err := r2.Run(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	// 1. Assignments authoritative: every randomized job exactly once,
	// treatment never changes across the crash.
	seen := map[string]string{}
	dups := 0
	for _, tx := range []string{"t0", "t1", "t2", "t3"} {
		asg, _, err := harness.LoadTreatmentDir(filepath.Join(dir, tx))
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range asg {
			if prev, ok := seen[a.JobID]; ok {
				if prev != a.Treatment {
					t.Fatalf("job %s treatment changed %s -> %s", a.JobID, prev, a.Treatment)
				}
				dups++
				continue
			}
			seen[a.JobID] = a.Treatment
		}
	}
	if len(seen) != len(m.Jobs) {
		t.Fatalf("assigned %d/%d jobs", len(seen), len(m.Jobs))
	}
	if dups != 0 {
		t.Fatalf("%d duplicate assignment rows", dups)
	}
	// 2. No outcome version stored twice in any ledger.
	for _, tx := range []string{"t0", "t1", "t2", "t3"} {
		_, evs, err := harness.LoadTreatmentDir(filepath.Join(dir, tx))
		if err != nil {
			t.Fatal(err)
		}
		vers := map[string]uint64{}
		for _, e := range evs {
			if v, ok := vers[e.JobID]; ok && v == e.Version {
				t.Fatalf("%s: job %s version %d stored twice", tx, e.JobID, v)
			}
			if e.Version > vers[e.JobID] {
				vers[e.JobID] = e.Version
			}
		}
	}
	// 3. Suspension survived the crash.
	if !safetyEventSeen(t, filepath.Join(dir, "t3", "safety.jsonl"), "ARM_SUSPENDED") {
		t.Fatal("suspension event missing after crash+resume")
	}
	// 4. Post-resume t3 decisions avoid cheap (assigned t3 ≠ executed cheap).
	decs := readDecisions(t, filepath.Join(dir, "t3", "decisions.jsonl"))
	for _, d := range decs[len(decs)*2/3:] {
		if d.SelectedArmID == "cheap" {
			t.Fatalf("suspended arm selected post-resume: %s", d.DecisionID)
		}
	}
	// 5. Report accounts every randomized job and renders a verdict, using
	// the same jobmap-joined path as the exp-report CLI (gateway outcome
	// IDs join to manifest assignments; identity mapping would strand
	// every record as unmatched).
	now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC).Add(time.Duration(len(m.Jobs)) * time.Minute).Add(25 * time.Hour)
	var allAssign []harness.Assignment
	var allEvents []outcome.OutcomeEvent
	jobMaps := map[string]harness.JobMap{}
	txNames := []string{"t0", "t1", "t2", "t3"}
	for _, tx := range txNames {
		asg, evs, err := harness.LoadTreatmentDir(filepath.Join(dir, tx))
		if err != nil {
			t.Fatal(err)
		}
		allAssign = append(allAssign, asg...)
		allEvents = append(allEvents, evs...)
		jm, err := harness.LoadJobMap(filepath.Join(dir, tx))
		if err != nil {
			t.Fatal(err)
		}
		jobMaps[tx] = jm
	}
	rep, err := harness.BuildReport(allAssign, allEvents, txNames, "t0", "t3", []string{"t1", "t2"}, harness.ReportConfig{
		Maturation: 24 * time.Hour, Now: now, MinJobs: 5, CensorGate: 0.5,
		QualityFloor: 0.3, MinEffect: 0.05, BootstrapN: 100, BootstrapSeed: 9,
		MaxUnmeteredShare: 0.10,
	}, jobMaps)
	if err != nil {
		t.Fatalf("report build: %v", err)
	}
	assigned := 0
	for _, st := range rep.Treatments {
		assigned += st.Assigned
	}
	if assigned != len(m.Jobs) {
		t.Fatalf("report assigned=%d want %d (interventions excluded?)", assigned, len(m.Jobs))
	}
	t.Logf("integrity verdict=%s comparisons=%d", rep.Verdict, len(rep.Comparisons))
	for _, c := range rep.Comparisons {
		if c.MeetsBar && rep.Verdict != "RANKABLE" {
			t.Fatalf("unsupported bar claim outside rankable verdict: %+v", c)
		}
	}
}

func countTerminalOrProgress(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, "progress.jsonl"))
	if err != nil {
		return 0
	}
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, `"phase":"settled-v1"`) || strings.Contains(l, `"terminal":true`) {
			n++
		}
	}
	return n
}

func operatorCall(healthURL, opURL, arm, reason, token, op string) (int, error) {
	// Bind to the live gateway instance first: /health reports
	// X-Instance-ID, which this call must present as X-Expect-Instance.
	// (This also proves cross-process identity binding works.)
	inst, err := fetchInstanceID(healthURL)
	if err != nil {
		return 0, err
	}
	body, _ := json.Marshal(map[string]string{"arm": arm, "reason": reason})
	req, err := http.NewRequest(http.MethodPost, opURL, strings.NewReader(string(body)))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token+":"+op)
	req.Header.Set("X-Expect-Instance", inst)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func fetchInstanceID(healthURL string) (string, error) {
	resp, err := http.DefaultClient.Get(healthURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("health %d", resp.StatusCode)
	}
	id := resp.Header.Get("X-Instance-ID")
	if id == "" {
		return "", fmt.Errorf("health reports no instance identity")
	}
	return id, nil
}

func tailLines(s string, n int) string {
	ls := strings.Split(s, "\n")
	if len(ls) <= n {
		return s
	}
	return strings.Join(ls[len(ls)-n:], "\n")
}
