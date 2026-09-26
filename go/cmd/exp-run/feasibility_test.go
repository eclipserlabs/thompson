package main

import (
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

func settledJob(id, strata string, exec string, cost *float64, verifiedBy string) CustomerRecord {
	a := outcome.Attempt{
		AttemptID: id + "-a0", Seq: 0, ExecutorID: exec, ArmID: exec,
		Transport: outcome.TransportOK, LatencyMs: 120, CostUSD: cost,
		Validation: outcome.ValidationPass, Verified: outcome.VerifiedSuccess,
		VerifiedBy: verifiedBy,
	}
	return CustomerRecord{
		JobID: id, Strata: strata, Eligible: []string{"t0", "t2"},
		Executed: "t2", AssignProb: fptr(0.5), Assigner: "assigner:hash-v1",
		Version: 1, Supersedes: 0, Status: outcome.StatusAccepted,
		Attempts: []outcome.Attempt{a}, DecidingAttemptID: id + "-a0",
		VerifiedBy: verifiedBy, Provenance: "customer:export-v3",
	}
}

func TestAssessCoreCounts(t *testing.T) {
	recs := []CustomerRecord{
		settledJob("j1", "support", "cheap", fptr(0.10), "checker:independent-v1"),
		settledJob("j2", "support", "cheap", fptr(0.20), "checker:independent-v1"),
		settledJob("j3", "billing", "cheap", fptr(0.30), "checker:independent-v1"),
		{
			JobID: "j4", Strata: "billing",
			Version: 1, Supersedes: 0, Status: outcome.StatusUnknown,
			Attempts: []outcome.Attempt{{
				AttemptID: "j4-a0", Seq: 0, ExecutorID: "cheap", ArmID: "cheap",
				Transport: outcome.TransportTimeout, LatencyMs: 500,
				Validation: outcome.ValidationNotRun, Verified: outcome.VerifiedUnknown,
			}},
			Provenance: "customer:export-v3",
		},
	}
	rep := Assess(recs, nil, "customer:acme-2026-09")
	if rep.UniqueJobs != 4 || rep.SettledJobs != 3 {
		t.Fatalf("jobs=%d settled=%d", rep.UniqueJobs, rep.SettledJobs)
	}
	if rep.UnknownJobs != 1 || rep.UnknownPct != 25 {
		t.Fatalf("unknown=%d pct=%.1f", rep.UnknownJobs, rep.UnknownPct)
	}
	if rep.FullyMeteredJobs != 3 || rep.IncompleteCostJobs != 1 {
		t.Fatalf("metered=%d incomplete=%d", rep.FullyMeteredJobs, rep.IncompleteCostJobs)
	}
	if rep.VarianceN != 3 || !approxEqual(rep.VarianceMean, 0.20) {
		t.Fatalf("variance n=%d mean=%.6f", rep.VarianceN, rep.VarianceMean)
	}
	if !rep.SizingPossible || len(rep.RequiredPerGroup) != 3 {
		t.Fatalf("sizing possible=%v groups=%v", rep.SizingPossible, rep.RequiredPerGroup)
	}
	// j4 lacks eligibility and assignment provenance, so the export as a
	// whole cannot support valid comparisons.
	if rep.Comparable {
		t.Fatal("observational history must not read as comparable")
	}
	if len(rep.Blockers) == 0 {
		t.Fatal("expected blockers for a non-randomized export")
	}
}

func TestAssessUnknownNeverConverted(t *testing.T) {
	recs := []CustomerRecord{{
		JobID: "u1", Strata: "s", Version: 1, Supersedes: 0,
		Status: outcome.StatusUnknown,
		Attempts: []outcome.Attempt{{
			AttemptID: "u1-a0", Seq: 0, ExecutorID: "cheap", ArmID: "cheap",
			Transport: outcome.TransportTimeout, LatencyMs: 500, CostUSD: nil,
			Validation: outcome.ValidationNotRun, Verified: outcome.VerifiedUnknown,
		}},
	}}
	rep := Assess(recs, nil, "src")
	if rep.SettledJobs != 0 || rep.UnknownJobs != 1 {
		t.Fatalf("UNKNOWN converted: settled=%d unknown=%d", rep.SettledJobs, rep.UnknownJobs)
	}
	if rep.FullyMeteredJobs != 0 {
		t.Fatal("unmetered timeout must not read as metered")
	}
}

func TestAssessCorrectionsCollapseToLatest(t *testing.T) {
	v1 := settledJob("c1", "s", "cheap", fptr(0.10), "checker:independent-v1")
	v2 := v1
	v2.Version, v2.Supersedes = 2, 1
	v2.Status = outcome.StatusRejected
	v2.Attempts[0].Verified = outcome.VerifiedFailure
	rep := Assess([]CustomerRecord{v1, v2}, nil, "src")
	if rep.UniqueJobs != 1 || rep.SettledJobs != 1 {
		t.Fatalf("jobs=%d settled=%d", rep.UniqueJobs, rep.SettledJobs)
	}
	if rep.IndependentlyVerified != 1 {
		t.Fatalf("verified=%d", rep.IndependentlyVerified)
	}
	// Conflicting duplicate of the same version.
	dup := v1
	dup.Status = outcome.StatusRejected
	rep2 := Assess([]CustomerRecord{v1, dup}, nil, "src")
	if rep2.StableUniqueIDs || rep2.DuplicateConflicts != 1 {
		t.Fatalf("stable=%v conflicts=%d", rep2.StableUniqueIDs, rep2.DuplicateConflicts)
	}
	// Version gap.
	gap := v2 // version 2 without version 1
	rep3 := Assess([]CustomerRecord{gap}, nil, "src")
	if rep3.VersionGaps != 1 {
		t.Fatalf("gaps=%d", rep3.VersionGaps)
	}
}

func TestAssessComparableNeedsEverything(t *testing.T) {
	r := settledJob("k1", "s", "cheap", fptr(0.10), "checker:independent-v1")
	rep := Assess([]CustomerRecord{r}, nil, "src")
	if !rep.Comparable {
		t.Fatalf("fully-provenanced record must compare: %+v", rep.ComparisonConditions)
	}
	noAssign := r
	noAssign.AssignProb = nil
	noAssign.Assigner = ""
	if rep := Assess([]CustomerRecord{noAssign}, nil, "src"); rep.Comparable {
		t.Fatal("missing assignment provenance must block comparisons")
	}
	self := r
	self.VerifiedBy = "cheap"
	if rep := Assess([]CustomerRecord{self}, nil, "src"); rep.Comparable {
		t.Fatal("self-verified history must block comparisons")
	}
}

func TestAssessDeterministic(t *testing.T) {
	recs := []CustomerRecord{
		settledJob("j2", "b", "cheap", fptr(0.20), "checker:independent-v1"),
		settledJob("j1", "a", "cheap", fptr(0.10), "checker:independent-v1"),
	}
	a := Assess(recs, nil, "src")
	b := Assess([]CustomerRecord{recs[1], recs[0]}, nil, "src")
	a.CreatedAt, b.CreatedAt = "", ""
	if a.Summary() != b.Summary() {
		t.Fatal("assessment must be order-independent")
	}
	// Sizing is a pure function of observed variance: reproducible.
	for k, v := range a.RequiredPerGroup {
		if b.RequiredPerGroup[k] != v {
			t.Fatalf("sizing not reproducible for %s", k)
		}
	}
}

func approxEqual(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
