# Supervised Experiment Integrity (Phase 6)

## Design under test
Four treatments through real gateway binaries (fixed, cheap-static,
cost-blind Thompson, supervised cost-aware Thompson with safety
controller). Frozen manifest (seed-pinned, deterioration + drift thirds)
and frozen safety envelope. Intent-to-treat: assignment rows persist
before execution; fallback/refused jobs are never discarded from analysis.

## Intervention accounting
- Assigned treatment (assignment ledger) vs executed strategy (decision
  ledger: `SelectedArmID` + cost-aware identity/fallback flags) vs
  deciding attempt (outcome tape) are three separate facts joined by
  jobmap for reporting. Suspension changes execution, never assignment.
- Safety events (suspensions, operator actions, budget exhaustion) live in
  `t3/safety.jsonl`; operator demo stop/release verified present.
- Refusals (503, no safe arm) abort the job visibly with assignment +
  attempted rows persisted; nothing is settled for them.

## Crash coverage (proven by TestSupervisedIntegrityUnderIntervention)
Real subprocess SIGKILL crash mid-run (exit 3) during an active suspension
and drift onset, then in-process reboot + resume: assignments exactly once
with unchanged treatments, no duplicate outcome versions, suspension event
durable, post-resume decisions avoid the suspended arm, report renders over
all randomized jobs via the jobmap-joined path.

## Verdict behavior
Observed: NOT_RANKABLE with 3 comparisons, zero unsupported `MeetsBar`
claims — suspension, crash, drift, and missingness correctly refuse a
rankable conclusion instead of manufacturing one.
