package gateway

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/propensity"
)

func ledgerRow(id, selected string, reward float64, state []EligibleArmState) *LedgerDecision {
	ids := make([]string, len(state))
	for i, s := range state {
		ids[i] = s.ArmID
	}
	return &LedgerDecision{
		Started: &DecisionStarted{
			DecisionID: id, EligibleArmIDs: ids, SelectedArmID: selected,
			EligibleArmState: state, LoggingPolicyID: "exact-thompson-v1", LoggingPolicyConfigHash: "h",
		},
		Primary:  &ExecutionObserved{DecisionID: id, ArmID: selected},
		Learned:  &DecisionLearned{DecisionID: id, ArmID: selected, ComputedReward: reward},
		Eligible: ids,
	}
}

// dominantState is a posterior pair where arm b is genuinely reachable -- the
// numerical reference puts it at about 4e-9, well above the 1e-12 resolution
// floor -- yet no achievable Monte-Carlo run will ever sample it. It is the
// exact configuration that turns a zero win count into a false zero-support
// claim if the estimate is believed.
func dominantState() []EligibleArmState {
	return []EligibleArmState{
		{ArmID: "a", Alpha: 70, Beta: 30},
		{ArmID: "b", Alpha: 30, Beta: 70},
	}
}

func TestBanditLogRefusesMCZeroWinsDenominator(t *testing.T) {
	rec := ToBanditLogWith(ledgerRow("d1", "b", 1, dominantState()), NewMCEstimator(2000, 42))
	if rec.PropensityStatus != propensity.MCZeroWins {
		t.Fatalf("status %q, want %s (wins=%d)", rec.PropensityStatus, propensity.MCZeroWins, rec.Propensity.Wins)
	}
	if rec.LoggingPropensity != nil {
		t.Fatalf("a zero-win estimate must not become a denominator, got %v", *rec.LoggingPropensity)
	}
	if rec.Propensity.ZeroWinUpperBound == nil {
		t.Fatal("zero-win row must carry an upper bound")
	}
	if rec.Usable() {
		t.Fatal("MC_ZERO_WINS row must not be usable")
	}
	if rec.Status != OPEEligible {
		t.Fatalf("the row is still OPE-eligible evidence; status %q", rec.Status)
	}
}

func TestIPSRefusesMCZeroWinsRows(t *testing.T) {
	var decisions []*LedgerDecision
	for i := 0; i < 40; i++ {
		decisions = append(decisions, ledgerRow("z"+string(rune('a'+i%26))+string(rune('a'+i/26)), "b", 1, dominantState()))
	}
	var records []BanditLogRecord
	for _, d := range decisions {
		records = append(records, ToBanditLogWith(d, NewMCEstimator(2000, 42)))
	}
	est := EvaluateOPE(records, UniformCandidate{}, nil, 0, 0, 0, 0)
	if est.MCZeroWinsCount != len(records) {
		t.Fatalf("expected all %d rows counted as MC_ZERO_WINS, got %d", len(records), est.MCZeroWinsCount)
	}
	if est.UsableDecisions != 0 {
		t.Fatalf("no row should be usable, got %d", est.UsableDecisions)
	}
	if est.IPS != 0 || est.SNIPS != 0 {
		t.Fatalf("refused rows must not produce an estimate: IPS=%v SNIPS=%v", est.IPS, est.SNIPS)
	}
	if !hasWarning(est.Warnings, "MC_ZERO_WINS") {
		t.Fatalf("expected an MC_ZERO_WINS warning, got %v", est.Warnings)
	}
	if !hasWarning(est.Warnings, "not zero support") {
		t.Fatalf("the warning must say a zero win count is not zero support, got %v", est.Warnings)
	}
}

// TestReferenceResolvesWhatMonteCarloRefuses shows the two estimators disagreeing
// on the same row for the right reason: the reference computes the small
// probability the simulation could not sample.
func TestReferenceResolvesWhatMonteCarloRefuses(t *testing.T) {
	row := ledgerRow("d1", "b", 1, dominantState())
	mc := ToBanditLogWith(row, NewMCEstimator(2000, 42))
	ref := ToBanditLogWith(row, NewReferenceEstimator())
	if mc.LoggingPropensity != nil {
		t.Fatal("Monte Carlo at 2000 draws should refuse this row")
	}
	if ref.LoggingPropensity == nil {
		t.Fatalf("the numerical reference should resolve it: status %s (%s)", ref.PropensityStatus, ref.Propensity.Reason)
	}
	if p := *ref.LoggingPropensity; p <= 0 || p > 1e-6 {
		t.Fatalf("reference propensity %g is not the small positive number expected", p)
	}
	// Even a million draws does not reach it: the fix is the estimator, not the
	// budget.
	big := ToBanditLogWith(row, NewMCEstimator(1000000, 42))
	if big.PropensityStatus != propensity.MCZeroWins {
		t.Fatalf("1e6 draws gave status %s; expected the action to remain unsampled", big.PropensityStatus)
	}
}

