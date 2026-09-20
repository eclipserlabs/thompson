// Command propensity-audit validates Thompson propensity reconstruction against
// an estimator-independent numerical reference, and applies the rankability
// gates to candidate policies.
//
// It changes no online behaviour. It reads (or generates) a bandit log, rebuilds
// the logging propensities under several estimators, and reports where they
// agree, where they do not, and which candidates may therefore be ranked.
//
// Usage:
//
//	go run ./cmd/propensity-audit --decisions 10000 --draws 2000,20000,200000,1000000
//	go run ./cmd/propensity-audit --evidence ./evidence.jsonl
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
	"github.com/wiramahendra/thompson-sampling/go/opeval"
	"github.com/wiramahendra/thompson-sampling/go/propensity"
)

func main() {
	var (
		evidencePath string
		decisions    int
		drawsCSV     string
		matrixDraws  string
		seed         uint64
		bootstrap    int
		harnessN     int
		calDecisions int
		skipMatrix   bool
		skipLog      bool
	)
	flag.StringVar(&evidencePath, "evidence", "", "path to a JSONL evidence ledger; empty generates a synthetic 0.8/0.6/0.4/0.2 log")
	flag.IntVar(&decisions, "decisions", 10000, "synthetic log length when --evidence is empty")
	flag.StringVar(&drawsCSV, "draws", "2000,20000,200000", "Monte-Carlo draw counts for the per-decision sweep")
	flag.StringVar(&matrixDraws, "matrix-draws", "2000,20000,200000,1000000", "Monte-Carlo draw counts for the posterior test matrix")
	flag.Uint64Var(&seed, "seed", 42, "deterministic seed for every estimator and for log generation")
	flag.IntVar(&bootstrap, "bootstrap", 200, "bootstrap resamples for the IPS interval (0 disables)")
	flag.IntVar(&harnessN, "harness-states", 25, "harness-sampled posterior states per arm count (x4 arm counts)")
	flag.IntVar(&calDecisions, "calibration-decisions", 2000, "decisions used for the calibration and weight-sensitivity sections")
	flag.BoolVar(&skipMatrix, "skip-matrix", false, "skip the posterior test matrix")
	flag.BoolVar(&skipLog, "skip-log", false, "skip the bandit-log diagnostics")
	flag.Parse()

	sweepDraws := parseInts(drawsCSV)
	matDraws := parseInts(matrixDraws)
	th := propensity.DefaultThresholds()
	refOpts := propensity.DefaultReferenceOptions()
	gate := gateway.DefaultRankabilityConfig()

	section("CONFIGURATION")
	fmt.Printf("seed: %d\n", seed)
	fmt.Printf("reference: AbsTol=%.1g RelTol=%.1g MaxDepth=%d MaxSumError=%.1g\n",
		refOpts.AbsTol, refOpts.RelTol, refOpts.MaxDepth, refOpts.MaxSumError)
	fmt.Printf("per-action thresholds: MinWins=%d MaxRelCIHalfWidth=%.2f ZeroWinAlpha=%.2f MaxRefDisagreement=%.2f MinReferenceProb=%.1g\n",
		th.MinWins, th.MaxRelCIHalfWidth, th.ZeroWinAlpha, th.MaxReferenceRelDisagreement, th.MinReferenceProb)
	fmt.Printf("rankability gates: MinESS=%.0f MinESS/N=%.2f MaxWeight=%.0f MaxLowPrecisionFrac=%.3f MaxMCZeroWinsFrac=%.3f MaxUnusableFrac=%.3f MaxSensitivity=%.3f\n",
		gate.MinESS, gate.MinESSOverN, gate.MaxWeight, gate.MaxLowPrecisionFraction, gate.MaxMCZeroWinsFraction, gate.MaxUnusableFraction, gate.MaxPropensitySensitivity)

	// ---------------------------------------------------------------- matrix
	if !skipMatrix {
		env := opeval.FourArmEnv()
		states := opeval.Matrix(env, seed)
		states = append(states, opeval.HarnessStates(harnessN, 10000, seed)...)
		section(fmt.Sprintf("POSTERIOR TEST MATRIX (%d states, draws %v)", len(states), matDraws))
		t0 := time.Now()
		cmps := opeval.CompareAll(states, matDraws, seed, refOpts, th)
		fmt.Printf("compared %d state x draw-count combinations in %v\n\n", len(cmps), time.Since(t0).Round(time.Millisecond))

		reportReferenceHealth(cmps)
		reportErrorByDraws(cmps)
		for _, d := range matDraws {
			if d > 200000 {
				continue
			}
			reportWorst(cmps, d, 20)
		}
		reportBuckets(cmps)
		reportZeroWins(cmps)
	}

	if skipLog {
		return
	}

	// ------------------------------------------------------------- bandit log
	var ledger []*gateway.LedgerDecision
	env := opeval.FourArmEnv()
	if evidencePath != "" {
		var err error
		ledger, err = gateway.LedgerFromFile(evidencePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load ledger: %v\n", err)
			os.Exit(1)
		}
		section(fmt.Sprintf("BANDIT LOG: %s (%d decisions)", evidencePath, len(ledger)))
	} else {
		ledger = opeval.Run(env, decisions, seed)
		section(fmt.Sprintf("BANDIT LOG: synthetic 0.8/0.6/0.4/0.2, %d decisions, seed %d", len(ledger), seed))
		fmt.Printf("known exact uniform-policy value: %.4f\n", env.UniformValue())
		fmt.Printf("known exact best-arm value:       %.4f\n", env.BestValue())
	}
	fmt.Println(loggingPolicyCounts(ledger))

	specs := opeval.DefaultSweep(seed, sweepDraws)
	section("RECONSTRUCTING LOGGING PROPENSITIES")
	t0 := time.Now()
	rowSets := opeval.BuildRowSets(ledger, specs, func(label string) {
		fmt.Printf("  %-24s ... ", label)
		os.Stdout.Sync()
	})
	fmt.Printf("\ntotal reconstruction time %v\n", time.Since(t0).Round(time.Millisecond))
	for _, rs := range rowSets {
		reportRowSet(rs)
	}

	// ------------------------------------------- independent Thompson calibration
	section("INDEPENDENT THOMPSON CALIBRATION")
	fmt.Println("weights are pi_numerical-reference(a) / pi_monte-carlo(a) on the logged action.")
	fmt.Println("identical-estimator self-evaluation is reported last and is IMPLEMENTATION_SANITY_ONLY.")
	calN := min(len(ledger), calDecisions)
	fmt.Printf("(computed over the first %d decisions)\n\n", calN)
	calLedger := ledger[:calN]
	refCache := opeval.BuildRefCache(calLedger, refOpts)
	fmt.Printf("%-8s %-6s %10s %10s %10s %10s %10s %8s\n", "draws", "n", "mean w", "median w", "min w", "max w", "p95|w-1|", "skipped")
	for _, d := range sweepDraws {
		c := opeval.CalibrateCached(calLedger, refCache, d, seed, false)
		fmt.Printf("%-8s %-6d %10.6f %10.6f %10.6f %10.6f %10.6f %8d\n",
			human(d), c.N, c.MeanWeight, c.MedianWeight, c.MinWeight, c.MaxWeight, c.P95AbsDeviation, c.Skipped)
	}
	fmt.Println("\nreversed direction (pi_monte-carlo / pi_numerical-reference):")
	fmt.Printf("%-8s %-6s %10s %10s %10s %10s %10s %8s\n", "draws", "n", "mean w", "median w", "min w", "max w", "p95|w-1|", "skipped")
	for _, d := range sweepDraws {
		c := opeval.CalibrateCached(calLedger, refCache, d, seed, true)
		fmt.Printf("%-8s %-6d %10.6f %10.6f %10.6f %10.6f %10.6f %8d\n",
			human(d), c.N, c.MeanWeight, c.MedianWeight, c.MinWeight, c.MaxWeight, c.P95AbsDeviation, c.Skipped)
	}
	san := opeval.SanityOnlySelfEvaluation(calLedger[:min(calN, 200)], sweepDraws[0], seed)
	fmt.Printf("\n%s: numerator=%s denominator=%s n=%d min w=%.6f max w=%.6f\n",
		san.Label, san.Numerator, san.Denominator, san.N, san.MinWeight, san.MaxWeight)
	fmt.Println("  -> weights are 1 by construction. This checks row joining, not propensities.")

	// -------------------------------------------- importance-weight sensitivity
	section("IMPORTANCE-WEIGHT SENSITIVITY")
	for _, cand := range []gateway.CandidatePolicy{gateway.UniformCandidate{}, gateway.GreedyCandidate{}} {
		fmt.Printf("\ncandidate %s, denominator error at %s draws:\n", cand.ID(), human(sweepDraws[0]))
		fmt.Printf("%-20s %6s %14s %14s %14s %14s %8s\n", "reference p band", "n", "mean rel w err", "p95 rel w err", "max rel w err", "max true w", "MC zeros")
		for _, r := range opeval.WeightSensitivityCached(calLedger, refCache, cand, sweepDraws[0], seed) {
			fmt.Printf("%-20s %6d %14s %14s %14s %14s %8d\n", r.Label, r.N,
				f(r.MeanRelWeightError), f(r.P95RelWeightError), f(r.MaxRelWeightError), f(r.MaxTrueWeight), r.ZeroWins)
		}
	}

	// ------------------------------------------------------------- candidates
	section("CANDIDATE DIAGNOSTICS AND RANKABILITY")
	candidates := []gateway.CandidatePolicy{
		gateway.UniformCandidate{},
		gateway.GreedyCandidate{},
		gateway.NewThompsonReferenceCandidate(),
		gateway.ExactThompsonCandidate{Draws: sweepDraws[0], Seed: seed},
	}
	fmt.Println("cells marked SANITY share an estimator between numerator and denominator:")
	fmt.Println("their weights are 1 by construction, so they reproduce the empirical logging")
	fmt.Println("mean whatever the propensity error is. They are excluded from gating and ranking.")
	var reports []gateway.RankabilityReport
	for _, cand := range candidates {
		results := opeval.EvaluateOver(rowSets, cand, nil, bootstrap, seed)
		fmt.Printf("\n--- %s ---\n", cand.ID())
		fmt.Printf("%-22s %9s %9s %9s %9s %9s %9s %9s %9s  %s\n",
			"propensity source", "IPS", "SNIPS", "ESS", "ESS/N", "maxW", "usable", "zeroWin", "lowPrec", "note")
		var independent []opeval.SweepResult
		for _, r := range results {
			e := r.Estimate
			note := ""
			if selfPaired(cand.ID(), r.Label, sweepDraws[0]) {
				note = "SANITY"
			} else {
				independent = append(independent, r)
			}
			fmt.Printf("%-22s %9.4f %9.4f %9.1f %9.4f %9.2f %9d %9d %9d  %s\n",
				r.Label, e.IPS, e.SNIPS, e.ESS, e.ESSOverN, e.MaxWeight, e.UsableDecisions, e.MCZeroWinsCount, e.LowPrecisionCount, note)
		}
		if len(independent) == 0 {
			fmt.Println("no estimator-independent denominator available for this candidate: NOT_RANKABLE")
			reports = append(reports, gateway.RankabilityReport{
				CandidateID: cand.ID(), Status: gateway.NotRankable,
				Support:  gateway.PoorSupportAndPropensityUncertain,
				Failures: []string{"every available denominator shares an estimator with the candidate (self-evaluation)"},
				Config:   gate,
			})
			continue
		}
		if bootstrap > 0 && independent[0].Estimate.BootstrapSE != nil {
			e := independent[0].Estimate
			fmt.Printf("gate denominator %q bootstrap: SE %.4f  95%% CI [%.4f, %.4f]\n",
				independent[0].Label, *e.BootstrapSE, e.BootstrapCI95[0], e.BootstrapCI95[1])
		}
		sens, used := opeval.Sensitivity(independent, gate.MaxUnusableFraction)
		if sens != nil {
			fmt.Printf("propensity sensitivity (SNIPS spread over %v): %.6f\n", used, *sens)
		} else {
			fmt.Printf("propensity sensitivity: UNMEASURED (only %d source(s) accepted: %v)\n", len(used), used)
		}
		// The gate is applied to the most accurate estimator-independent
		// denominator available, so a failure is not an artefact of draw count.
		rep := gateway.AssessRankability(independent[0].Estimate, gate, sens, used)
		rep.CandidateID = cand.ID()
		fmt.Printf("gate denominator: %s\n", independent[0].Label)
		fmt.Printf("status: %s   support/precision: %s\n", rep.Status, rep.Support)
		for _, fl := range rep.Failures {
			fmt.Printf("  gate failed: %s\n", fl)
		}
		fmt.Printf("remedy: %s\n", rep.Remedy())
		reports = append(reports, rep)
	}

	section("RANKING")
	rank := gateway.Rank(reports)
	for i, r := range rank.Ranked {
		fmt.Printf("%d. %s  SNIPS=%.4f  ESS=%.1f\n", i+1, r.CandidateID, r.Estimate.SNIPS, r.Estimate.ESS)
	}
	for _, n := range rank.Notes {
		fmt.Printf("note: %s\n", n)
	}
	fmt.Printf("winner: %s\n", orNone(rank.Winner))
	fmt.Printf("loser:  %s\n", orNone(rank.Loser))
}

