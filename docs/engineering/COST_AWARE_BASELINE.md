# Cost-Aware Baseline (Phase 0)

## Base SHAs
- `origin/main`: `d7a33e68e9484732fad40561f21f038811540ca8` (Thompson pilot readiness V1 #19, fetched 2026-09-27).
- Feature branch: `feat/cost-aware-v1` on isolated worktree `~/Documents/thompson-costaware`.
- Baseline merge: `f26342c` "Integrate reviewed correctness stack (PRs A-D) for cost-aware baseline" (merge of `origin/review/correctness-d` into `d7a33e6`; conflicts in 3 files resolved by keeping HEAD/newer integration versions: `syncBuffer`, `storage_from_kind`, variadic `openTestRunner`).
- Open PRs at baseline: none (`gh pr list --state open` empty).
- Unrelated branches preserved: `docs/readme-*`, `feat/*`, `review/correctness-*`, `release/*` untouched; no push to main, no force-push, no deploy.
- Frozen charter: `docs/engineering/PR3B_CHARTER.md` untouched (empty diff vs `origin/main`).

## Corrections present in working base
Verified by grep on HEAD:
- `go/harness/report.go`: `bootstrapCompare` (4 hits), strict-gate + validity fraction.
- `go/gateway/settle.go`: `checkAttribution` (decision/outcome binding, single vs multi-attempt rules).
- `go/outcome/learner.go`: `armInGenesisLocked` (6 hits), genesis guard, `rngFor` deterministic per (job,version) Bernoulli replay.
- `go/cmd/exp-run/cluster.go`: `syncBuffer` mutex-guarded stderr (race fix).
- `crates/control-plane/src/storage.rs` + `main.rs`: `storage_from_kind` fail-fast on s3/postgres/bogus, unit-tested.
- Protocol: `TestProtocol*` (7 Go) + `tests/protocol.rs` (6 Rust), fixtures in `protocol/testdata/`.

## Current implementation (record)

### BinaryStatusMapper — `go/outcome/learner.go:31-42`
`ACCEPTED→(1.0,true)`, `REJECTED→(0.0,true)`, else `(0,false)`. Cost/latency live in offline pilot metric, not bandit scalar. `NoopMapper` (line 48-50) never learns; static treatments run full verified machinery with fixed behavior.

Wiring: `go/gateway/settle.go:63-69` (nil→Binary default), `go/harness/treatment.go:248` (learn branch hardcodes Binary), `go/router/main.go:269-275` (`MAPPER=""|binary` default, `noop` opt-in), `go/cmd/exp-run/cluster.go:53-64` (`MAPPER=noop` when `!Learn`).

### Policy.Record — `go/thompson/policy.go:622-681`, `posterior.go:93-122`
`Record(rng,id,reward)` range-checked via `Posterior.Observe` (`Binarize/Bernoulli/Fractional`), `CumulativeReward+=reward`, `totalPulls++`, discount support. `Select/SelectWithScores/SelectWith`, `Snapshot/Restore`, `LoggingPolicyID()`. Sampler untouched by mappers.

### outcome.Learner — `go/outcome/learner.go:71-478`
`NewLearner(policy,mapper,history)`, `Apply(ev)` (incremental vs rebuild-on-correction, UNKNOWN/PENDING advance cursor without learning), `Rebuild`, `foldLocked` (at-most-one `Policy.Record` vs deciding arm via `rngFor` sha256→PCG), `Settle` (Submit-then-Apply), `Resume`, `Checkpoint/Save/Load/CheckpointOf`, `Rebase`. Genesis guard prevents unknown-arm injection.

### Reward configuration
- Legacy scalar: `go/thompson/reward.go:8-161` (`Weights{Latency,Success,Cache,Cost,Quality}`, `DefaultRewardPolicy`, `Reward(o)` in [0,1]). Legacy path only via `go/gateway/router.go:211-222,471-499` `computeReward()`; skipped in `VerifiedMode`.
- Verified path uses `RewardMapper`, not `RewardPolicy`. No migration of old outcomes without explicit version bump.

### Experiment runner — `go/cmd/exp-run/`
`RunnerConfig{Manifest,Root,RouterBin,PubPorts,SettlePorts,Token,Timeout,T0Clock,Step,CrashAfter,SelectionSeed}` (`runner.go:41-77`), `OpenRunner/Boot/Run/runJob/executeAttempt/resolveTimeout`, `ProgressRow/Phase/RunHeader`. Manifest v1 (`manifest.go:16-81`): `TreatmentConfig{ID,Arms,MaxAttempts,Learn}`, `ArmTruth/HumanTruth/ManifestJob/JobBehavior`, `LoadManifest/Validate/contentHash/SimClock`. `FixtureVerifier` (`verifier.go`): deterministic `exp-truth-v1` hash, behaviors normal/invalid-output/timeout-then-accept/unresolved/delayed-accept/correct-to-reject/unknown-then-accept. `SpawnGateway/Route/Settle` (`cluster.go`), CLI subcommands in `main.go`.

Current harness supports 3 treatments (T0 fixed, T1 cheapest-qualified static, T2 cost-blind Thompson) via `go/harness/treatment.go:30-211` (`Assigner/StaticStrategy/ThompsonStrategy/TreatmentInstance`, `OpenTreatment/RunJob/decide`).

### Reporting — `go/harness/report.go`, `go/cmd/exp-report/main.go`
`ReportConfig{Maturation,MinJobs,CensorGate,QualityFloor,MinEffect,BootstrapN/Seed,MinBootstrapValidFraction,MaxUnmeteredShare,MaxPlausibleCost,ExpectedWeights}`. `Analyze/MatureJobs/MatureJobsMapped/primaryOf()` (metered ACCEPTED+REJECTED spend / verified successes), `bootstrapCompare`, `BuildReport`. `jobCost()` (`report.go:400-421`): sums metered attempt `CostUSD` + `HumanReviewCostUSD`; null costs increment unmetered count, never zero-filled. Gate `missing-cost-*` fails treatment above `MaxUnmeteredShare` (default 0.10). CLI flags `--root/--treatments/--baseline/--candidate/--maturation/--min-jobs/--format`, exit 2 on `NotReadyError`.

Known baseline defect (carried from main, fixed on unmerged `release/consolidation-v1@63afe94`, to be re-applied here): gate emission iterates `rep.Treatments` map directly → nondeterministic gate/reason order. Fix: sort treatment names before emitting gates.

### Outcome contract — `go/outcome/outcome.go`, `store.go`
`JobStatus{P_PENDING,UNKNOWN,ACCEPTED,REJECTED}`, `Attempt{AttemptID,Seq,ExecutorID,ArmID,Transport,LatencyMs,CostUSD*,Tokens,Validation,Verified,VerifiedBy/At}`, `OutcomeEvent{SchemaVersion,EventType=JobSettled,DecisionID,JobID,StrategyID,Version,Supersedes,Status,Attempts,DecidingAttemptID,HumanReviewCostUSD*,...Seq}`. `Validate()` structural only. `TotalCostUSD()` returns `(metered, unmetered)`; callers must not zero-fill. `MemoryOutcomeStore` + `FileOutcomeStore` (JSONL+fsync+flock, torn-tail recovery).

## Compatibility constraints
1. Do not rewrite Thompson algorithm; `Policy.Record` semantics frozen for existing configs.
2. Do not modify `docs/engineering/PR3B_CHARTER.md` or existing 3-treatment charter/spec.
3. Do not reinterpret old outcomes: new objective needs distinct policy identity + versioned state + explicit migration.
4. Outcome/protocol contract frozen: `SchemaVersion=1`, `protocol/SPEC.md` + `schema.json`; breaking change → stop and document smallest compatible alternative.
5. Decision snapshots internally consistent; every decision bound to policy identity/objective version/config (`LoggingPolicyID`, decision ledger).
6. Regression fixtures that must keep passing unchanged: `protocol/testdata/*.json`, `TestProtocol*`, cost-blind Thompson suites, `TestLedgerFromFileGroupsPaired`, crash/resume equivalence (modulo analysis clock).
7. Toolchains: go1.27.1 (compat check go1.22.0), rustc/cargo 1.90.0.

## Test baseline (Phase 0, this worktree)
- `go test -count=1 ./...` launched in background at baseline (shell `sh_0e0d85212001qal2Ytm8jvM8Fv`); result to be recorded in engineering report.
- Rust workspace baseline to follow where affected (control-plane untouched by cost-aware Go change except pre-existing merge).
