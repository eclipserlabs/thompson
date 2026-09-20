package gateway

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"

	"github.com/wiramahendra/thompson-sampling/go/propensity"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// OPEStatus marks whether a decision is evaluable.
type OPEStatus string

const (
	OPEEligible   OPEStatus = "OPE_ELIGIBLE"
	OPEIneligible OPEStatus = "OPE_INELIGIBLE"
)

// BanditLogRecord is the conceptual OPE row for one primary decision.
type BanditLogRecord struct {
	DecisionID              string
	EligibleArmIDs          []string
	EligibleArmState        []EligibleArmState // alpha,beta,pulls per arm at decision time
	LoggingPolicyID         string
	LoggingPolicyConfigHash string
	SelectedArmID           string
	ObservedReward          float64
	LoggingPropensity       *float64 // nil if not reconstructible
	Status                  OPEStatus
	Reason                  string // if ineligible

	// PropensityStatus is the reliability verdict on LoggingPropensity. An empty
	// value means the record was built before the reliability gate existed and is
	// treated as unclassified: it is neither vouched for nor refused here.
	PropensityStatus propensity.Status
	// PropensitySource names the estimator that produced LoggingPropensity.
	PropensitySource string
	// Propensity is the full per-action audit record: reference value, Monte-Carlo
	// win count, confidence interval, integration error, and the reason for the
	// status. Kept on the row so a refusal can always be explained.
	Propensity propensity.Estimate
}

// ToBanditLog converts a LedgerDecision into a BanditLogRecord using
// Monte-Carlo propensity reconstruction with the given draw count and seed.
//
// It is the compatibility entry point. Prefer [ToBanditLogWith], which lets the
// caller choose the estimator and therefore lets an evaluation avoid using the
// same estimator on both sides of an importance weight.
func ToBanditLog(d *LedgerDecision, draws int, seed uint64) BanditLogRecord {
	return ToBanditLogWith(d, NewMCEstimator(draws, seed))
}

// ToBanditLogWith converts a LedgerDecision into a BanditLogRecord, using est to
// reconstruct the logging propensity from the persisted eligible_arm_state.
//
// A row is OPE_INELIGIBLE when the snapshot is missing or inconsistent, or when
// the logging policy is not the exact-Thompson argmax that both estimators
// model. A row that is OPE_ELIGIBLE still carries a PropensityStatus: eligible
// means "we know what was logged", not "the denominator is trustworthy".
func ToBanditLogWith(d *LedgerDecision, est PropensityEstimator) BanditLogRecord {
	rec := BanditLogRecord{
		DecisionID:              d.Started.DecisionID,
		EligibleArmIDs:          d.Started.EligibleArmIDs,
		EligibleArmState:        d.Started.EligibleArmState,
		LoggingPolicyID:         d.Started.LoggingPolicyID,
		LoggingPolicyConfigHash: d.Started.LoggingPolicyConfigHash,
		SelectedArmID:           d.Started.SelectedArmID,
	}
	if d.Learned != nil {
		rec.ObservedReward = d.Learned.ComputedReward
	}
	// No DecisionLearned means no reward was ever computed for this decision.
	// ObservedReward stays 0 rather than being imputed from the execution record;
	// callers filter on d.Learned before building rows.
	// Check OPE eligibility
	if len(rec.EligibleArmIDs) == 0 || len(rec.EligibleArmState) == 0 {
		rec.Status = OPEIneligible
		rec.Reason = "missing eligible_arm_state"
		return rec
	}
	if len(rec.EligibleArmState) != len(rec.EligibleArmIDs) {
		rec.Status = OPEIneligible
		rec.Reason = "eligible_arm_state length mismatch"
		return rec
	}
	// Verify all eligible IDs have state
	stateMap := make(map[string]EligibleArmState)
	for _, s := range rec.EligibleArmState {
		stateMap[s.ArmID] = s
	}
	for _, id := range rec.EligibleArmIDs {
		if _, ok := stateMap[id]; !ok {
			rec.Status = OPEIneligible
			rec.Reason = fmt.Sprintf("missing state for eligible arm %s", id)
			return rec
		}
	}
	// The logging policy must be the one both estimators model. A UCB or phased
	// selection, or an approximate sampler, has a different action distribution
	// and neither the integral nor the Monte-Carlo simulation describes it.
	if rec.LoggingPolicyID != "" {
		if err := propensity.CheckLoggingPolicy(rec.LoggingPolicyID); err != nil {
			rec.Status = OPEIneligible
			rec.Reason = err.Error()
			return rec
		}
	}

	// Reconstruct from EligibleArmState, the posterior-*before* snapshot. Using
	// the posterior after the update would condition the denominator on the
	// outcome being weighted.
	recon := est.Reconstruct(rec.EligibleArmState)
	e, ok := recon.Get(rec.SelectedArmID)
	if !ok {
		rec.Status = OPEIneligible
		rec.Reason = "no propensity estimate for selected arm"
		return rec
	}
	rec.Propensity = e
	rec.PropensityStatus = e.Status
	rec.PropensitySource = est.ID()
	rec.Status = OPEEligible

	// Only a RELIABLE estimate becomes a denominator. In particular a zero
	// Monte-Carlo win count leaves LoggingPropensity nil: it is an unresolved
	// positive probability, and neither the estimate nor an invented floor may
	// stand in for it.
	if e.Status.Usable() && e.Value > 0 && !math.IsNaN(e.Value) && !math.IsInf(e.Value, 0) {
		v := e.Value
		rec.LoggingPropensity = &v
	} else {
		rec.Reason = e.Reason
	}
	return rec
}

