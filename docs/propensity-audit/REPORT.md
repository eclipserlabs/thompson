# Propensity ground-truth validation — findings

Reproduce with:

```
go run ./go/cmd/propensity-audit --seed 42                       # everything, defaults
go run ./go/cmd/propensity-audit --skip-matrix --decisions 10000 --draws 2000,20000
```

Raw output from the runs quoted below is in `matrix.txt` (posterior test matrix,
all four draw counts), `log-10k.txt` (10 000-decision diagnostics at reference /
2k / 20k) and `ladder-300.txt` (300-decision diagnostics at reference / 20k /
200k / 1M). Seed 42 throughout. The split exists only because the host ran out
of memory during the long run; see "Remaining limitations".

---

## 1. Numerical reference health

132 distinct posterior states: 7 hand-built shape families × {2,4,8,16} arms
(28), 4 states lifted from a Thompson run at 10/100/1000/10000 decisions, and
100 states sampled from real Thompson runs at 2, 4, 8 and 16 arms.

| metric | value |
| --- | --- |
| `NUMERICAL_REFERENCE_FAILURE` | 0 / 132 states |
| non-converged quadrature | 0 |
| worst pre-normalization `sum(P_i) - 1` | **-7.76e-07** (`highly-asymmetric/K=16`) |

The worst residual comes from the only family containing sub-unit shapes
(`Beta(0.5, 250)`), where the density has an integrable `x^-1/2` singularity at
0. Every other family sits at 1e-13 or below. Normalization is applied only
after this residual is reported, and a residual above 1e-6 is a failure rather
than something divided away.

Closed-form checks (`propensity/reference_test.go`):

| case | exact value | agreement |
| --- | --- | --- |
| 2 × Beta(1,1) | 0.5 each | < 1e-12 |
| 4 × Beta(1,1) | 0.25 each | < 1e-12 |
| K × Beta(1,1), Beta(4,1), Beta(100,100) for K ∈ {2,4,8,16} | 1/K each | < 1e-10 |
| Beta(2,1) vs Beta(1,2) | 5/6 | < 1e-9 rel |
| Beta(3,1) vs Beta(1,1) | 3/4 | < 1e-9 rel |
| Beta(1,1) vs Beta(7,1) | 1/8 | < 1e-9 rel |
| Beta(1,1) vs Beta(10⁶,1) | 1/1000001 | < 1e-9 rel |

The incomplete beta implementation is itself checked against the binomial
identity `I_x(a,b) = Σ_{j≥a} C(a+b-1,j) x^j (1-x)^(a+b-1-j)` for integer a, b,
which needs no special functions at all.

---

## 2. Monte Carlo versus reference

976 arm-observations per draw count, across all 132 states.

| draws | mean abs err | max abs err | median rel err | max rel err |
| --- | --- | --- | --- | --- |
| 2 000 | 3.167e-03 | 3.37e-02 | 0.0757 | 3.235 |
| 20 000 | 1.001e-03 | 7.03e-03 | 0.0238 | 1.753 |
| 200 000 | 3.280e-04 | 2.53e-03 | 0.0080 | 1.000 |
| 1 000 000 | 1.490e-04 | 1.09e-03 | 0.0035 | 1.840 |

Absolute error falls as `1/√draws`, as expected. **Maximum relative error does
not**, and that is the whole finding: at 10⁶ draws the worst relative error is
still 184%, because the worst-case action is one the simulation never sampled.

Worst individual errors at each draw count are in `matrix.txt`. At 2 000 draws
the largest absolute error is 0.0337 on an action whose true probability is
0.309 — an 11% relative error on a *common* action, on a state taken straight
from a four-arm Thompson run.

### Rare actions

