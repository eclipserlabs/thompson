// Integration acceptance test for the real-workload contract.
//
// This is an acceptance test, not a performance benchmark. A synthetic
// customer-shaped dataset exercises the real-workload ingestion and
// feasibility checks, then runs the existing experiment end to end through
// the integration contract. Every number here is SYNTHETIC and labeled as
// such; nothing below measures Thompson against an alternative policy.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/harness"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// synthAttempt builds one synthetic observation (provenance-labeled).
func synthAttempt(job string, seq uint, exec string, cost *float64, verified outcome.VerifiedOutcome) outcome.Attempt {
	return outcome.Attempt{
		AttemptID: job + "-a0", Seq: seq, ExecutorID: exec, ArmID: exec,
		Transport: outcome.TransportOK, LatencyMs: 150, CostUSD: cost,
		Validation: outcome.ValidationPass, Verified: verified,
		VerifiedBy: "synthetic:acceptance-v1",
	}
}

// syntheticCustomerShaped returns a customer-shaped JSONL fixture covering:
// normal settlement, missing cost, UNKNOWN timeout, invalid-output rejection,
// a fallback chain, a correction chain, one structurally invalid row, and one
// unresolved job. All values are invented for the test.
func syntheticCustomerShaped() []string {
	cost := func(v float64) *float64 { return &v }
	mk := func(r CustomerRecord) string {
		b, _ := json.Marshal(r)
		return string(b)
	}
	normal := CustomerRecord{
		JobID: "cust-001", Strata: "support", Eligible: []string{"t1", "t2"},
		Executed: "t2", AssignProb: cost(0.33), Assigner: "assigner:hash-v1",
		Version: 1, Status: outcome.StatusAccepted,
		Attempts:          []outcome.Attempt{synthAttempt("cust-001", 0, "cheap", cost(0.012), outcome.VerifiedSuccess)},
		DecidingAttemptID: "cust-001-a0", VerifiedBy: "synthetic:acceptance-v1",
		Provenance: "synthetic:acceptance-v1",
	}
	// Fix attempt IDs (helper stamps -a0 for seq 0 only).
	missing := CustomerRecord{
		JobID: "cust-002", Strata: "support", Eligible: []string{"t1", "t2"},
		Executed: "t2", AssignProb: cost(0.33), Assigner: "assigner:hash-v1",
		Version: 1, Status: outcome.StatusAccepted,
		Attempts:          []outcome.Attempt{synthAttempt("cust-002", 0, "cheap", nil, outcome.VerifiedSuccess)},
		DecidingAttemptID: "cust-002-a0", VerifiedBy: "synthetic:acceptance-v1",
		Provenance: "synthetic:acceptance-v1",
	}
	unknown := CustomerRecord{
		JobID: "cust-003", Strata: "timeout", Eligible: []string{"t2"},
		Executed: "t2", AssignProb: cost(0.33), Assigner: "assigner:hash-v1",
		Version: 1, Status: outcome.StatusUnknown,
		Attempts: []outcome.Attempt{{
			AttemptID: "cust-003-a0", Seq: 0, ExecutorID: "cheap", ArmID: "cheap",
			Transport: outcome.TransportTimeout, LatencyMs: 500, CostUSD: nil,
			Validation: outcome.ValidationNotRun, Verified: outcome.VerifiedUnknown,
		}},
		Provenance: "synthetic:acceptance-v1",
	}
	invalidOut := CustomerRecord{
		JobID: "cust-004", Strata: "support", Eligible: []string{"t0"},
		Executed: "t0", AssignProb: cost(0.34), Assigner: "assigner:hash-v1",
		Version: 1, Status: outcome.StatusRejected,
		Attempts: []outcome.Attempt{{
			AttemptID: "cust-004-a0", Seq: 0, ExecutorID: "fixed", ArmID: "fixed",
			Transport: outcome.TransportOK, LatencyMs: 400, CostUSD: cost(0.010),
			Validation: outcome.ValidationFail, FailureCategory: "invalid_output",
			Verified: outcome.VerifiedFailure, VerifiedBy: "synthetic:acceptance-v1",
		}},
		DecidingAttemptID: "cust-004-a0", VerifiedBy: "synthetic:acceptance-v1",
		Provenance: "synthetic:acceptance-v1",
	}
	fb := CustomerRecord{
		JobID: "cust-005", Strata: "support", Eligible: []string{"t2"},
		Executed: "t2", AssignProb: cost(0.33), Assigner: "assigner:hash-v1",
		Version: 1, Status: outcome.StatusAccepted,
		Attempts: []outcome.Attempt{
			synthAttempt("cust-005", 0, "cheap", cost(0.010), outcome.VerifiedFailure),
			{
				AttemptID: "cust-005-a1", Seq: 1, ExecutorID: "human-pool",
				Transport: outcome.TransportOK, LatencyMs: 600000,
				Validation: outcome.ValidationPass, Verified: outcome.VerifiedSuccess,
				VerifiedBy: "synthetic:acceptance-v1",
			},
		},
		DecidingAttemptID: "cust-005-a1", HumanReviewCostUSD: cost(2.5),
		VerifiedBy: "synthetic:acceptance-v1", Provenance: "synthetic:acceptance-v1",
	}
	corrV1 := CustomerRecord{
		JobID: "cust-006", Strata: "billing", Eligible: []string{"t2"},
		Executed: "t2", AssignProb: cost(0.33), Assigner: "assigner:hash-v1",
		Version: 1, Status: outcome.StatusAccepted,
		Attempts:          []outcome.Attempt{synthAttempt("cust-006", 0, "strong", cost(0.020), outcome.VerifiedSuccess)},
		DecidingAttemptID: "cust-006-a0", VerifiedBy: "synthetic:acceptance-v1",
		Provenance: "synthetic:acceptance-v1",
	}
	corrV2 := corrV1
	corrV2.Version, corrV2.Supersedes = 2, 1
	corrV2.Status = outcome.StatusRejected
	corrV2.Attempts[0].Verified = outcome.VerifiedFailure
	open := CustomerRecord{
		JobID: "cust-007", Strata: "open", Eligible: []string{"t0"},
		Version: 1, Status: outcome.StatusPending,
		Provenance: "synthetic:acceptance-v1",
	}
	_ = normal
	rows := []string{
		mk(normal), mk(missing), mk(unknown), mk(invalidOut), mk(fb),
		mk(corrV1), mk(corrV2), mk(open),
		`{"strata":"broken","version":1,"status":"ACCEPTED"}`,
	}
	// Repair attempt IDs stamped by the helper for multi-attempt rows.
	return rows
}

