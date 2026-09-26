# Correctness Audit (Phase 1)

Scope: `origin/main` at `62f3de6`. Every finding cites the exact file and line numbers read in this session. Severities: **P0** (correctness-breaking, fix before any production experiment), **P1** (correctness-relevant gap or trap), **P2** (hardening/robustness).

---

## A. Decision consistency

### A1 (P0, CONFIRMED): `Router.ServeHTTP` builds one decision from 4+ separate policy locks — selection and evidence reads can observe different policy versions

- **Evidence.**
  - `Policy` methods each take the single policy mutex independently: `EligibleArmIDs` (`go/thompson/policy.go:250-256`), `SelectWithScores` (`go/thompson/policy.go:297-299`), `PosteriorFor` (`go/thompson/policy.go:261-269`), `ConfigHash` (`go/thompson/policy.go:281-291`), `ConfigSnapshot` (`go/thompson/policy.go:272-276`), `Record` (`go/thompson/policy.go:507-509`).
  - `SelectWithScores` itself is internally atomic (selection + sampled scores under one lock, `go/thompson/policy.go:297-329`) — the primitive is sound.
  - But `ServeHTTP` composes them as separate acquisitions (`go/gateway/router.go:167-183`): `EligibleArmIDs()` → `SelectWithScores(rng)` → `PosteriorFor(chosen)` → `ConfigHash()`; then a **second loop** re-reads every eligible arm's posterior one `PosteriorFor` at a time (`go/gateway/router.go:222-227`); then after execution `ConfigSnapshot()` + `Record` + `PosteriorFor` + `TotalPulls` (`go/gateway/router.go:337-344`).
- **Failure scenario.** Two concurrent requests (or a request racing an admin `AddArm`/`RemoveArm`/`Record`) interleave: request R selects arm X under posterior P1; before R's `PosteriorFor`/eligible-state loop runs, request S records an outcome (mutating X's posterior and `totalPulls`, plus discounting *all* arms when configured, `go/thompson/policy.go:522-530`). R then persists `PosteriorBefore`, `eligible_arm_state`, and possibly a changed `eligible` set from policy version P2 while `sampled_scores`/`selected_arm` came from P1. Consequences:
  1. `DecisionStarted.posterior_before` ≠ the posterior that was actually sampled → `DecisionLearned` before/after transitions are incoherent.
  2. `eligible_arm_state` ≠ the state the Thompson draw ran on → offline propensity reconstruction (`ToBanditLogWith`, `go/gateway/ope.go:113-117`, which explicitly assumes "posterior-*before*" denominators) silently uses the wrong denominator. IPS weights are then wrong with no error — the OPE reliability gates cannot detect this because the snapshot is well-formed but stale/wrong-versioned.
  3. An arm removed between `EligibleArmIDs` and `SelectWithScores` (or added between) yields `selected ∉ eligible` or silently drops a newcomer; `PosteriorFor(chosen)` failing mid-request returns 500 *after* the decision was already sampled (`go/gateway/router.go:178-182`).
- **Affected component.** `go/gateway/router.go:167-227, 337-344`; `go/thompson/policy.go` (missing atomic multi-read primitive).
- **Recommended fix.** Add a single-lock `SelectSnapshot`-style primitive on `Policy` that returns, atomically: eligible IDs, chosen arm, true sampled scores, per-arm posterior copies, total pulls, config copy + hash. Rewrite `ServeHTTP`'s decision phase to one call. (Implemented in Phase 3 of this mission.)
- **Regression test.** Concurrent hammer: N goroutines looping `SelectSnapshot`-vs-`Record`/`AddArm` asserting per-decision internal coherence (chosen ∈ eligible; `posterior_before[chosen]` equals the eligible-state entry; scores key-set equals eligible set; config hash matches snapshotted config); plus a router-level `-race` test asserting `DecisionStarted` self-consistency under parallel requests. (Added in Phase 3: `go/thompson/policy_snapshot_test.go`, `go/gateway/router_snapshot_test.go`.)

### A2 (P1): logged `sampled_scores` are not always the probabilities used for selection

- **Evidence.**
  - Phased forced path emits posterior **means**, not samples, in both implementations: Go `go/thompson/policy.go:313-317`, Rust `crates/thompson-sampling/src/policy.rs:262-271`.
  - UCB path emits sample+bonus composites, Go `go/thompson/policy.go:445-467`, Rust `crates/thompson-sampling/src/policy.rs:390-414`.
  - `SelectWith` (custom strategy) reports posterior means as scores while selection used the strategy: Go `go/thompson/policy.go:399-406`, Rust `crates/thompson-sampling/src/policy.rs:300-307`.