| ref p below | draws | n | zero wins | mean rel err | max rel err |
| --- | --- | --- | --- | --- | --- |
| 1e-2 | 2k | 329 | 87 | 0.563 | 3.235 |
| 1e-2 | 20k | 329 | 43 | 0.284 | 1.753 |
| 1e-2 | 200k | 329 | 37 | 0.166 | 1.000 |
| 1e-2 | 1M | 329 | 36 | 0.138 | 1.840 |
| 1e-3 | 2k | 121 | 78 | 0.945 | 3.235 |
| 1e-3 | 20k | 121 | 43 | 0.582 | 1.753 |
| 1e-3 | 200k | 121 | 37 | 0.389 | 1.000 |
| 1e-3 | 1M | 121 | 36 | 0.348 | 1.840 |
| 1e-4 | 2k | 46 | 46 | 1.000 | 1.000 |
| 1e-4 | 20k | 46 | 40 | 0.991 | 1.753 |
| 1e-4 | 200k | 46 | 37 | 0.857 | 1.000 |
| 1e-4 | 1M | 46 | 36 | 0.841 | 1.840 |
| 1e-5 | 2k | 37 | 37 | 1.000 | 1.000 |
| 1e-5 | 20k | 37 | 37 | 1.000 | 1.000 |
| 1e-5 | 200k | 37 | 37 | 1.000 | 1.000 |
| 1e-5 | 1M | 37 | 36 | 1.023 | 1.840 |

**Of the 37 actions with reference probability below 1e-5, 36 are still
unsampled at one million draws.** A 500× increase in budget resolved exactly
one of them. `1/√draws` is 0.001 there and says nothing about it.

---

## 3. Zero wins

75 (state, arm, draw-count) combinations produced zero Monte-Carlo wins for an
action whose reference probability is positive **and above the 1e-12 numerical
resolution floor**. A further 128 produced zero wins for actions whose reference
underflowed, where neither estimator resolves the action and the row is refused
in both directions rather than believed in either.

The list includes states from the actual four-arm harness run:

| state | arm | draws | reference p | one-sided 95% upper bound |
| --- | --- | --- | --- | --- |
| `harness-after-10000-decisions/K=4` | b | 2k | 2.44e-04 | 1.5e-03 |
| `harness-after-10000-decisions/K=4` | c | 2k | 1.64e-04 | 1.5e-03 |
| `harness-after-10000-decisions/K=4` | d | 2k, 20k | 6.37e-05 | 1.5e-03, 1.5e-04 |
| `harness/K=8/@251` | arm07 | 2k | 2.14e-03 | 1.5e-03 |
| `late-stage-concentrated/K=8` | arm01 | 2k, 20k | 1.49e-05 | 1.5e-03, 1.5e-04 |

Every one is labelled `MC_ZERO_WINS`, never `TRUE_ZERO_PROPENSITY`, reports the
exact Clopper–Pearson upper bound `1 - α^(1/n)`, and is refused as an IPS
denominator. No floor is substituted.

---

## 4. Independent Thompson calibration

Weights are `π_reference(a) / π_MC(a)` on the logged action, over the first
2 000 decisions of the 10 000-decision log. The two probabilities come from
estimators sharing no code and no random stream, so the weights are 1 only to
the extent the Monte-Carlo reconstruction is correct.

| draws | n | mean w | median w | min w | max w | p95 \|w−1\| |
| --- | --- | --- | --- | --- | --- | --- |
| 2 000 | 2000 | 1.001857 | 0.999755 | 0.813 | **2.559** | 0.00741 |
| 20 000 | 2000 | 1.000223 | 1.000342 | 0.800 | 1.261 | 0.00269 |

Reversed (`π_MC / π_reference`):

| draws | n | mean w | median w | min w | max w | p95 \|w−1\| |
| --- | --- | --- | --- | --- | --- | --- |
| 2 000 | 2000 | 0.999534 | 1.000244 | **0.391** | 1.230 | 0.00736 |
| 20 000 | 2000 | 0.999897 | 0.999658 | 0.793 | 1.251 | 0.00269 |

Both directions converge toward 1 at the same rate, so the deviation is a
property of the Monte-Carlo estimate rather than of which side of the ratio it
sits on. At 2 000 draws a single row's denominator is wrong by a factor of 2.6.

By contrast, the identical-estimator configuration (Monte-Carlo candidate against
Monte-Carlo logging propensities, same draws and seed):

```
IMPLEMENTATION_SANITY_ONLY: n=200  min w = 1.000000  max w = 1.000000
```

