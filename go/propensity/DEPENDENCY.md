# Why this package depends on gonum

The numerical reference needs the regularized incomplete beta function
`I_x(a, b)` — the Beta CDF — and its inverse, at full double precision, for
parameters ranging from below 1 to the tens of thousands.

## What was already available

- **Go standard library.** `math` provides `Lgamma`, which covers `log B(a, b)`
  and therefore the Beta *density*. It has no incomplete beta, no incomplete
  gamma, and no continued-fraction machinery. The density alone is not enough:
  the order-statistic integral needs the CDF of every rival arm.
- **Existing dependencies.** The module previously required only
  `go.opentelemetry.io/otel` and its transitive logging/metric/trace packages.
  None of them contain special functions.

So neither source could supply the required function.

## What was added

`gonum.org/v1/gonum` v0.15.0, for `mathext.RegIncBeta` and
`mathext.InvRegIncBeta`. Both are thin wrappers over gonum's port of the Cephes
`incbet`/`incbi` routines — an implementation that predates this repository by
decades, is used by SciPy for the same function, and carries its own test suite.

Only `mathext` (and its `internal/cephes` support) is imported. Nothing from
gonum's linear algebra, statistics, graph or plotting trees is reachable from
this package.

## Why not hand-roll it

A continued-fraction incomplete beta is about forty lines and is the standard
thing to write when avoiding a dependency. It is also the wrong move here. The
whole purpose of this package is to be an *independent* check on a propensity
estimator: a hand-written special function, tested only against the integral
that consumes it, would move the unvalidated step rather than remove it. An
implementation with independent provenance and independent tests is the point,
not a compromise.

## How the dependency is itself validated

`TestBetaCDFAgainstBinomialIdentity` checks `RegIncBeta` against an identity
that needs no special functions at all. For integer `a` and `b`,

    I_x(a, b) = sum_{j=a}^{a+b-1} C(a+b-1, j) x^j (1-x)^(a+b-1-j)

i.e. the upper tail of a binomial. The test evaluates that sum directly from log
factorials over a grid of `a`, `b` and `x`, and requires agreement to 1e-11.

`TestBetaLogPDFAgainstDirectForm` does the same for the density against its
direct (non-log) form, and `TestQuadratureOnKnownIntegrals` checks the
Gauss-Kronrod driver against integrals with closed-form values. On top of that,
`TestReferenceMatchesClosedForm` checks the assembled integral against Thompson
probabilities that can be written down by hand, such as
`P(Beta(1,1) beats Beta(n,1)) = 1/(n+1)`.

## Placement

The dependency is reachable only from offline evaluation packages
(`propensity`, `opeval`, `cmd/propensity-audit`) and from `gateway`'s
evaluation path. `TestRouterDoesNotImportNumericalIntegration` fails if the
`router` or `thompson` packages ever import it.