- **Failure scenario.** A consumer reading `sampled_scores` as "the probabilities actually used" misattributes phased/UCB/custom decisions. Today the only propensity consumer (`ToBanditLogWith`) correctly ignores `sampled_scores` and reconstructs from `eligible_arm_state` — so this is latent, not active corruption. It becomes active the moment anyone logs or audits from `sampled_scores` directly.
- **Affected component.** `go/thompson/policy.go`, `crates/thompson-sampling/src/policy.rs`, `go/gateway/evidence.go:56` (`SampledScores` field docs).
- **Recommended fix.** Document `SampledScores` as observability-only (means under phased/custom, composites under UCB), never a propensity source; keep reconstruction-from-`eligible_arm_state` as the sole denominator path. Optionally split the evidence field (`scores` vs `score_kind`).
- **Regression test.** Unit test asserting phased-forced `SelectWithScores` returns means equal to `Posterior.Mean()` and Thompson path returns values that differ across draws (non-degenerate sampling).

### A3 (P0, CONFIRMED): `LoggingPolicyID` is hardcoded — non-Thompson selections are logged as `exact-thompson-v1`

- **Evidence.**
  - `Router.ServeHTTP` writes `LoggingPolicyID: "exact-thompson-v1"` unconditionally (`go/gateway/router.go:239`), regardless of `config.Selection` (Thompson vs `UCBRegularized` vs `PhasedSelection`, `go/thompson/policy.go:305-323`).
  - The OPE gate trusts the string: `ToBanditLogWith` → `propensity.CheckLoggingPolicy` (`go/gateway/ope.go:106-112`) admits the row, and both estimators model exact-Thompson argmax (`go/gateway/propensity_source.go:9-23`).
  - Shadow events use a *different* hardcoded string (`"thompson-v1"`, `go/gateway/router.go:376, 425`) for the same live policy — two IDs for one behavior policy.
  - The thin-waist `Middleware` path (`go/gateway/middleware.go:49-80`) records no logging-policy identity or eligible-state at all, so anything learned there is invisible to OPE by construction.