// Usable reports whether this row may contribute an ordinary IPS/SNIPS term.
//
// An unclassified row (empty PropensityStatus, i.e. built before the gate
// existed) is admitted so that pre-existing fixtures keep their meaning; every
// classified row must be RELIABLE.
func (r BanditLogRecord) Usable() bool {
	if r.Status != OPEEligible {
		return false
	}
	if r.PropensityStatus != "" && !r.PropensityStatus.Usable() {
		return false
	}
	return true
}

// CandidatePolicy defines action probabilities for a candidate.
type CandidatePolicy interface {
	ID() string
	// ActionProbability returns pi_candidate(a | eligible, state). Must be deterministic and sum to 1 across eligible if needed.
	ActionProbability(eligible []string, eligibleState []EligibleArmState, armID string) float64
}

// ExactThompsonCandidate uses Monte-Carlo reconstructed Thompson probabilities.
//
// IMPLEMENTATION_SANITY_ONLY when paired with Monte-Carlo logging propensities
// of the same draw count and seed: the numerator and denominator are then the
// same vector and every weight is exactly 1, so the resulting IPS reproduces
// the empirical mean whatever the estimator's bias. That configuration checks
// the plumbing, not the propensities. For a real self-calibration check use
// [ThompsonReferenceCandidate] against Monte-Carlo logging propensities (or the
// reverse); see opeval.Calibrate.
type ExactThompsonCandidate struct {
	Draws int
	Seed  uint64
}

func (c ExactThompsonCandidate) ID() string { return "exact-thompson-v1" }
func (c ExactThompsonCandidate) ActionProbability(eligible []string, eligibleState []EligibleArmState, armID string) float64 {
	posteriors := make(map[string]thompson.Posterior)
	for _, s := range eligibleState {
		posteriors[s.ArmID] = thompson.Posterior{Alpha: s.Alpha, Beta: s.Beta, Pulls: s.Pulls}
	}
	m := EstimateThompsonPropensities(posteriors, c.Draws, c.Seed)
	return m[armID]
}

// UniformCandidate 1/K
type UniformCandidate struct{}

func (UniformCandidate) ID() string { return "uniform-v1" }
func (UniformCandidate) ActionProbability(eligible []string, _ []EligibleArmState, armID string) float64 {
	if len(eligible) == 0 {
		return 0
	}
	return 1.0 / float64(len(eligible))
}

// GreedyCandidate picks argmax posterior mean, tie-break lexicographically smallest.
// This matches runtime's deterministic ordering.
type GreedyCandidate struct{}

func (GreedyCandidate) ID() string { return "greedy-posterior-mean-v1" }
func (GreedyCandidate) ActionProbability(eligible []string, eligibleState []EligibleArmState, armID string) float64 {
	if len(eligibleState) == 0 {
		return 0
	}
	bestID := ""
	bestMean := -1.0
	for _, s := range eligibleState {
		mean := s.Alpha / (s.Alpha + s.Beta)
		if bestID == "" || mean > bestMean || (mean == bestMean && s.ArmID < bestID) {
			bestID = s.ArmID
			bestMean = mean
		}
	}
	if armID == bestID {
		return 1
	}
	return 0
}

