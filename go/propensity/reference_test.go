package propensity

import (
	"fmt"
	"math"
	"testing"
)

func identical(k int, alpha, beta float64) []Arm {
	arms := make([]Arm, k)
	for i := range arms {
		arms[i] = Arm{ID: fmt.Sprintf("a%02d", i), Alpha: alpha, Beta: beta}
	}
	return arms
}

func mustReference(t *testing.T, arms []Arm) ReferenceResult {
	t.Helper()
	r, err := Reference(arms, ReferenceOptions{})
	if err != nil {
		t.Fatalf("Reference: %v", err)
	}
	return r
}

func TestReferenceTwoIdenticalArmsIsHalfEach(t *testing.T) {
	r := mustReference(t, identical(2, 1, 1))
	for _, id := range []string{"a00", "a01"} {
		if math.Abs(r.Probs[id]-0.5) > 1e-12 {
			t.Fatalf("arm %s = %.15g, want 0.5", id, r.Probs[id])
		}
	}
}

func TestReferenceFourIdenticalArmsIsQuarterEach(t *testing.T) {
	r := mustReference(t, identical(4, 1, 1))
	for _, a := range identical(4, 1, 1) {
		if math.Abs(r.Probs[a.ID]-0.25) > 1e-12 {
			t.Fatalf("arm %s = %.15g, want 0.25", a.ID, r.Probs[a.ID])
		}
	}
}

// TestReferenceSymmetryAcrossArmCounts also covers warm-start Beta(4,1), where
// symmetry must hold at a shape the uniform prior does not exercise.
func TestReferenceSymmetryAcrossArmCounts(t *testing.T) {
	for _, k := range []int{2, 4, 8, 16} {
		for _, p := range []struct{ a, b float64 }{{1, 1}, {4, 1}, {100, 100}} {
			arms := identical(k, p.a, p.b)
			r := mustReference(t, arms)
			want := 1 / float64(k)
			for _, a := range arms {
				if math.Abs(r.Probs[a.ID]-want) > 1e-10 {
					t.Fatalf("K=%d Beta(%g,%g) arm %s = %.15g, want %.15g", k, p.a, p.b, a.ID, r.Probs[a.ID], want)
				}
			}
		}
	}
}

// TestReferenceMatchesClosedForm checks the integral against cases whose value
// can be written down by hand, which is the only way to know the quadrature is
// integrating the right thing rather than merely converging.
func TestReferenceMatchesClosedForm(t *testing.T) {
	cases := []struct {
		name string
		arms []Arm
		arm  string
		want float64
	}{
		{
			// f_a = 2x, F_b = 2x - x^2. Integral of 4x^2 - 2x^3 over [0,1] = 5/6.
			name: "Beta(2,1) vs Beta(1,2)",
			arms: []Arm{{"a", 2, 1}, {"b", 1, 2}},
			arm:  "a", want: 5.0 / 6.0,
		},
		{
			// f_a = 1 (uniform), F_b = x^n. Integral of x^n = 1/(n+1).
			name: "Beta(1,1) vs Beta(1e6,1)",
			arms: []Arm{{"a", 1, 1}, {"b", 1e6, 1}},
			arm:  "a", want: 1.0 / (1e6 + 1),
		},
		{
			// Same identity at a smaller exponent, to catch a scale-specific bug.
			name: "Beta(1,1) vs Beta(7,1)",
			arms: []Arm{{"a", 1, 1}, {"b", 7, 1}},
			arm:  "a", want: 1.0 / 8.0,
		},
		{
			// f_a = 3x^2, F_b = x. Integral of 3x^3 = 3/4.
			name: "Beta(3,1) vs Beta(1,1)",
			arms: []Arm{{"a", 3, 1}, {"b", 1, 1}},
			arm:  "a", want: 3.0 / 4.0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := mustReference(t, tc.arms)
			got := r.Probs[tc.arm]
			if rel := math.Abs(got-tc.want) / tc.want; rel > 1e-9 {
				t.Fatalf("got %.15g want %.15g (relative error %.3g)", got, tc.want, rel)
			}
		})
	}
}

