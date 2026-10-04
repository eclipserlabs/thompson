package gateway

import (
	"math"
	"math/rand/v2"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

func TestEligibleArmStatePersisted(t *testing.T) {
	policy := thompson.NewDefault("a", "b", "c")
	mem := &MemoryEvidenceWriter{}
	reg := NewProviderRegistry()
	for _, id := range []string{"a", "b", "c"} {
		reg.Register(NewFakeProvider(id))
	}
	rt, _ := NewRouter(RouterConfig{
		Policy: policy, Registry: reg, Writer: mem,
		RNGFactory:        func() *rand.Rand { return rand.New(rand.NewPCG(1, 1)) },
		ShadowEligibility: HeaderShadowEligibility{}, ShadowSampleRate: 0,
	})
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	if len(mem.Started) != 1 {
		t.Fatal("no started")
	}
	if len(mem.Started[0].EligibleArmState) != 3 {
		t.Fatalf("expected 3 arm states, got %d", len(mem.Started[0].EligibleArmState))
	}
	if mem.Started[0].EligibleArmState[0].ArmID != "a" {
		t.Fatalf("order not deterministic")
	}
}

func TestHistoricalWithoutStateIsOPEIneligible(t *testing.T) {
	d := &LedgerDecision{
		Started: &DecisionStarted{
			DecisionID: "old", EligibleArmIDs: []string{"a", "b"}, SelectedArmID: "a",
			EligibleArmState: nil,
			LoggingPolicyID:  "exact-thompson-v1", LoggingPolicyConfigHash: "h",
		},
		Primary: &ExecutionObserved{DecisionID: "old", ArmID: "a"},
		Learned: &DecisionLearned{DecisionID: "old", ArmID: "a", ComputedReward: 0.5},
	}
	rec := ToBanditLog(d, 10000, 42)
	if rec.Status != OPEIneligible {
		t.Fatalf("expected ineligible, got %v", rec.Status)
	}
}

func TestThompsonPropensityIdenticalTwoArms(t *testing.T) {
	posteriors := map[string]thompson.Posterior{
		"a": {Alpha: 1, Beta: 1},
		"b": {Alpha: 1, Beta: 1},
	}
	m := EstimateThompsonPropensities(posteriors, 50000, 42)
	if math.Abs(m["a"]-0.5) > 0.02 || math.Abs(m["b"]-0.5) > 0.02 {
		t.Fatalf("two identical Beta(1,1) should be 0.5 each, got %v", m)
	}
	if math.Abs(m["a"]+m["b"]-1) > 0.001 {
		t.Fatalf("sum not 1: %v", m)
	}
}

func TestThompsonPropensityIdenticalFourArms(t *testing.T) {
	posteriors := map[string]thompson.Posterior{
		"a": {Alpha: 1, Beta: 1}, "b": {Alpha: 1, Beta: 1}, "c": {Alpha: 1, Beta: 1}, "d": {Alpha: 1, Beta: 1},
	}
	m := EstimateThompsonPropensities(posteriors, 50000, 42)
	for _, id := range []string{"a", "b", "c", "d"} {
		if math.Abs(m[id]-0.25) > 0.02 {
			t.Fatalf("four identical should be 0.25, got %v", m)
		}
	}
}

func TestPropensitiesSumToOne(t *testing.T) {
	posteriors := map[string]thompson.Posterior{
		"a": {Alpha: 5, Beta: 1}, "b": {Alpha: 2, Beta: 2}, "c": {Alpha: 1, Beta: 5},
	}
	m := EstimateThompsonPropensities(posteriors, 10000, 123)
	sum := m["a"] + m["b"] + m["c"]
	if math.Abs(sum-1) > 0.001 {
		t.Fatalf("sum %v", sum)
	}
}

func TestPropensityDeterministic(t *testing.T) {
	posteriors := map[string]thompson.Posterior{"a": {Alpha: 2, Beta: 3}, "b": {Alpha: 4, Beta: 1}}
	m1 := EstimateThompsonPropensities(posteriors, 10000, 99)
	m2 := EstimateThompsonPropensities(posteriors, 10000, 99)
	if m1["a"] != m2["a"] || m1["b"] != m2["b"] {
		t.Fatalf("not deterministic: %v vs %v", m1, m2)
	}
}

func TestPropensityStabilityImprovesWithDraws(t *testing.T) {
	posteriors := map[string]thompson.Posterior{"a": {Alpha: 5, Beta: 1}, "b": {Alpha: 1, Beta: 5}}
	mLarge := EstimateThompsonPropensities(posteriors, 50000, 42)
	if mLarge["a"] < 0.8 {
		t.Fatalf("dominant should approach 1, got %v", mLarge)
	}
}

func TestGreedyTieBreak(t *testing.T) {
	state := []EligibleArmState{
		{ArmID: "b", Alpha: 1, Beta: 1},
		{ArmID: "a", Alpha: 1, Beta: 1},
	}
	cand := GreedyCandidate{}
	if cand.ActionProbability([]string{"a", "b"}, state, "a") != 1 {
		t.Fatal("greedy should pick lexicographically smallest on tie")
	}
	if cand.ActionProbability([]string{"a", "b"}, state, "b") != 0 {
		t.Fatal("greedy should not pick b")
	}
}

func TestUniformCandidate(t *testing.T) {
	state := []EligibleArmState{{ArmID: "a"}, {ArmID: "b"}, {ArmID: "c"}}
	cand := UniformCandidate{}
	for _, id := range []string{"a", "b", "c"} {
		if math.Abs(cand.ActionProbability([]string{"a", "b", "c"}, state, id)-1.0/3) > 1e-9 {
			t.Fatalf("uniform 1/3")
		}
	}
}

func TestIPSFixture(t *testing.T) {
	records := []BanditLogRecord{
		{SelectedArmID: "a", ObservedReward: 1, LoggingPropensity: floatPtr(0.5), EligibleArmIDs: []string{"a", "b"}, EligibleArmState: []EligibleArmState{{ArmID: "a", Alpha: 2, Beta: 1}, {ArmID: "b", Alpha: 1, Beta: 1}}, Status: OPEEligible},
		{SelectedArmID: "a", ObservedReward: 0, LoggingPropensity: floatPtr(0.5), EligibleArmIDs: []string{"a", "b"}, EligibleArmState: []EligibleArmState{{ArmID: "a", Alpha: 2, Beta: 1}, {ArmID: "b", Alpha: 1, Beta: 1}}, Status: OPEEligible},
		{SelectedArmID: "a", ObservedReward: 1, LoggingPropensity: floatPtr(0.5), EligibleArmIDs: []string{"a", "b"}, EligibleArmState: []EligibleArmState{{ArmID: "a", Alpha: 2, Beta: 1}, {ArmID: "b", Alpha: 1, Beta: 1}}, Status: OPEEligible},
	}
	cand := GreedyCandidate{}
	est := EvaluateOPE(records, cand, nil, 0, 0, 0, 0)
	expectedIPS := (1.0/0.5*1 + 1.0/0.5*0 + 1.0/0.5*1) / 3
	if math.Abs(est.IPS-expectedIPS) > 1e-9 {
		t.Fatalf("IPS %v expected %v", est.IPS, expectedIPS)
	}
	expectedSNIPS := 4.0 / 6.0
	if math.Abs(est.SNIPS-expectedSNIPS) > 1e-9 {
		t.Fatalf("SNIPS %v expected %v", est.SNIPS, expectedSNIPS)
	}
}

func TestESSFixture(t *testing.T) {
	records := []BanditLogRecord{
		{SelectedArmID: "a", ObservedReward: 1, LoggingPropensity: floatPtr(1), EligibleArmIDs: []string{"a", "b"}, EligibleArmState: []EligibleArmState{{ArmID: "a", Alpha: 2, Beta: 1}, {ArmID: "b", Alpha: 1, Beta: 1}}, Status: OPEEligible},
		{SelectedArmID: "a", ObservedReward: 1, LoggingPropensity: floatPtr(0.5), EligibleArmIDs: []string{"a", "b"}, EligibleArmState: []EligibleArmState{{ArmID: "a", Alpha: 2, Beta: 1}, {ArmID: "b", Alpha: 1, Beta: 1}}, Status: OPEEligible},
		{SelectedArmID: "a", ObservedReward: 1, LoggingPropensity: floatPtr(1.0 / 3), EligibleArmIDs: []string{"a", "b"}, EligibleArmState: []EligibleArmState{{ArmID: "a", Alpha: 2, Beta: 1}, {ArmID: "b", Alpha: 1, Beta: 1}}, Status: OPEEligible},
	}
	cand := GreedyCandidate{}
	est := EvaluateOPE(records, cand, nil, 0, 0, 0, 0)
	expectedESS := 36.0 / 14.0
	if math.Abs(est.ESS-expectedESS) > 1e-9 {
		t.Fatalf("ESS %v expected %v", est.ESS, expectedESS)
	}
}

func TestZeroPropensityRejected(t *testing.T) {
	records := []BanditLogRecord{
		{SelectedArmID: "a", ObservedReward: 1, LoggingPropensity: floatPtr(0), EligibleArmIDs: []string{"a"}, EligibleArmState: []EligibleArmState{{ArmID: "a"}}, Status: OPEEligible},
	}
	est := EvaluateOPE(records, UniformCandidate{}, nil, 0, 0, 0, 0)
	if est.UnsupportedFraction != 1 {
		t.Fatalf("should be unsupported")
	}
}

func TestShadowNotCountedAsIndependentRow(t *testing.T) {
	policy := thompson.NewDefault("a", "b")
	mem := &MemoryEvidenceWriter{}
	reg := NewProviderRegistry()
	for _, id := range []string{"a", "b"} {
		reg.Register(NewFakeProvider(id))
	}
	rt, _ := NewRouter(RouterConfig{Policy: policy, Registry: reg, Writer: mem, RNGFactory: func() *rand.Rand { return rand.New(rand.NewPCG(1, 1)) }, ShadowEligibility: HeaderShadowEligibility{}, ShadowSampleRate: 1})
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
	req.Header.Set("X-Shadow-Eligible", "true")
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	// Write to temp file and load via LedgerFromFile to simulate real log
	tmp, _ := os.CreateTemp("", "shadow-count-*.jsonl")
	path := tmp.Name()
	for _, line := range mem.Lines {
		tmp.Write(line)
	}
	tmp.Close()
	defer os.Remove(path)
	decisions, err := LedgerFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 {
		t.Fatalf("should be 1 decision, not 2, got %d", len(decisions))
	}
	records := []BanditLogRecord{}
	for _, d := range decisions {
		records = append(records, ToBanditLog(d, 1000, 42))
	}
	if len(records) != 1 {
		t.Fatalf("should be 1 bandit log, got %d", len(records))
	}
	est := EvaluateOPE(records, UniformCandidate{}, nil, 0, 0, 0, 0)
	if est.TotalDecisions != 1 {
		t.Fatalf("IPS should see 1 row, not shadow")
	}
}

func TestSyntheticUniformRecovery(t *testing.T) {
	// Logging policy uniform 1/2, arms rewards: a=1, b=0, candidate uniform should recover 0.5
	records := []BanditLogRecord{}
	for i := 0; i < 1000; i++ {
		arm := "a"
		if i%2 == 1 {
			arm = "b"
		}
		reward := 0.0
		if arm == "a" {
			reward = 1
		}
		records = append(records, BanditLogRecord{
			SelectedArmID: arm, ObservedReward: reward, LoggingPropensity: floatPtr(0.5),
			EligibleArmIDs: []string{"a", "b"}, EligibleArmState: []EligibleArmState{{ArmID: "a"}, {ArmID: "b"}}, Status: OPEEligible,
		})
	}
	cand := UniformCandidate{}
	est := EvaluateOPE(records, cand, nil, 0, 0, 0, 0)
	if math.Abs(est.IPS-0.5) > 0.02 {
		t.Fatalf("IPS should recover 0.5, got %v", est.IPS)
	}
	if math.Abs(est.SNIPS-0.5) > 0.02 {
		t.Fatalf("SNIPS should recover 0.5")
	}
	// Wrong propensity should bias
	wrongRecords := make([]BanditLogRecord, len(records))
	copy(wrongRecords, records)
	for i := range wrongRecords {
		wrongRecords[i].LoggingPropensity = floatPtr(0.9) // wrong
	}
	estWrong := EvaluateOPE(wrongRecords, cand, nil, 0, 0, 0, 0)
	if math.Abs(estWrong.IPS-0.5) < 0.05 {
		t.Fatalf("wrong propensity should bias, got %v", estWrong.IPS)
	}
}

func TestSelfEvaluationThompson(t *testing.T) {
	// Generate 500 Thompson decisions with synthetic rewards, evaluate self
	policy := thompson.New(thompson.DefaultConfig(), thompson.ExactSampler{})
	for _, id := range []string{"a", "b"} {
		policy.AddArm(id)
	}
	means := map[string]float64{"a": 0.7, "b": 0.3}
	rng := rand.New(rand.NewPCG(42, 42))
	mem := &MemoryEvidenceWriter{}
	reg := NewProviderRegistry()
	for _, id := range []string{"a", "b"} {
		reg.Register(NewFakeProvider(id))
	}
	// Use router-like evidence generation but synthetic reward directly
	tmpWriter := mem
	for i := 0; i < 500; i++ {
		eligible := policy.EligibleArmIDs()
		eligibleState := []EligibleArmState{}
		for _, id := range eligible {
			if p, ok := policy.PosteriorFor(id); ok {
				eligibleState = append(eligibleState, EligibleArmState{ArmID: id, Alpha: p.Alpha, Beta: p.Beta, Pulls: p.Pulls})
			}
		}
		chosen, scores, _ := policy.SelectWithScores(rng)
		postBefore, _ := policy.PosteriorFor(chosen)
		hash := policy.ConfigHash()
		tmpWriter.WriteDecisionStarted(DecisionStarted{
			SchemaVersion: 1, EventType: "DecisionStarted", DecisionID: t.Name() + "-" + string(rune(i)),
			EligibleArmIDs: eligible, SelectedArmID: chosen, SampledScores: scores, PolicyConfigHash: hash,
			PosteriorBefore:  PosteriorSnapshot{Alpha: postBefore.Alpha, Beta: postBefore.Beta, Pulls: postBefore.Pulls},
			EligibleArmState: eligibleState, LoggingPolicyID: "exact-thompson-v1", LoggingPolicyConfigHash: hash,
		})
		mean := means[chosen]
		reward := 0.0
		if rng.Float64() < mean {
			reward = 1
		}
		tmpWriter.WriteExecutionObserved(ExecutionObserved{
			SchemaVersion: 1, EventType: "ExecutionObserved", DecisionID: t.Name() + "-" + string(rune(i)),
			ArmID: chosen, LatencyMs: 10, Success: reward == 1,
		})
		policy.Record(rng, chosen, reward)
		postAfter, _ := policy.PosteriorFor(chosen)
		tmpWriter.WriteDecisionLearned(DecisionLearned{
			SchemaVersion: 1, EventType: "DecisionLearned", DecisionID: t.Name() + "-" + string(rune(i)),
			ArmID: chosen, ComputedReward: reward,
			PosteriorBefore: PosteriorSnapshot{Alpha: postBefore.Alpha, Beta: postBefore.Beta, Pulls: postBefore.Pulls},
			PosteriorAfter:  PosteriorSnapshot{Alpha: postAfter.Alpha, Beta: postAfter.Beta, Pulls: postAfter.Pulls},
			TotalPullsAfter: policy.TotalPulls(),
		})
	}
	// Convert to bandit logs via direct ToBanditLog (simulate file load)
	var decisions []*LedgerDecision
	for i := 0; i < 500; i++ {
		d := &LedgerDecision{
			Started:  &tmpWriter.Started[i],
			Primary:  &tmpWriter.Observed[i],
			Learned:  &tmpWriter.Learned[i],
			Eligible: tmpWriter.Started[i].EligibleArmIDs,
		}
		decisions = append(decisions, d)
	}
	var records []BanditLogRecord
	for _, d := range decisions {
		records = append(records, ToBanditLog(d, 2000, 42))
	}
	eligibleCount := 0
	for _, r := range records {
		if r.Status == OPEEligible {
			eligibleCount++
		}
	}
	if eligibleCount < 400 {
		t.Fatalf("eligible %d", eligibleCount)
	}
	est := EvaluateOPE(records, ExactThompsonCandidate{Draws: 2000, Seed: 42}, nil, 100, 42, 2000, 42)
	// Self-evaluation should reproduce empirical within 0.03
	if math.Abs(est.IPS-est.EmpiricalMean) > 0.03 {
		t.Fatalf("self-eval IPS %.4f vs empirical %.4f diff %.4f", est.IPS, est.EmpiricalMean, est.IPS-est.EmpiricalMean)
	}
	if math.Abs(est.SNIPS-est.EmpiricalMean) > 0.03 {
		t.Fatalf("self-eval SNIPS diff")
	}
	if est.ESSOverN < 0.5 {
		t.Fatalf("ESS/N low for self-eval: %.3f", est.ESSOverN)
	}
}

func floatPtr(v float64) *float64 { return &v }