Every weight exactly 1, by construction, at the same draw count where the
independent check shows a 2.6× error. This is why the old self-evaluation proved
nothing.

---

## 5. Importance-weight sensitivity

For candidate probability `c`, reference propensity `p` and estimate `p̂`, the
relative weight error `|c/p̂ − c/p| / (c/p)` reduces to `|p − p̂| / p̂` — a
property of the denominator alone. At 2 000 draws over 2 000 decisions:

| reference p band | n | mean rel weight err | p95 | max | max true weight (uniform) |
| --- | --- | --- | --- | --- | --- |
| p ≥ 0.1 | 1973 | 0.0018 | 0.0049 | 0.101 | 2.29 |
| 0.01 ≤ p < 0.1 | 17 | 0.0796 | 0.169 | 0.181 | 24.5 |
| 1e-3 ≤ p < 0.01 | 9 | **0.582** | **1.190** | **1.559** | **228.3** |
| 1e-4 ≤ p < 1e-3 | 1 | 0.027 | — | — | **486.7** |

The instability regime is unambiguous: below p ≈ 0.01 the denominator error and
the weight magnitude blow up together. A 9-row band carries weights up to 228
whose denominators are themselves wrong by up to 156%. These are exactly the
rows that dominate an IPS estimate, and exactly the rows Monte Carlo cannot
resolve.

---

## 6. Uniform diagnostic (10 000 decisions, 0.8/0.6/0.4/0.2)

Logging-policy arm counts: `a=9916 b=36 c=35 d=13`. The exact uniform-policy
value is 0.5.

| propensity source | IPS | SNIPS | ESS | ESS/N | max weight | usable | zero-win | low-prec | min usable p |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| numerical reference | 0.4360 | 0.5126 | **17.1** | **0.0017** | **1417.60** | 10000 | 0 | 0 | 1.76e-04 |
| Monte Carlo 2k | 0.2035 | 0.7997 | 9582.3 | 0.9629 | 1.52 | 9952 | 3 | 45 | 0.1645 |
| Monte Carlo 20k | 0.2067 | 0.7867 | 6387.0 | 0.6400 | 10.40 | 9980 | 0 | 20 | 0.0241 |

Bootstrap on the reference denominators: SE 0.1105, 95% CI **[0.2627, 0.7143]**.
Propensity sensitivity (SNIPS spread across sources): **0.2871**.

**Verdict: `NOT_RANKABLE`, `POOR_SUPPORT_AND_PROPENSITY_UNCERTAIN`.** Gates
failed: ESS 17.1 < 200; ESS/N 0.0017 < 0.10; max weight 1417.6 > 50; sensitivity
0.2871 > 0.02.

The decomposition matters here. With exact reference denominators — where
propensity accuracy is not in question — uniform still has an effective sample
size of **17 out of 10 000**. That is a pure overlap failure: a Thompson logger
that plays arm `a` 99.2% of the time simply does not tell you what uniform would
have earned. More draws cannot fix it.

And the Monte-Carlo denominators actively **hide** it. At 2 000 draws the same
candidate reports ESS 9 582 and max weight 1.52 — near-perfect overlap for a
policy that has almost none. The mechanism is that Monte Carlo cannot resolve
anything below roughly `1/draws`, so the smallest propensity it will report is
0.1645 rather than the true 1.76e-04; the thousand-fold weights that reveal the
overlap failure are truncated away, and the rows carrying them are refused,
which biases the survivors further. **The old diagnostic would have reported
this candidate as well-supported.**

Note also that the reference IPS (0.4360) and SNIPS (0.5126) bracket the true
0.5 while the Monte-Carlo IPS (0.20) is nowhere near it. That is not a reason to
trust the reference estimate: with ESS 17 the interval spans [0.26, 0.71] and
the point estimate carries no information. Recovering 0.5 was never required.

---

## 7. Greedy diagnostic (same log)

| propensity source | IPS | SNIPS | ESS | ESS/N | max weight | usable |
| --- | --- | --- | --- | --- | --- | --- |
| numerical reference | 0.8033 | 0.8032 | 9859.4 | 0.9859 | 3.02 | 10000 |
| Monte Carlo 2k | 0.8070 | 0.8032 | 9859.0 | 0.9907 | 2.98 | 9952 |
| Monte Carlo 20k | 0.8049 | 0.8032 | 9858.5 | 0.9878 | 3.03 | 9980 |

