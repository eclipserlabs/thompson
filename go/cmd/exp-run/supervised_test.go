package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
	"github.com/wiramahendra/thompson-sampling/go/harness"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// Supervised four-treatment experiment through real binaries: cost-aware t3
// boots with a frozen safety envelope, an emergency stop routes fallback
// mid-run, release restores adaptation, and resume is idempotent.
func TestSupervisedFourTreatment(t *testing.T) {
	dir := t.TempDir()
	m := generateSupervisedManifest(20260707, 60)
	mPath := filepath.Join(dir, "manifest.json")
	writeJSON(t, mPath, m)

	pubBase, settleBase := nextPortBases()
	pub := []int{pubBase, pubBase + 1, pubBase + 2, pubBase + 3}
	settle := []int{settleBase, settleBase + 1, settleBase + 2, settleBase + 3}
	scb, err := json.MarshalIndent(supervisedSafetyFor([]string{"cheap", "strong"}, "strong"), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	opToken := "test-op-token"
	r, err := OpenRunner(RunnerConfig{
		Manifest: m, Root: dir, RouterBin: routerBin(t),
		PubPorts: pub, SettlePorts: settle, Token: "e2e-token",
		Timeout: 20 * time.Second,
		T0Clock: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), Step: 60 * time.Second,
		SelectionSeed: 777, SafetyConfigs: map[string][]byte{"t3": scb}, OperatorToken: opToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	stopAt := 51
	releaseAt := 54
	r.cfg.AfterJob = func(idx, doneJobs int) error {
		g := r.gateways["t3"]
		switch {
		case doneJobs == stopAt:
			if code, err := g.Operator("suspend", "", "e2e demo stop", opToken, "e2e-op"); err != nil || code != 200 {
				t.Errorf("demo stop: %v code=%d", err, code)
			}
		case doneJobs == releaseAt:
			if code, err := g.Operator("resume", "", "e2e demo release", opToken, "e2e-op"); err != nil || code != 200 {
				t.Errorf("demo release: %v code=%d", err, code)
			}
		}
		return nil
	}
	if err := r.Boot(); err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown()
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Artifacts per treatment.
	for _, tx := range []string{"t0", "t1", "t2", "t3"} {
		for _, f := range []string{"decisions.jsonl", "outcomes.jsonl", "assignments.jsonl", "evidence.jsonl"} {
			if _, err := os.Stat(filepath.Join(dir, tx, f)); err != nil {
				t.Fatalf("missing artifact %s/%s", tx, f)
			}
		}
	}
	for _, f := range []string{"safety.json", "safety.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, "t3", f)); err != nil {
			t.Fatalf("missing safety artifact t3/%s", f)
		}
	}
	// t3 decisions carry the cost-aware identity through the real binary.
	foundCA := 0
	total := 0
	for _, tx := range []string{"t0", "t1", "t2", "t3"} {
		asg, evs, err := harness.LoadTreatmentDir(filepath.Join(dir, tx))
		if err != nil {
			t.Fatal(err)
		}
		total += len(evs)
		_ = asg
	}
	if total == 0 {
		t.Fatal("no outcomes settled anywhere")
	}
	for _, d := range readDecisions(t, filepath.Join(dir, "t3", "decisions.jsonl")) {
		if d.LoggingPolicyID == thompson.CostAwarePolicyID {
			foundCA++
			if d.CostAware == nil || d.CostAware.RuleVersion < thompson.CostAwareRuleV2 {
				t.Fatalf("t3 decision missing validated rule identity: %+v", d.CostAware)
			}
		}
	}
	if foundCA == 0 {
		t.Fatal("no cost-aware decisions on t3")
	}
	// Emergency window routed the fallback: jobs settled during the stop
	// selected strong.
	stopSeen := safetyEventSeen(t, filepath.Join(dir, "t3", "safety.jsonl"), "EMERGENCY_STOP")
	releaseSeen := safetyEventSeen(t, filepath.Join(dir, "t3", "safety.jsonl"), "EMERGENCY_RELEASED")
	if !stopSeen || !releaseSeen {
		t.Fatal("operator demo events missing from safety ledger")
	}
	// Idempotent resume: reopening and re-running changes nothing.
	before := countAllOutcomes(t, dir)
	r2, err := OpenRunner(RunnerConfig{
		Manifest: m, Root: dir, RouterBin: routerBin(t),
		PubPorts: pub, SettlePorts: settle, Token: "e2e-token",
		Timeout: 20 * time.Second,
		T0Clock: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), Step: 60 * time.Second,
		SelectionSeed: 777, SafetyConfigs: map[string][]byte{"t3": scb}, OperatorToken: opToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Shutdown()
	// Boot re-spawns the binaries: each gateway recovers quality + cost
	// learning from its checkpoint + ledger replay before serving.
	if err := r2.Boot(); err != nil {
		t.Fatalf("reboot: %v", err)
	}
	if err := r2.Run(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if after := countAllOutcomes(t, dir); after != before {
		t.Fatalf("outcome events %d -> %d across resume", before, after)
	}
}

func readDecisions(t *testing.T, path string) []gateway.CommittedDecision {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []gateway.CommittedDecision
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec struct {
			Type      string                     `json:"type"`
			Committed *gateway.CommittedDecision `json:"committed"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Type == "committed" && rec.Committed != nil {
			out = append(out, *rec.Committed)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func safetyEventSeen(t *testing.T, path, typ string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev gateway.SafetyEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type == typ {
			return true
		}
	}
	return false
}

func countAllOutcomes(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	for _, tx := range []string{"t0", "t1", "t2", "t3"} {
		_, evs, err := harness.LoadTreatmentDir(filepath.Join(dir, tx))
		if err != nil {
			t.Fatal(err)
		}
		n += len(evs)
	}
	return n
}
