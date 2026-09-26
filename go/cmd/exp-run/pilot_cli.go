// Pilot CLI: feasibility assessment, pilot-config validation, and the
// frozen-configuration gate enforced before experimental runs.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

// gatePilotConfig enforces explicit acknowledgement of the frozen pilot
// configuration before any experimental run. Without --pilot-config the run
// proceeds (synthetic dry runs); with it, the config must validate against
// the manifest and --acknowledge must carry the frozen content hash.
func gatePilotConfig(f *runFlags, m *Manifest) error {
	if f.pilotConfig == "" {
		return nil
	}
	raw, err := os.ReadFile(f.pilotConfig)
	if err != nil {
		return fmt.Errorf("pilot: read config: %w", err)
	}
	var c PilotConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return fmt.Errorf("pilot: bad config: %w", err)
	}
	if err := c.Validate(m); err != nil {
		return err
	}
	hash, err := c.FrozenHash()
	if err != nil {
		return err
	}
	if f.acknowledge == "" {
		return fmt.Errorf("pilot: config %s is frozen (hash %s): re-run with --acknowledge %s to sign off",
			f.pilotConfig, hash, hash)
	}
	if f.acknowledge != hash {
		return fmt.Errorf("pilot: --acknowledge %q != frozen hash %q: refusing to run", f.acknowledge, hash)
	}
	fmt.Printf("pilot %s acknowledged (config hash %s, workload %s)\n",
		c.ExperimentID, hash, c.WorkloadVersion)
	return nil
}

// feasibilityCmd implements: exp-run feasibility --in records.jsonl
// [--out report.json] [--source name] [--format json|text]
//
// Read-only: invalid or incomplete rows are rejected with reasons and
// counted; missing costs stay missing; UNKNOWN stays UNKNOWN.
func feasibilityCmd(args []string) error {
	fs := flag.NewFlagSet("feasibility", flag.ContinueOnError)
	in := fs.String("in", "", "historical workload records JSONL path")
	out := fs.String("out", "", "report output path (default: stdout)")
	source := fs.String("source", "customer:unspecified", "dataset provenance label")
	format := fs.String("format", "json", "json|text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return fmt.Errorf("missing --in")
	}
	rep, err := assessFile(*in, *source)
	if err != nil {
		return err
	}
	var b []byte
	switch *format {
	case "json":
		b, err = json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		b = append(b, '\n')
	case "text":
		b = []byte(rep.Summary())
	default:
		return fmt.Errorf("bad --format %q", *format)
	}
	if *out == "" {
		_, err = os.Stdout.Write(b)
		return err
	}
	return os.WriteFile(*out, b, 0o644)
}

// assessFile reads JSONL records, validates each row, and assesses.
func assessFile(path, source string) (FeasibilityReport, error) {
	f, err := os.Open(path)
	if err != nil {
		return FeasibilityReport{}, fmt.Errorf("feasibility: open: %w", err)
	}
	defer f.Close()
	var valid []CustomerRecord
	var rejected []Rejection
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		var r CustomerRecord
		if err := json.Unmarshal(raw, &r); err != nil {
			rejected = append(rejected, Rejection{Line: line, Reason: fmt.Sprintf("malformed JSON: %v", err)})
			continue
		}
		if err := ValidateRecord(r); err != nil {
			rejected = append(rejected, Rejection{Line: line, JobID: r.JobID, Reason: err.Error()})
			continue
		}
		valid = append(valid, r)
	}
	if err := sc.Err(); err != nil {
		return FeasibilityReport{}, fmt.Errorf("feasibility: scan: %w", err)
	}
	return Assess(valid, rejected, source), nil
}

// pilotCheckCmd implements: exp-run pilot-check --config pilot.json
// [--manifest manifest.json] [--format json|text]
func pilotCheckCmd(args []string) error {
	fs := flag.NewFlagSet("pilot-check", flag.ContinueOnError)
	config := fs.String("config", "", "pilot configuration path")
	manifest := fs.String("manifest", "", "workload manifest path (optional cross-check)")
	format := fs.String("format", "text", "json|text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *config == "" {
		return fmt.Errorf("missing --config")
	}
	c, err := LoadPilotConfig(*config, *manifest)
	if err != nil {
		return err
	}
	hash, err := c.FrozenHash()
	if err != nil {
		return err
	}
	switch *format {
	case "text":
		fmt.Printf("pilot %s valid: workload=%s treatments=%v baseline=%s candidate=%s\n",
			c.ExperimentID, c.WorkloadVersion, c.Treatments, c.Baseline, c.Candidate)
		fmt.Printf("verifier=%s quality_floor=%.3f primary=%s maturation=%.1fh censor_gate=%.3f\n",
			c.Verifier.Provenance, c.QualityFloor, c.PrimaryMetric, c.MaturationH, c.CensorGate)
		fmt.Printf("sample: source=%s per_group_n=%.1f min_rel_effect=%.3f\n",
			c.SampleSize.Source, c.SampleSize.PerGroupN, c.SampleSize.MinRelEffect)
		fmt.Printf("commercial_threshold=%.3f stopping_rules=%q\n",
			c.CommercialThreshold, c.StoppingRules)
		fmt.Printf("acknowledged by %s at %s (hash %s)\n",
			c.Acknowledgement.By, c.Acknowledgement.At, hash)
		return nil
	case "json":
		b, err := json.MarshalIndent(map[string]any{
			"valid": true, "experiment": c.ExperimentID,
			"workload_version": c.WorkloadVersion, "config_hash": hash,
			"acknowledged_by": c.Acknowledgement.By,
		}, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	default:
		return fmt.Errorf("bad --format %q", *format)
	}
}
