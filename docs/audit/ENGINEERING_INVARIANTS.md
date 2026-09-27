# Engineering Invariants (Phase 1)

## Job lifecycle with boundary invariants
1. **Assign** — row persisted before execution; resume never double-appends
   (assigned-set). Invariant: one row per (treatment, job).
2. **Select** — atomic snapshot under one lock; cost-aware overlays the
   validated rule plus the safety mask with deterministic reserve/re-pick;
   empty mask errors with nothing committed. Invariant: no pick outside the
   authorized mask; identity bound per decision.
3. **Commit** — persisted before dispatch; commit failure fails closed with
   no dispatch. Invariant: no dispatch without a committed decision.
   Caveat: `X-Decision-ID` headers are emitted pre-commit and are
   provisional until 2xx (runner enforces; no other in-repo consumer).
4. **Dispatch/execute** — provider call; timeouts stay ambiguous and resolve
   via ledger poll, never fabrication. Invariant: no invented outcomes.
   Unsupported: exactly-once dispatch across crash+resume (runner
   re-executes; acceptable for synthetic providers, must not be assumed
   for side-effecting ones).
5. **Settle** — auth → validate → decision lookup → job/strategy match →
   attribution → cost pre-validate → submit → quality apply → cost apply →
   safety observe. Duplicates idempotent; gaps/conflicts rejected.
   Invariant: ledger, quality, and cost converge through deterministic
   rebuild from the same history.
6. **Safety observe** — monitor folds authoritative events; breaches persist
   suspension before effect; store failure fails closed (fallback-or-nothing
   + 500). Invariant: no blind adaptation.
7. **Checkpoint/recovery** — quality checkpoint + cost sidecar (audit) +
   event logs; recovery restores, replays past the watermark, rebuilds the
   book, refolds safety, rescans budgets. Invariant: identical authoritative
   state or explicit refusal.

## Atomic vs reconcilable
Atomic (fsync before ack): assignment rows, decision commits, outcome
submits, safety events, checkpoints. Reconcilable: learner/book posteriors,
monitor views, budget counters (all re-derivable from ledgers).

## Lock discipline (verified by read)
`Submit`/`Events` take and release the store mutex without nesting into
learner/policy; `Apply` chains run sequentially. Selection takes
policy → book → safety, each released before the next. Settlement runs
settleMu, then the same leaf-ward sequence. No thread holds two locks while
acquiring a third in a conflicting order: no deadlock cycle exists. Safety
file I/O happens under its mutex (fsync latency only on transitions;
steady-state Reserve is memory-only).

## State duplication (each justified)
Live policy/book/counters (serve traffic) vs durable ledgers/checkpoints
(survive crashes) vs per-decision evidence (immutable audit). No two are
writable authorities for the same fact: ledgers win, everything else replays.

## Finding classes
- Confirmed defect: R1 safety-store lock (repaired, regression-tested).
- Under reproduction: R2 budget/commit boundary, R3 crash-consistency set.
- Documentation error: flock comment (made true by the fix).
- Unsupported assumptions: exactly-once dispatch; multi-process operation
  (now OS-enforced single-writer on all three ledgers); Linux/load numbers;
  live disk-full behavior.
