// Command exp-report renders the offline experiment readout from treatment
// ledgers. It reads assignments + outcomes per treatment, enforces the
// maturation window, and prints metrics, comparison CIs, sensitivities, and
// gate verdicts. It never learns, never writes to ledgers, and exits 2 when
// the data are not ready for analysis.
//
// Usage:
//
//	exp-report --root ./exp --treatments t0,t1,t2 --baseline t0 --candidate t2 \
//	  --maturation 24h --min-jobs 1000 --format json
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/harness"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// refused signals not-ready data (exit 2) as opposed to a usage error (exit 1).
type refused struct{ reason string }

func (e *refused) Error() string { return "analysis refused: " + e.reason }

func run(args []string) error {
	fs := flag.NewFlagSet("exp-report", flag.ContinueOnError)
	root := fs.String("root", "", "experiment root dir (subdirs per treatment)")
	treatments := fs.String("treatments", "t0,t1,t2", "comma-separated treatment IDs")
	baseline := fs.String("baseline", "t0", "baseline treatment for comparisons")
	candidate := fs.String("candidate", "t2", "candidate treatment for comparisons")
	maturation := fs.Duration("maturation", 24*time.Hour, "common outcome-maturation window")
	nowStr := fs.String("now", "", "analysis clock RFC3339 (default: current time)")
	minJobs := fs.Int("min-jobs", 1000, "minimum matured jobs per treatment")
	censorGate := fs.Float64("censor-gate", 0.05, "max censored fraction per treatment")
	qualityFloor := fs.Float64("quality-floor", 0.95, "minimum winner accept rate")
	minEffect := fs.Float64("min-effect", 0.15, "minimum relative improvement (commercial bar)")
	bootstrap := fs.Int("bootstrap", 2000, "bootstrap resamples per comparison")
	seed := fs.Uint64("seed", 0xE1C, "bootstrap seed")
	minValid := fs.Float64("min-bootstrap-valid", 0.5, "min valid bootstrap fraction for conclusive verdicts")
	maxUnmetered := fs.Float64("max-unmetered", 0, "max unmetered-cost share per treatment (0 = 0.10 default; negative disables)")
	maxCost := fs.Float64("max-cost", 0, "defensible upper cost fill (0 = p90 bounded sensitivity)")
	expectedWeights := fs.String("expected-weights", "", "charter allocation t0=..,t1=.. (empty = uniform-only check)")
	format := fs.String("format", "text", "text|json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return errors.New("missing --root")
	}
	now := time.Now().UTC()
	if *nowStr != "" {
		t, err := time.Parse(time.RFC3339, *nowStr)
		if err != nil {
			return fmt.Errorf("bad --now: %w", err)
		}
		now = t
	}
	var txNames []string
	for _, n := range strings.Split(*treatments, ",") {
		if n = strings.TrimSpace(n); n != "" {
			txNames = append(txNames, n)
		}
	}
	var others []string
	for _, n := range txNames {
		if n != *baseline && n != *candidate {
			others = append(others, n)
		}
	}

	var allAssign []harness.Assignment
	var allEvents []outcome.OutcomeEvent
	jobMaps := map[string]harness.JobMap{}
	for _, n := range txNames {
		as, evs, err := harness.LoadTreatmentDir(*root + "/" + n)
		if err != nil {
			return fmt.Errorf("treatment %s: %w", n, err)
		}
		allAssign = append(allAssign, as...)
		allEvents = append(allEvents, evs...)
		jm, err := harness.LoadJobMap(*root + "/" + n)
		if err != nil {
			return fmt.Errorf("treatment %s jobmap: %w", n, err)
		}
		jobMaps[n] = jm
	}
	cfg := harness.ReportConfig{
		Maturation: *maturation, Now: now,
		MinJobs: *minJobs, CensorGate: *censorGate, QualityFloor: *qualityFloor,
		MinEffect: *minEffect, BootstrapN: *bootstrap, BootstrapSeed: *seed,
		MinBootstrapValidFraction: *minValid, MaxUnmeteredShare: *maxUnmetered,
		MaxPlausibleCost: *maxCost, ExpectedWeights: parseWeights(*expectedWeights),
	}
	rep, err := harness.BuildReport(allAssign, allEvents, txNames, *baseline, *candidate, others, cfg, jobMaps)
	if err != nil {
		var nr *harness.NotReadyError
		if errors.As(err, &nr) {
			return &refused{nr.Reason}
		}
		return err
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	case "text":
		printText(rep)
		return nil
	default:
		return fmt.Errorf("bad --format %q", *format)
	}
}

// parseWeights parses "t0=0.33,t1=0.33" into a weight map (empty = nil).
func parseWeights(s string) map[string]float64 {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	out := map[string]float64{}
	for _, kv := range strings.Split(s, ",") {
		parts := strings.SplitN(strings.TrimSpace(kv), "=", 2)
		if len(parts) != 2 {
			continue
		}
		var v float64
		if _, err := fmt.Sscanf(strings.TrimSpace(parts[1]), "%f", &v); err != nil {
			continue
		}
		out[strings.TrimSpace(parts[0])] = v
	}
	return out
}

func printText(rep harness.Report) {
	fmt.Printf("experiment report (maturation %s, now %s)\n", rep.Maturation, rep.Now)
	for _, g := range rep.Gates {
		status := "PASS"
		if !g.Pass {
			status = "FAIL"
		}
		fmt.Printf("gate %-20s %s %s\n", g.Name, status, g.Detail)
	}
	for name, st := range rep.Treatments {
		fmt.Printf("%s: matured=%d accepted=%d rejected=%d unknown=%d unresolved=%d censored=%.3f primary=%.4f metered=%.2f accept_rate=%.3f\n",
			name, st.Matured, st.Accepted, st.Rejected, st.Unknown, st.Unresolved,
			st.CensoredFraction, st.Primary, st.MeteredShare, st.AcceptRate)
	}
	for _, c := range rep.Comparisons {
		fmt.Printf("%s: diff=%+.4f rel=%.3f ci95=[%+.4f,%+.4f] wins=%v bar=%v\n",
			c.Pair, c.Diff, c.RelImprovement, c.CILow, c.CIHigh, c.Wins, c.MeetsBar)
	}
	fmt.Printf("sensitivity: worst_case_wins=%v missing=[%+.4f,%+.4f] half_stable=%v double_stable=%v corrections=%s\n",
		rep.Sensitivity.WorstCaseCensoringWins, rep.Sensitivity.MissingCostLow,
		rep.Sensitivity.MissingCostHigh, rep.Sensitivity.HalfMaturityStable,
		rep.Sensitivity.DoubleMaturityStable, rep.Sensitivity.CorrectionAsymmetry)
	fmt.Printf("verdict: %s\n", rep.Verdict)
	for _, r := range rep.Reasons {
		fmt.Printf("  - %s\n", r)
	}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		var r *refused
		if errors.As(err, &r) {
			fmt.Fprintf(os.Stderr, "exp-report: %v\n", err)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "exp-report: %v\n", err)
		os.Exit(1)
	}
}

// exitCode maps errors to process exit codes (2 = refused/not-ready).
func exitCode(err error) int {
	var r *refused
	var nr *harness.NotReadyError
	if errors.As(err, &r) || errors.As(err, &nr) {
		return 2
	}
	return 1
}
