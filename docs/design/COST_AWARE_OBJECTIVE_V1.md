# Cost-Aware Objective V1 — Technical Design

## A. Success and quality

### Independently verified job acceptance
A job is a verified success iff a settled `OutcomeEvent` has `status=ACCEPTED` with a named `deciding_attempt_id` whose attempt carries `verified=success` from an independent verifier (`VerifiedBy` ≠ executor), and no later superseding version revokes it. Verification provenance (`verified_by/at`, attempt `verified` vector, `deciding_attempt_id`) is preserved in the ledger. Transport, validation, and verified outcome are never conflated (see `OUTCOME_CONTRACT_V1.md`).

### Status taxonomy
- **Transport success** (`transport=ok`): bytes moved. Measurement only; never a task verdict, never learns.
- **Task acceptance** (`status=ACCEPTED`): independent verifier confirms task done. Only this (and REJECTED) moves the policy.
- **Partial task completion**: modeled as `REJECTED` with diagnostic `field_corrections` / `failure_category`, or as a new attempt in a fallback chain; never as fractional ACCEPTED in v1. Fractional credit is explicitly out of scope (would require changing frozen reward semantics).
- **Human-corrected completion**: attempt with `executor_id=human-pool` plus `human_review_cost_usd` recorded (never imputed). If the human fix verifies, status is ACCEPTED with full chain cost preserved.
- **UNKNOWN / unresolved** (`status=UNKNOWN`, `PENDING`): verification abstained, timed out ambiguously, or never completed. Censored: excluded from learning numerators and OPE; counted in `CensoredFraction` and gated (`censoring-*`).

### Which statuses learn
`BinaryStatusMapper` (and v1 cost-aware mapper): ACCEPTED→learn, REJECTED→learn, UNKNOWN/PENDING→`learn=false` (cursor advances, posterior untouched). Armless attempts (empty `ArmID`, e.g. human fallback) contribute to strategy accounting, never to an arm posterior.

### Quality floor (constraint, not price)
`ReportConfig.QualityFloor` (e.g. 0.30 pilot, pre-registered per experiment) is a hard gate: `quality-floor-<tx>` fails any treatment with `accept_rate < floor` over matured jobs. The optimizer may not trade quality for price silently: any preference for a cheaper arm below the floor is a violation, and the report verdict refuses `RANKABLE` conclusions when gates fail. Quality is enforced at selection time (v1 policy gate) and at analysis time (report gate); the two are independent checks.

## B. Fully loaded cost

Per-job fully loaded cost (v1 definition, additive, USD):

```
fully_loaded(job) = Σ_attempts metered CostUSD
                  + HumanReviewCostUSD (if present)
```

Components mapped:
- Model inference: attempt `CostUSD` on model attempts.
- Retry: each retry is a separate attempt; sum counts it exactly once.
- Fallback execution: fallback attempts carry their own `CostUSD` + `ArmID` (or empty for non-bandit fallback); summed once.
- Validation: validator cost, when billed, is an attempt with executor `validator-*` or folded into the deciding attempt's cost by the application (must be explicit; no hidden imputation).
- Human review: `HumanReviewCostUSD` top-level (never imputed; missing + human attempt → unmetered, never zero).
- Other application costs: reported as additional attempts or explicit cost fields; unknown schema → unmetered, never zero.

### Missing-cost discipline
`jobCost()` returns `(metered, unmetered_count)`. Any nil `CostUSD` or nil `HumanReviewCostUSD` alongside a human attempt increments unmetered. Missing is preserved as missing:
- Never converted to zero or to a favorable reward.
- Excluded from cost means; tracked as `UnmeteredJobs` / `unmetered share`.
- `missing-cost-*` gate fails treatments above `MaxUnmeteredShare` (default 0.10).
- Primary metric `primaryOf` uses fully-metered matured jobs only; sensitivity bounds use `MaxPlausibleCost`/p90 as bounded sensitivity, never as silent fill.
- The v1 policy fails explicitly (refuses cost-aware selection) when required cost data is unavailable for a candidate arm, rather than falling back silently to cost-blind while retaining cost-aware identity.

## C. Objective

### Candidate formulation
Minimize expected fully loaded cost per verified successful job, subject to a pre-registered quality floor:

```
minimize  E[cost | arm] / max(E[success | arm], eps)
subject to E[success | arm] >= floor  (with uncertainty handling)
```

