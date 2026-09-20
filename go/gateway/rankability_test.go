package gateway

import (
	"strings"
	"testing"
)

func goodEstimate(id string, snips float64) OPEEstimate {
	return OPEEstimate{
		CandidateID: id, TotalDecisions: 1000, EligibleDecisions: 1000, UsableDecisions: 1000,
		SNIPS: snips, IPS: snips, ESS: 800, ESSOverN: 0.8, MaxWeight: 3,
	}
}

func ptr(v float64) *float64 { return &v }

func TestRankableWhenAllGatesPass(t *testing.T) {
	rep := AssessRankability(goodEstimate("good", 0.7), DefaultRankabilityConfig(), ptr(0.001), []string{"numerical-reference", "mc-200k"})
	if rep.Status != Rankable {
		t.Fatalf("status %s, failures %v", rep.Status, rep.Failures)
	}
	if rep.Support != GoodSupportGoodPrecision {
		t.Fatalf("support %s", rep.Support)
	}
}

// TestLowESSIsNotRankableRegardlessOfPointEstimate is the uniform case in
// miniature: a flattering value must not buy its way past the overlap gate.
func TestLowESSIsNotRankableRegardlessOfPointEstimate(t *testing.T) {
	est := goodEstimate("flattering", 0.99)
	est.ESS = 12
	est.ESSOverN = 0.012
	rep := AssessRankability(est, DefaultRankabilityConfig(), ptr(0.001), []string{"numerical-reference", "mc-200k"})
	if rep.Status != NotRankable {
		t.Fatalf("status %s", rep.Status)
	}
	if rep.Support != PoorSupportGoodPrecision {
		t.Fatalf("support %s: propensity precision was fine, only overlap failed", rep.Support)
	}
	if !strings.Contains(rep.Remedy(), "already accurate") {
		t.Fatalf("remedy must not suggest more draws: %q", rep.Remedy())
	}
}

func TestPropensityFailureIsDistinctFromOverlapFailure(t *testing.T) {
	est := goodEstimate("uncertain", 0.7)
	est.MCZeroWinsCount = 100 // 10% of eligible
	est.UnusablePropensityFraction = 0.10
	rep := AssessRankability(est, DefaultRankabilityConfig(), ptr(0.001), []string{"numerical-reference", "mc-200k"})
	if rep.Status != NotRankable {
		t.Fatalf("status %s", rep.Status)
	}
	if rep.Support != GoodSupportPropensityUncertain {
		t.Fatalf("support %s: overlap was fine, only precision failed", rep.Support)
	}
	if !strings.Contains(rep.Remedy(), "more logged data will not help") {
		t.Fatalf("remedy must not suggest better overlap: %q", rep.Remedy())
	}
}

func TestBothFailuresReportedSeparately(t *testing.T) {
	est := goodEstimate("bad", 0.7)
	est.ESS = 5
	est.ESSOverN = 0.005
	est.MCZeroWinsCount = 100
	est.UnusablePropensityFraction = 0.10
	rep := AssessRankability(est, DefaultRankabilityConfig(), ptr(0.5), nil)
	if rep.Support != PoorSupportAndPropensityUncertain {
		t.Fatalf("support %s", rep.Support)
	}
	if !strings.Contains(rep.Remedy(), "independent failures") {
		t.Fatalf("remedy %q", rep.Remedy())
	}
}

func TestUnmeasuredSensitivityIsNotRankable(t *testing.T) {
	rep := AssessRankability(goodEstimate("nosweep", 0.7), DefaultRankabilityConfig(), nil, []string{"numerical-reference"})
	if rep.Status != NotRankable {
		t.Fatalf("status %s", rep.Status)
	}
	if !strings.Contains(strings.Join(rep.Failures, " "), "sensitivity sweep") {
		t.Fatalf("failures %v", rep.Failures)
	}
}

func TestUnstableAcrossEstimatorsIsNotRankable(t *testing.T) {
	rep := AssessRankability(goodEstimate("unstable", 0.7), DefaultRankabilityConfig(), ptr(0.2), []string{"numerical-reference", "mc-200k"})
	if rep.Status != NotRankable {
		t.Fatalf("status %s", rep.Status)
	}
	if rep.Support != GoodSupportPropensityUncertain {
		t.Fatalf("support %s", rep.Support)
	}
}

func TestMaxWeightGate(t *testing.T) {
	est := goodEstimate("heavy", 0.7)
	est.MaxWeight = 500
	rep := AssessRankability(est, DefaultRankabilityConfig(), ptr(0.001), []string{"a", "b"})
	if rep.Status != NotRankable {
		t.Fatalf("status %s", rep.Status)
	}
}

