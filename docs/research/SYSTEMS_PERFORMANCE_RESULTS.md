# Systems Performance Results (Phase 5)

## Learner scaling (darwin; Linux pending)
| n | minimal/op | logged/settle | full/settle |
|---|---|---|---|
| 1,000 | 745ns | 4.60ms | 4.15ms |
| 10,000 | 483ns | 4.38ms | 5.52ms |
| 100,000 | 574ns | — (linear, capped) | — |
| 1,000,000 | 657ns | — | — |

Minimal state is O(1) (0KB growth to 1M). fsync dominates both durable
variants; policy math is ~10^4× below persistence. Distributions across
repeated trials: sub-microsecond stability for minimal; fsync variance
dominates durable paths (see p50/p95/p99 in the Linux fault suite:
110/s, p50 139ms, p95 195ms, p99 270ms at 16-way concurrency).

## Monitor scaling
Index-based fold: 0.38ms @1k, 7.9ms @10k, ~150ms @100k per settlement
(darwin; differential proof vs the original fold). Envelope: comfortable
to ~10k outcomes/ledger; windowed projection specified, deferred.

## Gateway decision path (darwin)
13–17ms quiet (fsync'd decision + evidence commits), 55ms observed on a
box at load 100+ (guardrail at 250ms documents the variance); checkpoint
~19ms; recovery sub-millisecond; ~1.2KB/decision, ~0.6KB/outcome.

## Linux / fault results
Per LINUX_RELIABILITY_REPORT.md (build/vet/units/faults/actual-binary e2e
green; disk-full refusal proven; read-only proven on darwin non-root).

## Sustained-load ceiling
Not established beyond the above: no run was pushed to a documented
bottleneck. The fsync-per-commit design implies the ceiling analytically
(~1/fsync-latency settles/s single-writer), but an arrival-rate sweep to
refusal is UNMEASURED — recorded as a gap, not extrapolated.
