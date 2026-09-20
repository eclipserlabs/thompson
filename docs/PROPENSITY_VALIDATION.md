# Propensity ground truth for off-policy evaluation

Off-policy evaluation of a Thompson Sampling router divides each logged reward
by the probability that the logging policy would have chosen the action it
chose. Thompson Sampling does not compute that probability, so it is
reconstructed offline. Everything downstream — IPS, SNIPS, ESS, confidence
intervals, any policy comparison at all — rests on that reconstruction being
right.

This document describes how the reconstruction is validated, and what the
system refuses to do when it cannot be.

## The circularity that had to be removed

The reconstruction was Monte Carlo: simulate one Beta draw per arm, take the
argmax, repeat, count wins. To check it, an "exact Thompson" candidate policy
was evaluated against Thompson logging propensities and its IPS estimate came
out equal to the empirical mean.

That result is guaranteed regardless of whether the estimator is any good. If
the candidate's action probabilities and the logging propensities come from the
same Monte-Carlo vector, the importance weight is

    w = pi_candidate(a) / pi_logging(a) = p_hat(a) / p_hat(a) = 1

for every row, and IPS collapses to the empirical mean by construction. A
uniformly biased estimator produces the identical output. The check exercises
row joining, not propensities; it is now labelled `IMPLEMENTATION_SANITY_ONLY`
everywhere it appears.

## The independent reference

`go/propensity` computes the Thompson action probability directly, from the
order-statistic identity. If arm *i* has posterior Beta(α_i, β_i) and one
independent sample is drawn per arm, arm *i* wins exactly when its sample
exceeds every other, so

    P_i = ∫₀¹ f_i(x) · ∏_{j≠i} F_j(x) dx

with `f_i` the Beta density and `F_j` the regularized incomplete beta function.

It shares nothing with the Monte-Carlo estimator: no random number stream, no
sampler, no code. That is what makes it an independent leg rather than a second
opinion from the same witness.

Implementation notes:

- **Quadrature.** Adaptive Gauss–Kronrod (G7, K15) with recursive bisection.
  Each panel reports `|K15 − G7|` as an error indicator and the sum over accepted
  panels is returned per arm.
- **Partition seeding.** The initial panels come from every arm's Beta quantiles
  plus a geometric ladder toward 0 and 1. Concentrated posteriors put all their
  mass in a window far narrower than any fixed grid resolves — a Beta(9000,1000)
  lives inside a band of width ~0.03 — and a rival like Beta(10⁶,1) leaves the
  CDF product flat zero until *x* is within 10⁻⁶ of 1. Without the seeding the
  same integrals cost 20 000× more evaluations for the same answer.
- **Log space.** The integrand is assembled as `log f_i(x) + Σ_{j≠i} log F_j(x)`
  and exponentiated once, so a product of many small CDF values cannot underflow
  before the density is applied.
- **Normalization.** Probabilities are integrated independently and their
  pre-normalization sum is reported as `RawSum`/`SumError`. Normalization is
  applied only as a final correction, and a residual above `MaxSumError` (10⁻⁶)
  is a `NUMERICAL_REFERENCE_FAILURE` rather than something to divide away.
- **Special functions.** `gonum.org/v1/gonum/mathext`, itself cross-checked
  against a binomial identity that needs no special functions. See
  `go/propensity/DEPENDENCY.md`.

### Where the reference is valid

The identity describes "one independent exact Beta draw per arm, argmax". That
is `thompson.ThompsonStrategy` with `thompson.ExactSampler`, and nothing else.
It does **not** describe `UCBRegularized` (adds a pull-count bonus),
`PhasedSelection` (forces the least-pulled arm below a quota), or the
approximate samplers, which do not draw from the Beta posterior at all.
`propensity.CheckLoggingPolicy` refuses decisions logged under any of them, and
those rows become `OPE_INELIGIBLE`.

## Per-action reliability

Every reconstructed propensity carries a status:

| status | meaning |
| --- | --- |
| `RELIABLE` | precise enough to be an importance-weight denominator |
| `LOW_PRECISION` | positive, but its own uncertainty makes the weight untrustworthy |
| `MC_ZERO_WINS` | the simulation saw the action zero times |
| `NUMERICAL_REFERENCE_FAILURE` | quadrature did not converge or did not integrate to unity |
| `INVALID_POSTERIOR` | α or β not finite and positive |