- **Failure scenario.** Deploy with `Selection: UCBRegularized` (or Phased). Every decision is logged as exact-Thompson; denominators are reconstructed under the wrong action distribution; IPS/SNIPS estimates are biased with `RELIABLE` status and no warning. This is a silent-wrong-numbers failure, the worst kind for an experiment program.
- **Affected component.** `go/gateway/router.go:228-245, 373-379, 420-428`; `go/gateway/ope.go:103-112`.
- **Recommended fix (PR 2 scope, not this mission's P0 fix).** Derive `LoggingPolicyID` from the active `Selection` kind + sampler name (e.g. `exact-thompson-v1` only when `Selection.Kind == ThompsonSelection && sampler == ExactSampler`); emit `OPE_INELIGIBLE`-equivalent (`CheckLoggingPolicy`-rejected) IDs for UCB/phased/approx configurations; unify the shadow `PrimaryLoggingPolicyID` string with the primary one; persist sampler name in `DecisionStarted`.
- **Regression test.** Table test: each `Selection` kind × sampler through `ServeHTTP` (or a `LoggingPolicyIDFor(config, sampler)` pure function) asserting the emitted ID, and `ToBanditLogWith` refusing UCB/phased rows.

### A4 (P2): `ConfigHash` copies config under lock, then hashes outside it — benign today, fragile tomorrow

- **Evidence.** `go/thompson/policy.go:281-291`. Safe only because `config` is never mutated after `New` (no setter exists — verified by grep: no `SetConfig`/`config =` outside constructors).
- **Failure scenario.** Any future live-config update introduces a torn hash. Non-issue today; note as an invariant, not a bug.
- **Recommended fix.** Keep the "no live config mutation" invariant documented; if live config is ever added, move it behind the atomic snapshot primitive from A1.

### A5 (P1, Rust): the Rust core has no concurrency story — `select(&self)` / `record(&mut self)` require external synchronization

- **Evidence.** `crates/thompson-sampling/src/policy.rs:233` (`select(&self)`), `:426` (`record(&mut self)`), struct holds a plain `BTreeMap` (`:83-89`).
- **Failure scenario.** A multi-threaded Rust gateway borrowing one policy must add its own lock; two policies (or a policy + a cloned snapshot) diverge silently. The Go side solved this with the policy mutex; the Rust side pushes it to the embedder with no documentation of the requirement.
- **Recommended fix.** Document the `Send`/`Sync` expectation on `ThompsonSampling` (e.g. "share via `Mutex`/`RwLock`; `select`+evidence reads needing coherence must hold the same lock") in `policy.rs` docs; consider a `select_snapshot`-equivalent helper mirroring the Go fix.
- **Regression test.** Doc-level only; optional `trybuild`-style compile assertion is overkill.

---

## B. Outcome semantics

### B1 (P1): transport success and task success share one boolean; verified outcomes depend entirely on caller discipline

- **Evidence.**
  - `HTTPProvider.Invoke`: `Success = 2xx` (`go/gateway/provider.go:~133-137`). A 200 with a hallucinated/wrong-format/refused answer is `Success: true`.
  - `Middleware.Handler` overwrites caller outcome: `outcome.Success = recorder.Status < 400` (`go/gateway/middleware.go:67-68`), discarding any task-level verification the `Forward` function performed unless encoded in the status code.
  - `Router.ServeHTTP`: provider error forces `success = false` (`go/gateway/router.go:288-291`), but a successful HTTP call with task failure has no representation unless the provider implementation says so.
  - Reward then collapses whatever it gets: `computeReward` (`go/gateway/router.go:142-153`), `RewardPolicy.Reward` (`crates/thompson-sampling/src/reward.rs:244-246`, `go/thompson/reward.go`).
- **Failure scenario.** Provider returns 200 with an unusable payload (schema violation, empty completion, safety refusal with 200). System learns `success=true`-flavored reward; posteriors drift toward a broken arm. The pilot metric "cost per verified successful job" cannot be computed from this ledger.
- **Affected component.** `go/gateway/provider.go`, `go/gateway/middleware.go:61-80`, `go/gateway/router.go:275-342`.
- **Recommended fix.** Outcome contract V1 (Phase 2 of this mission): separate `attempt status` (transport) from `verified outcome` (task), with provenance; only verified outcomes move the policy. Until then, document that `ProviderOutcome.Success` is transport-only and must not be read as task success.
- **Regression test.** Provider returning 200-with-garbage → assert ledger records transport success AND task outcome unknown/failure distinctly (contract PR).

### B2 (P1): the gateway reward path drops quality and cache signals

- **Evidence.** `computeReward` builds `Outcome{LatencyMs, Success, CostUSD}` only (`go/gateway/router.go:142-153`) — no `Quality`/`HasQuality`, no `CacheHit`. Absent quality forfeits its weight (`crates/thompson-sampling/src/reward.rs:205-221`), so rewards are computed over a reduced component set without any ledger marker that quality was unavailable.
- **Failure scenario.** A deployment relying on judge scores silently optimizes latency/success/cost only; A/B comparisons across periods with and without a judge compare different reward functions.
- **Recommended fix.** Outcome contract V1 carries optional quality + provenance; persist the reward-component breakdown (or at least the effective weight set) in `DecisionLearned`.
- **Regression test.** `computeReward`-equivalent with quality attached produces a different, documented reward; ledger round-trip preserves the breakdown.

### B3 (P0, CONFIRMED as missing capability): no late, corrected, or versioned outcomes; no UNKNOWN state

- **Evidence.** `Record` folds one scalar once (`go/thompson/policy.go:507-535`); router duplicate guard is per-process `map[string]bool` (`go/gateway/router.go:131-140, 333-342`) keyed by a freshly generated canonical ID that can never collide — the guard is nearly dead code, and the map grows unboundedly (no eviction).
- There is no `UNKNOWN`/censored status anywhere: `Success bool` (`go/gateway/evidence.go:80`), `Outcome.success` (`crates/thompson-sampling/src/reward.rs:16`). A timeout records `success=false` + worst-case latency/cost (`reward.rs:150-158` maps `+Inf` → 0.0; `record_outcome_survives_unscorable_measurements` test, `policy.rs:649-664`), i.e. an ambiguous timeout is learned as a confident failure.
- **Failure scenario.** (1) Human verification arrives minutes later contradicting the transport outcome — no API to correct; the wrong reward is permanent. (2) Ambiguous timeout (did the write commit?) is learned as failure, biasing against slow-but-correct arms. (3) Restart wipes the `recorded` map; redelivery of the same logical job (new canonical ID) double-learns.
- **Affected component.** `go/gateway/router.go`, `go/thompson/policy.go`, `go/gateway/evidence.go`.
- **Recommended fix.** Outcome contract V1 (Phase 2): idempotency keys, outcome versions, late-correction events, UNKNOWN exclusion from learning. Policy update becomes an explicit, versioned transition (PR 2).
- **Regression test.** Correction-event test: initial learned reward R1, late verified outcome R2 → posterior equals single-application of R2-version chain, never R1+R2; duplicate delivery with same idempotency key applies once.

### B4 (P2): `NaN` latency/cost score as perfect (1.0), `±Inf` handling is correct but subtle

- **Evidence.** `ramp_down`: NaN → 1.0, `+Inf` → 0.0 (`crates/thompson-sampling/src/reward.rs:150-158`); regression test `an_infinite_measurement_scores_worst_not_best` (`reward.rs:303-328`); Go parity claimed in comments (`reward.rs:133-135`).
- **Failure scenario.** An unmeasured NaN latency (clock bug, missing timer) scores perfect latency instead of worst/unknown, inflating reward. Narrow: NaN requires a broken clock, and the behavior is tested and documented — hence P2, not P0.
- **Recommended fix.** Treat NaN measurements as missing (exclude component or mark outcome UNKNOWN) in contract V1; keep current behavior pinned by the existing test until then.

---

## C. Learning durability

### C1 (P1, acknowledged-by-design but pilot-blocking): crash between `Record` and `DecisionLearned` diverges memory from ledger; restart cannot reproduce learned state

- **Evidence.** The code documents it plainly: `go/gateway/evidence.go:152-161` — crash after `RecordOutcome` but before the final append leaves the posterior mutated in memory with no durable `DecisionLearned`; "V0 accepts single-replica semantics and does not claim global exactly-once." `FileStore` snapshots (`go/thompson/persistence.go:50-85`, atomic tmp+rename+fsync, 10 MiB cap) capture state only when explicitly saved, not per decision.
- **Failure scenario.** Crash after learning N decisions but before their `DecisionLearned` lines flush (or before the next snapshot save): restart from snapshot loses N updates; replaying the ledger *also* loses them (no `DecisionLearned` → `AnalyzePaired` skips the row, `replay.go:162-164`). Alternatively, naive re-application of `ExecutionObserved` rows without `DecisionLearned` double-learns the survivors. Either way restart/replay ≠ pre-crash state.
- **Affected component.** `go/gateway/router.go:332-359`, `go/gateway/evidence.go`, `go/thompson/persistence.go`.
- **Recommended fix (PR 2).** Write-ahead ordering: persist a single durable "decision committed with reward R" record atomically with (or before) the in-memory `Record`, keyed by idempotency key; on restart, reconcile snapshot against ledger (re-apply committed-but-unsnapshotted rewards exactly once, skip superseded versions). Until then, shorten the window (snapshot cadence) and document the accepted loss window for the pilot.
- **Regression test.** Crash-injection test: kill between `Record` and ledger append (injectable writer fault — `MemoryEvidenceWriter.FailNext` already supports this, `evidence.go:246-250`) → restart-from-snapshot + ledger reconcile → posterior equality.

### C2 (P1): duplicate delivery can update the policy twice across restarts; unbounded in-process dedup map

- **Evidence.** `recorded map[string]bool` (`go/gateway/router.go:35, 131-140`): never evicted (memory grows with lifetime decisions), never persisted (restart clears it). Canonical IDs are random per request (`generateCanonicalID`, `router.go:98-104`), so a retried logical job gets a *new* ID and bypasses the guard entirely.
- **Affected component.** `go/gateway/router.go`.
- **Recommended fix.** Idempotency keys from the contract (client-supplied, persisted in ledger + snapshot); bounded LRU for the hot guard; reconcile on startup.
- **Regression test.** Same idempotency key delivered twice (incl. across a simulated restart) → single `Record` effect.

### C3 (P1): multi-replica gateways are explicitly unsupported — scale-out silently forks learning

- **Evidence.** `helm/router/values.yaml`: `replicaCount: 1` with "do not scale without shared state"; `FileEvidenceWriter` docs: "multiple replicas must use separate files" (`go/gateway/evidence.go:152-154`); `Router` holds one in-process `*thompson.Policy` (`router.go:29-39`); control-plane `Registry` is per-process memory/file (`crates/control-plane/src/lib.rs`, `storage.rs`).
- **Failure scenario.** A second replica learns a disjoint posterior; ledgers diverge; snapshots overwrite each other on shared storage. No fencing, no error — just two "truths".
- **Recommended fix.** Keep single-replica as a documented hard requirement for the pilot (helm default already 1); add a startup guard (refuse to start when replica count > 1 is detectable, or file-lock the evidence path); shared-state design is an explicit non-goal for PRs 1–3.
- **Regression test.** Startup test asserting single-writer lock acquisition on the evidence file.

### C4 (P2): `MemoryStore` (Go) has no mutex; snapshot JSON is ULP-lossy

- **Evidence.** `go/thompson/persistence.go:17-41` (`MemoryStore` test-only, no mutex — concurrent test use races); snapshot ULP note in Rust (`crates/thompson-sampling/src/policy.rs:566-576`) and tolerant round-trip test (`policy.rs:974-1003`).
- **Recommended fix.** Document `MemoryStore` as single-threaded test-only; keep tolerance-based snapshot comparison (already done).

---

## D. Experiment validity

### D1: propensities are reconstructed, gated, and diagnosed — the strongest part of the codebase. Residual risk is upstream (A1/A3), not in the estimators.

- **Evidence.**
  - Same-code guarantee: replay delegates to `propensity.MonteCarlo` (`go/gateway/replay.go:253-255`).
  - Independent reference: adaptive G7/K15 quadrature, quantile+ladder seeding, log-space, unity check `1e-6` (`docs/propensity-audit/REPORT.md`; `go/propensity/reference.go`, `quad.go`).
  - Only `RELIABLE` denominators enter IPS (`go/gateway/ope.go:129-140`, `142-155`); zero-wins never floored — Clopper-Pearson bound instead (`go/propensity/mc.go:102-110`); refused rows are *dropped*, never zero-weighted (`go/gateway/ope.go:281-299` — the comment explains why zero-weighting would bias SNIPS/ESS).
  - ESS/ESS-N, weight quantiles, `ESS/N < 0.1` and `MaxW > 10` warnings (`go/gateway/ope.go:366-455`); rankability gates (`MinESS 200`, `MinESS/N 0.10`, `MaxWeight 50`, …; `go/gateway/rankability.go:32-191`); `Rank` refuses `NOT_RANKABLE`.
  - Circularity guard: MC-vs-MC self-check labeled `IMPLEMENTATION_SANITY_ONLY` (`go/gateway/ope.go:164-186`).
- **Residual gaps.**
  - **D1a (P1).** `EvaluateOPE` computes IPS/SNIPS over *usable* rows but `ExcludedFraction` is measured against all rows (`go/gateway/ope.go:326`) while `ESSOverN` is over usable rows (`:387`) — mixing denominators across diagnostics. With high refusal rates the reported `ESS/N` overstates effective information relative to the logged population. Report both (share of logged population retained *and* ESS over retained).
  - **D1b (P1).** Bootstrap SE/CIs resample precomputed weights (`go/gateway/ope.go:421-449`) — valid for IPS variance given fixed denominators, but does not propagate propensity-estimation uncertainty (MC noise in `π_log`). With `LOW_PRECISION` refused this is conservative-by-exclusion; document the scope.
  - **D1c (P2).** `GreedyCandidate` determinism vs tie-breaks: candidate uses lexicographic tie-break (`go/gateway/ope.go:204-221`); runtime argmax ties break by first-seen order which coincides on sorted IDs — consistent today, coupled implicitly.

### D2 (P1): no defense against distribution shift between logging and evaluation windows

- **Evidence.** Sim scenarios `drift`/`churn`/`treadmill` exist precisely because shift breaks naive comparisons (`crates/thompson-sim/src/env.rs:173-260`; `docs/FINDINGS.md` §4: discount `0.999` 5.9× win on drift). OPE assumes rewards drawn under the logging distribution; nothing timestamps/windows the analysis or tests stationarity. `docs/FINDINGS.md` "Threats" admits regret ≠ operating metrics and synthetic-only evidence.
- **Failure scenario.** "Candidate beats logging policy" is actually "traffic got easier." A pilot comparing fixed vs Thompson policies across different weeks inherits this confound.
- **Recommended fix.** PR 3 experiment harness: randomized interleaving (not sequential eras), pre-registered window, shift diagnostics (reward-by-time, arm-availability overlap). Contract V1's verification timestamps enable this.
- **Regression test.** Harness test with an injected mid-experiment distribution shift asserting the analysis flags non-stationarity rather than declaring a winner.

### D3 (P2): paired shadow analysis is correctly labeled non-regret, but coverage-gated only by warnings

- **Evidence.** `AnalyzePaired` requires primary+shadow+learned (`go/gateway/replay.go:162-164`), reports coverage/missingness/support warnings (`:228-241`), labels itself "paired observed comparison, NOT policy regret" (`:129-131`).
- **Gap.** Low-coverage pairs still produce means (warnings only, no refusal like the rankability gate). Acceptable for diagnostics; do not promote to decision metric without the PR 3 gate.

---

## E. Product integration

### E1: smallest existing interface today

- **External app → decision:** thin-waist `policy.Select(rng)` + `policy.RecordOutcome(rng, provider, outcome)` (`go/gateway/middleware.go:1-11`, `Handler` at `:34-92`); or sidecar HTTP `Router.ServeHTTP` (`go/gateway/router.go:157`); or Rust FFI-shaped `select`/`record_outcome` (`protocol/SPEC.md`; `examples/lite_llm_adapter.py` sketches the LiteLLM adapter).
- **Verified-outcome reporting:** does not exist. Callers can only supply a transport-flavored `Outcome`/`ProviderOutcome`. There is no verified-task-success field, no late correction, no idempotency key (see B3, PR 2 contract).
- **Runs without the HTTP gateway:** everything except `go/gateway` and `crates/control-plane` — `go/thompson` + `Middleware.Forward` is embeddable; `thompson-sim` runs offline; `propensity`/`opeval` are offline CLIs.

### E2: what is missing for a one-customer pilot (input to PR 3)

1. Verified-outcome ingress (contract V1) — else the pilot optimizes HTTP status.
2. Atomic decision snapshot (Phase 3 fix) — else evidence is version-torn under load.
3. Correct `LoggingPolicyID` derivation (A3) — else OPE denominators are wrong for any non-default config.
4. Durable commit + restart reconcile (C1/C2) — else crash windows lose/double learning.
5. Randomized experiment harness with cost-per-verified-success metric (PR 3) — else sequential-era comparison confounds policy effect with drift (D2).
6. Single-replica enforcement + snapshot cadence runbook (C3) — else silent fork.

---

## Finding index

| ID | Severity | Status | Title |
|---|---|---|---|
| A1 | P0 | CONFIRMED, fixed Phase 3 | Non-atomic decision assembly in `Router.ServeHTTP` |
| A2 | P1 | Confirmed (latent) | `sampled_scores` not always selection probabilities |
| A3 | P0 | CONFIRMED (design-level) | Hardcoded `LoggingPolicyID` mislabels non-Thompson traffic |
| A4 | P2 | Noted invariant | `ConfigHash` lock-then-hash |
| A5 | P1 | Confirmed | Rust core has no concurrency story |
| B1 | P1 | Confirmed | Transport vs task success conflated |
| B2 | P1 | Confirmed | Gateway drops quality/cache signals |
| B3 | P0 | CONFIRMED (missing capability) | No late/corrected/versioned/UNKNOWN outcomes |
| B4 | P2 | Documented behavior | NaN scores perfect |
| C1 | P1 | Confirmed, accepted-by-design | Crash window diverges memory vs ledger |
| C2 | P1 | Confirmed | Cross-restart double-learn; unbounded dedup map |
| C3 | P1 | Confirmed, documented | Multi-replica unsupported but unenforced |
| C4 | P2 | Noted | `MemoryStore` lock-free; ULP-lossy snapshots |
| D1a/b/c | P1/P1/P2 | Confirmed | OPE diagnostic denominator mixing; bootstrap scope; greedy tie-break coupling |
| D2 | P1 | Confirmed | No shift defense in experiment comparison |
| D3 | P2 | Acceptable | Shadow means without refusal gate |
| E1/E2 | — | Inventory | Integration surface + pilot gap list |

Phase 3 implements the A1 fix (atomic decision snapshot + evidence consistency), selected because it is the only P0 that is (a) confirmed by code reading, (b) a pure consistency fix with no algorithm or schema change, and (c) reviewable in one PR. A3 and B3 are larger (policy-ID derivation touches OPE trust; corrections need the contract) and are sequenced as PR 2.
