package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/harness"
)

func validPilotConfig() PilotConfig {
	return PilotConfig{
		ConfigVersion:   PilotConfigVersion,
		ExperimentID:    "pilot-acme-001",
		WorkloadName:    "acme-support",
		WorkloadVersion: "abc123",
		Treatments:      []string{"t0", "t1", "t2"},
		Baseline:        "t0",
		Candidate:       "t2",
		Verifier:        PilotVerifierConfig{Provenance: "checker:acme-independent-v2"},
		QualityFloor:    0.80,
		PrimaryMetric:   "fully-loaded-cost-per-verified-success",
		MaturationH:     24,
		MissingCostRule: "exclude-unmetered",
		CensorGate:      0.05,
		SampleSize: PilotSamplePlan{
			Source: "customer:acme-feasibility-v1", BaselineMean: 0.20,
			Stddev: 0.10, MinRelEffect: 0.15, Alpha: 0.05, Power: 0.8,
			PerGroupN: 5000,
		},
		StoppingRules:       "No interim efficacy stops. Stop only for safety review or after all assigned jobs mature.",
		CommercialThreshold: 0.15,
		MinJobs:             100,
		BootstrapN:          500,
		BootstrapSeed:       7,
	}
}

func acknowledged(t *testing.T, c PilotConfig) PilotConfig {
	t.Helper()
	h, err := c.FrozenHash()
	if err != nil {
		t.Fatal(err)
	}
	c.Acknowledgement = PilotAcknowledgement{
		By: "pilot-owner@acme", At: time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		ConfigHash: h,
	}
	return c
}

func TestPilotConfigAcknowledgementRoundTrip(t *testing.T) {
	c := acknowledged(t, validPilotConfig())
	if err := c.Validate(nil); err != nil {
		t.Fatal(err)
	}
}

func TestPilotConfigRefusesUnacknowledged(t *testing.T) {
	c := validPilotConfig() // no acknowledgement
	if err := c.Validate(nil); err == nil {
		t.Fatal("unacknowledged config must be refused")
	}
}

func TestPilotConfigDetectsDrift(t *testing.T) {
	c := acknowledged(t, validPilotConfig())
	c.MaturationH = 48 // frozen content changed after sign-off
	if err := c.Validate(nil); err == nil {
		t.Fatal("drifted config must fail the acknowledgement hash")
	}
}

func TestPilotConfigStoppingAndSizingRules(t *testing.T) {
	c := acknowledged(t, validPilotConfig())
	c.StoppingRules = "peek weekly and stop when winning"
	c.Acknowledgement.ConfigHash, _ = c.FrozenHash()
	if err := c.Validate(nil); err == nil {
		t.Fatal("interim efficacy stops must be refused")
	}
	c2 := acknowledged(t, validPilotConfig())
	c2.SampleSize.PerGroupN = 1
	c2.Acknowledgement.ConfigHash, _ = c2.FrozenHash()
	if err := c2.Validate(nil); err == nil {
		t.Fatal("under-sized plan must be refused")
	}
	c3 := acknowledged(t, validPilotConfig())
	c3.PrimaryMetric = "cost-aware-bandit-reward"
	c3.Acknowledgement.ConfigHash, _ = c3.FrozenHash()
	if err := c3.Validate(nil); err == nil {
		t.Fatal("cost-aware reward relabeling must be refused: the learner stays cost-blind")
	}
}

func TestPilotConfigManifestCrossCheck(t *testing.T) {
	dir := t.TempDir()
	m := generateManifest(99, 8)
	m.ExperimentID = "pilot-acme-001"
	sum, err := m.contentHash()
	if err != nil {
		t.Fatal(err)
	}
	m.WorkloadVersion = sum
	mPath := filepath.Join(dir, "manifest.json")
	writeJSON(t, mPath, m)

	c := validPilotConfig()
	c.WorkloadVersion = sum
	c.Treatments = []string{"t0", "t1", "t2"}
	c = acknowledged(t, c)
	lm, err := LoadManifest(mPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(lm); err != nil {
		t.Fatalf("matching manifest must validate: %v", err)
	}
	c.WorkloadVersion = "drifted"
	c.Acknowledgement.ConfigHash, _ = c.FrozenHash()
	if err := c.Validate(lm); err == nil {
		t.Fatal("workload drift must fail validation")
	}
}

func TestPilotReportConfigMapping(t *testing.T) {
	c := acknowledged(t, validPilotConfig())
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	rc := c.ReportConfig(now)
	if rc.Maturation != 24*time.Hour || rc.MinEffect != 0.15 || rc.QualityFloor != 0.80 {
		t.Fatalf("charter mapping wrong: %+v", rc)
	}
	var _ = harness.ReportConfig{}
}

func TestGatePilotConfigEnforced(t *testing.T) {
	dir := t.TempDir()
	m := generateManifest(7, 4)
	m.ExperimentID = "pilot-acme-001"
	sum, _ := m.contentHash()
	m.WorkloadVersion = sum
	mPath := filepath.Join(dir, "manifest.json")
	writeJSON(t, mPath, m)

	c := validPilotConfig()
	c.WorkloadVersion = sum
	c = acknowledged(t, c)
	cPath := filepath.Join(dir, "pilot.json")
	writeJSON(t, cPath, c)
	lm, _ := LoadManifest(mPath)

	hash, _ := c.FrozenHash()
	// No acknowledgement flag: refuse.
	if err := gatePilotConfig(&runFlags{pilotConfig: cPath}, lm); err == nil {
		t.Fatal("run without --acknowledge must be refused")
	}
	// Wrong hash: refuse.
	if err := gatePilotConfig(&runFlags{pilotConfig: cPath, acknowledge: "wrong"}, lm); err == nil {
		t.Fatal("run with wrong hash must be refused")
	}
	// Correct hash: proceed.
	if err := gatePilotConfig(&runFlags{pilotConfig: cPath, acknowledge: hash}, lm); err != nil {
		t.Fatalf("acknowledged run must proceed: %v", err)
	}
	_ = os.Getenv
}