// OPEEstimate holds IPS/SNIPS results for one candidate.
type OPEEstimate struct {
	CandidateID         string
	TotalDecisions      int
	EligibleDecisions   int
	EmpiricalMean       float64
	IPS                 float64
	SNIPS               float64
	ESS                 float64
	ESSOverN            float64
	MaxWeight           float64
	MeanWeight          float64
	P50Weight           float64
	P95Weight           float64
	P99Weight           float64
	MinPropensity       float64
	UnsupportedFraction float64
	ExcludedFraction    float64

	// PropensityEstimatorID names the estimator that produced the denominators.
	PropensityEstimatorID string
	// UsableDecisions is the number of OPE-eligible rows whose propensity passed
	// the reliability gate. IPS, SNIPS, ESS and the weight quantiles are computed
	// over exactly these rows.
	UsableDecisions int
	// Counts of eligible rows refused as denominators, by reason.
	MCZeroWinsCount       int
	LowPrecisionCount     int
	ReferenceFailureCount int
	InvalidPosteriorCount int
	// UnusablePropensityFraction is the refused share of OPE-eligible rows.
	UnusablePropensityFraction float64
	ClippedIPS                 *float64
	ClippedCount               int
	ClippingThreshold          *float64
	BootstrapSE                *float64
	BootstrapCI95              [2]float64
	Warnings                   []string
}