// TestRankingCannotSelectNotRankableCandidate is the API-level guarantee.
func TestRankingCannotSelectNotRankableCandidate(t *testing.T) {
	cfg := DefaultRankabilityConfig()
	// A candidate with an unbeatable point estimate but no overlap.
	bad := goodEstimate("no-overlap-but-highest-value", 0.99)
	bad.ESS = 4
	bad.ESSOverN = 0.004
	// A candidate with a modest but well-supported estimate.
	ok := goodEstimate("supported", 0.62)
	// A second supported candidate so a loser can exist.
	ok2 := goodEstimate("supported-worse", 0.41)

	reports := []RankabilityReport{
		AssessRankability(bad, cfg, ptr(0.001), []string{"a", "b"}),
		AssessRankability(ok, cfg, ptr(0.001), []string{"a", "b"}),
		AssessRankability(ok2, cfg, ptr(0.001), []string{"a", "b"}),
	}
	res := Rank(reports)
	if res.Winner == "no-overlap-but-highest-value" {
		t.Fatal("a NOT_RANKABLE candidate was named winner")
	}
	if res.Loser == "no-overlap-but-highest-value" {
		t.Fatal("a NOT_RANKABLE candidate was named loser")
	}
	if res.Winner != "supported" || res.Loser != "supported-worse" {
		t.Fatalf("winner %q loser %q", res.Winner, res.Loser)
	}
	for _, r := range res.Ranked {
		if r.Status != Rankable {
			t.Fatalf("%s is in the ranking with status %s", r.CandidateID, r.Status)
		}
	}
	if len(res.Refused) != 1 {
		t.Fatalf("expected 1 refused candidate, got %d", len(res.Refused))
	}
	if !strings.Contains(strings.Join(res.Notes, " "), "NOT_RANKABLE") {
		t.Fatalf("notes must explain the exclusion: %v", res.Notes)
	}
}

func TestRankingNamesNobodyWhenNothingIsRankable(t *testing.T) {
	cfg := DefaultRankabilityConfig()
	bad := goodEstimate("bad", 0.9)
	bad.ESS = 1
	bad.ESSOverN = 0.001
	res := Rank([]RankabilityReport{AssessRankability(bad, cfg, ptr(0.001), []string{"a", "b"})})
	if res.Winner != "" || res.Loser != "" {
		t.Fatalf("winner %q loser %q: neither should be named", res.Winner, res.Loser)
	}
	if !strings.Contains(strings.Join(res.Notes, " "), "no winner or loser named") {
		t.Fatalf("notes %v", res.Notes)
	}
}

func TestRankingWithOneRankableNamesNoLoser(t *testing.T) {
	cfg := DefaultRankabilityConfig()
	res := Rank([]RankabilityReport{AssessRankability(goodEstimate("only", 0.7), cfg, ptr(0.001), []string{"a", "b"})})
	if res.Winner != "only" {
		t.Fatalf("winner %q", res.Winner)
	}
	if res.Loser != "" {
		t.Fatalf("loser %q: nothing rankable to compare against", res.Loser)
	}
}

func TestRankingFlagsOrderingInsideTheErrorBars(t *testing.T) {
	cfg := DefaultRankabilityConfig()
	a := goodEstimate("a", 0.803)
	b := goodEstimate("b", 0.801)
	se := 0.004
	a.BootstrapSE, b.BootstrapSE = &se, &se
	res := Rank([]RankabilityReport{
		AssessRankability(a, cfg, ptr(0.001), []string{"x", "y"}),
		AssessRankability(b, cfg, ptr(0.001), []string{"x", "y"}),
	})
	if res.Winner != "a" || res.Loser != "b" {
		t.Fatalf("winner %q loser %q", res.Winner, res.Loser)
	}
	if !strings.Contains(strings.Join(res.Notes, " "), "not evidence of a difference") {
		t.Fatalf("a 0.002 gap against SE 0.004 must be flagged: %v", res.Notes)
	}
}

func TestRankingDoesNotFlagAClearSeparation(t *testing.T) {
	cfg := DefaultRankabilityConfig()
	a := goodEstimate("a", 0.90)
	b := goodEstimate("b", 0.50)
	se := 0.004
	a.BootstrapSE, b.BootstrapSE = &se, &se
	res := Rank([]RankabilityReport{
		AssessRankability(a, cfg, ptr(0.001), []string{"x", "y"}),
		AssessRankability(b, cfg, ptr(0.001), []string{"x", "y"}),
	})
	if strings.Contains(strings.Join(res.Notes, " "), "not evidence of a difference") {
		t.Fatalf("a 0.4 gap against SE 0.004 must not be flagged: %v", res.Notes)
	}
}

func TestRankingWithoutBootstrapSaysSo(t *testing.T) {
	cfg := DefaultRankabilityConfig()
	res := Rank([]RankabilityReport{
		AssessRankability(goodEstimate("a", 0.9), cfg, ptr(0.001), []string{"x", "y"}),
		AssessRankability(goodEstimate("b", 0.5), cfg, ptr(0.001), []string{"x", "y"}),
	})
	if !strings.Contains(strings.Join(res.Notes, " "), "point-estimate ordering only") {
		t.Fatalf("notes %v", res.Notes)
	}
}
