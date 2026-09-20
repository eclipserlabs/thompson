package propensity

import "math"

// Gauss-Kronrod (G7, K15) nodes and weights, QUADPACK dqk15.
// xgk holds the 15 Kronrod abscissae on [-1,1] by absolute value, descending;
// the odd indices are the 7 Gauss abscissae. wg are the Gauss weights.
var (
	xgk = [8]float64{
		0.991455371120812639206854697526329,
		0.949107912342758524526189684047851,
		0.864864423359769072789712788640926,
		0.741531185599394439863864773280788,
		0.586087235467691130294144838258730,
		0.405845151377397166906606412076961,
		0.207784955007898467600689403773245,
		0.000000000000000000000000000000000,
	}
	wgk = [8]float64{
		0.022935322010529224963732008058970,
		0.063092092629978553290700663189204,
		0.104790010322250183839876322541518,
		0.140653259715525918745189590510238,
		0.169004726639267902826583426598550,
		0.190350578064785409913256402421014,
		0.204432940075298892414161999234649,
		0.209482141084727828012999174891714,
	}
	wg = [4]float64{
		0.129484966168869693270611432679082,
		0.279705391489276667901467771423780,
		0.381830050505118944950369775488975,
		0.417959183673469387755102040816327,
	}
)

// gk15 applies one Gauss-Kronrod pair to f over [a,b] and returns the K15
// estimate together with the |K15-G7| error indicator and the evaluation count.
func gk15(f func(float64) float64, a, b float64) (value, abserr float64, evals int) {
	center := 0.5 * (a + b)
	half := 0.5 * (b - a)

	fc := f(center)
	resk := wgk[7] * fc
	resg := wg[3] * fc
	evals = 1

	for j := 0; j < 3; j++ { // Gauss abscissae: xgk[1], xgk[3], xgk[5]
		idx := 2*j + 1
		d := half * xgk[idx]
		s := f(center-d) + f(center+d)
		evals += 2
		resg += wg[j] * s
		resk += wgk[idx] * s
	}
	for j := 0; j < 4; j++ { // Kronrod-only abscissae: xgk[0], xgk[2], xgk[4], xgk[6]
		idx := 2 * j
		d := half * xgk[idx]
		s := f(center-d) + f(center+d)
		evals += 2
		resk += wgk[idx] * s
	}

	value = resk * half
	abserr = math.Abs((resk - resg) * half)
	return value, abserr, evals
}

// quadResult is the outcome of an adaptive integration.
type quadResult struct {
	Value     float64
	AbsErr    float64 // summed |K15-G7| over accepted panels; deliberately conservative
	Evals     int
	Converged bool // false if the subdivision budget ran out before tolerance
	MaxDepth  int
}

// adaptiveGK integrates f over [a,b] by recursive bisection of Gauss-Kronrod
// panels until the local error indicator falls under max(absTol_local, relTol*|I|)
// or maxDepth is reached.
//
// Bisection (rather than a global priority queue) keeps the routine allocation
// free and short; the integrands here are unimodal-ish products of a Beta
// density and a monotone CDF product, and the caller seeds a partition at every
// arm's quantiles, so panels start close to the right scale already.
func adaptiveGK(f func(float64) float64, a, b, absTol, relTol float64, maxDepth int) quadResult {
	var res quadResult
	if !(b > a) {
		res.Converged = true
		return res
	}
	whole, wholeErr, evals := gk15(f, a, b)
	res.Evals = evals
	width := b - a
	var rec func(a, b, prev, prevErr float64, depth int)
	rec = func(a, b, prev, prevErr float64, depth int) {
		if depth > res.MaxDepth {
			res.MaxDepth = depth
		}
		// The absolute tolerance is shared out in proportion to panel width so
		// that summing accepted panels cannot exceed the caller's budget.
		tol := absTol * (b - a) / width
		if t := relTol * math.Abs(prev); t > tol {
			tol = t
		}
		if prevErr <= tol || !(b > a) {
			res.Value += prev
			res.AbsErr += prevErr
			return
		}
		if depth >= maxDepth {
			res.Value += prev
			res.AbsErr += prevErr
			res.Converged = false
			return
		}
		m := 0.5 * (a + b)
		if m <= a || m >= b { // interval collapsed to adjacent floats
			res.Value += prev
			res.AbsErr += prevErr
			return
		}
		l, lErr, le := gk15(f, a, m)
		r, rErr, re := gk15(f, m, b)
		res.Evals += le + re
		rec(a, m, l, lErr, depth+1)
		rec(m, b, r, rErr, depth+1)
	}
	res.Converged = true
	rec(a, b, whole, wholeErr, 0)
	return res
}
