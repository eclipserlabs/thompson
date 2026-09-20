// Evaluate reads a gateway evidence JSONL and estimates candidate policy value via IPS/SNIPS.
// Usage: go run ./cmd/evaluate --evidence ./evidence.jsonl --policy exact-thompson-v1 --draws 10000 --bootstrap 200
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
)

func main() {
	var path string
	var policyID string
	var draws int
	var epsilon float64
	var bootstrap int
	var clip float64
	var clipEnabled bool
	var propSource string
	flag.StringVar(&path, "evidence", "./evidence.jsonl", "path to JSONL evidence ledger")
	flag.StringVar(&policyID, "policy", "exact-thompson-v1", "candidate policy: exact-thompson-v1, thompson-numerical-reference-v1, uniform-v1, greedy-posterior-mean-v1")
	flag.IntVar(&draws, "draws", 10000, "Monte Carlo draws for Thompson propensity reconstruction (ignored when --propensity=reference)")
	flag.StringVar(&propSource, "propensity", "reference", "logging-propensity estimator: reference (numerical integration) or mc (Monte Carlo)")
	flag.Float64Var(&epsilon, "epsilon", 0.01, "tie threshold for paired report (also used for greedy tie-break)")
	flag.IntVar(&bootstrap, "bootstrap", 200, "bootstrap samples for SE (0 disables)")
	flag.Float64Var(&clip, "clip", 10, "weight clipping threshold for diagnostic (0 disables)")
	flag.Parse()
	_ = epsilon
	clipEnabled = true
	if clip == 0 {
		clipEnabled = false
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "missing --evidence")
		os.Exit(2)
	}

	decisions, err := gateway.LedgerFromFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load ledger: %v\n", err)
		os.Exit(1)
	}
	// Build bandit log records with propensity reconstruction. The estimator is
	// explicit because it is the thing an off-policy estimate most depends on.
	var estimator gateway.PropensityEstimator
	switch propSource {
	case "reference":
		estimator = gateway.NewReferenceEstimator()
	case "mc":
		estimator = gateway.NewMCEstimator(draws, 42).WithReferenceScoring()
	default:
		fmt.Fprintf(os.Stderr, "unknown --propensity %q (want reference or mc)\n", propSource)
		os.Exit(2)
	}
	var records []gateway.BanditLogRecord
	for _, d := range decisions {
		if d.Started == nil || d.Learned == nil {
			continue
		}
		records = append(records, gateway.ToBanditLogWith(d, estimator))
	}

	// Also show paired stats for context (shadow diagnostics not used in IPS)
	paired := gateway.AnalyzePaired(decisions, 0.01)
	fmt.Printf("evidence ledger: %s\n", path)
	fmt.Printf("total decisions: %d\n", len(decisions))
	fmt.Printf("OPE eligible: %d (%.1f%%)  ineligible: %d\n", countEligible(records), 100*float64(countEligible(records))/float64(len(records)), len(records)-countEligible(records))
	fmt.Printf("paired shadow observations: %d (diagnostics only, not in IPS)\n", paired.PairedCount)

	var candidate gateway.CandidatePolicy
	switch policyID {
	case "exact-thompson-v1":
		candidate = gateway.ExactThompsonCandidate{Draws: draws, Seed: 42}
	case "thompson-numerical-reference-v1":
		candidate = gateway.NewThompsonReferenceCandidate()
	case "uniform-v1":
		candidate = gateway.UniformCandidate{}
	case "greedy-posterior-mean-v1":
		candidate = gateway.GreedyCandidate{}
	default:
		fmt.Fprintf(os.Stderr, "unknown policy %s\n", policyID)
		os.Exit(2)
	}

	var clipPtr *float64
	if clipEnabled {
		clipPtr = &clip
	}
	est := gateway.EvaluateOPE(records, candidate, clipPtr, bootstrap, 42, draws, 42)

	fmt.Printf("\n--- per-policy report: %s ---\n", est.CandidateID)
	fmt.Printf("total decisions: %d\n", est.TotalDecisions)
	fmt.Printf("OPE-eligible decisions: %d\n", est.EligibleDecisions)
	fmt.Printf("empirical logging reward (direct): %.4f\n", est.EmpiricalMean)
	fmt.Printf("IPS estimated value: %.4f\n", est.IPS)
	fmt.Printf("SNIPS estimated value: %.4f\n", est.SNIPS)
	if est.BootstrapSE != nil {
		fmt.Printf("bootstrap SE (%.d samples): %.4f  95%% CI [%.4f, %.4f]\n", bootstrap, *est.BootstrapSE, est.BootstrapCI95[0], est.BootstrapCI95[1])
	}
	fmt.Printf("ESS: %.1f  ESS/N: %.3f\n", est.ESS, est.ESSOverN)
	fmt.Printf("max weight: %.3f  p95: %.3f p99: %.3f mean: %.3f min propensity: %.4f\n", est.MaxWeight, est.P95Weight, est.P99Weight, est.MeanWeight, est.MinPropensity)
	fmt.Printf("unsupported fraction: %.3f  excluded: %.3f\n", est.UnsupportedFraction, est.ExcludedFraction)
	if est.ClippedIPS != nil {
		fmt.Printf("clipped IPS (threshold %.2f, clipped %d): %.4f\n", *est.ClippingThreshold, est.ClippedCount, *est.ClippedIPS)
	}
	if len(est.Warnings) > 0 {
		fmt.Println("warnings:")
		for _, w := range est.Warnings {
			fmt.Printf("  - %s\n", w)
		}
	}
	fmt.Printf("propensity estimator: %s\n", est.PropensityEstimatorID)
	fmt.Printf("usable rows: %d of %d eligible (refused: %d MC_ZERO_WINS, %d LOW_PRECISION, %d NUMERICAL_REFERENCE_FAILURE, %d INVALID_POSTERIOR)\n",
		est.UsableDecisions, est.EligibleDecisions,
		est.MCZeroWinsCount, est.LowPrecisionCount, est.ReferenceFailureCount, est.InvalidPosteriorCount)

	// Rankability. A single candidate cannot have its propensity sensitivity
	// measured from one run, so this command never reports RANKABLE on its own;
	// it reports the gate outcome and points at the sweep that can.
	gate := gateway.DefaultRankabilityConfig()
	rep := gateway.AssessRankability(est, gate, nil, []string{est.PropensityEstimatorID})
	fmt.Printf("\nrankability: %s   support/precision: %s\n", rep.Status, rep.Support)
	for _, fl := range rep.Failures {
		fmt.Printf("  gate failed: %s\n", fl)
	}
	fmt.Printf("remedy: %s\n", rep.Remedy())
	fmt.Println("run ./cmd/propensity-audit for the estimator sweep that can measure propensity sensitivity")

	if isSelfPaired(policyID, propSource) {
		fmt.Println("\nIMPLEMENTATION_SANITY_ONLY: this candidate and the logging propensities come")
		fmt.Println("from the same estimator, so every importance weight is 1 by construction and")
		fmt.Println("IPS reproduces the empirical mean whatever the propensity error is.")
		fmt.Println("It checks that rows join correctly. It is not evidence that the propensities")
		fmt.Println("are accurate. For that, run ./cmd/propensity-audit, which pairs the numerical")
		fmt.Println("reference against Monte Carlo and reports the resulting weights.")
	}
	fmt.Println("\nclaim levels: PAIRED_OBSERVED vs OFF_POLICY_ESTIMATE vs FULL_INFORMATION (not available)")
}

func countEligible(records []gateway.BanditLogRecord) int {
	c := 0
	for _, r := range records {
		if r.Status == gateway.OPEEligible {
			c++
		}
	}
	return c
}

// isSelfPaired reports whether the candidate policy and the logging-propensity
// estimator are the same machinery, which makes the resulting estimate a
// tautology rather than an evaluation.
func isSelfPaired(policyID, propSource string) bool {
	return (policyID == "exact-thompson-v1" && propSource == "mc") ||
		(policyID == "thompson-numerical-reference-v1" && propSource == "reference")
}