func TestLowPrecisionRowsAreRefused(t *testing.T) {
	state := []EligibleArmState{
		{ArmID: "a", Alpha: 1, Beta: 1}, {ArmID: "b", Alpha: 1, Beta: 1},
		{ArmID: "c", Alpha: 1, Beta: 1}, {ArmID: "d", Alpha: 1, Beta: 1},
	}
	// 200 draws over 4 arms gives ~50 wins each, below MinWins=100.
	rec := ToBanditLogWith(ledgerRow("d1", "a", 1, state), NewMCEstimator(200, 42))
	if rec.PropensityStatus != propensity.LowPrecision {
		t.Fatalf("status %q, want %s", rec.PropensityStatus, propensity.LowPrecision)
	}
	if rec.LoggingPropensity != nil {
		t.Fatal("a LOW_PRECISION estimate must not become a denominator")
	}
	// The same state at 200000 draws must pass.
	ok := ToBanditLogWith(ledgerRow("d1", "a", 1, state), NewMCEstimator(200000, 42))
	if ok.PropensityStatus != propensity.Reliable || ok.LoggingPropensity == nil {
		t.Fatalf("200k draws should be reliable, got %s (%s)", ok.PropensityStatus, ok.Propensity.Reason)
	}
}

func TestNonThompsonLoggingPolicyIsIneligible(t *testing.T) {
	d := ledgerRow("d1", "a", 1, dominantState())
	d.Started.LoggingPolicyID = "ucb-regularized-v1"
	rec := ToBanditLogWith(d, NewReferenceEstimator())
	if rec.Status != OPEIneligible {
		t.Fatalf("status %q, want %s", rec.Status, OPEIneligible)
	}
	if !strings.Contains(rec.Reason, "not exact Thompson") {
		t.Fatalf("reason %q should explain the action distribution mismatch", rec.Reason)
	}
}

func TestInvalidPosteriorRowRefused(t *testing.T) {
	state := []EligibleArmState{{ArmID: "a", Alpha: 0, Beta: 1}, {ArmID: "b", Alpha: 1, Beta: 1}}
	rec := ToBanditLogWith(ledgerRow("d1", "a", 1, state), NewReferenceEstimator())
	if rec.PropensityStatus != propensity.InvalidPosterior {
		t.Fatalf("status %q, want %s", rec.PropensityStatus, propensity.InvalidPosterior)
	}
	if rec.LoggingPropensity != nil {
		t.Fatal("invalid posterior must not produce a denominator")
	}
	est := EvaluateOPE([]BanditLogRecord{rec}, UniformCandidate{}, nil, 0, 0, 0, 0)
	if est.InvalidPosteriorCount != 1 || est.UsableDecisions != 0 {
		t.Fatalf("invalid=%d usable=%d", est.InvalidPosteriorCount, est.UsableDecisions)
	}
}

// TestLegacyUnclassifiedRowsStillEvaluate protects the pre-existing fixtures:
// a hand-built record with no PropensityStatus is neither vouched for nor
// refused by the new gate.
func TestLegacyUnclassifiedRowsStillEvaluate(t *testing.T) {
	records := []BanditLogRecord{
		{SelectedArmID: "a", ObservedReward: 1, LoggingPropensity: floatPtr(0.5), Status: OPEEligible,
			EligibleArmIDs: []string{"a", "b"}, EligibleArmState: []EligibleArmState{{ArmID: "a", Alpha: 2, Beta: 1}, {ArmID: "b", Alpha: 1, Beta: 1}}},
	}
	est := EvaluateOPE(records, GreedyCandidate{}, nil, 0, 0, 0, 0)
	if est.UsableDecisions != 1 {
		t.Fatalf("unclassified row should still be evaluated, usable=%d", est.UsableDecisions)
	}
	if math.Abs(est.IPS-2) > 1e-9 {
		t.Fatalf("IPS %v, want 2", est.IPS)
	}
}

func TestThompsonReferenceCandidateProbabilitiesSumToOne(t *testing.T) {
	state := []EligibleArmState{
		{ArmID: "a", Alpha: 7, Beta: 3}, {ArmID: "b", Alpha: 3, Beta: 7}, {ArmID: "c", Alpha: 5, Beta: 5},
	}
	cand := NewThompsonReferenceCandidate()
	sum := 0.0
	for _, s := range state {
		sum += cand.ActionProbability([]string{"a", "b", "c"}, state, s.ArmID)
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Fatalf("candidate probabilities sum to %v", sum)
	}
	// Second call must hit the memo and return identical values.
	again := cand.ActionProbability([]string{"a", "b", "c"}, state, "a")
	first := cand.ActionProbability([]string{"a", "b", "c"}, state, "a")
	if again != first {
		t.Fatalf("memoized value changed: %v then %v", again, first)
	}
}

// TestRouterDoesNotImportNumericalIntegration enforces the placement rule: the
// quadrature must never be reachable from the online router binary.
func TestRouterDoesNotImportNumericalIntegration(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range []string{"router", "thompson"} {
		dir := filepath.Join(root, pkg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, banned := range []string{"go/propensity", "gonum.org/v1/gonum"} {
				if strings.Contains(string(b), banned) {
					t.Fatalf("%s/%s imports %s: numerical integration must stay off the online path",
						pkg, e.Name(), banned)
				}
			}
		}
	}
}

func hasWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}