func TestReferenceSumsToOneBeforeNormalization(t *testing.T) {
	states := [][]Arm{
		identical(2, 1, 1),
		identical(16, 1, 1),
		{{"a", 4, 1}, {"b", 1, 4}, {"c", 2, 2}},
		{{"a", 900, 100}, {"b", 100, 900}, {"c", 500, 500}, {"d", 3, 7}},
		{{"a", 4000, 6000}, {"b", 4040, 5960}, {"c", 4080, 5920}},
		{{"a", 1, 4000}, {"b", 4000, 1}, {"c", 250, 1}},
	}
	for _, arms := range states {
		r := mustReference(t, arms)
		sum := 0.0
		for _, a := range arms {
			sum += r.Raw[a.ID]
		}
		if math.Abs(sum-1) > 1e-9 {
			t.Fatalf("pre-normalization sum %.15g for %v (residual %.3g)", sum, arms, sum-1)
		}
		if math.Abs(r.RawSum-sum) > 1e-15 {
			t.Fatalf("RawSum %.15g disagrees with recomputed %.15g", r.RawSum, sum)
		}
		if math.Abs(r.SumError-(r.RawSum-1)) > 1e-18 {
			t.Fatalf("SumError %g is not RawSum-1", r.SumError)
		}
	}
}

// TestReferenceFiniteForConcentratedPosteriors is the underflow guard: with
// thousands of observations per arm the Beta density peaks in the hundreds and
// the CDF product spans hundreds of orders of magnitude, which is where a
// non-log-space implementation returns NaN or zero for every arm.
func TestReferenceFiniteForConcentratedPosteriors(t *testing.T) {
	states := [][]Arm{
		{{"a", 80000, 20000}, {"b", 60000, 40000}, {"c", 40000, 60000}, {"d", 20000, 80000}},
		{{"a", 1e6, 1e6}, {"b", 1e6 + 1000, 1e6 - 1000}},
		{{"a", 900, 100}, {"b", 100, 900}},
	}
	for _, arms := range states {
		r := mustReference(t, arms)
		total := 0.0
		for _, a := range arms {
			p := r.Probs[a.ID]
			if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
				t.Fatalf("arm %s probability %v is not a probability", a.ID, p)
			}
			total += p
		}
		if math.Abs(total-1) > 1e-9 {
			t.Fatalf("probabilities sum to %.15g", total)
		}
		if !r.Converged {
			t.Fatalf("did not converge for %v", arms)
		}
	}
}

func TestReferenceRejectsInvalidPosterior(t *testing.T) {
	bad := [][]Arm{
		{{"a", 0, 1}, {"b", 1, 1}},
		{{"a", 1, 0}, {"b", 1, 1}},
		{{"a", -1, 1}, {"b", 1, 1}},
		{{"a", math.NaN(), 1}, {"b", 1, 1}},
		{{"a", math.Inf(1), 1}, {"b", 1, 1}},
	}
	for _, arms := range bad {
		if _, err := Reference(arms, ReferenceOptions{}); err == nil {
			t.Fatalf("expected error for %v", arms)
		}
	}
}

func TestReferenceSingleArmIsCertain(t *testing.T) {
	r := mustReference(t, []Arm{{"only", 3, 5}})
	if r.Probs["only"] != 1 {
		t.Fatalf("single arm probability %v, want 1", r.Probs["only"])
	}
}

func TestReferenceIsDeterministic(t *testing.T) {
	arms := []Arm{{"a", 17, 3}, {"b", 5, 11}, {"c", 2, 2}}
	first := mustReference(t, arms)
	for i := 0; i < 3; i++ {
		again := mustReference(t, arms)
		for _, a := range arms {
			if first.Probs[a.ID] != again.Probs[a.ID] {
				t.Fatalf("arm %s: %v then %v", a.ID, first.Probs[a.ID], again.Probs[a.ID])
			}
		}
	}
}

// TestReferenceReportsIntegrationError checks the error estimate is present and
// is not silently zero for a non-trivial integrand.
func TestReferenceReportsIntegrationError(t *testing.T) {
	r := mustReference(t, []Arm{{"a", 7, 3}, {"b", 3, 7}, {"c", 5, 5}})
	for id, e := range r.IntegrationAbsErr {
		if math.IsNaN(e) || e < 0 {
			t.Fatalf("arm %s integration error %v", id, e)
		}
		if e > 1e-8 {
			t.Fatalf("arm %s integration error %g is implausibly large", id, e)
		}
	}
}