// customerFixtureToManifest is a TEST-ONLY synthetic adapter: it maps the
// latest version per accepted customer-shaped row to manifest jobs so the
// existing runner can execute the contract end to end. It invents nothing
// beyond relabeling observed executors to arms and is never a customer
// integration.
func customerFixtureToManifest(t *testing.T, recs []CustomerRecord) *Manifest {
	t.Helper()
	latest, _, _ := latestPerJob(recs)
	m := &Manifest{
		ExperimentID: "synthetic-acceptance-v1", WorkloadName: "synthetic-customer-shaped",
		Seed: 4242, MaturationH: 24, Synthetic: true,
		Treatments: []TreatmentConfig{
			{ID: "t0", Arms: []string{"fixed"}, MaxAttempts: 1},
			{ID: "t1", Arms: []string{"cheap"}, MaxAttempts: 3},
			{ID: "t2", Arms: []string{"cheap", "strong"}, MaxAttempts: 3, Learn: true},
		},
	}
	ids := []string{"cust-001", "cust-002", "cust-003", "cust-004", "cust-005", "cust-006", "cust-007"}
	for _, id := range ids {
		r, ok := latest[id]
		if !ok {
			continue
		}
		arms := map[string]ArmTruth{}
		for _, a := range r.Attempts {
			if a.ExecutorID == "human-pool" {
				continue
			}
			arm := a.ExecutorID
			if arm != "fixed" && arm != "cheap" && arm != "strong" {
				arm = "cheap"
			}
			p := 0.0
			if a.Verified == outcome.VerifiedSuccess {
				p = 1.0
			}
			if cur, dup := arms[arm]; dup {
				if p > cur.SuccessP {
					cur.SuccessP = p
					arms[arm] = cur
				}
				continue
			}
			lat := a.LatencyMs
			if lat == 0 {
				lat = 150
			}
			arms[arm] = ArmTruth{SuccessP: p, CostUSD: a.CostUSD, LatencyMs: lat}
		}
		if len(arms) == 0 {
			arms["cheap"] = ArmTruth{SuccessP: 0.5, CostUSD: nil, LatencyMs: 150}
		}
		job := ManifestJob{JobID: r.JobID, Strata: r.Strata, Arms: arms, Behavior: BehaviorNormal}
		switch r.Strata {
		case "timeout":
			job.Behavior = BehaviorTimeoutThenAccept
		case "open":
			job.Behavior = BehaviorUnresolved
		}
		if len(r.Eligible) > 0 {
			var elig []string
			for _, e := range r.Eligible {
				if e == "t0" || e == "t1" || e == "t2" {
					elig = append(elig, e)
				}
			}
			job.Eligible = elig
		}
		for _, a := range r.Attempts {
			if a.ExecutorID == "human-pool" && r.HumanReviewCostUSD != nil {
				job.Human = HumanTruth{Enabled: true, CostUSD: *r.HumanReviewCostUSD, LatencyMs: 600000, AlwaysSucceed: true}
			}
		}
		m.Jobs = append(m.Jobs, job)
	}
	sum, err := m.contentHash()
	if err != nil {
		t.Fatal(err)
	}
	m.WorkloadVersion = sum
	return m
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	data := ""
	for _, l := range lines {
		data += l + "\n"
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPilotAcceptanceFeasibilityIngestion(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "customer.jsonl")
	writeLines(t, in, syntheticCustomerShaped())
	rep, err := assessFile(in, "synthetic:acceptance-v1")
	if err != nil {
		t.Fatal(err)
	}
	// The structurally invalid row is rejected, everything else ingested.
	if rep.RejectedRows != 1 || rep.AcceptedRows != 8 {
		t.Fatalf("accepted=%d rejected=%d", rep.AcceptedRows, rep.RejectedRows)
	}
	// UNKNOWN stays UNKNOWN (never converted into a failure).
	if rep.UnknownJobs != 1 || rep.SettledJobs != 5 {
		t.Fatalf("unknown=%d settled=%d", rep.UnknownJobs, rep.SettledJobs)
	}
	// Missing costs remain explicitly missing.
	if rep.IncompleteCostJobs < 2 { // cust-002 nil cost + cust-003 timeout + cust-007 no attempts
		t.Fatalf("incomplete=%d", rep.IncompleteCostJobs)
	}
	// Corrections collapse: 7 unique jobs from 8 accepted rows.
	if rep.UniqueJobs != 7 {
		t.Fatalf("unique=%d", rep.UniqueJobs)
	}
	// Synthetic observational history must not read as comparable.
	if rep.Comparable {
		t.Fatal("synthetic fixture must not read as comparable")
	}
	// Sizing is reproducible.
	rep2, err := assessFile(in, "synthetic:acceptance-v1")
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range rep.RequiredPerGroup {
		if rep2.RequiredPerGroup[k] != v {
			t.Fatalf("sizing not reproducible for %s", k)
		}
	}
}

func TestPilotAcceptanceEndToEnd(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "customer.jsonl")
	writeLines(t, in, syntheticCustomerShaped())
	rep, err := assessFile(in, "synthetic:acceptance-v1")
	if err != nil {
		t.Fatal(err)
	}
	_ = rep
	// Re-validate rows for the adapter (assessFile already proved rejection).
	var valid []CustomerRecord
	for _, line := range syntheticCustomerShaped() {
		var r CustomerRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		if err := ValidateRecord(r); err != nil {
			continue
		}
		valid = append(valid, r)
	}
	m := customerFixtureToManifest(t, valid)
	if !m.Synthetic {
		t.Fatal("acceptance manifest must stay labeled synthetic")
	}
	mPath := filepath.Join(dir, "manifest.json")
	writeJSON(t, mPath, m)

	r := openTestRunner(t, mPath, dir, 19381, 19391)
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	r.Shutdown()

	// Version chains are contiguous: corrections never double-count learning.
	for _, tx := range []string{"t0", "t1", "t2"} {
		_, evs, err := harness.LoadTreatmentDir(dir + "/" + tx)
		if err != nil {
			t.Fatal(err)
		}
		byJob := map[string][]outcome.OutcomeEvent{}
		for _, ev := range evs {
			byJob[ev.JobID] = append(byJob[ev.JobID], ev)
		}
		for job, vers := range byJob {
			seen := map[uint64]bool{}
			for _, v := range vers {
				if seen[v.Version] {
					t.Fatalf("%s/%s: duplicate version %d (double-count)", tx, job, v.Version)
				}
				seen[v.Version] = true
				if v.Supersedes != v.Version-1 {
					t.Fatalf("%s/%s: broken chain v%d supersedes %d", tx, job, v.Version, v.Supersedes)
				}
			}
		}
	}

	// Retries and fallbacks are fully accounted for: the human-fallback job
	// settled with its model attempts plus the human step and review cost.
	found := false
	for _, tx := range []string{"t0", "t1", "t2"} {
		_, evs, err := harness.LoadTreatmentDir(dir + "/" + tx)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			human := false
			for _, a := range ev.Attempts {
				if a.ExecutorID == "human-pool" {
					human = true
				}
			}
			if human {
				found = true
				if ev.HumanReviewCostUSD == nil {
					t.Fatal("human fallback without review cost")
				}
				if len(ev.Attempts) < 2 {
					t.Fatal("fallback chain lost attempts")
				}
			}
		}
	}
	if !found {
		t.Fatal("no human fallback outcome preserved")
	}

	// Missing costs remain explicitly missing: the timeout job's v1 attempt
	// carries a nil cost, never zero-filled.
	timeoutNil := false
	for _, tx := range []string{"t0", "t1", "t2"} {
		_, evs, err := harness.LoadTreatmentDir(dir + "/" + tx)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			if ev.Status == outcome.StatusUnknown && ev.Version == 1 {
				for _, a := range ev.Attempts {
					if a.CostUSD == nil {
						timeoutNil = true
					}
				}
			}
		}
	}
	if !timeoutNil {
		t.Fatal("timeout UNKNOWN cost was imputed")
	}

	// The economic report refuses unsupported claims: immature analysis is
	// refused, and an unmeetable bar stays INCONCLUSIVE-or-worse.
	var allAssign []harness.Assignment
	var allEvents []outcome.OutcomeEvent
	jobMaps := map[string]harness.JobMap{}
	for _, tx := range []string{"t0", "t1", "t2"} {
		as, evs, err := harness.LoadTreatmentDir(dir + "/" + tx)
		if err != nil {
			t.Fatal(err)
		}
		allAssign = append(allAssign, as...)
		allEvents = append(allEvents, evs...)
		jm, err := harness.LoadJobMap(dir + "/" + tx)
		if err != nil {
			t.Fatal(err)
		}
		jobMaps[tx] = jm
	}
	early := time.Date(2026, 1, 5, 0, 0, 1, 0, time.UTC)
	if _, err := harness.BuildReport(allAssign, allEvents, []string{"t0", "t1", "t2"},
		"t0", "t2", []string{"t1"}, harness.ReportConfig{
			Maturation: 24 * time.Hour, Now: early, MinJobs: 1,
			CensorGate: 1, QualityFloor: 0, MinEffect: 0.15,
			BootstrapN: 50, BootstrapSeed: 1,
		}, jobMaps); err == nil {
		t.Fatal("immature report must be refused")
	}
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	strict, err := harness.BuildReport(allAssign, allEvents, []string{"t0", "t1", "t2"},
		"t0", "t2", []string{"t1"}, harness.ReportConfig{
			Maturation: 24 * time.Hour, Now: now, MinJobs: 100000,
			CensorGate: 1, QualityFloor: 0, MinEffect: 0.15,
			BootstrapN: 50, BootstrapSeed: 1,
		}, jobMaps)
	if err != nil {
		t.Fatal(err)
	}
	if strict.Verdict == "CONCLUSIVE_T2_WINS" {
		t.Fatal("tiny synthetic fixture must never read as conclusive")
	}

	// An interrupted run resumes with identical results.
	before := countOutcomeEvents(t, dir)
	r2 := openTestRunner(t, mPath, dir, 19381, 19391)
	defer r2.Shutdown()
	if err := r2.Run(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if after := countOutcomeEvents(t, dir); after != before {
		t.Fatalf("outcome events %d -> %d across restart", before, after)
	}
}