// ------------------------------------------------------------------ reporting

func section(title string) {
	fmt.Printf("\n%s\n%s\n", title, strings.Repeat("=", len(title)))
}

func reportReferenceHealth(cmps []opeval.StateComparison) {
	seen := map[string]bool{}
	worstSum, worstName := 0.0, ""
	failures, nonConverged, n := 0, 0, 0
	for _, c := range cmps {
		if seen[c.StateName] {
			continue
		}
		seen[c.StateName] = true
		n++
		if c.RefFailed {
			failures++
			fmt.Printf("  NUMERICAL_REFERENCE_FAILURE %s: %s\n", c.StateName, c.RefErr)
			continue
		}
		if !c.RefConverged {
			nonConverged++
		}
		if math.Abs(c.RefSumError) > math.Abs(worstSum) {
			worstSum, worstName = c.RefSumError, c.StateName
		}
	}
	fmt.Printf("reference health over %d states: %d failures, %d non-converged\n", n, failures, nonConverged)
	fmt.Printf("worst pre-normalization sum residual: %.3g (%s)\n\n", worstSum, worstName)
}

func reportErrorByDraws(cmps []opeval.StateComparison) {
	type agg struct{ abs, rel []float64 }
	byDraws := map[int]*agg{}
	for _, c := range cmps {
		if c.RefFailed {
			continue
		}
		a := byDraws[c.Draws]
		if a == nil {
			a = &agg{}
			byDraws[c.Draws] = a
		}
		for _, e := range c.Errors {
			a.abs = append(a.abs, e.AbsError)
			if !math.IsNaN(e.RelError) {
				a.rel = append(a.rel, e.RelError)
			}
		}
	}
	var ds []int
	for d := range byDraws {
		ds = append(ds, d)
	}
	sort.Ints(ds)
	fmt.Printf("%-10s %8s %14s %14s %14s %14s\n", "draws", "arms", "mean abs err", "max abs err", "median rel", "max rel err")
	for _, d := range ds {
		a := byDraws[d]
		sort.Float64s(a.abs)
		sort.Float64s(a.rel)
		fmt.Printf("%-10s %8d %14s %14s %14s %14s\n", human(d), len(a.abs),
			f(mean(a.abs)), f(last(a.abs)), f(median(a.rel)), f(last(a.rel)))
	}
	fmt.Println()
}

