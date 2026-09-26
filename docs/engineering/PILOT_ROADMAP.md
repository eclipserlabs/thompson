# Pilot Roadmap (Phase 4)

Goal: a production-safe adaptive execution experiment for one customer, comparing three strategies on **fully loaded cost per verified successful job at an agreed quality floor**. Each PR below is independently reviewable, with exact modules, schema/API changes, compatibility, acceptance tests, failure modes, and non-goals.

Baseline: `62f3de6`. Audit: `docs/audit/CORRECTNESS.md`. Contract design: `docs/design/OUTCOME_CONTRACT_V1.md`.

---

## PR 1 — Atomic decision snapshot + evidence consistency (Phase 3, DONE on `fix/atomic-decision-snapshot`)

Fixes audit A1 (P0, confirmed).

- **Modules/interfaces.**
  - NEW `go/thompson/decision_snapshot.go`: `DecisionSnapshot{Eligible, Selected, Scores, Posteriors, TotalPulls, Config, ConfigHash}` + `Policy.SelectSnapshot(rng)` (single-lock; same locked argmax helpers and RNG stream as `SelectWithScores`).
  - `go/thompson/policy.go`: extracted `hashConfig` (behavior-identical; `ConfigHash` delegates).
  - `go/gateway/router.go` `ServeHTTP`: decision phase is one `SelectSnapshot` call; `eligible_arm_state` built from the snapshot; reward computed from `snap.Config.Reward` (decision-time config, immutable today).
  - Tests: `go/thompson/decision_snapshot_test.go` (legacy-path equivalence across Thompson/UCB/phased; empty-policy error; concurrent Record + AddArm/RemoveArm coherence, `-race`), `go/gateway/router_snapshot_test.go` (64 parallel requests + concurrent `Record` traffic asserting per-decision `DecisionStarted` coherence and execution/learning joins; ordering preservation).
