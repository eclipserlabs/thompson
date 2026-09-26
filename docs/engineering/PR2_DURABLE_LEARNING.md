# PR 2 — Durable Learning V1: storage design, migration, compatibility, performance

Implements `docs/engineering/PILOT_ROADMAP.md` PR 2 and the implementation half of
`docs/design/OUTCOME_CONTRACT_V1.md` §§1–5. Branch: `feat/durable-learning-v1`
(stacked on PR 1 branch `fix/atomic-decision-snapshot`; PR 1 not yet merged on
remote — see §6).

## 1. What was built

New package `go/outcome` (no dependency on `go/gateway`; depends only on
`go/thompson`):

| File | Contents |
|---|---|
| `outcome.go` | `JobStatus` (PENDING/UNKNOWN/ACCEPTED/REJECTED), `TransportStatus`, `ValidationVerdict`, `VerifiedOutcome`, `Attempt`, `OutcomeEvent` (versioned, cumulative history), structural `Validate` (NaN latency rejected, ACCEPTED/REJECTED require a deciding attempt present in the chain, `Supersedes == Version-1`), null-aware `TotalCostUSD` |
| `store.go` | `OutcomeStore` interface; `MemoryOutcomeStore`; `FileOutcomeStore` (JSONL append + per-write `Sync`, `0600`, exclusive non-blocking `flock` single-writer lock, torn-tail truncation on recovery). Admission rules shared by both: exact-duplicate → idempotent no-op; same-version-different-content → conflict error; stale/gap → error; new job must start at v1. `Seq` assigned on commit in ledger order. |
| `learner.go` | `RewardMapper` + default `BinaryStatusMapper` (ACCEPTED→1.0, REJECTED→0.0, else skip — flows through the existing `Policy.Record`; sampler and update math untouched); deterministic per-(job,version) RNG (`rngFor`, sha256→PCG) so Bernoulli flips replay identically; `Learner.Apply` (incremental first-time versions, rebuild-from-genesis on corrections, cursor advance without learning for UNKNOWN/PENDING/armless deciders); `Rebuild` (latest-version-per-job in ledger order); `Settle` (Submit-then-Apply crash-safe order); `Checkpoint` + atomic `SaveCheckpoint`/`LoadCheckpoint`; `Resume` (checkpoint restore + replay beyond high-watermark). One job version produces **at most one** `Record` call, against the deciding attempt's arm only. |
| `store_test.go`, `learner_test.go` | All six mandatory scenarios + ordering/conflict/gap/flock/torn-tail/validation/perschaden tests (see §4). |
| `bench_test.go` | Persistence + replay benchmarks (see §5). |

Small, behavior-preserving changes outside the package:

- `go/thompson/policy.go`: `Policy.LoggingPolicyID()` derives the behavior-policy ID from (`Selection.Kind`, sampler name) — `exact-thompson-v1` only for exact+Thompson; `ucb-regularized-v1`, `phased-v1`, `thompson-<sampler>-v1` otherwise (all gate-refused). `Policy.RestoreSnapshot()` replaces learned state in place under lock (lets the learner re-fold without copying a mutex or swapping Policy pointers).
- `go/gateway/router.go`: all five hardcoded policy strings (1× `DecisionStarted`, 4× shadow events incl. `ShadowSkipped`) replaced with `rt.policy.LoggingPolicyID()`. No schema change; no behavior change for exact-Thompson deployments (same string as before).
- Tests: `go/thompson/logging_policy_test.go` (6-kind table), `go/gateway/policy_identity_test.go` (live==shadow identity; UCB row refused by `ToBanditLogWith`).

## 2. Storage design

- **Ledger file** (`FileOutcomeStore`, e.g. `outcomes.jsonl`): one JSON object per line, `O_APPEND`, `Sync` per committed event. In-memory index (`latest[job]`, ordered slice) rebuilt by `recover()` on open; a torn trailing line is truncated (file synced after truncate) and reported via `TornTail()`. An invalid non-tail line fails the open loudly — ledgers never silently skip history.
- **Checkpoint file** (e.g. `checkpoint.json`): `{version:1, genesis, policy, applied, ledger_len}` written tmp→rename (no fsync-of-dir; same guarantee class as `thompson.FileStore`). `genesis` is immutable after first write and is the rebuild base; `policy`+`applied` advance each checkpoint; `ledger_len` is the replay high-watermark.
- **Recovery order**: open ledger (flock + recover) → load checkpoint → `RestoreSnapshot(checkpoint.policy)` → cursor = `checkpoint.applied` → `Apply` every event with `Seq > ledger_len`. No-checkpoint recovery = `Rebuild(all events)`.
- **Crash matrix**: before persistence → `Submit` never returned success, nothing indexed, nothing to replay (failed/invalid submits commit nothing — tested); after persistence, before checkpoint → event on disk, cursor stale → replayed exactly once; during replay → torn tail truncated, prefix salvaged; redelivery after checkpoint → cursor skip. Cross-restart duplicate learning is impossible as long as all writes go through `Settle` (Submit-then-Apply) on a single writer.
- **Single-writer**: `LOCK_EX|LOCK_NB` held for the store lifetime; second opener fails with an explicit error. Lock released on `Close`. Multi-process/multi-replica shared ledgers remain a non-goal.