Primary metric (already implemented): metered spend on ACCEPTED+REJECTED / verified successes. Secondary: verified success rate, cost/assigned, retry/fallback frequency, human-review burden, censoring, missing-cost rate, p95 latency, exploration cost, recovery/settlement overhead.

A naive scalar reward (e.g. `success - λ·cost`) is rejected for v1: λ is unprincipled across workloads, unbounded costs break Beta-Bernoulli conjugacy, and a single scalar lets price silently override the floor.

### Method 1 — Quality-constrained selection with separate success and cost estimates (SELECTED for v1)
- Success: existing Beta-Bernoulli `Policy` per arm, unchanged math, unchanged `Record` path.
- Cost: new versioned per-arm cost ledger (`CostStateV1`: `metered_sum`, `metered_n`, `unmetered_n`, mean; optionally bounded variance), updated only on fully-metered settled jobs, correction-safe (rebuild on supersede, idempotent on duplicate delivery).
- Selection: (i) quality gate — arm qualified iff posterior mean (or Thompson sample, see below) ≥ floor, or arm is cold (`pulls < bootstrap`, explicitly explorable); (ii) among qualified, minimize `mean_cost / max(success_sample, eps)`; if none qualified, fall back to cost-blind Thompson sample (logged, counted as `quality-fallback` exploration, never mislabeled cost-aware-optimal).
- Identity: distinct `PolicyID` (e.g. `thompson-costaware-v1`), objective version `cost-aware-v1`, config hash bound to every decision snapshot.

Why smallest: reuses the audited sampler and `BinaryStatusMapper` semantics for quality; cost state is additive, independently checkpointed, deterministically replayable; no change to historical posteriors; failure mode is explicit (unqualified / unmetered → refuse or fallback-logged).

### Method 2 — Bounded cost-sensitive reward with explicit quality safeguards (REJECTED for v1)
`reward = success · (1 − normalized_cost)` in [0,1] folded into the same Beta posterior, plus a hard floor veto. Rejected: forces an incompatible (cost-scaled, non-Bernoulli) signal into a Beta-Bernoulli posterior, destroying conjugacy interpretation and replay comparability; normalization bounds (`Target/MaxCostUSD`) are workload-fragile; a single scalar still admits floor erosion under mis-set bounds; migration of old outcomes would silently change their meaning. Documented here as the considered-and-rejected alternative.

### Analysis of hard cases
- **Delayed outcomes**: learn only on settlement; pending jobs excluded; maturation window + `MatureJobs` enforce settlement lag; late corrections trigger rebuild, not incremental patch.
- **Censored outcomes**: UNKNOWN excluded from both estimators; `CensoredFraction` gated; cost-aware selection never treats censored as success/failure.
- **Cost outliers**: means use fully-metered jobs only; report sensitivity with p90/`MaxPlausibleCost` bounds; v1 does not winsorize the learner (explicit non-goal; documented).
- **Unequal task difficulty**: randomization (assignment RNG independent from policy RNG) + comparable eligible workload per treatment + allocation check (A3) + job-map stratification; no per-difficulty modeling in v1.
- **Exploration safety**: cold-start arms explicitly qualified with capped pulls (`Bootstrap`/`MinPullsForExploit`/`PhasedSelection` where configured); quality-fallback path logged; no UCB bonus unless approximate sampler warrants it.
- **Cold starts**: new arm → qualified-by-insufficiency with cost estimate marked absent → selected only via exploration quota, never as cost-optimal until metered_n ≥ min.
- **Distribution shift**: frozen experiment manifest + content hash; shift detected via allocation/censoring/quality gates refusing verdicts, not via adaptive re-tuning mid-experiment.
- **Feedback delay**: maturation window; `T0Clock`/`Step` sim clock; late-verifier behaviors (`delayed-accept`) exercised in fixtures.
- **Failure/retry accounting**: every attempt costed once; fallback chains verified end-to-end (test 3); duplicate delivery idempotent (test 7).

### Assumptions and failure modes
Assumes: independent verifier; additive USD costs; missingness is observable (nil vs zero); assignment ignorability via RNG separation. Fails explicitly on: missing cost above gate, all-arms-below-floor, nil RNG, unknown arm, malformed cost (NaN/Inf/negative → reject, test: malformed inputs). Residual risks: unobserved confounders in task difficulty; human-cost under-reporting; cost-mean gaming via unmetered attempts (mitigated by unmetered gate, not eliminated).

### v1 scope lock
Implement Method 1 only. No scalar-reward path, no fractional credit, no winsorization, no multi-replica coordination, no hosted control plane.
