# Adaptive Economics Results (Phase 6)

Method: four treatments (T0 fixed, T1 dev-picked cheap static, T2
cost-blind Thompson, T3 supervised cost-aware) over held-out eval seeds
(seed 101, n=160), independent assignment/policy RNG, frozen analyzer
(floor 0.3, bootstrap 200). Dev static picked on dev seeds only (cheap,
$0.0020). All verdicts from refusal-gated comparison; point estimates
reported alongside, never as ranks.

## Scenario outcomes (cost per verified success, primary metric)
| Scenario | T3 vs T0 | T3 vs T1 | T3 vs T2 | Verdict |
|---|---|---|---|---|
| equal-cost-gap | −0.072 win | +0.001 tie | −0.030 win | INCONCLUSIVE |
| quality-gap | −0.049 win | +0.002 tie | −0.032 win | INCONCLUSIVE |
| static-matches | −0.061 win | +0.001 tie | −0.012 win | INCONCLUSIVE |
| deterioration | −0.059 win | +0.001 tie | −0.032 win | INCONCLUSIVE |
| drift | −0.019 – | −0.001 – | −0.015 – | INCONCLUSIVE (all ties) |
| missing-costs | −0.017 – | **+0.056 loss** | −0.027 – | NOT_RANKABLE |
| heavy-tail | **+0.071 loss** | −0.012 – | −0.072 – | INCONCLUSIVE |
| human-trap | +0.010 – | NaN – | +0.000 – | NOT_RANKABLE |

(− = cheaper; + = pricier; wins require CI exclusion + validity + effect.)

## Reading
- T3 consistently beats the expensive fixed baseline and usually beats
  cost-blind Thompson on cost — while riding much cheaper, lower-quality
  arms (quality-gap: T3 accept 0.42 vs T2 0.77 at floor 0.3). The floor
  choice, not the optimizer, decides the quality outcome. Reported, not
  hidden.
- T3 never beats the competent cheap static by a supported margin
  (diffs +0.001–0.002, never wins): adaptation adds exploration overhead
  and no advantage when the static is already optimal (static-matches).
- Nonstationarity (drift) defeats all four equally at this horizon.
- Missing costs strand the learner (24×-class loss vs T1 in the
  adversarial profile; +0.056 here) and the gate refuses rankability.
- Heavy tails punish learned cheap preference (+0.071 vs fixed).
- Human-trap refuses rankability (NaN contained, never a win).

## Required outputs (per scenario in test logs)
Cost/verified-success and cost/assigned (report treatments), accept rates
with bootstrap CIs, floor compliance (gates), exploration counts
(decision fallback flags), missing/censored rates (gates), adaptation
delay (deterioration windows: full commitment ~80 jobs post-collapse, then
exclusion), safety-intervention frequency (suspension/cascade tests).
Operating overhead included: evidence/decision bytes per job (S4) are part
of the measured system cost; verification/human costs are scenario truth.

## Negative findings retained
No scenario supports a rankable T3-over-T1 claim at n=160; two scenarios
show T3 materially worse than a static; drift shows universal ties. A
sweep where T3 always won would have indicted the baselines.
