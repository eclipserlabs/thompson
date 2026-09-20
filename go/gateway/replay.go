package gateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/wiramahendra/thompson-sampling/go/propensity"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// LedgerDecision groups all evidence for one canonical decision_id.
// It is the replay footbridge: offline analysis can compare primary vs shadow
// without reconstructing request contents, and without imputing missing rewards.
type LedgerDecision struct {
	Started  *DecisionStarted
	Primary  *ExecutionObserved
	Learned  *DecisionLearned
	Shadow   *ShadowExecutionObserved
	Skipped  *ShadowSkipped
	Eligible []string
}

// LedgerFromFile reads a V0 JSONL evidence file and groups events by decision_id.
func LedgerFromFile(path string) ([]*LedgerDecision, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)
	byID := make(map[string]*LedgerDecision)
	var order []*LedgerDecision

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var base struct {
			EventType  string `json:"event_type"`
			DecisionID string `json:"decision_id"`
		}
		if err := json.Unmarshal(line, &base); err != nil {
			return nil, fmt.Errorf("unmarshal base: %w line=%s", err, string(line))
		}
		d, ok := byID[base.DecisionID]
		if !ok {
			d = &LedgerDecision{}
			byID[base.DecisionID] = d
			order = append(order, d)
		}
		switch base.EventType {
		case "DecisionStarted":
			var e DecisionStarted
			if err := json.Unmarshal(line, &e); err != nil {
				return nil, err
			}
			ec := e
			d.Started = &ec
			d.Eligible = e.EligibleArmIDs
		case "ExecutionObserved":
			var e ExecutionObserved
			if err := json.Unmarshal(line, &e); err != nil {
				return nil, err
			}
			ec := e
			d.Primary = &ec
		case "DecisionLearned":
			var e DecisionLearned
			if err := json.Unmarshal(line, &e); err != nil {
				return nil, err
			}
			ec := e
			d.Learned = &ec
		case "ShadowExecutionObserved":
			var e ShadowExecutionObserved
			if err := json.Unmarshal(line, &e); err != nil {
				return nil, err
			}
			ec := e
			d.Shadow = &ec
		case "ShadowSkipped":
			var e ShadowSkipped
			if err := json.Unmarshal(line, &e); err != nil {
				return nil, err
			}
			ec := e
			d.Skipped = &ec
		default:
			// ignore unknown for forward compat
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return order, nil
}

// PairedStats is the offline analysis summary over shadowed decisions.
// Explicitly labeled as paired observed comparison, not policy regret.
type PairedStats struct {
	PairedCount       int
	MeanPrimaryReward float64
	MeanShadowReward  float64
	MeanDelta         float64 // shadow - primary, paired observed
	Win               int
	Tie               int
	Loss              int
	SuccessDiff       float64 // shadow success rate - primary
	MeanLatencyDelta  float64
	MeanCostDelta     *float64 // nil if insufficient cost data
	CostPairs         int
	// V1 extended
	PrimaryCounts        map[string]int            // arm -> times chosen as primary
	ShadowCounts         map[string]int            // arm -> times observed as shadow
	Coverage             map[string]map[string]int // primary -> shadow -> count
	MissingCostPrimary   int
	MissingCostShadow    int
	MissingTokensPrimary int
	MissingTokensShadow  int
	SupportWarnings      []string
}

