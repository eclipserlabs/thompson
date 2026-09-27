# Monitoring Correctness and Scaling (Phase 4)

## Population under monitoring
`armHealth` folds the same authoritative events the learner rebuilds from:
latest version per job (corrections supersede; duplicates collapse), newest-
first by Seq, last `MonitorWindow` outcomes attributed to the deciding arm.
Human-fixed jobs (armless deciding attempt) enter no arm window and move no
estimator; their burden is credited separately (`HumanFixed/HumanCostSum`)
within the same newest-first scan bounds. UNKNOWN/PENDING count as censored
and never enter any rate denominator. Delayed verification surfaces as
`AvgDelayH` plus thin matured counts: below `MonitorMinObs` nothing suspends
by estimates, and staleness is visible rather than healthy-looking.

## Human-correction verdict
Human-fixed jobs must NOT trigger quality suspension (they carry no arm
verdict), but they must NOT be invisible either. Current handling: visible
burden counters + exploration-budget dynamics (perpetual fallback spends
out, then suspension/refusal) + report gates. No silent redefinition of
verified success; the frozen floor is untouched.

## Scaling (darwin arm64, in-memory fold; approximate, machine-dependent)
| outcomes | fold/settlement (sort+copy) | fold/settlement (index-based) |
|---|---|---|
| 1,000 | 2.34ms | 0.38ms |
| 10,000 | 14.5ms | 7.9ms |
| 100,000 | 162ms | 154ms |

The index rewrite (no event copies, index sort, differential proof in
`monitor_equivalence_test.go` over corrections/duplicates/unknowns/human
legs/missing costs/shuffled order) removes the copy term but the sort still
dominates at scale. `Events()` additionally copies the full slice per call
(O(n) memcpy + lock hold).

## Envelope decision
Sustained single-writer operation is supported to ~10,000 outcomes per
treatment ledger (fold ≤ ~8ms, well under fsync-dominated settlement).
At 100k the fold alone costs ~150ms per settlement (~6 settles/s ceiling).
Beyond ~10k, the specified (not implemented) remedy is a newest-first
backward scan with early exit once each monitored arm fills its window,
valid under the store's Seq-order invariant with a tested unordered-input
fallback to the full fold. Deferred deliberately: pilot scale does not need
it, and the current fold is exactly equivalent to the validated original.
Ledger rotation past 100k belongs to the retention story (file-level
expiry per pilot-data-handling), not to this mission.
