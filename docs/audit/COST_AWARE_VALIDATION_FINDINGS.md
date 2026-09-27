# Cost-Aware Validation Findings (Phase 1)

Scope: actual code on `review/cost-aware-validation-v1` (= `7c3c4c9`).
Severity = safety impact on an experimental pilot. Each item names source,
scenario, severity, reproduction.

## F1 (High): sample crossing the floor is not satisfying the floor
`go/thompson/costaware.go:130-135,153-164`. Qualification tests the Thompson
SAMPLE (`scores[a] >= floor`), then minimizes cost among qualifiers as a
"genuine optimum". A posterior sample above the floor proves nothing about the
arm's true rate. Reproduction (`zz_scratch_test.go`, seed 100, cheap true
p=0.10, strong p=0.90, floor 0.50, 200 jobs): cheap selected 18/200 (9.0%)
with only 2 flagged fallbacks — 16 unsafe picks labeled optimal. Cold-start
(`pulls < ColdStartPulls`) bypass is intentional and bounded (~arms×pulls
jobs), but the post-evidence sample leak is not. The design doc's "posterior
mean (or Thompson sample, see below)" never resolves the ambiguity; the code
picked the unsafe reading.

## F2 (High): malformed cost partially applies learning
`go/harness/costaware.go:261-266`. `RunJob` settles (event submitted, quality
learner moved) BEFORE `Book.Apply`. A NaN/negative cost fails the book after
the quality update stands and the event is durably stored. Estimators diverge
on exactly the jobs that most need consistent handling. Fix: pre-validate
costs before `Settle` so refusal is atomic.

## F3 (Medium): cost book and report count different populations
`go/outcome/costbook.go:63-87` vs `go/harness/report.go:400-421` (`jobCost`).
The book excuses nil costs on `human-pool` attempts (cost lives in
`HumanReviewCostUSD`); the report counts every nil attempt cost as unmetered.
A human-reviewed job with recorded review cost is LEARNED by T3 but EXCLUDED
from the primary metric — selection optimizes over jobs the evaluator drops.
Fix on the experimental side only (evaluator frozen): align the book with the
report's conservative rule via one shared helper; observable report behavior
unchanged.

## F4 (Medium): config hash is weak and full config is not persisted
`go/harness/costaware.go:186-197`. `h*31` over platform `int`, cast to
uint32 — collision-prone, inconsistent with the sha256 `hashConfig` used on
the quality path. The "full config persisted alongside decisions" comment is
false: only the hash is written. Fix: sha256 hash + `config.json` at open.

## F5 (Medium): T3 has no checkpoint/resume wiring
`go/harness/costaware.go` has no Save/Resume; `SaveCostCheckpoint`
(`go/outcome/costbook.go:308-340`) lacks the directory fsync the learner's
checkpoint performs. "Restart reconstructs the same state" holds only for
replay-from-scratch, never crash-tested for T3. Fix: treatment checkpoint +
resume-from-ledger + crash test; add dir sync.

## F6 (Medium): experiment truth cannot express missing costs or unresolved jobs
`go/harness/treatment.go` (`JobTruth.ArmCost map[string]float64`, no behavior
flags). Costs are always metered; tapes never empty → no UNKNOWN. The
adversarial "missing-cost" scenario is a stub (comment admits it) and
censoring gates are unreachable at experiment level. Fix (evaluation-only,
nil-default fields): `MissingCostArms map[string]bool`, `Unresolved bool`,
honored by all three strategies; existing behavior bit-identical when unset.

## F7 (Low): known-without-mean arm silently dropped
`go/thompson/costaware.go:142-148`. `known[a]==true` with no `means[a]` entry
lands in neither `qualKnown` nor `qualUnknown` — excluded even from fallback
pools. Unreachable via current `costMaps` (sets both together) but a trap for
API callers. Fix: treat as cost-unknown (fallback pool), never drop.

## F8 (Info): selection and truth share one RNG stream
`go/harness/costaware.go:67-99`. Thompson samples and success draws consume
the same `rng`; deterministic given seeds, but any selection-path change
reshapes truth draws. Assignment RNG remains independent (requirement met).
Document only; no change (altering streams would invalidate all fixtures).

## Verified satisfactory (no action)
- Correction/duplicate/replay in `CostBookV1` faithfully mirrors `Learner`
  (idempotent duplicates, gap refusal, latest-per-job ledger-order rebuild).
- UNKNOWN/PENDING advance cursor without learning; missing costs never zeroed
  (fully-metered-only means + `UnmeteredN` counters).
- No silent objective switch: fallback always flagged with identity retained;
  cost-blind `LoggingPolicyID` unchanged (`exact-thompson-v1`).
- Report gates (quality floor, missing-cost, censoring, allocation,
  min-jobs) are independent of selection and correctly refuse rankability;
  cost-drift regression is exposed (`Wins=false`), not hidden.
- Additive-only delta: cost-blind policy, mappers, outcome schema v1,
  protocol fixtures, frozen charters untouched.

## Disposition
F1 → Phase 3 safety decision (rule fix + identity/version handling).
F2–F7 → Phase 4 narrow fixes, each with a failing-first regression test.
F8 → documented limitation.