- **Schema/API changes.** None. `DecisionStarted` field values are now mutually coherent; no field added, removed, or redefined. All existing public APIs preserved (`SelectWithScores`, `PosteriorFor`, `ConfigHash`, `EligibleArmIDs` untouched).
- **Migrations/backward compatibility.** None required. Snapshots/ledger written before or after are byte-compatible.
- **Acceptance tests.** `go test -race ./thompson/ ./gateway/` green (incl. the two new test files, `-count=3` for flake check); `TestSelectSnapshotMatchesLegacyPath` proves zero algorithm/RNG-stream change; `cargo test -p thompson-sampling -p thompson-sim` unaffected.
- **Operational failure modes.** None new. A selected arm removed before `Record` still fails learning with an error (pre-existing async-learn race, now documented; durable fix is PR 2's commit protocol).
- **Non-goals.** Logging-policy identity (A3), late corrections (B3), durable commit (C1/C2), any reward/selection math change, Rust-side helper, any `gofmt`/formatting cleanup beyond the touched hunks (verified: zero new `gofmt` diffs).

## PR 2 — Durable decision/outcome contract and replay

Fixes audit A3 (P0 design-level), B3 (P0 missing capability), C1/C2 (P1), D1a (P1).

- **Modules/interfaces.**
  - `go/gateway/evidence.go`: additive `DecisionStarted` fields — `selection_kind`, `sampler_id`, `policy_version`, `score_kind`, `strategy_id`, `job_id` (all `omitempty`-safe for old readers); new `JobSettled` + `OutcomeVersioned` event types per `docs/design/OUTCOME_CONTRACT_V1.md` §§1–5.
  - `go/thompson/policy.go`: monotonic `policy_version` counter (bumped on every `Record`/membership change, included in snapshots); `Record` gains a versioned, idempotent path keyed by (`job_id`, `outcome_version`) with a durable applied-cursor persisted in `FileStore` snapshots.
  - `go/gateway/router.go`: derive `logging_policy_id` from (`selection_kind`, `sampler_id`) — `exact-thompson-v1` only for exact+Thompson, gate-rejected IDs otherwise; unify shadow `PrimaryLoggingPolicyID` string; learn **only** from `JobSettled` (transport feed kept, labeled `provenance: transport-deprecated`).
  - `go/gateway/replay.go` + `ope.go`: UNKNOWN/censored exclusion with `censored_fraction` + per-arm censoring rates; report retained-share alongside ESS-over-retained (D1a); document bootstrap scope (D1b).
- **Schema/API changes.** Additive JSONL fields; two new event types; snapshot gains `policy_version` + applied-cursor (snapshot `version` stays 1 if the cursor ships ledger-side first — decide in PR; if embedded, bump with dual-read).
- **Migrations/backward compatibility.** Old readers ignore new fields; `ToBanditLogWith` keeps accepting pre-contract rows; contract rows with non-Thompson IDs are refused as denominators exactly like today's `MC_ZERO_WINS` path.
- **Acceptance tests.** Correction test (v1 then v2 applies once, latest-wins); duplicate-delivery across simulated restart applies once; UNKNOWN jobs excluded from IPS/ESS and counted; crash-injection between `Record` and ledger append reconciles to equality on restart; non-Thompson configs emit gate-rejected IDs.
- **Operational failure modes.** Reconcile-on-startup latency (bounded by ledger scan); cursor growth (compact on snapshot); correction storms (version cap + alert).
- **Non-goals.** Human-review UX, validator implementations, multi-replica shared state, changing IPS/SNIPS math.

## PR 3 — Minimal external integration + randomized experiment harness

Closes audit E2; guards D2/D3.

- **Modules/interfaces.**
  - `go/gateway/middleware.go` (+ example): verified-outcome ingress — `Forward` returns transport outcome; a new `ReportVerifiedOutcome(job_id, verdict, provenance)` completes the job; thin-waist stays two calls plus one optional verification call. `examples/` gains a minimal external-app adapter (extend `examples/thin_waist.go` pattern).
  - `go/harness/` + `go/opeval/`: randomized interleaving harness — traffic randomly assigned to the three strategies **concurrently** (never sequential eras), pre-registered window, shift diagnostics (reward-by-time, availability overlap), rankability-style refusal including `censored_fraction` gate.
  - Metric: fully loaded cost per verified successful job = Σ(inference + retries + validation + human review where observable) / #ACCEPTED, subject to quality floor; HTTP success never substituted.
- **Schema/API changes.** One new client-facing method (`ReportVerifiedOutcome`); harness CLI flags. No ledger changes.
- **Migrations/backward compatibility.** Opt-in; existing gateway behavior unchanged when verification is absent (jobs stay UNKNOWN/censored, excluded — pilot must wire verification to get a readout).
- **Acceptance tests.** End-to-end pilot dry run on synthetic + shadow traffic: three strategies interleave, metric computed, shift-injection test flags non-stationarity instead of declaring a winner, low-coverage output refuses rather than ranks.
- **Operational failure modes.** Verification-lag censoring (dashboard on `censored_fraction`); misconfigured quality floor (harness validates floor observability before starting); single-replica enforcement (file lock on evidence path, per C3).
- **Non-goals.** Dashboard/SaaS, new gateway, new bandit algorithm, multi-tenant billing, cloud deployment.

## Strategy comparison (PR 3 experiment)

| Arm | Description |
|---|---|
| Fixed | Customer's existing static routing policy (baseline; defines the quality floor reference) |
| Cheapest-satisfying | Cheapest strategy meeting the agreed quality threshold (cost floor reference) |
| Thompson-adaptive | Thompson policy over the same arms, learning from verified `JobSettled` outcomes only |

Randomize per job across all three concurrently. Primary metric: fully loaded cost per verified successful job at the agreed quality floor (inference + retries + validation + human review where observable). Secondary: quality margin above floor, latency distribution, censoring rate per arm. Decision rule: pre-registered; refused (`NOT_RANKABLE`-equivalent) on insufficient overlap/precision/excess censoring — never a winner declared from warnings alone.