Bootstrap on reference denominators: SE 0.0038, 95% CI [0.7963, 0.8110].
Propensity sensitivity: **2.0e-05**.

**Verdict: `RANKABLE`, `GOOD_SUPPORT_GOOD_PRECISION`.** Greedy's high ESS is
genuine: it survives replacing the Monte-Carlo denominators with the exact
reference, and SNIPS agrees to four decimal places across all three sources.

This is a rankability verdict, not a superiority claim. Greedy-posterior-mean
selects arm `a` on nearly every logged state, which is what the Thompson logger
already did 99.2% of the time — the two policies are close to identical on this
log, which is exactly why the overlap is excellent. A high ESS against a logger
you nearly are is not evidence of being better than anything.

---

## 8. Ranking

```
1. greedy-posterior-mean-v1          SNIPS=0.8032  ESS=9859.4
2. thompson-numerical-reference-v1   SNIPS=0.8028  ESS=9951.9
3. exact-thompson-v1                 SNIPS=0.8011  ESS=9991.4
note: uniform-v1 NOT_RANKABLE (POOR_SUPPORT_AND_PROPENSITY_UNCERTAIN) — excluded
winner: greedy-posterior-mean-v1
loser:  exact-thompson-v1
```

`uniform-v1` cannot be named winner or loser despite being the candidate whose
point estimate is closest to a known truth. The three ranked candidates separate
by 0.002 against a bootstrap SE of 0.004, so the ordering is inside its own error
bars and the ranking layer says so.

Self-paired cells are marked `SANITY` in the per-candidate tables and excluded
from gating: `thompson-numerical-reference-v1` against reference denominators
gives ESS exactly 10 000.0 and max weight exactly 1.00, and
`exact-thompson-v1` against matching-seed Monte-Carlo denominators gives ESS
9 952.0 and max weight exactly 1.00. Both are tautologies. Each candidate is
gated on its first estimator-independent denominator instead.


---

## 9. Full draw ladder (300 decisions, reference / 20k / 200k / 1M)

The 10 000-decision diagnostic in §6–§8 was run at reference / 2k / 20k. The
200 000- and 1 000 000-draw levels of the per-decision ladder were run on a
300-decision log instead, because reconstructing 10 000 decisions at 10⁶ draws
costs about an hour of saturated CPU and the host could not supply it (§11).
Logging arm counts on this log: `a=232 b=32 c=30 d=6`.

### Reconstruction

| propensity source | usable / 300 | refused | smallest usable propensity |
| --- | --- | --- | --- |
| numerical reference | 300 | — | 0.008681 |
| Monte Carlo 20k | 296 | 4 `LOW_PRECISION` | 0.024050 |
| Monte Carlo 200k | 300 | — | 0.008940 |
| Monte Carlo 1M | 300 | — | **0.008674** |

At 10⁶ draws the smallest reconstructed propensity is 0.008674 against a
reference of 0.008681 — 0.08% relative error. This log's rarest logged action is
not rare enough to defeat a million draws; the 10 000-decision log's is
(smallest reference propensity there: 1.76e-04).

### Independent Thompson calibration (`π_reference / π_MC`, 150 decisions)

| draws | mean w | median w | min w | max w | p95 \|w−1\| |
| --- | --- | --- | --- | --- | --- |
| 20 000 | 0.998088 | 1.000673 | 0.9166 | 1.0209 | 0.024283 |
| 200 000 | 0.999601 | 0.999447 | 0.9606 | 1.0114 | 0.006478 |
| 1 000 000 | **0.999976** | 0.999577 | 0.9947 | 1.0120 | **0.003341** |

Reversed (`π_MC / π_reference`): 0.024888 → 0.006436 → 0.003330 at the same draw
counts. Both directions fall monotonically toward zero deviation, and the mean
weight converges on 1 to five decimal places. The identical-estimator
configuration on the same log gives min = max = 1.000000 at every draw count,
which is why it distinguishes nothing.

