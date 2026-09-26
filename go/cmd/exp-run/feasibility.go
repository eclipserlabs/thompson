// Historical-workload feasibility assessment (read-only).
//
// Assess answers the ten feasibility questions over validated customer
// records without imputing anything: missing costs stay missing, UNKNOWN
// outcomes are censored (never converted to failures), and historical
// observations are never presented as causal proof of Thompson outperforming
// an alternative policy.
package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/harness"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// FeasibilityVersion versions the machine-readable report.
const FeasibilityVersion = "feasibility-v1"

// CandidateEffects are the relative improvements sized from observed
// variance. They parameterize data collection; they are not claims.
var CandidateEffects = []float64{0.10, 0.15, 0.20}

// Rejection is one record the assessment refused to interpret, with its
// reason. Rejected rows never enter any numerator or denominator.
type Rejection struct {
	Line   int    `json:"line,omitempty"`
	JobID  string `json:"job_id,omitempty"`
	Reason string `json:"reason"`
}

// FeasibilityReport is the machine-readable assessment.
type FeasibilityReport struct {
	Version   string `json:"version"`
	Source    string `json:"source"`
	CreatedAt string `json:"created_at"`

	TotalRows    int         `json:"total_rows"`
	AcceptedRows int         `json:"accepted_rows"`
	RejectedRows int         `json:"rejected_rows"`
	Rejections   []Rejection `json:"rejections,omitempty"`
	UniqueJobs   int         `json:"unique_jobs"`

	// Q1: stable unique identifiers.
	StableUniqueIDs    bool `json:"stable_unique_ids"`
	DuplicateConflicts int  `json:"duplicate_conflicts"`
	VersionGaps        int  `json:"version_gaps"`

	// Q2: independent task-level verification over settled jobs.
	SettledJobs           int     `json:"settled_jobs"`
	IndependentlyVerified int     `json:"independently_verified_jobs"`
	SelfVerifiedJobs      int     `json:"self_verified_jobs"`
	VerifiedShare         float64 `json:"verified_share"`

	// Q3: UNKNOWN share over latest-version jobs.
	UnknownJobs int     `json:"unknown_jobs"`
	UnknownPct  float64 `json:"unknown_pct"`

	// Q4: cost completeness over latest-version jobs.
	IncompleteCostJobs int `json:"incomplete_cost_jobs"`
	FullyMeteredJobs   int `json:"fully_metered_jobs"`

	// Q5: retries and fallback observability.
	RetryJobs       int `json:"retry_jobs"`
	FallbackJobs    int `json:"fallback_jobs"`
	TimeoutAttempts int `json:"timeout_attempts"`

	// Q6: eligibility and task-mix coverage.
	EligibilityCoverage float64        `json:"eligibility_coverage"`
	UnclassifiedJobs    int            `json:"unclassified_jobs"`
	StrataMix           map[string]int `json:"strata_mix"`

	// Q7: can historical records support valid comparisons?
	Comparable           bool            `json:"comparable"`
	ComparisonConditions map[string]bool `json:"comparison_conditions"`
	ComparisonNote       string          `json:"comparison_note"`

	// Q8: observed variance in fully loaded costs (fully-metered settled).
	VarianceN      int     `json:"variance_n"`
	VarianceMean   float64 `json:"variance_mean"`
	VarianceStddev float64 `json:"variance_stddev"`

	// Q9: sample-size planning.
	SizingPossible   bool               `json:"sizing_possible"`
	RequiredPerGroup map[string]float64 `json:"required_per_group,omitempty"`

	// Q10: missing fields that block a randomized pilot.
	Blockers []string `json:"blockers,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// latestPerJob folds version chains: the highest version per job wins.
// Exact (job, version) duplicates collapse when identical; conflicting
// duplicates and version gaps are counted, never repaired.
func latestPerJob(recs []CustomerRecord) (latest map[string]CustomerRecord, dupConflicts, gaps int) {
	byJob := map[string][]CustomerRecord{}
	for _, r := range recs {
		byJob[r.JobID] = append(byJob[r.JobID], r)
	}
	latest = map[string]CustomerRecord{}
	for job, vers := range byJob {
		seen := map[uint64]CustomerRecord{}
		for _, v := range vers {
			if prev, ok := seen[v.Version]; ok {
				if !recordsEqual(prev, v) {
					dupConflicts++
				}
				continue
			}
			seen[v.Version] = v
		}
		if len(seen) == 0 {
			continue
		}
		maxV := uint64(0)
		for ver := range seen {
			if ver > maxV {
				maxV = ver
			}
		}
		for ver := uint64(1); ver <= maxV; ver++ {
			if _, ok := seen[ver]; !ok {
				gaps++
				break
			}
		}
		latest[job] = seen[maxV]
	}
	return latest, dupConflicts, gaps
}

func recordsEqual(a, b CustomerRecord) bool {
	if a.Status != b.Status || a.DecidingAttemptID != b.DecidingAttemptID ||
		a.VerifiedBy != b.VerifiedBy || len(a.Attempts) != len(b.Attempts) {
		return false
	}
	for i := range a.Attempts {
		aa, bb := a.Attempts[i], b.Attempts[i]
		if aa.AttemptID != bb.AttemptID || aa.Verified != bb.Verified ||
			aa.ExecutorID != bb.ExecutorID || aa.Transport != bb.Transport {
			return false
		}
		if (aa.CostUSD == nil) != (bb.CostUSD == nil) {
			return false
		}
		if aa.CostUSD != nil && *aa.CostUSD != *bb.CostUSD {
			return false
		}
	}
	return true
}

// Assess folds validated records into a FeasibilityReport. Validation of each
// row must happen first (ValidateRecord); rows that fail it are passed as
// rejections and counted, never interpreted.
func Assess(valid []CustomerRecord, rejections []Rejection, source string) FeasibilityReport {
	rep := FeasibilityReport{
		Version: FeasibilityVersion, Source: source,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		TotalRows:    len(valid) + len(rejections),
		AcceptedRows: len(valid), RejectedRows: len(rejections),
		Rejections:           rejections,
		ComparisonConditions: map[string]bool{},
		RequiredPerGroup:     map[string]float64{},
		StrataMix:            map[string]int{},
	}
	latest, dups, gaps := latestPerJob(valid)
	rep.DuplicateConflicts = dups
	rep.VersionGaps = gaps
	rep.UniqueJobs = len(latest)
	rep.StableUniqueIDs = dups == 0 && gaps == 0 && len(valid) > 0

	ordered := make([]CustomerRecord, 0, len(latest))
	for _, r := range latest {
		ordered = append(ordered, r)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].JobID < ordered[j].JobID })

	// Q2/Q3/Q4/Q5/Q6 over latest versions.
	var settled, verified, self int
	var unknown int
	var incomplete, metered int
	var retries, fallbacks, timeouts int
	var eligible int
	var unclassified int
	var assignCovered int
	var fullyMeteredCosts []float64
	for _, r := range ordered {
		switch r.Status {
		case outcome.StatusAccepted, outcome.StatusRejected:
			settled++
			if strings.TrimSpace(r.VerifiedBy) != "" {
				verified++
			}
			if SelfVerified(r) {
				self++
			}
		case outcome.StatusUnknown:
			unknown++
		}
		_, unmet := FullyLoadedCost(r)
		if unmet > 0 {
			incomplete++
		} else {
			metered++
		}
		if len(r.Attempts) > 1 {
			retries++
		}
		if HasFallback(r) {
			fallbacks++
		}
		for _, a := range r.Attempts {
			if a.Transport == outcome.TransportTimeout {
				timeouts++
				break
			}
		}
		if len(r.Eligible) > 0 {
			eligible++
		}
		if strings.TrimSpace(r.Strata) == "" {
			unclassified++
		}
		if r.AssignProb != nil && strings.TrimSpace(r.Assigner) != "" &&
			strings.TrimSpace(r.Executed) != "" {
			assignCovered++
		}
		if r.Status == outcome.StatusAccepted || r.Status == outcome.StatusRejected {
			if cost, un := FullyLoadedCost(r); un == 0 {
				fullyMeteredCosts = append(fullyMeteredCosts, cost)
			}
		}
	}
	rep.SettledJobs = settled
	rep.IndependentlyVerified = verified - self
	rep.SelfVerifiedJobs = self
	if settled > 0 {
		rep.VerifiedShare = float64(rep.IndependentlyVerified) / float64(settled)
	}
	if len(ordered) > 0 {
		rep.UnknownJobs = unknown
		rep.UnknownPct = 100 * float64(unknown) / float64(len(ordered))
		rep.EligibilityCoverage = float64(eligible) / float64(len(ordered))
	}
	rep.IncompleteCostJobs = incomplete
	rep.FullyMeteredJobs = metered
	rep.RetryJobs = retries
	rep.FallbackJobs = fallbacks
	rep.TimeoutAttempts = timeouts
	rep.UnclassifiedJobs = unclassified
	rep.StrataMix = StrataMix(ordered)

	// Q7: valid comparisons need jointly: stable IDs, independent
	// verification of every settled job, assignment provenance on every
	// settled job with execution inside the eligible set, and no version
	// gaps. Historical exports almost never satisfy this; the honest answer
	// is then "no — run a randomized pilot".
	conds := map[string]bool{
		"stable_ids":            rep.StableUniqueIDs,
		"all_settled_verified":  settled > 0 && rep.IndependentlyVerified == settled,
		"assignment_provenance": settled > 0 && assignCovered == settled,
		"eligibility_recorded":  len(ordered) > 0 && eligible == len(ordered),
		"no_version_gaps":       gaps == 0,
		"has_settled_jobs":      settled > 0,
	}
	eligibleExec := true
	for _, r := range ordered {
		if r.Status != outcome.StatusAccepted && r.Status != outcome.StatusRejected {
			continue
		}
		if len(r.Eligible) > 0 && r.Executed != "" {
			found := false
			for _, e := range r.Eligible {
				if e == r.Executed {
					found = true
					break
				}
			}
			if !found {
				eligibleExec = false
			}
		}
		if r.Executed == "" {
			eligibleExec = false
		}
	}
	conds["execution_within_eligibility"] = settled > 0 && eligibleExec
	rep.ComparisonConditions = conds
	rep.Comparable = true
	for _, v := range conds {
		if !v {
			rep.Comparable = false
			break
		}
	}
	if rep.Comparable {
		rep.ComparisonNote = "historical records carry assignment provenance and can support covariate-adjusted comparison; a randomized pilot is still required for causal claims"
	} else {
		rep.ComparisonNote = "historical records are observational: they cannot support valid causal comparisons; a randomized pilot with recorded assignment probabilities is required"
	}

	// Q8: observed variance over fully-metered settled jobs.
	rep.VarianceN = len(fullyMeteredCosts)
	if len(fullyMeteredCosts) >= 1 {
		mean := 0.0
		for _, c := range fullyMeteredCosts {
			mean += c
		}
		mean /= float64(len(fullyMeteredCosts))
		rep.VarianceMean = mean
	}
	if len(fullyMeteredCosts) >= 2 {
		v := 0.0
		for _, c := range fullyMeteredCosts {
			v += (c - rep.VarianceMean) * (c - rep.VarianceMean)
		}
		rep.VarianceStddev = math.Sqrt(v / float64(len(fullyMeteredCosts)-1))
	}

	// Q9: sizing is possible when mean and variance are positive.
	if rep.VarianceN >= 2 && rep.VarianceMean > 0 && rep.VarianceStddev > 0 {
		rep.SizingPossible = true
		for _, eff := range CandidateEffects {
			key := fmt.Sprintf("rel_%.2f", eff)
			rep.RequiredPerGroup[key] = harness.RequiredPerGroup(
				rep.VarianceStddev, rep.VarianceMean, eff, 0.05, 0.8)
		}
	}

	// Q10: blockers.
	var blockers []string
	if len(ordered) == 0 {
		blockers = append(blockers, "no usable records: nothing to assess")
	}
	if !rep.StableUniqueIDs {
		blockers = append(blockers, "unstable or duplicated job identifiers: randomize and re-export with stable job_id per job")
	}
	if settled == 0 {
		blockers = append(blockers, "no settled jobs with verified outcomes: independent verification required before any pilot")
	} else if rep.IndependentlyVerified < settled {
		blockers = append(blockers, fmt.Sprintf("%d of %d settled jobs lack independent verification: route every pilot job through the independent verifier",
			settled-rep.IndependentlyVerified, settled))
	}
	if self > 0 {
		rep.Warnings = append(rep.Warnings,
			fmt.Sprintf("%d settled jobs are self-verified (verified_by == executor): excluded from independent evidence", self))
	}
	if rep.FullyMeteredJobs == 0 {
		blockers = append(blockers, "no fully-metered jobs: complete cost metering required; missing costs will not be imputed")
	} else if incomplete > 0 {
		rep.Warnings = append(rep.Warnings,
			fmt.Sprintf("%d jobs have incomplete cost records: excluded from primary-metric analysis, retained in accounting", incomplete))
	}
	if eligible < len(ordered) {
		blockers = append(blockers, "strategy eligibility not recorded for every job: the pilot must log eligible strategies per job")
	}
	if !conds["assignment_provenance"] || !conds["execution_within_eligibility"] {
		blockers = append(blockers, "no randomized assignment provenance: the first pilot must randomize with recorded assignment probabilities")
	}
	if !rep.SizingPossible {
		blockers = append(blockers, "insufficient fully-metered data for sample-size planning: collect metered outcomes before freezing the charter")
	}
	if unknown > 0 {
		rep.Warnings = append(rep.Warnings,
			fmt.Sprintf("%.1f%% of jobs are UNKNOWN at export: censored from learning and numerators, covered by the missing-data rule", rep.UnknownPct))
	}
	rep.Blockers = blockers
	return rep
}

// Summary renders the human-readable feasibility summary.
func (r FeasibilityReport) Summary() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "feasibility %s (source %s, %s)\n", r.Version, r.Source, r.CreatedAt)
	fmt.Fprintf(&sb, "rows: total=%d accepted=%d rejected=%d unique_jobs=%d\n",
		r.TotalRows, r.AcceptedRows, r.RejectedRows, r.UniqueJobs)
	fmt.Fprintf(&sb, "Q1 stable unique IDs: %v (conflicts=%d gaps=%d)\n",
		r.StableUniqueIDs, r.DuplicateConflicts, r.VersionGaps)
	fmt.Fprintf(&sb, "Q2 independently verified: %d/%d settled (self-verified=%d)\n",
		r.IndependentlyVerified, r.SettledJobs, r.SelfVerifiedJobs)
	fmt.Fprintf(&sb, "Q3 UNKNOWN outcomes: %d (%.1f%%)\n", r.UnknownJobs, r.UnknownPct)
	fmt.Fprintf(&sb, "Q4 cost records: incomplete=%d fully-metered=%d\n",
		r.IncompleteCostJobs, r.FullyMeteredJobs)
	fmt.Fprintf(&sb, "Q5 retries=%d fallbacks=%d jobs with timeouts=%d\n",
		r.RetryJobs, r.FallbackJobs, r.TimeoutAttempts)
	fmt.Fprintf(&sb, "Q6 eligibility coverage=%.3f unclassified=%d strata=%v\n",
		r.EligibilityCoverage, r.UnclassifiedJobs, r.StrataMix)
	fmt.Fprintf(&sb, "Q7 valid comparisons: %v (%s)\n", r.Comparable, r.ComparisonNote)
	for _, k := range SortedKeys(mapKeysToTally(r.ComparisonConditions)) {
		fmt.Fprintf(&sb, "    - %s: %v\n", k, r.ComparisonConditions[k])
	}
	fmt.Fprintf(&sb, "Q8 fully-loaded cost variance: n=%d mean=%.4f stddev=%.4f\n",
		r.VarianceN, r.VarianceMean, r.VarianceStddev)
	fmt.Fprintf(&sb, "Q9 sizing possible: %v required_per_group=%v\n",
		r.SizingPossible, r.RequiredPerGroup)
	fmt.Fprintf(&sb, "Q10 blockers (%d):\n", len(r.Blockers))
	for _, b := range r.Blockers {
		fmt.Fprintf(&sb, "    - %s\n", b)
	}
	if len(r.Warnings) > 0 {
		fmt.Fprintf(&sb, "warnings (%d):\n", len(r.Warnings))
		for _, w := range r.Warnings {
			fmt.Fprintf(&sb, "    - %s\n", w)
		}
	}
	fmt.Fprintf(&sb, "Historical observations are observational and are not causal proof that Thompson outperforms any alternative policy.\n")
	return sb.String()
}

func mapKeysToTally(m map[string]bool) map[string]int {
	out := make(map[string]int, len(m))
	for k := range m {
		out[k] = 1
	}
	return out
}
