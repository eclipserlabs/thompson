// Package harness — offline experiment report.
//
// Analyze folds treatment ledgers (assignments + versioned outcomes) into the
// readout required by PR3B_EXPERIMENT_SPEC.md: full-assignment accounting,
// primary metric over fully-metered matured jobs, bootstrap comparison CIs,
// pre-registered sensitivities, and gate verdicts. The learner, sampler, and
// reward mapping are untouched: this code only reads ledgers.
package harness

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/outcome"
)

// ReportConfig freezes the analysis parameters. Every field is an experiment
// charter item: changing one restarts the experiment.
type ReportConfig struct {
	Maturation    time.Duration
	Now           time.Time
	MinJobs       int
	CensorGate    float64
	QualityFloor  float64
	MinEffect     float64
	BootstrapN    int
	BootstrapSeed uint64
}

// JobRecord is one assigned job evaluated at the maturity cutoff.
type JobRecord struct {
	JobID          string
	Treatment      string
	Probability    float64
	AssignedAt     time.Time
	Matured        bool
	HasOutcome     bool
	Status         outcome.JobStatus
	CostMetered    float64
	Unmetered      int
	FullyMetered   bool
	Accepted       bool
	CorrectedAfter bool
}

// TreatmentStats is the per-treatment readout over matured jobs.
type TreatmentStats struct {
	// Assigned counts every randomized job (matured or not).
	Assigned   int `json:"assigned"`
	Matured    int `json:"matured"`
	Accepted   int `json:"accepted"`
	Rejected   int `json:"rejected"`
	Unknown    int `json:"unknown"`
	Pending    int `json:"pending"`
	Unresolved int `json:"unresolved"`
	// CensoredFraction counts UNKNOWN-at-maturity over matured jobs.
	CensoredFraction float64 `json:"censored_fraction"`
	MeteredCost      float64 `json:"metered_cost"`
	UnmeteredJobs    int     `json:"unmetered_jobs"`
	// Primary is fully loaded cost per verified success over fully-metered
	// matured jobs. NaN when no verified success exists.
	Primary      float64 `json:"primary"`
	MeteredShare float64 `json:"metered_share"`
	// AcceptRate is ACCEPTED over settled (ACCEPTED+REJECTED) matured jobs.
	AcceptRate float64 `json:"accept_rate"`
}

// Comparison is one treatment pair difference (candidate minus baseline).
// MeetsBar is strict (commercial gate B): the whole relative-improvement
// confidence interval must clear the pre-registered threshold, not merely
// the point estimate with a CI above zero.
type Comparison struct {
	Pair           string  `json:"pair"`
	Diff           float64 `json:"diff"`
	RelImprovement float64 `json:"rel_improvement"`
	CILow          float64 `json:"ci_low"`
	CIHigh         float64 `json:"ci_high"`
	RelCILow       float64 `json:"rel_ci_low"`
	RelCIHigh      float64 `json:"rel_ci_high"`
	Wins           bool    `json:"wins"`
	MeetsBar       bool    `json:"meets_bar"`
}

// GateResult is one refusal gate.
type GateResult struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
}

// Sensitivity holds the four pre-registered robustness checks.
type Sensitivity struct {
	WorstCaseCensoringWins bool    `json:"worst_case_censoring_wins"`
	MissingCostLow         float64 `json:"missing_cost_low"`
	MissingCostHigh        float64 `json:"missing_cost_high"`
	HalfMaturityStable     bool    `json:"half_maturity_stable"`
	DoubleMaturityStable   bool    `json:"double_maturity_stable"`
	CorrectionAsymmetry    string  `json:"correction_asymmetry"`
}

// SampleSizeAssessment sizes each comparison from observed dry-run
// variance. Source labels provenance ("synthetic-observed" in dry runs);
// it informs future collection and never amends the frozen charter.
type SampleSizeAssessment struct {
	Source  string             `json:"source"`
	Entries map[string]float64 `json:"per_pair_n"`
}