Only `RELIABLE` produces a denominator. Everything else leaves
`LoggingPropensity` nil and the row is dropped from IPS/SNIPS with the reason
recorded — never entered with weight zero, which would quietly bias the estimate
toward whichever arms happened to be resolvable.

### Zero wins is not zero probability

An eligible arm with α > 0 and β > 0 always has strictly positive Thompson
probability. A zero win count is a statement about the estimator's budget, so
the status is `MC_ZERO_WINS`, never `TRUE_ZERO_PROPENSITY`. The row reports the
exact Clopper–Pearson one-sided upper bound `1 − α^(1/n)` (the rule of three,
≈3/n at 95%) — an upper bound on an unknown positive number. It is never used as
a floor and never enters a denominator.

### Draw count is not an accuracy claim

`1/√draws` bounds the *absolute* error of a binomial proportion. It says nothing
about the relative error of a rare action, which is what an importance-weight
denominator needs. An action with true probability 5×10⁻⁶ is estimated at
exactly zero by 10⁵ draws: nominal accuracy 0.3%, actual relative error 100%.
The gate is therefore on the win count (`MinWins`, default 100 ⇒ relative
standard error ≈10%) and on the relative width of the Wilson interval, not on
the draw budget.

## Overlap failure versus propensity-estimation failure

These are independent failures with disjoint remedies, and conflating them is
how an unsupported candidate gets promoted. Each candidate is classified:

| class | what is wrong | what fixes it |
| --- | --- | --- |
| `GOOD_SUPPORT_GOOD_PRECISION` | nothing | — |
| `GOOD_SUPPORT_PROPENSITY_UNCERTAIN` | the denominators are unreliable | more draws, or the numerical reference. **More logged data does not help.** |
| `POOR_SUPPORT_GOOD_PRECISION` | the logging policy rarely played the candidate's actions | log under a policy that plays them. **More draws do not help.** |
| `POOR_SUPPORT_AND_PROPENSITY_UNCERTAIN` | both | fix them separately |

## Rankability gates

A candidate may be named winner or loser only if every gate passes
(`gateway.DefaultRankabilityConfig`):

| gate | default | rationale |
| --- | --- | --- |
| `MinESS` | 200 | a bounded-reward mean over 200 effective samples resolves to about ±0.035 at 95% |
| `MinESSOverN` | 0.10 | fewer than one effective sample per ten logged rows is not overlap |
| `MaxWeight` | 50 | one row supplying >2% of a 2 500-row estimate |
| `MaxLowPrecisionFraction` | 0.02 | refused rows are a biased subsample, not a wider interval |
| `MaxMCZeroWinsFraction` | 0.01 | as above, and these are precisely the extreme-weight rows |
| `MaxUnusableFraction` | 0.05 | combined refusal budget |
| `MaxPropensitySensitivity` | 0.02 | SNIPS spread across accepted estimators; a candidate whose value moves when the denominator estimator changes is being ranked on the estimator |

`gateway.Rank` refuses to place a `NOT_RANKABLE` candidate in either position.
A flattering point estimate with no overlap cannot win, and a candidate whose
denominators could not be resolved cannot lose.

Sensitivity needs at least two accepted estimators. One estimator means the
candidate's stability is *unmeasured*, which is not the same as stable, and the
gate fails.

Passing the gates means the estimates are trustworthy enough to compare — not
that the comparison found anything. When the winner and loser are separated by
less than their combined bootstrap uncertainty, `Rank` says so in its notes
rather than letting an ordering of noise read as a result.

## Running the validation

```
go run ./cmd/propensity-audit                       # defaults: 10k synthetic decisions
go run ./cmd/propensity-audit --evidence ./evidence.jsonl
go run ./cmd/propensity-audit --draws 2000,20000,200000,1000000
```

The audit prints the posterior test matrix (hand-built shape families at 2, 4, 8
and 16 arms plus ≥100 states sampled from real Thompson runs), Monte-Carlo error
against the reference at each draw count, the worst individual errors, the
rare-action breakdown, every `MC_ZERO_WINS` action whose reference is positive,
the independent Thompson calibration, the importance-weight sensitivity by
propensity band, and the rankability verdict for each candidate.

`cmd/evaluate` gained `--propensity reference|mc`. It defaults to `reference`
and prints `IMPLEMENTATION_SANITY_ONLY` when the candidate and the denominator
share an estimator.

## Placement

Numerical integration is offline tooling and never runs on the router hot path.
`gateway.TestRouterDoesNotImportNumericalIntegration` fails if `router` or
`thompson` ever import `go/propensity` or gonum.