func reportWorst(cmps []opeval.StateComparison, draws, n int) {
	var errs []opeval.ArmError
	for _, c := range cmps {
		if c.Draws != draws || c.RefFailed {
			continue
		}
		errs = append(errs, c.Errors...)
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].AbsError > errs[j].AbsError })
	fmt.Printf("worst %d absolute Monte-Carlo errors at %s draws:\n", n, human(draws))
	fmt.Printf("%-34s %-8s %12s %12s %12s %12s %8s %-14s\n", "state", "arm", "reference", "estimated", "abs err", "rel err", "wins", "status")
	for i := 0; i < n && i < len(errs); i++ {
		e := errs[i]
		fmt.Printf("%-34s %-8s %12s %12s %12s %12s %8d %-14s\n",
			trunc(e.StateName, 34), e.ArmID, f(e.Reference), f(e.Estimated), f(e.AbsError), f(e.RelError), e.Wins, e.Status)
	}
	fmt.Println()
}

func reportBuckets(cmps []opeval.StateComparison) {
	fmt.Println("relative error for rare actions, by reference-probability band:")
	fmt.Printf("%-12s %-10s %8s %10s %14s %14s %14s\n", "ref p <", "draws", "n", "zero wins", "mean rel err", "median rel err", "max rel err")
	for _, b := range opeval.SummarizeBuckets(cmps) {
		if b.N == 0 {
			continue
		}
		fmt.Printf("%-12s %-10s %8d %10d %14s %14s %14s\n",
			f(b.Threshold), human(b.Draws), b.N, b.ZeroWins, f(b.MeanRelError), f(b.MedianRelErr), f(b.MaxRelError))
	}
	fmt.Println()
}

