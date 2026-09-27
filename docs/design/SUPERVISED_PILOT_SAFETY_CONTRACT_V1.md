# Supervised-Pilot Safety Contract V1

## Authority boundary
Adaptive SELECTION proposes; MONITORING observes; the OPERATOR disposes.
No selection path may suspend, resume, expand eligibility, or loosen a
budget by itself. Monitoring may only SUSPEND (never resume, never approve).
Only an authenticated operator action resumes, and only to a previously
approved state. Every transition is persisted with actor, reason, policy
version, and config hash before it takes effect.

## Estimates vs guarantees
All quality figures are statistical ESTIMATES over independently verified,
matured outcomes (posterior means, observed rates). None is a guarantee of
the true success probability. Suspension thresholds are heuristics with
measured false-alarm behavior, not proofs. The contract enforces RESPONSES
to estimates (suspend, fallback, stop); it never claims an arm MEETS the
floor, only that the system stops preferring arms whose estimates breach it.

## Selection states
- PREQUALIFIED: operator-approved for this workload, zero exposure yet.
  Entry: approval record (arm, workload, approver, time, config hash).
- COLD_EXPLORATION: prequalified arm with quality pulls < ColdStartPulls,
  selected while exploration budget remains. Every such pick is flagged,
  counted against the durable per-arm budget, and recorded.
- QUALIFIED: pulls >= ColdStartPulls, posterior mean >= floor, cost known
  (metered_n >= MinMeteredN), missing-cost share within budget, not
  suspended. Only QUALIFIED arms may be genuine cost-aware optima.
- SUSPENDED: excluded from all adaptive selection (optimum AND fallback
  pools) after a monitor breach, budget breach, or operator stop. Traffic
  routes to the approved fallback. Exit requires operator-authorized resume.
- OPERATOR_DISABLED: approval withdrawn. Stronger than SUSPENDED: return
  requires full re-approval (→ PREQUALIFIED with pulls preserved for audit,
  but exploration budget NOT refunded).

## Transitions and triggers
| From | To | Trigger | Authority |
|---|---|---|---|
| — | PREQUALIFIED | approval record | operator |
| PREQUALIFIED | COLD_EXPLORATION | selected while cold + budget left | policy (flagged) |
| COLD_EXPLORATION | QUALIFIED | pulls + mean + cost + budget gates pass | automatic (estimates) |
| any adaptive | SUSPENDED | quality breach / missing-budget breach / emergency stop | monitor / operator |
| SUSPENDED | QUALIFIED | authorized resume (auth + reason + frozen-config match) | operator |
| any | OPERATOR_DISABLED | approval withdrawn | operator |
| OPERATOR_DISABLED | PREQUALIFIED | re-approval | operator |

Recovery: all state in the durable safety store; restart replays it before
serving traffic. Unauthorized/accidental resume (bad credential, config
mismatch, missing reason) is rejected and logged. Automatic re-enablement is
forbidden in code (no path exists).

## Fallback
Every workload names one approved static fallback arm. Suspension routes
subsequent eligible jobs there with fallback identity recorded. If no safe
fallback exists (fallback suspended/disabled/unconfigured), selection fails
CLOSED: 503, no decision committed, nothing dispatched. Fallback jobs stay in
the intent-to-treat analysis (never discarded).

## Frozen vs tunable mid-run
Frozen (change restarts the experiment): arm set, quality floor, cost
objective config, missing-cost budgets, exploration budget, fallback arm,
monitoring windows/thresholds, charter digests. Tightening allowed live:
suspend arms, cut budgets (to >= spent), emergency stop. Loosening of any
kind (new arms, lower floor, bigger budgets) is rejected by config-match on
resume and requires a new experiment.

## Emergency procedures
STOP: authenticated `POST /v1/operator/suspend` (arm or all) → persisted
SUSPEND event → immediate effect → survives restart. RESUME: authenticated
`POST /v1/operator/resume` with reason; checks auth, frozen-config match,
and (for DISABLED) re-approval; every attempt (granted or rejected) is
logged. Operator credential (`OPERATOR_TOKEN`) is distinct from the
settlement token; either may be rotated by restart with new env.