### Candidates across the full ladder

**uniform-v1**

| propensity source | IPS | SNIPS | ESS | ESS/N | max weight |
| --- | --- | --- | --- | --- | --- |
| numerical reference | 0.4732 | 0.4779 | 30.3 | 0.1010 | 28.80 |
| Monte Carlo 20k | 0.3801 | 0.5698 | **83.0** | 0.2805 | **10.40** |
| Monte Carlo 200k | 0.4708 | 0.4792 | 31.1 | 0.1036 | 27.96 |
| Monte Carlo 1M | 0.4737 | 0.4773 | 30.2 | 0.1007 | 28.82 |

This is the mechanism from §6 caught mid-collapse. At 20 000 draws the
Monte-Carlo denominators still report 2.7× the true effective sample size and
hide a third of the true maximum weight. At 200 000 and 1 000 000 they land on
the reference. The correction happens once the draw budget exceeds `1/p_min`,
which for this log is about 115 draws' worth of resolution — and for the
10 000-decision log would need well over 10⁶.

Verdict unchanged: **`NOT_RANKABLE`**, ESS 30.3 < 200 and sensitivity 0.0924 >
0.02 (the spread is driven entirely by the 20k outlier).

**greedy-posterior-mean-v1**

| propensity source | IPS | SNIPS | ESS | ESS/N | max weight |
| --- | --- | --- | --- | --- | --- |
| numerical reference | 0.7654 | 0.7673 | 210.7 | 0.7023 | 3.02 |
| Monte Carlo 20k | 0.7769 | 0.7674 | 210.4 | 0.7107 | 3.03 |
| Monte Carlo 200k | 0.7652 | 0.7673 | 210.8 | 0.7025 | 3.02 |
| Monte Carlo 1M | 0.7652 | 0.7673 | 210.7 | 0.7023 | 3.01 |

SNIPS identical to four decimals across every estimator, sensitivity 3.8e-05.
**`RANKABLE`**, and its overlap is estimator-independent — which is the property
uniform lacks.

Ranking: greedy (0.7673) > thompson-reference (0.7234) > exact-thompson
(0.7160); uniform excluded as `NOT_RANKABLE`.

---

## 10. Verification

```
go vet ./...                    clean
go test ./...                   all packages ok
go test -race -count=1 ./...    all packages ok
```

New test coverage, by claim:

| claim | test |
| --- | --- |
| 2 identical Beta(1,1) arms give 0.5 each | `TestReferenceTwoIdenticalArmsIsHalfEach` |
| 4 identical arms give 0.25 each | `TestReferenceFourIdenticalArmsIsQuarterEach` |
| K identical arms give 1/K, K ∈ {2,4,8,16}, three shapes | `TestReferenceSymmetryAcrossArmCounts` |
| the integral matches hand-computable Thompson probabilities | `TestReferenceMatchesClosedForm` |
| probabilities sum to 1 before normalization | `TestReferenceSumsToOneBeforeNormalization` |
| reference stays finite for concentrated posteriors | `TestReferenceFiniteForConcentratedPosteriors` |
| high-draw Monte Carlo agrees within 6σ of the binomial SE | `TestHighDrawMonteCarloAgreesWithReference` |
| Monte-Carlo error actually shrinks with draws | `TestMonteCarloConvergesTowardReference` |
| a positive-probability arm with zero wins is `MC_ZERO_WINS` | `TestZeroWinsIsLabelledNotTreatedAsZeroProbability` |
| IPS refuses an `MC_ZERO_WINS` denominator | `TestIPSRefusesMCZeroWinsRows` |
| the reference resolves what Monte Carlo refuses, at any budget | `TestReferenceResolvesWhatMonteCarloRefuses` |
| `1/√draws` is not an accuracy bound for rare actions | `TestDrawCountAloneIsNotAnAccuracyBound` |
| calibration weights converge toward 1 with draws | `TestIndependentCalibrationWeightsConvergeTowardOne` |
| identical-estimator self-evaluation is `IMPLEMENTATION_SANITY_ONLY` | `TestIdenticalEstimatorSelfEvaluationIsLabelledSanityOnly` |
| uniform's low ESS makes it `NOT_RANKABLE` | `TestUniformIsNotRankableFromOverlap` |
| uniform stays `NOT_RANKABLE` even with exact propensities | `TestUniformStaysNotRankableEvenWithPerfectPropensities` |
| greedy is rankable only under independent-reference diagnostics | `TestGreedyOverlapIsGenuineUnderReferencePropensities` |
| the ranking layer cannot select a `NOT_RANKABLE` candidate | `TestRankingCannotSelectNotRankableCandidate` |
| overlap failure and precision failure are classified apart | `TestLowESSIsNotRankableRegardlessOfPointEstimate`, `TestPropensityFailureIsDistinctFromOverlapFailure` |
| numerical integration stays off the online path | `TestRouterDoesNotImportNumericalIntegration` |
| the incomplete beta dependency is right | `TestBetaCDFAgainstBinomialIdentity` |