// EvaluateOPE runs IPS/SNIPS over bandit logs for a candidate.
// draws is Monte Carlo draws for Thompson propensity reconstruction (if candidate is exact-thompson).
// clipThreshold nil means no clipping; if non-nil, weights > threshold are clipped for diagnostic.
// bootstrapSamples 0 means no bootstrap.
func EvaluateOPE(records []BanditLogRecord, candidate CandidatePolicy, clipThreshold *float64, bootstrapSamples int, bootstrapSeed uint64, draws int, evalSeed uint64) OPEEstimate {
	// For uniform/greedy, draws not used; for exact-thompson, we need draws/seed
	// But BanditLogRecord already has logging propensity estimated with draws/seed? We recompute candidate probs per record with same draws.
	est := OPEEstimate{CandidateID: candidate.ID(), TotalDecisions: len(records)}
	var eligibleAll, eligible []BanditLogRecord
	_, _ = draws, evalSeed // candidates and records carry their own estimator config
	for _, r := range records {
		if r.Status != OPEEligible {
			continue
		}
		eligibleAll = append(eligibleAll, r)
		if est.PropensityEstimatorID == "" {
			est.PropensityEstimatorID = r.PropensitySource
		}
		// A row whose denominator failed the reliability gate is dropped from the
		// estimator rather than entered with weight zero. Entering it as zero
		// would quietly pull SNIPS toward the arms whose propensity happened to
		// be resolvable, and would inflate ESS by adding a term that carries no
		// information.
		switch r.PropensityStatus {
		case propensity.MCZeroWins:
			est.MCZeroWinsCount++
		case propensity.LowPrecision:
			est.LowPrecisionCount++
		case propensity.NumericalReferenceFailure:
			est.ReferenceFailureCount++
		case propensity.InvalidPosterior:
			est.InvalidPosteriorCount++
		}
		if r.Usable() {
			eligible = append(eligible, r)
		}
	}
	est.EligibleDecisions = len(eligibleAll)
	est.UsableDecisions = len(eligible)
	if len(eligibleAll) > 0 {
		est.UnusablePropensityFraction = float64(len(eligibleAll)-len(eligible)) / float64(len(eligibleAll))
	}
	if est.MCZeroWinsCount > 0 {
		est.Warnings = append(est.Warnings, fmt.Sprintf(
			"%d row(s) refused: MC_ZERO_WINS. Zero Monte-Carlo wins is an unresolved positive probability, not zero support; raising draws or switching to the numerical reference is the fix, not a propensity floor",
			est.MCZeroWinsCount))
	}
	if est.LowPrecisionCount > 0 {
		est.Warnings = append(est.Warnings, fmt.Sprintf(
			"%d row(s) refused: LOW_PRECISION denominator", est.LowPrecisionCount))
	}
	if est.ReferenceFailureCount > 0 {
		est.Warnings = append(est.Warnings, fmt.Sprintf(
			"%d row(s) refused: NUMERICAL_REFERENCE_FAILURE", est.ReferenceFailureCount))
	}
	if est.InvalidPosteriorCount > 0 {
		est.Warnings = append(est.Warnings, fmt.Sprintf(
			"%d row(s) refused: INVALID_POSTERIOR", est.InvalidPosteriorCount))
	}
	if len(eligible) == 0 {
		est.Warnings = append(est.Warnings, "no OPE eligible decisions with a usable propensity denominator")
		return est
	}
	est.ExcludedFraction = float64(len(records)-len(eligibleAll)) / float64(len(records))
	// Empirical mean (logging policy)
	sumEmp := 0.0
	for _, r := range eligible {
		sumEmp += r.ObservedReward
	}
	est.EmpiricalMean = sumEmp / float64(len(eligible))

	// Compute weights and IPS
	weights := make([]float64, len(eligible))
	rewards := make([]float64, len(eligible))
	minProp := math.Inf(1)
	unsupported := 0
	for i, r := range eligible {
		rewards[i] = r.ObservedReward
		// unsupported counts rows admitted by the reliability gate that still
		// carry no positive denominator. Classified rows cannot reach here --
		// ToBanditLogWith leaves LoggingPropensity nil and the gate drops them --
		// so this is the unclassified/legacy path.
		if r.LoggingPropensity == nil || *r.LoggingPropensity <= 0 {
			weights[i] = 0
			unsupported++
			continue
		}
		if *r.LoggingPropensity < minProp {
			minProp = *r.LoggingPropensity
		}
		// candProb == 0 is legitimate: the candidate simply never plays this
		// action, so the row contributes nothing. It is not an error and is not
		// unsupported -- a deterministic candidate like greedy has that for most
		// rows by design.
		candProb := candidate.ActionProbability(r.EligibleArmIDs, r.EligibleArmState, r.SelectedArmID)
		weights[i] = candProb / *r.LoggingPropensity
	}
	est.MinPropensity = minProp
	if math.IsInf(minProp, 1) {
		est.MinPropensity = 0
	}
	est.UnsupportedFraction = float64(unsupported) / float64(len(eligible))

	// Weight diagnostics
	sortedWeights := make([]float64, len(weights))
	copy(sortedWeights, weights)
	sort.Float64s(sortedWeights)
	if len(sortedWeights) > 0 {
		est.MaxWeight = sortedWeights[len(sortedWeights)-1]
		sumW := 0.0
		for _, w := range weights {
			sumW += w
		}
		est.MeanWeight = sumW / float64(len(weights))
		est.P50Weight = percentile(sortedWeights, 50)
		est.P95Weight = percentile(sortedWeights, 95)
		est.P99Weight = percentile(sortedWeights, 99)
		// ESS
		sumWSq := 0.0
		for _, w := range weights {
			sumWSq += w * w
		}
		if sumWSq > 0 {
			est.ESS = sumW * sumW / sumWSq
			est.ESSOverN = est.ESS / float64(len(weights))
		}
		// IPS
		sumWR := 0.0
		for i, w := range weights {
			sumWR += w * rewards[i]
		}
		est.IPS = sumWR / float64(len(eligible))
		// SNIPS
		if sumW > 0 {
			est.SNIPS = sumWR / sumW
		}
		// Clipped diagnostic
		if clipThreshold != nil {
			clippedWeights := make([]float64, len(weights))
			clippedCount := 0
			for i, w := range weights {
				if w > *clipThreshold {
					clippedWeights[i] = *clipThreshold
					clippedCount++
				} else {
					clippedWeights[i] = w
				}
			}
			sumCWR := 0.0
			for i, w := range clippedWeights {
				sumCWR += w * rewards[i]
			}
			v := sumCWR / float64(len(eligible))
			est.ClippedIPS = &v
			est.ClippedCount = clippedCount
			est.ClippingThreshold = clipThreshold
		}
		// Bootstrap SE for IPS (simple nonparametric bootstrap)
		if bootstrapSamples > 0 {
			rng := rand.New(rand.NewPCG(bootstrapSeed, bootstrapSeed>>1))
			bootEstimates := make([]float64, bootstrapSamples)
			for b := 0; b < bootstrapSamples; b++ {
				sumB := 0.0
				for i := 0; i < len(eligible); i++ {
					idx := rng.IntN(len(eligible))
					// Need to recompute weight for sampled index? We already have weights/rewards
					sumB += weights[idx] * rewards[idx]
				}
				bootEstimates[b] = sumB / float64(len(eligible))
			}
			// Stddev
			meanB := 0.0
			for _, v := range bootEstimates {
				meanB += v
			}
			meanB /= float64(bootstrapSamples)
			varVar := 0.0
			for _, v := range bootEstimates {
				varVar += (v - meanB) * (v - meanB)
			}
			varVar /= float64(bootstrapSamples - 1)
			se := math.Sqrt(varVar)
			est.BootstrapSE = &se
			sort.Float64s(bootEstimates)
			est.BootstrapCI95[0] = percentile(bootEstimates, 2.5)
			est.BootstrapCI95[1] = percentile(bootEstimates, 97.5)
		}
		if est.ESSOverN < 0.1 {
			est.Warnings = append(est.Warnings, fmt.Sprintf("low ESS/N=%.3f (<0.1): poor overlap, estimates unreliable", est.ESSOverN))
		}
		if est.MaxWeight > 10 {
			est.Warnings = append(est.Warnings, fmt.Sprintf("large max weight %.2f: high variance, consider clipping diagnostic", est.MaxWeight))
		}
	}
	return est
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// ThompsonReferenceCandidate assigns candidate probabilities from the numerical
// reference rather than from Monte Carlo.
//
// Paired with Monte-Carlo logging propensities it is the honest Thompson
// self-calibration: the weights are pi_reference/pi_hat_MC, which converge to 1
// exactly as the Monte-Carlo reconstruction becomes accurate, and whose spread
// away from 1 measures the reconstruction error directly.
type ThompsonReferenceCandidate struct {
	Options    propensity.ReferenceOptions
	Thresholds propensity.Thresholds

	mu    sync.Mutex
	cache map[string]map[string]float64
}

// NewThompsonReferenceCandidate returns the reference candidate with audit
// defaults and a memo for repeated posterior states.
func NewThompsonReferenceCandidate() *ThompsonReferenceCandidate {
	return &ThompsonReferenceCandidate{
		Options:    propensity.DefaultReferenceOptions(),
		Thresholds: propensity.DefaultThresholds(),
		cache:      make(map[string]map[string]float64),
	}
}

// ID implements CandidatePolicy.
func (c *ThompsonReferenceCandidate) ID() string { return "thompson-numerical-reference-v1" }

// ActionProbability implements CandidatePolicy.
func (c *ThompsonReferenceCandidate) ActionProbability(_ []string, eligibleState []EligibleArmState, armID string) float64 {
	key := stateKey(eligibleState)
	c.mu.Lock()
	if m, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return m[armID]
	}
	c.mu.Unlock()

	opts := c.Options
	if opts.AbsTol == 0 {
		opts = propensity.DefaultReferenceOptions()
	}
	ref, err := propensity.Reference(ArmsFromState(eligibleState), opts)
	m := map[string]float64{}
	if err == nil {
		for k, v := range ref.Probs {
			m[k] = v
		}
	}
	c.mu.Lock()
	if c.cache == nil {
		c.cache = make(map[string]map[string]float64)
	}
	c.cache[key] = m
	c.mu.Unlock()
	return m[armID]
}

// stateKey is a canonical, exact key for a posterior snapshot. Float bits are
// used rather than a formatted decimal so two states that differ below print
// precision never collide in the memo.
func stateKey(state []EligibleArmState) string {
	ids := make([]string, len(state))
	byID := make(map[string]EligibleArmState, len(state))
	for i, s := range state {
		ids[i] = s.ArmID
		byID[s.ArmID] = s
	}
	sort.Strings(ids)
	var b strings.Builder
	for _, id := range ids {
		s := byID[id]
		fmt.Fprintf(&b, "%s:%x:%x;", id, math.Float64bits(s.Alpha), math.Float64bits(s.Beta))
	}
	return b.String()
}
