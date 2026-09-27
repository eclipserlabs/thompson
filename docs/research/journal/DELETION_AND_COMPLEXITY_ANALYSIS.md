# Deletion and Complexity Analysis (Phase 5)

Method: exact file/function map of coordination machinery that a
transactional journal makes unnecessary, vs new responsibilities the
prototype introduces. Production code untouched; this is a map, not a diff.

## Deletable with a journal as the single authority
| Current machinery | Location | Why it disappears |
|---|---|---|
| Decision flock open/recover/torn-tail | gateway/decision_store.go (~240 lines) | SQLite locking + page atomicity; no torn rows exist |
| Outcome flock open/recover/torn-tail | outcome/store.go FileOutcomeStore (~230 lines) | same |
| Safety flock open/recover/torn-tail | gateway/safety_store.go (~160 lines) | same |
| Learner applied-cursor + genesis snapshot + checkpoint save/load/resume | outcome/learner.go Checkpoint/Resume (~120 lines) | replay from one log; cursor = max applied seq per job in one table |
| Cost-book applied map + snapshot/restore + checkpoint files | outcome/costbook.go (~90 lines) | same fold, same log |
| Harness cost-aware checkpoint/resume | harness/costaware.go SaveCheckpoint/Resume (~80 lines) | same |
| Assignment double-append guard | cmd/exp-run assigned-set (~30 lines) | idempotency-key table (UNIQUE constraint) |
| Cost checkpoint sidecar write/read | gateway/settle.go checkpoint path | audit-only artifact already; deleted outright |
| Budget re-scan over decisions ledger | gateway/safety.go recover (~25 lines) | exploration table maintained transactionally, verified by replay |
| Report jobmap join (manifest↔gateway IDs) | harness MatureJobsMapped + jobmap.jsonl (~50 lines) | single job identity end-to-end (no gateway-local IDs) |
| Total candidate deletion | | ~500 lines of locking, torn-tail recovery, cursors, checkpoints, and join logic (coordination only — decision/settlement business rules stay) |

## Partial-commit states that disappear
W3 (submit→apply skew), W4 (quality/cost skew), W6 (reserve/commit
divergence), torn-tail variants in 3 readers, checkpoint/ledger skew
(cost sidecar staleness). Proven absent by construction: one commit
sequence, transactional multi-row updates, page-atomic writes.

## Partial-commit states that REMAIN (unchanged by any representation)
- Dispatch without verification (external effect + time separation).
- Timeout ambiguity (no record can resolve it).
- Crash between assignment and first commit (resume re-runs; idempotent).
- Operator action sent but unacknowledged (retry with same nonce).

## Projections that get simpler vs costlier
Simpler: learner rebuild (one ordered scan vs three ledger formats +
cursor maps); budget accounting (table + verify vs decisions re-scan);
monitoring fold (SQL window query vs full sort — measured below).
Costlier: none identified at pilot scale; ad-hoc report queries over
100k+ rows need indexes (provided: kind/key, job/version) or the
windowed projection still applies.

## Remaining needs (not deletable)
Checkpoints (as cached snapshots for fast boot, optional), retention/
compaction (VACUUM + time-based DELETE, unimplemented — same gap as
today's file rotation), schema migration discipline (version refusal
implemented; migration tooling explicitly out of scope).

## New operational responsibilities (SQLite)
WAL checkpoint tuning (autocheckpoint threshold vs restart replay time);
busy-timeout policy (currently fail-fast: contention = error, callers
must handle); backup procedure (sqlite3 backup API vs file copy —
file copy of a live WAL db is NOT crash-safe without care);
corruption triage (page-level, rarer but less transparent than a bad
JSON line); driver dependency (pure-Go modernc, no cgo).

## Net assessment
The collapse is real, not moved complexity: ~500 lines of hand-rolled
coordination delete against a ~40-line schema plus ~20 parameterized
statements, with the embedded engine (not our code) absorbing locking,
atomicity, and recovery. The adapter risk is low: the prototype shares
no code paths with production and needs none. The honest residual cost
is SQLite operational knowledge (WAL/backup/busy behavior) replacing
JSONL operational knowledge (flock/torn-tail/rotation) — a trade, stated
plainly.
