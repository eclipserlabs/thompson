// Pilot experiment configuration: schema, validation, frozen acknowledgement.
//
// The pilot reuses the existing experiment charter and reporting
// implementation (harness.ReportConfig, harness.BuildReport). This file adds
// the versioned customer-workload binding: a pilot configuration references
// the workload definition, approved treatments, verifier, frozen quality
// floor, primary metric, maturation window, missing-data rules, sizing,
// stopping rules and commercial threshold. Nothing here changes the sampling
// algorithm, the reward formula, or the statistical charter.
//
// A configuration takes effect only after explicit acknowledgement: the
// Acknowledgement.ConfigHash must equal the hash of the frozen content.
// Runners refuse to start on an unacknowledged or drifted configuration.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/harness"
)

// PilotConfigVersion versions the pilot configuration schema.
const PilotConfigVersion = "pilot-config-v1"

// PilotVerifierConfig pins the independent verifier for the pilot.
type PilotVerifierConfig struct {
	Provenance string `json:"provenance"`
	// Allowlist of verifier provenance prefixes permitted to settle jobs.
	Allowed []string `json:"allowed_verifiers,omitempty"`
}

// PilotSamplePlan records the sizing the charter was frozen with. Source
// names the dataset the inputs came from (a feasibility report source or
// "assumption:<name>"); assumptions must never be presented as measurements.
type PilotSamplePlan struct {
	Source       string  `json:"source"`
	BaselineMean float64 `json:"baseline_mean"`
	Stddev       float64 `json:"stddev"`
	MinRelEffect float64 `json:"min_rel_effect"`
	Alpha        float64 `json:"alpha"`
	Power        float64 `json:"power"`
	PerGroupN    float64 `json:"per_group_n"`
}

// PilotAcknowledgement is the explicit sign-off on the frozen content.
type PilotAcknowledgement struct {
	By         string `json:"by"`
	At         string `json:"at"`
	ConfigHash string `json:"config_hash"`
}

// PilotConfig is the frozen pilot definition.
type PilotConfig struct {
	ConfigVersion string `json:"config_version"`

	ExperimentID string `json:"experiment_id"`
	WorkloadName string `json:"workload_name"`
	// WorkloadVersion is the content hash of the versioned customer workload
	// definition (manifest workload_version). Runs refuse a manifest whose
	// version differs.
	WorkloadVersion string `json:"workload_version"`

	Treatments []string `json:"treatments"`
	Baseline   string   `json:"baseline"`
	Candidate  string   `json:"candidate"`

	Verifier PilotVerifierConfig `json:"verifier"`

	// QualityFloor is frozen: winners below it are NOT_RANKABLE.
	QualityFloor float64 `json:"quality_floor"`
	// PrimaryMetric names the offline economic metric. The only supported
	// value keeps the cost-blind learner / economic evaluation split: the
	// learner still folds binary task status while the report reads
	// fully-loaded cost per verified success.
	PrimaryMetric string `json:"primary_metric"`

	MaturationH float64 `json:"maturation_hours"`
	// Missing-data and censoring rules. The only supported missing-cost rule
	// is exclusion: unmetered jobs never enter the primary metric.
	MissingCostRule string  `json:"missing_cost_rule"`
	CensorGate      float64 `json:"censor_gate"`

	SampleSize PilotSamplePlan `json:"sample_size"`

	// StoppingRules is free text with one machine-checked invariant: it must
	// forbid interim efficacy stops (see Validate).
	StoppingRules string `json:"stopping_rules"`
	// CommercialThreshold is the minimum relative improvement (MinEffect).
	CommercialThreshold float64 `json:"commercial_threshold"`

	MinJobs       int    `json:"min_jobs"`
	BootstrapN    int    `json:"bootstrap_n"`
	BootstrapSeed uint64 `json:"bootstrap_seed"`

	Acknowledgement PilotAcknowledgement `json:"acknowledgement"`
}

// FrozenHash returns the sha256 of the canonical frozen content (everything
// except the acknowledgement itself). The acknowledgement signs this hash.
func (c PilotConfig) FrozenHash() (string, error) {
	clone := c
	clone.Acknowledgement = PilotAcknowledgement{}
	b, err := json.Marshal(clone)
	if err != nil {
		return "", err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return "", err
	}
	cb, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(cb)
	return fmt.Sprintf("%x", sum[:16]), nil
}