func reportZeroWins(cmps []opeval.StateComparison) {
	var rows []opeval.ArmError
	subResolution := 0
	for _, c := range cmps {
		if c.RefFailed {
			continue
		}
		for _, e := range c.Errors {
			if !e.ZeroWinButPositiveReference {
				continue
			}
			if e.ReferenceResolved {
				rows = append(rows, e)
			} else {
				subResolution++
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Reference > rows[j].Reference })
	fmt.Printf("MC_ZERO_WINS actions whose numerical reference is positive AND above the\n")
	fmt.Printf("numerical resolution floor (%.0e): %d\n", propensity.DefaultThresholds().MinReferenceProb, len(rows))
	fmt.Println("(each is an action the estimator reports as impossible and the reference reports")
	fmt.Println(" as reachable; believing the estimate would turn it into false zero support)")
	fmt.Printf("%-34s %-8s %-10s %14s %16s\n", "state", "arm", "draws", "reference p", "one-sided 95% UB")
	shown := 0
	for _, e := range rows {
		if shown >= 40 {
			fmt.Printf("... %d more\n", len(rows)-shown)
			break
		}
		fmt.Printf("%-34s %-8s %-10s %14s %16s\n",
			trunc(e.StateName, 34), e.ArmID, human(e.Draws), f(e.Reference), f(3.0/float64(e.Draws)))
		shown++
	}
	fmt.Printf("\na further %d zero-win actions have a reference below the resolution floor:\n", subResolution)
	fmt.Println("there the integral itself underflowed, so neither estimator resolves the action")
	fmt.Println("and the row is refused as LOW_PRECISION rather than believed in either direction.")
	fmt.Println()
}

func reportRowSet(rs opeval.RowSet) {
	elig, usable := 0, 0
	byStatus := map[propensity.Status]int{}
	minP := math.Inf(1)
	for _, r := range rs.Records {
		if r.Status != gateway.OPEEligible {
			continue
		}
		elig++
		byStatus[r.PropensityStatus]++
		if r.Usable() {
			usable++
			if r.LoggingPropensity != nil && *r.LoggingPropensity < minP {
				minP = *r.LoggingPropensity
			}
		}
	}
	fmt.Printf("  %-24s eligible=%-6d usable=%-6d minP=%-12s statuses=%v\n",
		rs.Label, elig, usable, f(minP), statusCounts(byStatus))
}

func statusCounts(m map[propensity.Status]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[propensity.Status(k)]))
	}
	return "{" + strings.Join(parts, " ") + "}"
}

