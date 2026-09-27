# Systems-Correctness Results (Phase 4)

## S1 replay equivalence
Uninterrupted learning, interrupted+resume (no checkpoint), and fresh full
rebuild over 20 versioned outcomes produce byte-identical checkpoints and
identical subsequent decisions under controlled RNG (3 seeds). Divergence:
zero. Metric recovery-state divergence = 0 across all paths.

## S2 crash-boundary matrix
| Boundary | Classification |
|---|---|
| Before reservation | nothing authoritative; re-runs cleanly |
| Reservation→commit crash | budget forgiven on recovery (replay counts committed only); safe direction, pinned by R2 tests |
| Decision commit failure | fail-closed, no dispatch (tested: 503 commits nothing) |
| Dispatch ambiguity (timeout) | unresolved by design; runner re-runs, ledger never fabricates; duplicate provider calls possible — exactly-once dispatch UNSUPPORTED (documented, synthetic-safe) |
| Outcome commit | atomic submit-then-apply; duplicates idempotent; gaps/conflicts rejected |
| Learner/book skew (book refused post-submit) | heals via deterministic ledger rebuild (R3 test) |
| Safety transition | event-atomic; store failure fails closed to fallback-or-nothing |
| Checkpoint publication | atomic tmp+rename; corrupt quality checkpoint refuses; corrupt cost sidecar cannot block (audit-only) |
| Harness re-execution | DUPLICATES assignment rows (2 rows/1 job): scoped finding — harness fixtures are single-pass by contract; the exp-run runner dedups via assigned-set. No silent learning corruption (join is by job). |

Silent divergence detected: none. Fail-open transitions: none found.

## S3 corrections and duplicates
Seven delivery permutations (duplicates, reversals, stale redelivery)
converge to authoritative-history posteriors exactly. Duplicate learning
updates: zero. Incorrect attribution: zero (attribution gate tested).

## S4 durability ablation (darwin, fsync-backed)
| Layer | per-settle | bytes/job | recovery |
|---|---|---|---|
| minimal (memory) | ~50ns | 0 | NONE |
| logged (append+fsync) | ~4.5ms | ~39B | replay only (redelivery double-learns — proven) |
| full (store+learner+evidence) | ~5.5–6.4ms | ~468B+evidence | checkpoint+replay+corrections |

Correction safety costs ~2ms/settle over plain logging; persistence costs
~10^5× policy math. Checkpoint 633µs/3.7KB at n=200.

## S5 safety-state recovery
Suspension + reason + mask survive controller rebuild; committed
exploration budgets replay exactly; operator transitions persist across
crash (emergency stop/release tested end-to-end).
