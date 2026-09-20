// Analyze reads a gateway evidence JSONL and prints paired primary vs shadow stats.
// Usage: go run ./go/cmd/analyze --evidence ./evidence.jsonl --epsilon 0.01
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
)

func main() {
	var path string
	var epsilon float64
	flag.StringVar(&path, "evidence", "./evidence.jsonl", "path to JSONL evidence ledger")
	flag.Float64Var(&epsilon, "epsilon", 0.01, "tie threshold for reward comparison (delta in [-epsilon,epsilon] is tie)")
	flag.Parse()
	if path == "" {
		fmt.Fprintln(os.Stderr, "missing --evidence")
		os.Exit(2)
	}
	decisions, err := gateway.LedgerFromFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load ledger: %v\n", err)
		os.Exit(1)
	}
	stats := gateway.AnalyzePaired(decisions, epsilon)
	total := len(decisions)
	paired := stats.PairedCount
	fmt.Printf("evidence ledger: %s\n", path)
	fmt.Printf("total decisions: %d\n", total)
	fmt.Printf("paired observations (primary+shadow): %d  [PAIRED OBSERVED comparison, NOT policy regret]\n", paired)
	if paired == 0 {
		fmt.Println("no paired observations — shadow_sample_rate may be 0 or eligibility deny")
		return
	}
	fmt.Printf("mean primary reward: %.4f\n", stats.MeanPrimaryReward)
	fmt.Printf("mean shadow reward:  %.4f\n", stats.MeanShadowReward)
	fmt.Printf("mean paired delta (shadow-primary): %.4f  [paired observed difference]\n", stats.MeanDelta)
	fmt.Printf("win/tie/loss (epsilon=%.3f): %d/%d/%d (%.1f%%/%.1f%%/%.1f%%)\n",
		epsilon, stats.Win, stats.Tie, stats.Loss,
		100*float64(stats.Win)/float64(paired),
		100*float64(stats.Tie)/float64(paired),
		100*float64(stats.Loss)/float64(paired))
	fmt.Printf("success-rate diff (shadow - primary): %.4f\n", stats.SuccessDiff)
	fmt.Printf("latency diff mean (shadow-primary ms): %.2f\n", stats.MeanLatencyDelta)
	if stats.MeanCostDelta != nil {
		fmt.Printf("cost diff mean (shadow-primary USD): %.6f (n=%d)\n", *stats.MeanCostDelta, stats.CostPairs)
	} else {
		fmt.Printf("cost diff: insufficient data (both costs known for %d/%d pairs)\n", stats.CostPairs, paired)
	}
	// V1 extended
	fmt.Println("\n-- logging policy summary (descriptive, no counterfactual correction) --")
	fmt.Printf("primary counts: %v\n", stats.PrimaryCounts)
	fmt.Printf("shadow counts: %v\n", stats.ShadowCounts)
	fmt.Println("coverage matrix primary -> shadow:")
	for p, m := range stats.Coverage {
		fmt.Printf("  %s -> %v\n", p, m)
	}
	if len(decisions) > 0 && decisions[0].Shadow != nil {
		fmt.Printf("shadow selection policy: %s prob %.3f eligible %d candidates %d\n",
			decisions[0].Shadow.ShadowSelectionPolicyID, decisions[0].Shadow.ShadowSelectionProbability,
			decisions[0].Shadow.EligibleArmCount, decisions[0].Shadow.ShadowCandidateCount)
	}
	fmt.Printf("missingness: cost primary %d shadow %d tokens primary %d shadow %d\n",
		stats.MissingCostPrimary, stats.MissingCostShadow, stats.MissingTokensPrimary, stats.MissingTokensShadow)
	if len(stats.SupportWarnings) > 0 {
		fmt.Println("support/overlap warnings:")
		for _, w := range stats.SupportWarnings {
			fmt.Printf("  - %s\n", w)
		}
	}
	fmt.Println("\nclaim levels:")
	fmt.Println("  PAIRED_OBSERVED: paired delta above is valid for observed pairs only")
	fmt.Println("  LOGGING_POLICY_SUMMARY: descriptive stats above need no correction")
	fmt.Println("  OFF_POLICY_ESTIMATE: requires IPS/SNIPS/DR with logged propensities — not claimed here (shadow prob logged, Thompson propensity unavailable offline in V1)")
	fmt.Println("  FULL_INFORMATION: not available (only 1 shadow per request)")
	fmt.Println("note: do not claim statistical significance for small n; delta is not regret unless all arm rewards observed for decision")
}
