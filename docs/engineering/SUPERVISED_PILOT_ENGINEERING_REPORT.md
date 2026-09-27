# Supervised Pilot Engineering Report (V1)

## Base and branch
- Base: `review/cost-aware-validation-v1` = `bf8822d` (verified pre-change:
  race 9/9 in pilot worktree before edits).
- Branch: `feat/supervised-pilot-v1` (isolated worktree
  `~/Documents/thompson-pilot`; no main contact; no merge/deploy/traffic).
- Dependencies: validation head contains the full implementation (no extra
  dependency); correctness stack already on main (nothing duplicated).

## What was built (commits on the branch)
1. `6702df6` gateway integration: SelectionPolicy interface, RuleV3
   eligibility mask + combined hash (validated extension, tested), wrapper
   with reserve/re-pick loop, bidirectional policy/book binding, evidence
   fields, settlement ValidateCosts + book apply + recovery rebuild.
2. `6d8465a` safety controls: SafetyController, durable event log,
   windowed monitor, operator endpoints, binary wiring (COSTAWARE=1).
3. `9c32959` missing-cost/exploration/human-review safety + monitor
   deduplication onto the shared counting rule + human-burden visibility.
4. `1023a84` runner phantom-decision refusal + supervised 4-treatment
   experiment (real binaries, demo stop/release, idempotent resume).
5. `336fae4` log durability (torn-tail), trust boundaries, legacy-mode
   COSTAWARE refusal fix.
6. This commit: perf harness, ops/runbook docs, this report.

## Confirmed fixes vs pre-fix reproductions
- Phantom decisions: pre-fix, fail-closed 503s settled 404s corrupting the
  join (reproduced at supervised job-00037); post-fix, loud refusal aborts
  visibly with intent-to-treat rows intact (gateway + runner tests).
- COSTAWARE-in-legacy half-enablement: refused by construction (test).
- Monitor blind spots: human-fixed jobs unattributed (now HumanFixed/Cost
  visible); third cost-rule copy eliminated (parity-tested shared helper).
- Exploration accounting: budgets replay from decisions ledger; exhaustion
  suspends; double-suspension fails closed 503 with zero committed rows.
- Cold-starvation finding (documented, not changed): once any arm is
  cost-known, cost-unknown arms are unreachable by design (conservative);
  new arms enter via operator suspension of the dominant arm (runbook).

## Measured behavior (frozen seeds, synthetic only)
- Deterioration: monitor suspends 5 post-collapse jobs after establishment;
  reroutes to fallback; budget cascade then fails closed; no auto-reenable.
- Missing-cost blackout: established arm going dark suspends on budget.
- Human trap: 0 picks while honest arm available; forced trap-only →
  budget exhaustion → 503 with $12 burden visible, NOT_RANKABLE-class end.
- Supervised e2e (60 jobs, real binaries): artifacts complete, t3 RuleV3
  identities, stop/release events, fallback routing, resume outcome-stable.
- Perf (darwin): decision 13.2ms, monitor 46µs/arm, checkpoint 19ms,
  recovery 389µs, ~1.2KB/decision, ~0.6KB/outcome.

## Properties enforced / assumed / unsupported
- Enforced: prequalified-only traffic, floor-gated optima (RuleV2 mean),
  bounded exploration with durable accounting, missing-budget suspension,
  fail-closed empties/store failures, atomic malformed-cost refusal,
  correction-safe dual rebuild, operator-only resume (auth+reason+config).
- Assumed (statistical, documented non-guarantees): monitor thresholds are
  heuristics with measured delay (~5 jobs at minObs 6–8 in fixtures;
  ~80-job posterior inertia upstream); accept rates estimate, never prove.
- Unsupported: multi-replica operation, load ceilings, Linux constants,
  live disk-full injection, customer traffic authorization.

## Compatibility and charters
Cost-blind behavior byte-identical (full suites green, no fixture edits);
frozen PR3B + cost-aware charters untouched; outcome schema v1 intact;
OPE gates auto-refuse the cost-aware identity as a Thompson denominator.

## Verification note (port interference, not a regression)
Final race: 9/9 ok. Go 1.22: 5/6 packages ok on the joint run with one
failure (TestE2ECrashAfterAssignment, 31s ledger-poll timeout) caused by
running the race and Go-1.22 suites SIMULTANEOUSLY: both processes'
exp-run binaries bind the same nextPortBases ports, so one run's gateway
answered another run's health/ledger polls. Re-ran the test alone under
Go 1.22: pass (8.7s). Process-level lesson, already true before this
mission: run binary-port suites serially across processes.

## Blockers and risks
Branch protection still disabled (local hook only) — operational blocker,
unchanged. Safety log lacks flock (single-writer by deployment; noted).
Budget sizing is per-experiment (runbook formula); undersizing aborts runs
loudly. Human-heavy workloads trip the evaluator's missing-cost gate by
(frozen) design — needs a customer decision before such pilots.

## Real-workload readiness
Technically ready for a SUPERVISED representative-workload pilot under the
runbook (protection on, alarming staffed, prequalified arms, sized budgets).
Not production, not autonomous, no commercial claims; all numbers synthetic.