// TestBetaCDFAgainstBinomialIdentity cross-checks the external incomplete-beta
// implementation against an identity that needs no special functions at all:
// for integer a and b, I_x(a, b) equals the upper tail of Binomial(a+b-1, x).
// This is what justifies leaning on the dependency rather than a hand-rolled
// continued fraction.
func TestBetaCDFAgainstBinomialIdentity(t *testing.T) {
	binomUpper := func(n, k int, x float64) float64 {
		// sum_{j=k}^{n} C(n,j) x^j (1-x)^(n-j)
		sum := 0.0
		for j := k; j <= n; j++ {
			logC, _ := math.Lgamma(float64(n + 1))
			l1, _ := math.Lgamma(float64(j + 1))
			l2, _ := math.Lgamma(float64(n - j + 1))
			term := math.Exp(logC - l1 - l2 + float64(j)*math.Log(x) + float64(n-j)*math.Log1p(-x))
			sum += term
		}
		return sum
	}
	for _, a := range []int{1, 2, 5, 13, 40} {
		for _, b := range []int{1, 3, 8, 25} {
			for _, x := range []float64{0.01, 0.1, 0.37, 0.5, 0.83, 0.99} {
				got := regIncBeta(float64(a), float64(b), x)
				want := binomUpper(a+b-1, a, x)
				if math.Abs(got-want) > 1e-11 {
					t.Fatalf("I_%g(%d,%d)=%.15g, binomial identity gives %.15g", x, a, b, got, want)
				}
			}
		}
	}
}

// TestBetaLogPDFAgainstDirectForm checks the log-space density against the
// direct form in the range where the direct form is still representable.
func TestBetaLogPDFAgainstDirectForm(t *testing.T) {
	for _, p := range []struct{ a, b float64 }{{1, 1}, {2, 3}, {7, 2}, {0.5, 0.5}} {
		lb := logBeta(p.a, p.b)
		for _, x := range []float64{0.05, 0.25, 0.5, 0.75, 0.95} {
			got := math.Exp(betaLogPDF(x, p.a, p.b, lb))
			ga, _ := math.Lgamma(p.a)
			gb, _ := math.Lgamma(p.b)
			gab, _ := math.Lgamma(p.a + p.b)
			want := math.Exp(gab-ga-gb) * math.Pow(x, p.a-1) * math.Pow(1-x, p.b-1)
			if rel := math.Abs(got-want) / want; rel > 1e-12 {
				t.Fatalf("Beta(%g,%g) pdf at %g: %.15g vs %.15g", p.a, p.b, x, got, want)
			}
		}
	}
}

// TestQuadratureOnKnownIntegrals exercises the Gauss-Kronrod driver directly.
func TestQuadratureOnKnownIntegrals(t *testing.T) {
	cases := []struct {
		name string
		f    func(float64) float64
		a, b float64
		want float64
	}{
		{"x^2 over [0,1]", func(x float64) float64 { return x * x }, 0, 1, 1.0 / 3},
		{"sin over [0,pi]", math.Sin, 0, math.Pi, 2},
		{"exp over [0,1]", math.Exp, 0, 1, math.E - 1},
		{"1/(1+x^2) over [0,1]", func(x float64) float64 { return 1 / (1 + x*x) }, 0, 1, math.Pi / 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := adaptiveGK(tc.f, tc.a, tc.b, 1e-14, 1e-12, 30)
			if !r.Converged {
				t.Fatalf("did not converge")
			}
			if math.Abs(r.Value-tc.want) > 1e-11 {
				t.Fatalf("got %.15g want %.15g", r.Value, tc.want)
			}
		})
	}
}

func TestCheckLoggingPolicy(t *testing.T) {
	for _, ok := range []string{"exact-thompson-v1", "thompson-v1"} {
		if err := CheckLoggingPolicy(ok); err != nil {
			t.Fatalf("%s should be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{"ucb-regularized-v1", "phased-v1", "mean-gaussian-v1", ""} {
		if err := CheckLoggingPolicy(bad); err == nil {
			t.Fatalf("%s should be rejected", bad)
		}
	}
}