---

## 11. Remaining limitations

1. **The reference describes exact-Thompson argmax only.** `UCBRegularized`,
   `PhasedSelection` and the approximate samplers have different action
   distributions. `propensity.CheckLoggingPolicy` refuses those logs rather than
   silently mis-reconstructing them, so decisions logged under them are
   `OPE_INELIGIBLE` and no candidate can currently be evaluated on them.

2. **Sub-unit Beta shapes are the weakest case.** `Beta(0.5, ·)` has an
   integrable `x^-1/2` singularity where the local `|K15 − G7|` indicator
   understates the true error; those states report `LOW_PRECISION` rather than a
   number with an untrustworthy error bar. They are reachable through discounted
   warm-start priors (`thompson/warmstart.go:166`) but do not occur in any log
   examined here. Worst observed effect: a 7.8e-07 partition-of-unity residual.

3. **Probabilities below ~1e-300 underflow.** 128 of the matrix's zero-win
   actions have references below the 1e-12 resolution floor. Those actions are
   refused rather than believed in either direction. Resolving them would need
   an extended-range integrand, which no importance weight here would survive
   anyway.

4. **The per-decision draw ladder is split across two logs.** §6–§8 use the full
   10 000-decision log at reference / 2k / 20k; §9 uses a 300-decision log for
   the 200k and 1M levels. Reconstructing 10 000 decisions at 10⁶ draws is about
   an hour of saturated CPU and the host was memory-thrashing. The 1M level is
   fully exercised on the 132-state posterior matrix (976 arm-observations),
   which is where the rare-action evidence lives; what is not measured is the 1M
   column of the 10 000-decision candidate table specifically.

5. **Greedy is rankable, not better.** It plays the same arm the Thompson logger
   played on 99.2% of logged states, so its excellent overlap is near-tautological
   and its SNIPS sits within the bootstrap interval of the other two ranked
   candidates. No superiority claim is made or supported.

6. **Rankability is not significance.** The gates certify that an estimate is
   trustworthy enough to compare. On the 10 000-decision log the three ranked
   candidates separate by 0.002 against a bootstrap SE of 0.004; `Rank` reports
   that the ordering sits inside its own error bars.

7. **Bootstrap intervals resample rows, not propensities.** They carry the
   sampling variability of the log, not the reconstruction uncertainty of the
   denominators. That second source is reported separately as the propensity
   sensitivity, and gated separately.

---

## Verdict

**PROPENSITY_GROUND_TRUTH_PASS**

- Thompson propensity reconstruction is validated against an estimator that
  shares no code and no random stream with it, and against closed forms.
- Zero Monte-Carlo wins cannot become false zero support: `MC_ZERO_WINS` carries
  an upper bound, is refused as a denominator, and no floor is invented.
- The system knows when propensity precision is insufficient: five per-action
  statuses, gated on win count and interval width rather than draw budget.
- Overlap failure is distinguished from propensity error, with disjoint remedies
  and a demonstration (§6, §9) that the Monte-Carlo denominators were previously
  hiding a real overlap failure.
- Unsupported candidates cannot be ranked: uniform is refused despite the point
  estimate closest to a known truth.
- No online Thompson behaviour changed.