func loggingPolicyCounts(ledger []*gateway.LedgerDecision) string {
	counts := map[string]int{}
	for _, d := range ledger {
		if d.Started != nil {
			counts[d.Started.SelectedArmID]++
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	return "logging-policy arm counts: " + strings.Join(parts, " ")
}

// ------------------------------------------------------------------- helpers

func parseInts(csv string) []int {
	var out []int
	for _, p := range strings.Split(csv, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad integer %q: %v\n", p, err)
			os.Exit(2)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		out = []int{10000}
	}
	sort.Ints(out)
	return out
}

func human(d int) string {
	switch {
	case d >= 1_000_000 && d%1_000_000 == 0:
		return fmt.Sprintf("%dM", d/1_000_000)
	case d >= 1000 && d%1000 == 0:
		return fmt.Sprintf("%dk", d/1000)
	default:
		return strconv.Itoa(d)
	}
}

func f(v float64) string {
	switch {
	case math.IsNaN(v):
		return "n/a"
	case math.IsInf(v, 0):
		return "inf"
	case v == 0:
		return "0"
	case math.Abs(v) < 1e-4 || math.Abs(v) >= 1e6:
		return fmt.Sprintf("%.3e", v)
	default:
		return fmt.Sprintf("%.6f", v)
	}
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}
func last(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	return v[len(v)-1]
}
func median(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	return v[len(v)/2]
}
func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "~"
}
func orNone(s string) string {
	if s == "" {
		return "(none named)"
	}
	return s
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// selfPaired reports whether a candidate policy and a propensity source are the
// same estimator, in which case every importance weight is 1 by construction
// and the resulting "estimate" is the empirical logging mean wearing a costume.
func selfPaired(candidateID, sourceLabel string, primaryDraws int) bool {
	switch candidateID {
	case "thompson-numerical-reference-v1":
		return sourceLabel == "numerical-reference"
	case "exact-thompson-v1":
		// ExactThompsonCandidate is constructed here with the primary draw count
		// and the shared seed, so it collides with exactly that Monte-Carlo source.
		return sourceLabel == "mc-"+human(primaryDraws)
	default:
		return false
	}
}