// Report is the full experiment readout.
type Report struct {
	Maturation  string                    `json:"maturation"`
	Now         string                    `json:"now"`
	Treatments  map[string]TreatmentStats `json:"treatments"`
	Comparisons []Comparison              `json:"comparisons"`
	Sensitivity Sensitivity               `json:"sensitivity"`
	SampleSize  SampleSizeAssessment      `json:"sample_size"`
	Gates       []GateResult              `json:"gates"`
	Verdict     string                    `json:"verdict"`
	Reasons     []string                  `json:"reasons"`
}

// LoadTreatmentDir reads one treatment's assignments + outcomes ledgers.
// Absent files mean zero rows (a treatment may legitimately receive no
// assignments); malformed lines are errors.
func LoadTreatmentDir(dir string) ([]Assignment, []outcome.OutcomeEvent, error) {
	assignments, err := loadAssignments(dir + "/assignments.jsonl")
	if err != nil {
		return nil, nil, err
	}
	events, err := loadOutcomeEvents(dir + "/outcomes.jsonl")
	if err != nil {
		return nil, nil, err
	}
	return assignments, events, nil
}

func loadAssignments(path string) ([]Assignment, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Assignment
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var a Assignment
		if err := json.Unmarshal(line, &a); err != nil {
			return nil, fmt.Errorf("harness: bad assignment line: %w", err)
		}
		out = append(out, a)
	}
	return out, sc.Err()
}

func loadOutcomeEvents(path string) ([]outcome.OutcomeEvent, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []outcome.OutcomeEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev outcome.OutcomeEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("harness: bad outcome line: %w", err)
		}
		out = append(out, ev)
	}
	return out, sc.Err()
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	if t.IsZero() {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t
}

// JobMap joins manifest job IDs to gateway job bindings
// ("job-<first-decision>"). Multi-attempt jobs execute several decisions;
// only the first decision's binding settles, so the map is many-to-one.
type JobMap map[string]string

