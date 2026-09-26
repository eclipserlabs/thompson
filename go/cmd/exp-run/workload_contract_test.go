package main

import (
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

func fptr(v float64) *float64 { return &v }

func validAttempt(id string, seq uint, exec string, cost *float64) outcome.Attempt {
	return outcome.Attempt{
		AttemptID: id, Seq: seq, ExecutorID: exec, ArmID: exec,
		Transport: outcome.TransportOK, LatencyMs: 100, CostUSD: cost,
		Validation: outcome.ValidationPass, Verified: outcome.VerifiedSuccess,
		VerifiedBy: "checker:independent-v1",
	}
}

func baseRecord() CustomerRecord {
	return CustomerRecord{
		JobID: "job-1", Strata: "support", Eligible: []string{"t0", "t2"},
		Executed: "t2", AssignProb: fptr(0.5), Assigner: "assigner:hash-v1",
		Version: 1, Supersedes: 0, Status: outcome.StatusAccepted,
		Attempts:          []outcome.Attempt{validAttempt("job-1-a0", 0, "cheap", fptr(0.01))},
		DecidingAttemptID: "job-1-a0",
		VerifiedBy:        "checker:independent-v1",
		Provenance:        "customer:export-v3",
	}
}

func TestValidateRecordAcceptsCompleteRow(t *testing.T) {
	if err := ValidateRecord(baseRecord()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRecordTable(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CustomerRecord)
	}{
		{"missing job id", func(r *CustomerRecord) { r.JobID = "" }},
		{"zero version", func(r *CustomerRecord) { r.Version = 0 }},
		{"bad chain", func(r *CustomerRecord) { r.Version, r.Supersedes = 2, 0 }},
		{"bad status", func(r *CustomerRecord) { r.Status = "WINNING" }},
		{"settled without decider", func(r *CustomerRecord) { r.DecidingAttemptID = "" }},
		{"settled without verifier", func(r *CustomerRecord) { r.VerifiedBy = "" }},
		{"decider not in attempts", func(r *CustomerRecord) { r.DecidingAttemptID = "nope" }},
		{"duplicate attempts", func(r *CustomerRecord) {
			r.Attempts = append(r.Attempts, r.Attempts[0])
		}},
		{"noncontiguous seq", func(r *CustomerRecord) { r.Attempts[0].Seq = 7 }},
		{"unknown transport", func(r *CustomerRecord) { r.Attempts[0].Transport = "teleport" }},
		{"negative cost", func(r *CustomerRecord) { r.Attempts[0].CostUSD = fptr(-1) }},
		{"bad assign prob", func(r *CustomerRecord) { r.AssignProb = fptr(2) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := baseRecord()
			tc.mutate(&r)
			if err := ValidateRecord(r); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestValidateRecordKeepsMissingDataValid(t *testing.T) {
	r := baseRecord()
	r.Strata = ""
	r.Eligible = nil
	r.Attempts[0].CostUSD = nil // missing cost is explicitly missing, not invalid
	r.Status = outcome.StatusUnknown
	r.DecidingAttemptID = ""
	r.VerifiedBy = ""
	if err := ValidateRecord(r); err != nil {
		t.Fatalf("missing data must stay valid: %v", err)
	}
}

func TestFullyLoadedCostPropagatesMissing(t *testing.T) {
	r := baseRecord()
	r.Attempts = append(r.Attempts, validAttempt("job-1-a1", 1, "strong", nil))
	metered, unmetered := FullyLoadedCost(r)
	if metered != 0.01 || unmetered != 1 {
		t.Fatalf("metered=%.3f unmetered=%d", metered, unmetered)
	}
}

func TestHasFallbackAndSelfVerified(t *testing.T) {
	r := baseRecord()
	if HasFallback(r) {
		t.Fatal("single attempt is not a fallback")
	}
	r.Attempts = append(r.Attempts, validAttempt("job-1-a1", 1, "strong", fptr(0.02)))
	if !HasFallback(r) {
		t.Fatal("two executors must read as fallback/retries")
	}
	if SelfVerified(r) {
		t.Fatal("independent verifier must not read as self-verified")
	}
	r.VerifiedBy = "cheap"
	if !SelfVerified(r) {
		t.Fatal("verifier == executor must read as self-verified")
	}
}
