# Outcome Contract V1 (Design — partially implemented, see below)

Status: proposal, with a first implementation in `go/outcome` (PR 2 branch
`feat/durable-learning-v1`; storage/migration details in
`docs/engineering/PR2_DURABLE_LEARNING.md`). Two deviations from this text
were adopted during implementation and are normative for V1 code: (1)
REJECTED events require `deciding_attempt_id` like ACCEPTED (one job version
produces at most one arm update, against the deciding attempt's arm);
(2) the additive `DecisionStarted` fields (`selection_kind`, `sampler_id`,
`policy_version`, `strategy_id`, `job_id`) are deferred to router wiring
(PR 3) — `logging_policy_id` derivation itself is implemented. This document
remains the application-independent contract; it still defines no storage
engine and changes no sampling mathematics.

## 0. Terminology and non-goals

- A **job** is one unit of work the customer cares about (e.g. "answer this request well").
- A **strategy** is a rule for executing a job: an ordered policy over attempts (e.g. "try cheap model, on validation failure try strong model, else escalate to human").
- An **attempt** is one execution of the job against one executor (model/provider/human/tool).
- **Transport status** (did the call complete?) is never **task outcome** (did the job succeed?). The contract carries both, separately, always.
- Non-goals: changing the bandit update math; defining validation rules for any specific application; specifying storage engines; prescribing human-review UX.

## 1. Entities and required fields

### 1.1 Decision (immutable snapshot, written once at decision time)

| Field | Type | Notes |
|---|---|---|
| `decision_id` | string, canonical, RNG-generated | Primary join key for all events below |
| `external_request_id` | string?, caller-supplied | Idempotency key scope (see §5); validated charset/length as today (`router.go` extract rules preserved) |
| `occurred_at` | RFC3339Nano | Decision time |
| `eligible_arms` | string[] | Sorted arm IDs available at decision time |
| `eligible_arm_state` | {arm_id, alpha, beta, pulls}[] | One entry per eligible arm, same policy version (atomic snapshot — Phase 3 primitive) |
| `selected_arm_id` | string | `selected ∈ eligible`, enforced |
| `selection_scores` | map arm → float | Observability only; see §1.1.1 |
| `selection_kind` | enum `thompson \| ucb_regularized \| phased \| custom` | Drives `logging_policy_id` derivation (fixes audit A3) |
| `sampler_id` | string | e.g. `exact-v1`; part of logging-policy identity |
| `policy_version` | uint64 | Monotonic per-policy mutation counter (new; enables "same version?" checks) |
| `policy_config_hash` | string | Hash of the config that decided |
| `logging_policy_id` | string | Derived from (`selection_kind`, `sampler_id`), never hardcoded |
| `strategy_id` | string | Which execution strategy this decision instantiates (fixed policy / cheapest-satisfying / Thompson-adaptive, …) |
| `job_id` | string | Groups attempts + final status for one job (see §1.4) |

§1.1.1: `selection_scores` MUST be accompanied by `score_kind`: `beta_samples` (Thompson), `sample_plus_bonus` (UCB), `posterior_means` (phased-forced / custom). Consumers MUST NOT use scores as action probabilities; denominators come only from `eligible_arm_state` reconstruction (audit A2).

### 1.2 Attempt (one per execution, appended; never mutated)

| Field | Type | Notes |
|---|---|---|
| `attempt_id` | string | Unique per attempt |
| `decision_id` / `job_id` / `strategy_id` | strings | Join keys |
| `seq` | uint, 0-based | Order within the job |
| `executor_id` | string | Model/provider/tool/human-pool identity, e.g. `openai/gpt-4o-mini` |
| `arm_id` | string | Bandit arm this attempt maps to (may differ from `selected_arm_id` for fallback/human steps, which may map to no arm) |
| `transport_status` | enum `ok \| timeout \| transport_error \| body_too_large \| cancelled` | What happened on the wire |
| `latency_ms` | float | Measured; NaN forbidden at the contract boundary (rejected or mapped to missing, cf. audit B4) |
| `cost_usd` | float? | Null = unmetered, never zero-synthesized |
| `tokens_in/out` | int? | Null = unknown |
| `validation` | enum `not_run \| pass \| fail` + `failure_category`? | Application validator verdict, independent of transport |
| `field_corrections` | [{field, before, after}]? | Optional field-level fixes applied by validator/human (diagnostic, not reward) |
| `verified_outcome` | enum `success \| failure \| unknown` + `verified_by` + `verified_at` | Independent task verdict; see §1.3 |
| `provenance` | {source, source_version, detail?} | Who verified and how |

### 1.3 Verified outcome (the only thing that may move the policy)

- `verified_outcome ∈ {success, failure, unknown}` is set by a party **independent of the executor** (validator, judge model different from executor, human, deterministic checker).
- `verified_by`: `validator:<name>:<version> | judge:<model>:<version> | human:<pool> | checker:<name>`; `verified_at`: timestamp.
- Self-verification (executor == verifier with no independent check) MUST be labeled `verified_by: self` and treated as `unknown` for learning purposes.
- `unknown` covers: ambiguous timeout (commit state unknowable), validator abstention, missing verification past the deadline, contradictory verifiers.

### 1.4 Job (terminal status derived, not asserted per attempt)

- `job_id` groups all attempts of one job. Derived final status:
  - `ACCEPTED`: at least one attempt has verified `success`, and no later superseding correction revokes it.
  - `REJECTED`: verification completed (all configured stages ran or budget exhausted) with no verified `success`.
  - `UNKNOWN`: verification never completed (deadline passed, validator down, contradictory evidence) — censored.
- The job record carries: `attempt_ids[]`, `final_status`, `deciding_attempt_id` (which attempt's verification determined the status), `human_review_cost_usd?`, `total_retry_cost_usd` (sum of attempt costs), `total_latency_ms` (wall), `outcome_version` (see §4).

### 1.5 Costs

- Per attempt: `cost_usd` (inference), share of `latency_ms`.
- Per job: `total_retry_cost_usd = Σ attempt costs` (null-aware: null propagates to null total with `unmetered_attempts` count, never zero-filled); `human_review_cost_usd` when a human touched the job (review minutes × rate, or flat per-review price — recorded, not imputed); `total_latency_ms` wall-clock.
- Fully loaded cost per verified successful job (pilot primary metric) = Σ job costs / #ACCEPTED jobs, at the agreed quality floor (see roadmap PR 3).

## 2. Which event changes the policy, and when

1. Only a **`JobSettled` event** (job reaches ACCEPTED/REJECTED with a verified outcome at `outcome_version = v`) may move the bandit posterior, and only via the arm mapped from the **deciding attempt**.
2. Transport/session events (`DecisionStarted`, attempt records, `ExecutionObserved`) NEVER move the policy directly. The current behavior — learning from transport success inside the request path — is retained only as a deprecated compatibility feed (see §6) and MUST be labeled `provenance: transport-deprecated`.
3. Learning applies **exactly once per (`job_id`, `outcome_version`)**. The learner keeps a durable `applied_outcomes(job_id) → version` cursor (in snapshot + ledger).
4. `UNKNOWN` jobs are **excluded** from learning and from IPS/OPE numerators (they contribute neither reward nor weight). They are still counted in coverage/diagnostic reports as censored mass. Optional sensitivity analysis may bound their effect (e.g. Lee-style worst-case assignment) but never silently imputes them.
5. Attempt-level signals (per-attempt latency, validation failure category, field corrections) are **diagnostic attribution only**: they explain *why* a strategy cost what it did. Strategy-level reward = f(final job status, total cost, total latency, quality floor). A successful fallback proves the **strategy** recovered, never that a preceding attempt "partially succeeded" — failed attempts keep their `failure` verdict in diagnostics.

## 3. Corrections (late and otherwise)

- Outcomes are **versioned, append-only**. A correction is a new `OutcomeVersioned` event: (`job_id`, `outcome_version = v+1`, supersedes `v`, new verified outcome + provenance + `corrected_at`).
- The learner applies the delta exactly once: conceptually un-apply v (inverse of the update if supported) or, simpler and preferred, **rebuild-from-ledger**: policy state = fold of latest version per job. Recommended: folding latest-version-wins over the ledger is the canonical state; snapshots are caches.
- Corrections that arrive after an evaluation window closed do NOT rewrite published estimates; they appear in the next window with a `correction_received` marker. Evaluation windows are defined by `verified_at`, not arrival time.
- `field_corrections` (validator fixed a field and the job then succeeded) are recorded on the attempt as diagnostics; they do not change that attempt's `failure` verdict — the attempt as executed failed; the strategy recovered.

## 4. Unknown / censored jobs in evaluation

- Excluded from IPS/SNIPS/ESS (both numerator and denominator counts), reported as `censored_fraction` alongside `excluded_fraction`.
- Rankability-style gate: if `censored_fraction` exceeds an agreed threshold (suggest 0.05, tune per pilot), the comparison is `NOT_RANKABLE` pending investigation — heavy censoring is a data-quality failure, not a policy result.
- Censoring correlated with arms (e.g. slow arm times out more → more UNKNOWN) MUST be reported per arm; per-arm censoring rates are part of the experiment readout.

## 5. Idempotency

- `external_request_id` (caller key) + `job_id` derivation is deterministic: same key → same `job_id`. Retried submissions with the same key append attempts to the existing job; they never create a second job.
- Learner dedup key is (`job_id`, `outcome_version`). The durable applied-cursor makes re-delivery safe across restarts (fixes audit C2).
- The current in-process `recorded` map becomes a bounded hot cache in front of the durable cursor, not the correctness mechanism.

## 6. Compatibility with existing interfaces (requirements for PR 2, not changes made here)

1. Existing JSONL event names (`DecisionStarted`, `ExecutionObserved`, `DecisionLearned`, shadow events) keep their names and field meanings; new fields are additive (`omitempty` where readers are strict). No silent redefinition: `Success` keeps meaning transport/session success wherever it exists today.
2. `eligible_arm_state` + `eligible_arm_ids` consistency rules in `ToBanditLogWith` (`ope.go:81-102`) remain valid inputs; the contract adds `policy_version` so future readers can assert same-version snapshots.
3. `Snapshot{version:1}` wire format (`protocol/SPEC.md`, `schema.json`) is unchanged by this design; the applied-outcomes cursor ships as a separate ledger-derived structure first, snapshot-embedded later with a version bump.
4. The gateway's current learn-on-transport path must keep working (behind its current behavior) until the `JobSettled` learner lands, with its ledger entries explicitly marked deprecated-provenance so evaluators can separate the two feeds.
5. Unknown-status rows must flow through `ToBanditLogWith`/`EvaluateOPE` as ineligible-with-reason (like today's `MC_ZERO_WINS` refusal pattern), never as zero reward.

## 7. Worked examples

### Example 1 — first attempt succeeds

1. `Decision` d1: eligible `[cheap, strong]`, selected `cheap`, scores `beta_samples`, `strategy_id: thompson-adaptive-v1`, `job_id: j1`.
2. `Attempt` a1 (seq 0, executor `cheap`, arm `cheap`): transport `ok`, 320 ms, $0.0004, validation `pass`, verified `success` by `checker:schema-v3` at T+1s.
3. `JobSettled` j1: ACCEPTED, deciding attempt a1, outcome v1, total cost $0.0004.
4. Learner applies (j1, v1) once → arm `cheap` posterior update with strategy reward r(ACCEPTED, cost, latency).
5. Diagnostics: attempt attribution = strategy attribution (single attempt). No imputation anywhere.

### Example 2 — two model failures, then human correction

1. `Decision` d2 → `job_id: j2`, selected `cheap`.
2. `Attempt` a1 (executor `cheap`): transport `ok`, validation `fail` category `schema_violation`, verified `failure` by `validator:v7`.
3. Strategy retries per policy: `Attempt` a2 (seq 1, executor `strong`, arm `strong`): transport `ok`, validation `fail` category `factual_error`, verified `failure` by `judge:strong-v2` (independent of executor — allowed).
4. Escalation: `Attempt` a3 (seq 2, executor `human-pool-b`, arm: none): human-corrected answer, validation `pass`, verified `success` by `human:pool-b` at T+40min, `human_review_cost_usd: $2.10`.
5. `JobSettled` j2: ACCEPTED, deciding attempt a3, total retry cost $0.0004+$0.02+$2.10, outcome v1.
6. Learning: strategy-level reward reflects ACCEPTED-at-high-cost (cheap for the *quality floor* comparison, expensive per job — the pilot metric captures both). Arm updates: per §2, only the deciding attempt's arm mapping moves a posterior — a3 maps to **no arm**, so this job contributes **no arm-level Beta update** while contributing fully to strategy-level cost/quality accounting. a1/a2 keep verified `failure` in diagnostics; nothing about a3's success rewrites them.
7. Anti-claim pinned: the ledger MUST NOT be read as "cheap succeeded" or "strong succeeded" — both failed; the strategy recovered via human.

### Example 3 — ambiguous timeout, then late verified outcome

1. `Decision` d3 → `job_id: j3`, selected `strong`.
2. `Attempt` a1: transport `timeout` at 30 s (server-side commit state unknowable), verified `unknown` by `system:timeout-policy` (self-verification → unknown per §1.3).
3. No retry budget left / caller deadline: `JobSettled` j3 initial: UNKNOWN, outcome v1. Learner applies nothing; OPE excludes j3, counts it in `censored_fraction`.
4. T+6min: provider-side log reconciliation shows the write committed and the result was correct; `OutcomeVersioned` (j3, v2, verified `success` by `checker:reconciler-v1`, `corrected_at` T+6min).
5. Learner applies (j3, v2) once (latest-version-wins). The evaluation window that already closed keeps v1 (UNKNOWN, excluded) with a `correction_received` marker; the next window evaluates v2.
6. Contrast with today: the timeout would have been learned immediately as confident failure (`success=false` + worst-case latency). Under this contract, ambiguity is preserved until evidence exists.