// LoadJobMap reads dir/jobmap.jsonl (latest row per manifest job wins).
// Absent file means identity mapping (legacy single-decision ledgers).
func LoadJobMap(dir string) (JobMap, error) {
	out := JobMap{}
	f, err := os.Open(dir + "/jobmap.jsonl")
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var row struct {
			ManifestJob string `json:"manifest_job_id"`
			GatewayJob  string `json:"gateway_job_id"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, fmt.Errorf("harness: bad jobmap line: %w", err)
		}
		out[row.ManifestJob] = row.GatewayJob
	}
	return out, sc.Err()
}

// MatureJobs joins assignments to outcomes under a per-job maturation
// window: a job assigned at t matures at t+window, evaluated against the
// analysis clock now. The status used is the latest version verified at or
// before the job's maturity instant; later versions only set
// CorrectedAfter. Jobs maturing after now are excluded (not finalized).
func MatureJobs(assignments []Assignment, events []outcome.OutcomeEvent, now time.Time, window time.Duration) []JobRecord {
	return MatureJobsMapped(assignments, events, now, window, nil)
}

// MatureJobsMapped is MatureJobs with an explicit manifest→gateway join. A
// nil map means identity (outcome JobIDs equal assignment JobIDs).
func MatureJobsMapped(assignments []Assignment, events []outcome.OutcomeEvent, now time.Time, window time.Duration, jobMap JobMap) []JobRecord {
	byJob := make(map[string][]outcome.OutcomeEvent)
	for _, ev := range events {
		byJob[ev.JobID] = append(byJob[ev.JobID], ev)
	}
	var out []JobRecord
	for _, a := range assignments {
		at := parseTime(a.AssignedAt)
		rec := JobRecord{JobID: a.JobID, Treatment: a.Treatment, Probability: a.Probability, AssignedAt: at}
		maturesAt := at.Add(window)
		rec.Matured = !maturesAt.After(now)
		oid := a.JobID
		if jobMap != nil {
			if mapped, ok := jobMap[a.JobID]; ok {
				oid = mapped
			} else {
				// Assigned but never executed far enough to map: unresolved
				// unless an identity-keyed outcome exists (legacy ledgers).
				if _, direct := byJob[a.JobID]; !direct {
					out = append(out, rec)
					continue
				}
			}
		}
		vers := byJob[oid]
		rec.HasOutcome = len(vers) > 0
		if !rec.Matured {
			out = append(out, rec)
			continue
		}
		if len(vers) == 0 {
			out = append(out, rec)
			continue
		}
		var best *outcome.OutcomeEvent
		for i := range vers {
			vt := parseTime(vers[i].VerifiedAt)
			if vt.IsZero() {
				vt = parseTime(vers[i].OccurredAt)
			}
			if !vt.After(maturesAt) && (best == nil || vers[i].Version > best.Version) {
				best = &vers[i]
			}
			if vt.After(maturesAt) {
				rec.CorrectedAfter = true
			}
		}
		if best == nil {
			out = append(out, rec)
			continue
		}
		rec.Status = best.Status
		rec.Accepted = best.Status == outcome.StatusAccepted
		metered, unmetered := jobCost(*best)
		rec.CostMetered, rec.Unmetered = metered, unmetered
		rec.FullyMetered = unmetered == 0
		out = append(out, rec)
	}
	return out
}

// jobCost sums attempt costs plus human-review cost; nils count as unmetered.
func jobCost(ev outcome.OutcomeEvent) (float64, int) {
	metered, unmetered := 0.0, 0
	humanAttempt := false
	for _, a := range ev.Attempts {
		if a.CostUSD == nil {
			unmetered++
		} else {
			metered += *a.CostUSD
		}
		if a.ExecutorID == "human-pool" {
			humanAttempt = true
		}
	}
	if ev.HumanReviewCostUSD == nil {
		if humanAttempt {
			unmetered++
		}
	} else {
		metered += *ev.HumanReviewCostUSD
	}
	return metered, unmetered
}

// Analyze produces the full readout over matured records. halfStable and
// doubleStable come from MaturityStability at half/double cutoffs: the
// caller computes them because they need the raw ledgers, not just records.
func Analyze(records []JobRecord, cfg ReportConfig, baseline, candidate string, others []string, halfStable, doubleStable bool) Report {
	rep := Report{
		Maturation: cfg.Maturation.String(), Now: cfg.Now.UTC().Format(time.RFC3339Nano),
		Treatments: make(map[string]TreatmentStats),
	}
	byTx := make(map[string][]JobRecord)
	for _, r := range records {
		if !r.Matured {
			continue
		}
		byTx[r.Treatment] = append(byTx[r.Treatment], r)
	}
	names := append([]string{baseline, candidate}, others...)
	seen := map[string]bool{}
	assignedCount := map[string]int{}
	for _, r := range records {
		assignedCount[r.Treatment]++
	}
	for _, n := range names {
		if seen[n] || n == "" {
			continue
		}
		seen[n] = true
		st := summarize(byTx[n])
		st.Assigned = assignedCount[n]
		rep.Treatments[n] = st
	}

	// Gates.
	for n, st := range rep.Treatments {
		if st.Matured < cfg.MinJobs {
			rep.Gates = append(rep.Gates, GateResult{"min-jobs-" + n, false,
				fmt.Sprintf("%d matured < %d", st.Matured, cfg.MinJobs)})
		}
		if st.CensoredFraction > cfg.CensorGate {
			rep.Gates = append(rep.Gates, GateResult{"censoring-" + n, false,
				fmt.Sprintf("%.3f > %.3f", st.CensoredFraction, cfg.CensorGate)})
		}
	}

	// Comparisons candidate-vs-baseline and candidate-vs-others.
	pairs := [][2]string{{candidate, baseline}}
	for _, o := range others {
		pairs = append(pairs, [2]string{candidate, o})
	}
	for _, p := range pairs {
		cs, bs := byTx[p[0]], byTx[p[1]]
		diff, rel := pairDiff(cs, bs)
		lo, hi := bootstrapDiff(cs, bs, cfg.BootstrapN, cfg.BootstrapSeed)
		rlo, rhi := bootstrapRel(cs, bs, cfg.BootstrapN, cfg.BootstrapSeed)
		wins := !math.IsNaN(hi) && hi < 0
		rep.Comparisons = append(rep.Comparisons, Comparison{
			Pair: p[0] + "-" + p[1], Diff: diff, RelImprovement: rel,
			CILow: lo, CIHigh: hi, RelCILow: rlo, RelCIHigh: rhi,
			Wins: wins, MeetsBar: wins && !math.IsNaN(rlo) && rlo >= cfg.MinEffect,
		})
	}

	rep.Sensitivity = sensitivities(byTx, cfg, baseline, candidate)
	rep.Sensitivity.HalfMaturityStable = halfStable
	rep.Sensitivity.DoubleMaturityStable = doubleStable
	rep.SampleSize = assessSampleSize(byTx, cfg, pairs)

	// Verdict.
	rep.Verdict, rep.Reasons = verdict(rep, cfg, baseline, candidate)
	return rep
}

func summarize(rs []JobRecord) TreatmentStats {
	var st TreatmentStats
	st.Matured = len(rs)
	settled := 0
	for _, r := range rs {
		switch {
		case !r.HasOutcome:
			st.Unresolved++
		case r.Status == outcome.StatusAccepted:
			st.Accepted++
			settled++
		case r.Status == outcome.StatusRejected:
			st.Rejected++
			settled++
		case r.Status == outcome.StatusUnknown:
			st.Unknown++
		default:
			st.Pending++
		}
		// Numerator spend: ACCEPTED + REJECTED with known outcomes. UNKNOWN
		// is censored (excluded from numerator and denominator); unresolved
		// jobs never enter the metric (see full-accounting table instead).
		if r.HasOutcome && r.FullyMetered &&
			(r.Status == outcome.StatusAccepted || r.Status == outcome.StatusRejected) {
			st.MeteredCost += r.CostMetered
		}
		if r.HasOutcome && !r.FullyMetered {
			st.UnmeteredJobs++
		}
	}
	if st.Matured > 0 {
		st.CensoredFraction = float64(st.Unknown) / float64(st.Matured)
	}
	meteredAccepted, meteredCount := 0.0, 0
	for _, r := range rs {
		if r.HasOutcome && r.FullyMetered {
			meteredCount++
			if r.Accepted {
				meteredAccepted++
			}
		}
	}
	if meteredCount > 0 {
		st.MeteredShare = float64(meteredCount) / float64(st.Matured)
	}
	if meteredAccepted > 0 {
		st.Primary = st.MeteredCost / meteredAccepted
	} else {
		st.Primary = math.NaN()
	}
	if settled > 0 {
		st.AcceptRate = float64(st.Accepted) / float64(settled)
	}
	return st
}

// primaryOf computes the primary metric over a job set: metered ACCEPTED +
// REJECTED spend over metered verified successes.
func primaryOf(rs []JobRecord) float64 {
	cost, acc := 0.0, 0.0
	for _, r := range rs {
		if !r.HasOutcome || !r.FullyMetered {
			continue
		}
		if r.Status == outcome.StatusAccepted || r.Status == outcome.StatusRejected {
			cost += r.CostMetered
		}
		if r.Accepted {
			acc++
		}
	}
	if acc == 0 {
		return math.NaN()
	}
	return cost / acc
}

func pairDiff(candidate, baseline []JobRecord) (diff, rel float64) {
	pc, pb := primaryOf(candidate), primaryOf(baseline)
	if math.IsNaN(pc) || math.IsNaN(pb) || pb == 0 {
		return math.NaN(), math.NaN()
	}
	return pc - pb, (pb - pc) / pb
}

// bootstrapDiff resamples jobs with replacement (fixed seed) and returns the
// 2.5/97.5 percentiles of the candidate-minus-baseline difference.
func bootstrapDiff(candidate, baseline []JobRecord, n int, seed uint64) (float64, float64) {
	if n <= 0 || len(candidate) == 0 || len(baseline) == 0 {
		return math.NaN(), math.NaN()
	}
	rng := rand.New(rand.NewPCG(seed, seed>>1))
	diffs := make([]float64, 0, n)
	for b := 0; b < n; b++ {
		rc := make([]JobRecord, len(candidate))
		for i := range rc {
			rc[i] = candidate[rng.IntN(len(candidate))]
		}
		rb := make([]JobRecord, len(baseline))
		for i := range rb {
			rb[i] = baseline[rng.IntN(len(baseline))]
		}
		d, _ := pairDiff(rc, rb)
		if !math.IsNaN(d) {
			diffs = append(diffs, d)
		}
	}
	if len(diffs) == 0 {
		return math.NaN(), math.NaN()
	}
	sort.Float64s(diffs)
	return percentile(diffs, 2.5), percentile(diffs, 97.5)
}

// bootstrapRel resamples jobs and returns the 2.5/97.5 percentiles of the
// relative improvement (pb-pc)/pb. Resamples with no baseline success carry
// no information and are skipped.
func bootstrapRel(candidate, baseline []JobRecord, n int, seed uint64) (float64, float64) {
	if n <= 0 || len(candidate) == 0 || len(baseline) == 0 {
		return math.NaN(), math.NaN()
	}
	rng := rand.New(rand.NewPCG(seed, seed>>1))
	rels := make([]float64, 0, n)
	for b := 0; b < n; b++ {
		rc := make([]JobRecord, len(candidate))
		for i := range rc {
			rc[i] = candidate[rng.IntN(len(candidate))]
		}
		rb := make([]JobRecord, len(baseline))
		for i := range rb {
			rb[i] = baseline[rng.IntN(len(baseline))]
		}
		_, rel := pairDiff(rc, rb)
		if !math.IsNaN(rel) {
			rels = append(rels, rel)
		}
	}
	if len(rels) == 0 {
		return math.NaN(), math.NaN()
	}
	sort.Float64s(rels)
	return percentile(rels, 2.5), percentile(rels, 97.5)
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	rank := p / 100 * float64(len(sorted)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return sorted[lo]
	}
	return sorted[lo] + (sorted[hi]-sorted[lo])*(rank-float64(lo))
}

// assessSampleSize sizes each pair from observed pooled variance. The
// source label travels with the numbers so synthetic dry-run sizing can
// never be mistaken for customer-data sizing.
func assessSampleSize(byTx map[string][]JobRecord, cfg ReportConfig, pairs [][2]string) SampleSizeAssessment {
	out := SampleSizeAssessment{Source: "synthetic-observed", Entries: map[string]float64{}}
	for _, p := range pairs {
		var costs []float64
		for _, r := range append(append([]JobRecord(nil), byTx[p[0]]...), byTx[p[1]]...) {
			if r.Matured && r.HasOutcome && r.FullyMetered {
				costs = append(costs, r.CostMetered)
			}
		}
		n := float64(len(costs))
		if n < 2 {
			out.Entries[p[0]+"-"+p[1]] = 0
			continue
		}
		mean := 0.0
		for _, c := range costs {
			mean += c
		}
		mean /= n
		v := 0.0
		for _, c := range costs {
			v += (c - mean) * (c - mean)
		}
		sd := math.Sqrt(v / (n - 1))
		base := primaryOf(byTx[p[1]])
		if math.IsNaN(base) || base <= 0 {
			out.Entries[p[0]+"-"+p[1]] = 0
			continue
		}
		out.Entries[p[0]+"-"+p[1]] = RequiredPerGroup(sd, base, cfg.MinEffect, 0.05, 0.8)
	}
	return out
}

func sensitivities(byTx map[string][]JobRecord, cfg ReportConfig, baseline, candidate string) Sensitivity {
	var s Sensitivity
	cs, bs := byTx[candidate], byTx[baseline]
	// 1. Worst-case censoring: leader's unresolved+unknown become REJECTED at
	// leader p90 job cost.
	p90 := jobCostP90(cs)
	worst := worstCaseCopy(cs, p90)
	d, _ := pairDiff(worst, bs)
	_, hi := bootstrapDiff(worst, bs, cfg.BootstrapN, cfg.BootstrapSeed)
	s.WorstCaseCensoringWins = !math.IsNaN(hi) && hi < 0 && d < 0
	// 2. Missing-cost bounds over all matured jobs (zero vs p90 fill).
	s.MissingCostLow, s.MissingCostHigh = missingCostBounds(cs, bs)
	// 3. Maturity stability is filled in by the caller (MaturityStability);
	// 4. Correction asymmetry tally.
	s.CorrectionAsymmetry = correctionTally(byTx)
	return s
}

func jobCosts(rs []JobRecord) []float64 {
	var out []float64
	for _, r := range rs {
		if r.HasOutcome {
			out = append(out, r.CostMetered)
		}
	}
	sort.Float64s(out)
	return out
}

func jobCostP90(rs []JobRecord) float64 {
	c := jobCosts(rs)
	if len(c) == 0 {
		return 0
	}
	return percentile(c, 90)
}

func worstCaseCopy(cs []JobRecord, p90 float64) []JobRecord {
	out := make([]JobRecord, len(cs))
	copy(out, cs)
	for i := range out {
		if !out[i].HasOutcome || out[i].Status == outcome.StatusUnknown || out[i].Status == outcome.StatusPending {
			out[i].HasOutcome = true
			out[i].Status = outcome.StatusRejected
			out[i].Accepted = false
			out[i].CostMetered = p90
			out[i].FullyMetered = true
		}
	}
	return out
}

func missingCostBounds(cs, bs []JobRecord) (float64, float64) {
	fill := func(rs []JobRecord, v float64) []JobRecord {
		out := make([]JobRecord, len(rs))
		copy(out, rs)
		for i := range out {
			if out[i].HasOutcome && !out[i].FullyMetered {
				out[i].CostMetered = v
				out[i].FullyMetered = true
			}
		}
		return out
	}
	loD, _ := pairDiff(fill(cs, 0), fill(bs, 0))
	hiD, _ := pairDiff(fill(cs, jobCostP90(cs)), fill(bs, jobCostP90(bs)))
	return loD, hiD
}

func correctionTally(byTx map[string][]JobRecord) string {
	names := make([]string, 0, len(byTx))
	counts := map[string]int{}
	totals := map[string]int{}
	for n, rs := range byTx {
		names = append(names, n)
		for _, r := range rs {
			totals[n]++
			if r.CorrectedAfter {
				counts[n]++
			}
		}
	}
	sort.Strings(names)
	out := ""
	for i, n := range names {
		if i > 0 {
			out += " "
		}
		out += fmt.Sprintf("%s:%d/%d", n, counts[n], totals[n])
	}
	return out
}

// MaturityStability recomputes the winner at half/double maturation windows.
// A window is stable when its winner equals the base winner.
func MaturityStability(baseWinner string, assignments []Assignment, events []outcome.OutcomeEvent, now time.Time, window time.Duration, treatments []string, jobMaps map[string]JobMap) (halfStable, doubleStable bool) {
	winner := func(w time.Duration) string {
		byTx := map[string][]JobRecord{}
		byTxAssign := map[string][]Assignment{}
		for _, a := range assignments {
			byTxAssign[a.Treatment] = append(byTxAssign[a.Treatment], a)
		}
		byTxEvents := map[string][]outcome.OutcomeEvent{}
		for _, ev := range events {
			byTxEvents[ev.StrategyID] = append(byTxEvents[ev.StrategyID], ev)
		}
		for _, n := range treatments {
			for _, r := range MatureJobsMapped(byTxAssign[n], byTxEvents[n], now, w, jobMaps[n]) {
				if r.Matured {
					byTx[r.Treatment] = append(byTx[r.Treatment], r)
				}
			}
		}
		best, bestP := "", math.Inf(1)
		for _, n := range treatments {
			if p := primaryOf(byTx[n]); !math.IsNaN(p) && p < bestP {
				best, bestP = n, p
			}
		}
		return best
	}
	return winner(window/2) == baseWinner, winner(window*2) == baseWinner
}

// verdict applies gates, floor, bar, and sensitivities to a decision.
func verdict(rep Report, cfg ReportConfig, baseline, candidate string) (string, []string) {
	var reasons []string
	fail := false
	for _, g := range rep.Gates {
		if !g.Pass {
			fail = true
			reasons = append(reasons, "gate "+g.Name+": "+g.Detail)
		}
	}
	if fail {
		return "NOT_RANKABLE", reasons
	}
	cand := rep.Treatments[candidate]
	if cand.AcceptRate < cfg.QualityFloor {
		return "NOT_RANKABLE", append(reasons,
			fmt.Sprintf("winner accept rate %.3f below floor %.3f", cand.AcceptRate, cfg.QualityFloor))
	}
	allWin, allBar := true, true
	for _, c := range rep.Comparisons {
		if !c.Wins {
			allWin = false
		}
		if !c.MeetsBar {
			allBar = false
		}
	}
	if !allWin {
		return "INCONCLUSIVE", append(reasons, "comparison CI overlaps zero or is NaN")
	}
	if !rep.Sensitivity.WorstCaseCensoringWins {
		return "INCONCLUSIVE", append(reasons, "win does not survive worst-case censoring")
	}
	if !rep.Sensitivity.HalfMaturityStable || !rep.Sensitivity.DoubleMaturityStable {
		return "INCONCLUSIVE", append(reasons, "ranking maturity-sensitive")
	}
	if !allBar {
		return "INCONCLUSIVE", append(reasons, "win below commercial bar")
	}
	return "CONCLUSIVE_T2_WINS", append(reasons, "all gates, floor, bar and sensitivities hold")
}

// NotReadyError signals not-ready data (immature window, empty input).
// Callers map it to a distinct exit status.
type NotReadyError struct{ Reason string }

func (e *NotReadyError) Error() string { return "analysis refused: " + e.Reason }

// BuildReport enforces readiness (every assigned job matured under the
// common window) and analyzes. jobMaps carries each treatment's
// manifest→gateway join (nil map per treatment means identity). It is shared
// by the exp-report CLI and the exp-run dry-run writer so the two can never
// disagree.
func BuildReport(allAssign []Assignment, allEvents []outcome.OutcomeEvent, txNames []string, baseline, candidate string, others []string, cfg ReportConfig, jobMaps map[string]JobMap) (Report, error) {
	if len(allAssign) == 0 {
		return Report{}, &NotReadyError{"no assigned jobs"}
	}

	// Do not finalize until the final assigned job has matured.
	latest := allAssign[0].AssignedAt
	for _, a := range allAssign[1:] {
		if a.AssignedAt > latest {
			latest = a.AssignedAt
		}
	}
	lastAssigned, err := time.Parse(time.RFC3339Nano, latest)
	if err != nil {
		return Report{}, fmt.Errorf("harness: bad assigned_at: %w", err)
	}
	if cfg.Now.Before(lastAssigned.Add(cfg.Maturation)) {
		return Report{}, &NotReadyError{fmt.Sprintf("final job assigned %s matures at %s (now %s)",
			lastAssigned.Format(time.RFC3339), lastAssigned.Add(cfg.Maturation).Format(time.RFC3339),
			cfg.Now.Format(time.RFC3339))}
	}

	records := MatureJobs(allAssign, allEvents, cfg.Now, cfg.Maturation)
	_ = records
	// Per-treatment maturity with joins (assignments and events are flat
	// across treatments; split them first).
	byTxAssign := map[string][]Assignment{}
	for _, a := range allAssign {
		byTxAssign[a.Treatment] = append(byTxAssign[a.Treatment], a)
	}
	byTxEvents := map[string][]outcome.OutcomeEvent{}
	for _, ev := range allEvents {
		byTxEvents[ev.StrategyID] = append(byTxEvents[ev.StrategyID], ev)
	}
	records = nil
	for _, n := range txNames {
		records = append(records, MatureJobsMapped(byTxAssign[n], byTxEvents[n], cfg.Now, cfg.Maturation, jobMaps[n])...)
	}
	byTx := map[string][]JobRecord{}
	for _, r := range records {
		if r.Matured {
			byTx[r.Treatment] = append(byTx[r.Treatment], r)
		}
	}
	baseWinner, bestP := "", math.Inf(1)
	for _, n := range txNames {
		if p := primaryOf(byTx[n]); !math.IsNaN(p) && p < bestP {
			baseWinner, bestP = n, p
		}
	}
	half, dbl := MaturityStability(baseWinner, allAssign, allEvents, cfg.Now, cfg.Maturation, txNames, jobMaps)
	return Analyze(records, cfg, baseline, candidate, others, half, dbl), nil
}
