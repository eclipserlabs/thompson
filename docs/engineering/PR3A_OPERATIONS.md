# PR 3A Operations: configuration, cutover, migration (Phase 4)

## 1. Modes

| | Legacy (`legacy`, default) | Verified (`verified`) |
|---|---|---|
| Request path learning | Yes: transport-derived `Record` (HTTP status/latency/cost) | **None.** Observation only; no `Record`, no `DecisionLearned` |
| Decision persistence | Best-effort commit (memory store by default); commit failure still blocks execution | Required durable `FileDecisionStore`; commit failure blocks execution |
| Settlement | Disabled (`SettleHandler` → 404) | `POST /v1/outcomes`, auth-required, validated, durable-then-applied |
| Allowed policy | Any | Exact Thompson only (Thompson selection + `exact` sampler), enforced at construction |
| Stores | Defaults (memory OK) | Explicit durable files required; memory stores rejected at construction |

Mode is per-`Router` (single policy owner). There is exactly one learning
writer per mode: legacy writes via the request path; verified writes via
`outcome.Settle` inside `SettleHandler`. The two can never both update one
policy: construction rejects mixed wiring, and the verified request path
contains no `Record` call (pinned by `TestDualLearningPrevention` + code
review; `grep '\.Record(' go/gateway/router.go` must show nothing).

## 2. Initialization (verified mode, single writer)

Open in this order (each `flock`s immediately, so a second writer fails fast
instead of forking state):

1. `NewFileDecisionStore(<decisions.jsonl>)` — 0600, exclusive lock.
2. `outcome.NewFileOutcomeStore(<outcomes.jsonl>)` — 0600, exclusive lock.
3. Build `*thompson.Policy` with arms registered (arm set is fixed from here;
   learner genesis = this snapshot).
4. `NewRouter({Mode: VerifiedMode, Decisions, Outcomes, SettleAuth, StrategyID, ...})`.
5. `RecoverVerifiedLearning(<checkpoint.json>)` **before serving traffic**
   (absent checkpoint = full ledger rebuild, also correct).
6. Serve. Checkpoint on a cadence (`CheckpointVerifiedLearning`) and on
   graceful shutdown — cadence trades replay time for write load
   (~5 ms/checkpoint, ~450k events/s rebuild; see PR2 doc §5).

`SettleAuth` trust boundary: the hook receives the raw request; production
deployments must verify a bearer token (or mTLS identity) there. There is no
default-allow: nil auth refuses construction. The endpoint binds to the
internal listener, never the public one — enforce at the deployment layer.

## 3. Cutover / migration for existing (legacy) deployments

1. Deploy the new binary in **legacy mode** with a durable decision store
   attached. Behavior is unchanged (transport learning continues), but every
   decision is now committed with identity + selection evidence. Run ≥1 full
   traffic cycle; verify `Lookup` coverage of recent decision IDs.
2. Stand up verification (validators/judges/human pool) off the critical path;
   dry-run settlement against a **separate** policy instance (register the same
   arms, replay `DecisionStarted` evidence, submit outcomes to a scratch
   outcome file). Compare settled rewards vs legacy `ComputedReward` for bias
   review. Do not point settlement at the live policy yet.
3. Cut over: start a **new** verified-mode instance on fresh files (do not
   reuse the legacy policy snapshot — posteriors carry transport-derived
   history that verified accounting must not inherit silently). Options: cold
   start (uniform priors) or documented warm-start import with the provenance
   recorded out-of-band. Switching modes on a live instance is unsupported:
   construction enforces one contract per process, and posteriors are never
   silently reset — a fresh process makes the boundary explicit.
4. Decommission legacy only after the verified ledger shows steady
   `applied/learned` flow and OPE rankability on settled rewards.

Rollback is per-process: keep the legacy instance config; verified files are
never read by legacy code.

## 4. Failure policy (as implemented + tested)

| Failure | Behavior | Test |
|---|---|---|
| Decision store unavailable / commit fails | 500, **no provider dispatched**, no evidence written | `TestCommitFailureBlocksExecution` |
| Crash before commit | Nothing dispatched; settlement of unknown ID → 404, policy untouched | `TestCrashBeforeDecisionPersistence` |
| Crash after commit, before outcome | Decision recovered identically; settles normally | `TestCrashAfterDecisionPersistence` |
| Crash after outcome persist, before learning | `RecoverVerifiedLearning` applies once; redelivery is duplicate-noop | `TestCrashAfterOutcomePersistenceBeforeLearning` |
| Correction after restart | Rebuild folds only authoritative latest; equals deterministic replay | `TestCorrectionAfterRestart` |
| Duplicate settlement | 200 `duplicate:true`, no state change | `TestConcurrentDuplicateSettlement` |
| Conflicting version | 409, accepted history byte-identical | same |
| Unauthenticated/malformed/job-mismatched | 401/400/409 before any store touch | `TestSettlementGuardrails` |
| Second writer (decisions or outcomes) | Open-time `flock` error, explicit | `TestDecisionStoreRestartSurvival`, PR2 flock tests |
| Torn tail (either ledger) | Truncated, prefix salvaged, `TornTail()` true | `TestDecisionStoreTornTail`, PR2 tail tests |
| Legacy `SettleHandler` call | 404 | `TestSettlementGuardrails` |

## 5. Single source of truth

- Current outcomes: the outcome ledger file. The learner cursor + policy
  snapshot in `checkpoint.json` are a cache; `Resume` recomputes correctness
  from the ledger (deleting the checkpoint only costs replay time).
- Current posteriors: the in-memory policy, checkpointed for restart speed.
- Decision identity/evidence: the decision ledger file (+ evidence JSONL for
  OPE continuity; `DecisionStarted` rows now carry `job_id`/`strategy_id`).
- No component reads posteriors from anywhere else; the middleware thin-waist
  path is legacy-only and documented as transport-derived (audit E1).

## 6. Measured persistence cost (this machine: i5-7360U darwin)

| Operation | Cost | Notes |
|---|---|---|
| Decision commit (file + sync) | ~7.4 ms | Larger payload than outcomes (eligible state + scores) |
| Dispatch/execution mark (file + sync) | ~same file, one more sync each | Two marks per request in the current flow |
| Outcome submit (PR 2) | ~4.1 ms | At settlement time, off the request path |
| Full ledger rebuild, 5k events (PR 2) | ~11 ms | Recovery cost, not per-request |
| Checkpoint save (PR 2) | ~5.0 ms | Cadence-traded |

Per verified request: 2 extra sync writes (commit + dispatched mark) ≈
15 ms on this hardware before provider dispatch; re-measure on the Linux
deploy target. If this dominates latency, the coalescing option is folding
the dispatched mark into the commit record — a schema change requiring a
ledger version bump, explicitly not done here.

## 7. Explicit non-goals / boundaries

- Review gate for every future change: `grep '\.Record(' go/gateway/router.go`
  must show exactly one call site, inside the `mode != VerifiedMode` block;
  `grep 'Record' go/gateway/shadow.go` must stay empty; any new learning path
  outside `outcome.Settle` fails review.
- Multi-replica operation: **unsupported and unenforced at the gateway layer**
  beyond per-file `flock`. Two routers must never share one policy object or
  one ledger file; a shared-state design is out of scope.
- The settlement API trusts the caller-supplied attempt tape (latency, cost,
  tokens) as *reported measurements* and the `verified_by` provenance as an
  *assertion* — independent verification is an organizational control, and
  `self` verification must be labeled per the contract.
- Cost-aware learning is not implemented: `BinaryStatusMapper` stays
  cost-blind; fully loaded cost is computed offline (PR 3B).