## 3. Migration and backward compatibility

- Existing JSONL event names and field meanings unchanged; `DecisionStarted` gains **no** new fields in this PR (contract §§1.1 `selection_kind`/`policy_version`/`job_id` remain future additive work — deliberately deferred to keep this PR reviewable).
- `LoggingPolicyID` values for exact-Thompson deployments are byte-identical to before (`exact-thompson-v1`); historical rows keep their strings and gate behavior (`propensity.CheckLoggingPolicy` untouched — both legacy IDs still admitted).
- `Snapshot{version:1}` wire format unchanged (`RestoreSnapshot` consumes the same struct; `protocol/SPEC.md` unaffected).
- The gateway's learn-on-transport path is untouched and keeps working; settled-outcome learning is a new, parallel API (`outcome.Settle`) that the router does not call yet — router wiring to `JobSettled` is PR 3 integration scope. No existing code path changed its learning behavior.
- New-file formatting: all new/edited files are `gofmt`-clean; `gateway/router.go` retains its 2 pre-existing `gofmt` hunks, verified byte-identical in kind before/after (zero new diffs).

## 4. Test results (exact)

`go test -race ./outcome/`: all pass —

- `TestDuplicateDelivered100TimesLearnsOnce` (mandatory 1)
- `TestUnknownThenAcceptedLearnsOnce` (mandatory 2; asserts UNKNOWN moves nothing)
- `TestAcceptedCorrectedToRejected` (mandatory 3; Bernoulli + Fractional; final == only-v2 reference)
- `TestRestartReplayEqualsUninterrupted` (mandatory 4; mid-stream + final checkpoints; resume with and without checkpoint; snapshot + cursor equality)
- `TestFallbackChainLearnsDecidingArmOnly` (mandatory 5; human-decided ACCEPTED moves no posterior, costs/metering asserted, failed verdicts preserved; arm-decided variant learns only that arm)
- `TestConcurrentApplyConverges` + `TestConcurrentSubmitVersionOrdering` (mandatory 6; 12 scrambled/duplicated versions linearize to 1..K; concurrent Apply == sequential reference)
- `TestCorrectedLearningAcrossSelectionKinds` (Thompson/UCB/phased correction == per-config direct-Record reference)
- `TestPendingNeverLearns`, `TestValidateRejectsBadEvents` (7 rejection cases), `TestMemoryStoreOrderingAndIdempotency` (dup/conflict/gap/stale), `TestFileStoreRoundTripAndTornTail`, `TestFileStoreSingleWriterEnforced`, `TestSubmitFailurePersistsNothing`
- `TestLoggingPolicyIDDerivesFromConfig`, `TestRouterLoggingPolicyIdentityLiveEqualsShadow`, `TestNonThompsonDecisionsAreOPEIneligible`

Full-suite results pending at write time; recorded in the final report message (commit gate: `go test -race ./...` + `go vet` + Rust policy/sim crates must be green; control-plane flakes pre-existing).

## 5. Performance (measured, this machine)

Darwin/arm… — Intel i5-7360U, `go test -bench`:

| Benchmark | Result | Reading |
|---|---|---|
| `BenchmarkFileSubmit` (JSONL + fsync/event) | ~4.1 ms/op (~240 submits/s) | fsync-dominated, as expected for a durable single-writer ledger; macOS fsync semantics differ from Linux — re-measure on the deploy target. Settlement throughput needs exceed this only at >240 jobs/s sustained, at which point batching (explicit non-goal for V1) would be the next step. |
| `BenchmarkRebuild5k` (rebuild 5,000 latest-version events) | ~11 ms (~450k events/s) | Full-ledger correction rebuilds are cheap; checkpointing is an optimization, not a correctness requirement, at pilot scale. |
| `BenchmarkCheckpointSave` (small snapshot, tmp+rename+sync) | ~5.0 ms/op | Per-decision checkpointing would halve submit throughput; checkpoint cadence (every N or T seconds) is a deployment setting. |

## 6. Stacking note / remaining blockers

- **PR 1 is not merged on `origin/main`** (still `62f3de6`); this branch stacks on `fix/atomic-decision-snapshot` (HEAD `66ec252`). Merge order must be PR 1 → PR 2. An external sync process fragmented PR 1's history with auto-commits; contents here verified by `git diff main...HEAD --stat` before the report.
- **Known limitations (not blockers for review, tracked for PR 3):** arm set fixed at learner genesis (events naming unknown arms fail loudly); `BinaryStatusMapper` is cost-blind by design (cost lives in the offline pilot metric); router does not yet call `Settle` (PR 3 wiring); contract `selection_kind`/`policy_version`/`job_id` ledger fields deferred; no multi-writer.
- **Suggested next PR:** PR 3 (verified-outcome ingress + router `Settle` wiring + randomized harness).