// AnalyzePaired computes PairedStats over decisions that have both primary and shadow.
// epsilon defines tie threshold for reward comparison.
// It is explicitly paired observed comparison, NOT policy regret, and reports coverage/missingness/support.
func AnalyzePaired(decisions []*LedgerDecision, epsilon float64) PairedStats {
	var s PairedStats
	s.PrimaryCounts = make(map[string]int)
	s.ShadowCounts = make(map[string]int)
	s.Coverage = make(map[string]map[string]int)
	var sumPrimary, sumShadow, sumLatencyDelta, sumCostDelta float64
	var sumPrimarySuccess, sumShadowSuccess int
	costPairs := 0

	// First pass for paired stats and coverage/missingness over all decisions
	allPrimaryCounts := make(map[string]int)
	allEligibleCounts := make(map[string]int)
	missingCostP, missingCostS, missingTokP, missingTokS := 0, 0, 0, 0
	for _, d := range decisions {
		if d.Primary != nil {
			allPrimaryCounts[d.Primary.ArmID]++
			if d.Primary.CostUSD == nil {
				missingCostP++
			}
			if d.Primary.InputTokens == nil || d.Primary.OutputTokens == nil {
				missingTokP++
			}
		}
		if d.Shadow != nil {
			allPrimaryCounts[d.Shadow.PrimaryArmID]++ // alternative view
		}
		for _, arm := range d.Eligible {
			allEligibleCounts[arm]++
		}
	}
	for _, d := range decisions {
		if d.Primary == nil || d.Shadow == nil || d.Learned == nil {
			continue
		}
		pr := d.Learned.ComputedReward
		sr := d.Shadow.ComputedReward
		s.PairedCount++
		sumPrimary += pr
		sumShadow += sr
		sumLatencyDelta += d.Shadow.LatencyMs - d.Primary.LatencyMs
		if d.Shadow.Success {
			sumShadowSuccess++
		}
		if d.Primary.Success {
			sumPrimarySuccess++
		}
		delta := sr - pr
		if delta > epsilon {
			s.Win++
		} else if delta < -epsilon {
			s.Loss++
		} else {
			s.Tie++
		}
		if d.Primary.CostUSD != nil && d.Shadow.CostUSD != nil {
			sumCostDelta += *d.Shadow.CostUSD - *d.Primary.CostUSD
			costPairs++
		}
		// Coverage
		p := d.Primary.ArmID
		sh := d.Shadow.ArmID
		s.PrimaryCounts[p]++
		s.ShadowCounts[sh]++
		if s.Coverage[p] == nil {
			s.Coverage[p] = make(map[string]int)
		}
		s.Coverage[p][sh]++
		if d.Primary.CostUSD == nil {
			missingCostP++
		}
		if d.Shadow.CostUSD == nil {
			missingCostS++
		}
		if d.Primary.InputTokens == nil {
			missingTokP++
		}
		if d.Shadow.InputTokens == nil {
			missingTokS++
		}
	}
	s.MissingCostPrimary = missingCostP
	s.MissingCostShadow = missingCostS
	s.MissingTokensPrimary = missingTokP
	s.MissingTokensShadow = missingTokS
	if s.PairedCount > 0 {
		s.MeanPrimaryReward = sumPrimary / float64(s.PairedCount)
		s.MeanShadowReward = sumShadow / float64(s.PairedCount)
		s.MeanDelta = sumShadow/float64(s.PairedCount) - sumPrimary/float64(s.PairedCount)
		s.SuccessDiff = float64(sumShadowSuccess)/float64(s.PairedCount) - float64(sumPrimarySuccess)/float64(s.PairedCount)
		s.MeanLatencyDelta = sumLatencyDelta / float64(s.PairedCount)
	}
	if costPairs > 0 {
		v := sumCostDelta / float64(costPairs)
		s.MeanCostDelta = &v
		s.CostPairs = costPairs
	}
	// Support/overlap warnings
	if s.PairedCount < 30 {
		s.SupportWarnings = append(s.SupportWarnings, fmt.Sprintf("low paired n=%d (<30): do not claim significance", s.PairedCount))
	}
	for arm, cnt := range allPrimaryCounts {
		if cnt == 0 {
			s.SupportWarnings = append(s.SupportWarnings, fmt.Sprintf("arm %s never primary (no support)", arm))
		}
	}
	for arm, cnt := range allEligibleCounts {
		if allPrimaryCounts[arm] == 0 {
			s.SupportWarnings = append(s.SupportWarnings, fmt.Sprintf("arm %s eligible %d times but never selected (support gap)", arm, cnt))
		}
	}
	return s
}

// EstimateThompsonPropensities uses offline Monte Carlo over the persisted Beta
// posteriors to estimate P(arm wins Thompson draw). This is NOT done on the hot
// path.
//
// It delegates to propensity.MonteCarlo so the estimator under audit and the
// estimator in production are literally the same code. draws controls accuracy;
// see propensity.Thresholds for why a draw count on its own is not an accuracy
// claim for rare actions.
func EstimateThompsonPropensities(posteriors map[string]thompson.Posterior, draws int, seed uint64) map[string]float64 {
	return propensity.MonteCarlo(armsFromPosteriors(posteriors), draws, seed).Probs
}

func armsFromPosteriors(posteriors map[string]thompson.Posterior) []propensity.Arm {
	ids := make([]string, 0, len(posteriors))
	for id := range posteriors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	arms := make([]propensity.Arm, 0, len(ids))
	for _, id := range ids {
		p := posteriors[id]
		arms = append(arms, propensity.Arm{ID: id, Alpha: p.Alpha, Beta: p.Beta})
	}
	return arms
}