// Validate checks the schema and the frozen acknowledgement. A manifest may
// be supplied for cross-checks (treatment set, workload version); nil skips
// them.
func (c PilotConfig) Validate(m *Manifest) error {
	if c.ConfigVersion != PilotConfigVersion {
		return fmt.Errorf("pilot: unsupported config_version %q (want %q)",
			c.ConfigVersion, PilotConfigVersion)
	}
	if strings.TrimSpace(c.ExperimentID) == "" {
		return fmt.Errorf("pilot: experiment_id required")
	}
	if strings.TrimSpace(c.WorkloadVersion) == "" {
		return fmt.Errorf("pilot: workload_version required (versioned customer workload definition)")
	}
	if len(c.Treatments) < 2 {
		return fmt.Errorf("pilot: at least two treatments required")
	}
	seen := map[string]bool{}
	for _, t := range c.Treatments {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("pilot: treatment with empty id")
		}
		if seen[t] {
			return fmt.Errorf("pilot: duplicate treatment %q", t)
		}
		seen[t] = true
	}
	if !seen[c.Baseline] {
		return fmt.Errorf("pilot: baseline %q not in treatments", c.Baseline)
	}
	if !seen[c.Candidate] {
		return fmt.Errorf("pilot: candidate %q not in treatments", c.Candidate)
	}
	if c.Baseline == c.Candidate {
		return fmt.Errorf("pilot: baseline and candidate must differ")
	}
	if strings.TrimSpace(c.Verifier.Provenance) == "" {
		return fmt.Errorf("pilot: verifier.provenance required (independent verifier configuration)")
	}
	if c.QualityFloor < 0 || c.QualityFloor > 1 {
		return fmt.Errorf("pilot: quality_floor must be in [0,1]")
	}
	if c.PrimaryMetric != "fully-loaded-cost-per-verified-success" {
		return fmt.Errorf("pilot: unsupported primary_metric %q (the cost-blind learner / offline economic evaluation split is mandatory)", c.PrimaryMetric)
	}
	if c.MaturationH <= 0 {
		return fmt.Errorf("pilot: maturation_hours must be positive")
	}
	if c.MissingCostRule != "exclude-unmetered" {
		return fmt.Errorf("pilot: unsupported missing_cost_rule %q (missing costs are excluded, never imputed)", c.MissingCostRule)
	}
	if c.CensorGate < 0 || c.CensorGate > 1 {
		return fmt.Errorf("pilot: censor_gate must be in [0,1]")
	}
	h := harness.HistoricalSampleInput{
		Source: c.SampleSize.Source, CollectedAt: "pilot-charter",
		BaselineMean: c.SampleSize.BaselineMean, Stddev: c.SampleSize.Stddev,
		Alpha: c.SampleSize.Alpha, Power: c.SampleSize.Power,
		MinRelEffect: c.SampleSize.MinRelEffect,
	}
	if err := h.Validate(); err != nil {
		return fmt.Errorf("pilot: sample_size: %w", err)
	}
	want, err := harness.RequiredFromHistorical(h)
	if err != nil {
		return fmt.Errorf("pilot: sample_size: %w", err)
	}
	if c.SampleSize.PerGroupN <= 0 {
		return fmt.Errorf("pilot: sample_size.per_group_n must be positive")
	}
	if c.SampleSize.PerGroupN+1 < want {
		return fmt.Errorf("pilot: sample_size.per_group_n %.1f below required %.1f for the stated inputs",
			c.SampleSize.PerGroupN, want)
	}
	if strings.TrimSpace(c.StoppingRules) == "" {
		return fmt.Errorf("pilot: stopping_rules required")
	}
	lower := strings.ToLower(c.StoppingRules)
	if !strings.Contains(lower, "no interim") && !strings.Contains(lower, "no efficacy") {
		return fmt.Errorf("pilot: stopping_rules must forbid interim efficacy stops")
	}
	if c.CommercialThreshold <= 0 {
		return fmt.Errorf("pilot: commercial_threshold must be positive")
	}
	if c.MinJobs <= 0 || c.BootstrapN <= 0 {
		return fmt.Errorf("pilot: min_jobs and bootstrap_n must be positive")
	}
	if m != nil {
		if m.ExperimentID != c.ExperimentID {
			return fmt.Errorf("pilot: manifest experiment %q != pilot experiment %q",
				m.ExperimentID, c.ExperimentID)
		}
		if m.WorkloadVersion != c.WorkloadVersion {
			return fmt.Errorf("pilot: manifest workload_version %q != pilot workload_version %q (frozen workload drifted)",
				m.WorkloadVersion, c.WorkloadVersion)
		}
		if len(m.Treatments) != len(c.Treatments) {
			return fmt.Errorf("pilot: manifest has %d treatments, pilot lists %d",
				len(m.Treatments), len(c.Treatments))
		}
		for _, t := range m.Treatments {
			if !seen[t.ID] {
				return fmt.Errorf("pilot: manifest treatment %q not in pilot treatments", t.ID)
			}
		}
	}
	// Acknowledgement last: the frozen content must be explicitly signed.
	hash, err := c.FrozenHash()
	if err != nil {
		return err
	}
	a := c.Acknowledgement
	if strings.TrimSpace(a.By) == "" || strings.TrimSpace(a.At) == "" {
		return fmt.Errorf("pilot: acknowledgement requires by/at sign-off on hash %s", hash)
	}
	if _, err := time.Parse(time.RFC3339, a.At); err != nil {
		if _, err2 := time.Parse(time.RFC3339Nano, a.At); err2 != nil {
			return fmt.Errorf("pilot: acknowledgement at must be RFC3339")
		}
	}
	if a.ConfigHash != hash {
		return fmt.Errorf("pilot: acknowledgement hash %q != frozen content hash %q: re-acknowledge the exact frozen configuration",
			a.ConfigHash, hash)
	}
	return nil
}

// ReportConfig maps the frozen pilot to the existing reporting charter.
func (c PilotConfig) ReportConfig(now time.Time) harness.ReportConfig {
	return harness.ReportConfig{
		Maturation: time.Duration(c.MaturationH * float64(time.Hour)), Now: now,
		MinJobs: c.MinJobs, CensorGate: c.CensorGate, QualityFloor: c.QualityFloor,
		MinEffect: c.CommercialThreshold, BootstrapN: c.BootstrapN,
		BootstrapSeed: c.BootstrapSeed,
	}
}

// LoadPilotConfig reads and validates a pilot configuration file. Manifest
// cross-checks apply when manifestPath is non-empty.
func LoadPilotConfig(configPath, manifestPath string) (*PilotConfig, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("pilot: read config: %w", err)
	}
	var c PilotConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("pilot: bad config: %w", err)
	}
	var m *Manifest
	if manifestPath != "" {
		m, err = LoadManifest(manifestPath)
		if err != nil {
			return nil, err
		}
	}
	if err := c.Validate(m); err != nil {
		return nil, err
	}
	return &c, nil
}
