# State and Write Inventory (Phase 1)

## One job's authoritative trail (verified against code)
| Step | Authoritative record | Writes + fsyncs | Seq authority | Lock |
|---|---|---|---|---|
| Assignment | assignments.jsonl row | 1 append + fsync | none | none (runner assigned-set in memory) |
| Selection | — (computed) | 0 | — | policy/book/safety mutexes |
| Decision commit | decisions.jsonl row | 1 append + fsync | decision Seq | FileDecisionStore flock |
| Evidence | evidence.jsonl (Started/Observed/Learned + shadow) | 3–4 appends + fsyncs | none | evidence writer flock |
| Outcome submit | outcomes.jsonl row | 1 append + fsync | outcome Seq | FileOutcomeStore flock |
| Safety transition | safety.jsonl row | 1 append + fsync (transitions only) | safety Seq | SafetyStore flock (R1 fix) |
| Checkpoints | quality JSON + cost sidecar | 2× (tmp+rename+fsync+dirsync) | cursors inside | settleMu |

Per settled job: ~6–7 file appends, ~6–7 fsyncs, across 4 files + 2
checkpoints, coordinated by 3 independent sequence counters and 4
single-writer locks.

## Authoritative vs derived
Authoritative: assignment rows, committed decisions, outcome versions,
safety events, config files. Derived/cached: policy posteriors, cost
means, monitor windows, budget counters, checkpoints (incl. cost
sidecar, which recovery ignores), OPE indexes, reports.

## Failure windows between stores (the hypothesis's target list)
- W1 commit→dispatch: orphaned decision, inert, never settled.
- W2 dispatch→verification: timeout/unresolved → censored, learns nothing.
- W3 submit→learner-apply: heals via checkpoint+replay.
- W4 quality→cost apply: heals via ledger rebuild on recovery.
- W5 settle→safety-observe: 500; re-evaluation on next settlement heals.
- W6 reserve→commit: forgiven on recovery (replay counts committed).
- W7 torn safety write: truncated = lost action; operator sees no ack and
  retries (consistent: no response ever promised commit).
- W8 torn checkpoint: refused; full rebuild.

## What cannot be combined (time/external-effect separations)
Dispatch (external side effect) stands alone. Verification arrives later
(seconds–hours). Operator actions arrive at human timescales. No
transaction can span these; uncertainty stays explicit by design.

## What can be combined (same instant, same process)
- Decision commit + exploration-budget consumption (one reservation
  ticket + one decision row → one transaction).
- Outcome submit + learner/book cursor advance (one event row + cursor
  table → one transaction; in-memory posteriors remain projections).
- Safety event + effect visibility (event row commit IS the transition;
  read-your-write within the same transaction).

## Before-and-after state model
Before: 4 append-only files + 2 checkpoint files, 3 sequence counters,
4 flock domains, 2 applied-cursor maps, torn-tail recovery in 3 readers,
budget re-scan over decisions, safety refold over events.
After (hypothesis): 1 SQLite WAL database, 1 commit sequence (rowid),
1 writer lock (SQLite locking), 1 idempotency-key table, projections
rebuilt from one log; checkpoints optional cached snapshots; torn-tail
handling deleted (page-level atomicity); cross-ledger reconciliation
deleted (single history). Dispatch ambiguity and time separations remain
exactly as they are.
