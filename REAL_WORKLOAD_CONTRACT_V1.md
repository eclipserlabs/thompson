# REAL_WORKLOAD_CONTRACT_V1

Version: `real-workload-contract-v1` (implemented in `go/cmd/exp-run/workload_contract.go`).
Status: general integration contract. No customer-specific integration exists:
without an actual customer specification, only this contract and the
read-only feasibility assessment are implemented.

This contract prepares Thompson for a first real-workload pilot **without
changing Thompson's learning algorithm**. The learner stays cost-blind
(binary task status → posterior); economics are evaluated offline over
fully-loaded costs per verified success.

## 1. Control boundary

The customer retains control over:

| # | Customer-controlled | Thompson interface |
|---|---|---|
| 1 | Job execution (their runners, models, providers) | Thompson receives attempt tapes, never executes customer jobs |
| 2 | Approved models and providers | `eligible_strategies` + per-attempt `executor_id` |
| 3 | Fallback behavior (retries, human review) | Full attempt tape incl. `human-pool` steps; nothing rewritten |
| 4 | Independent verification | `verified` / `verified_by` per attempt; Thompson never self-verifies |
| 5 | Quality thresholds | Frozen `quality_floor` in the pilot config; winners below it are `NOT_RANKABLE` |
| 6 | Cost measurement | Per-attempt `cost_usd` + `human_review_cost_usd`; missing stays missing |
| 7 | Data retention | Stable references/hashes preferred; raw prompts retained only per §5 |

Thompson must receive, per job version:

| # | Field | Rule |
|---|---|---|
| 1 | Stable job identifiers | `job_id` stable across exports; one job enters one analysis exactly once |
| 2 | Workload and task classifications | `strata` (task class); empty counts as `unclassified`, never imputed |
| 3 | Eligible execution strategies | `eligible_strategies`; required for every pilot job |
| 4 | Actual execution attempts | Full `attempts` tape (transport + latency + cost + validation + verified) |
| 5 | Verified final outcomes | `status` ∈ `PENDING/UNKNOWN/ACCEPTED/REJECTED` + `deciding_attempt_id` + `verified_by` |
| 6 | Complete observed costs | Nil costs stay nil; fully-loaded cost = attempt sum + human cost, with unmetered count |
| 7 | Outcome corrections and provenance | `version`/`supersedes` chains; `provenance` and `verified_by` on every settled row |

## 2. Record schema

One JSON object per line (JSONL). Field semantics reuse
`go/outcome` (`Attempt`, `JobStatus`) so historical records and live
settlement (`POST /v1/outcomes`) speak the same language.

| Field | Type | Required | Notes |
|---|---|---|---|
| `job_id` | string | yes | Stable, unique per job (uniqueness is per `job_id`+`version` for corrections) |
| `strata` | string | no | Task classification; empty → `unclassified` |
| `eligible_strategies` | string[] | pilot: yes | Strategies the job could have run on |
| `executed_strategy` | string | no | Strategy that actually ran; empty blocks causal comparison |
| `assignment_probability` | number | pilot-randomized: yes | Recorded randomization probability |
| `assignment_provenance` | string | pilot-randomized: yes | Assigner domain/seed reference |
| `version` / `supersedes` | uint | yes | Start at 1; each correction increments by exactly 1 |
| `status` | enum | yes | `PENDING`, `UNKNOWN`, `ACCEPTED`, `REJECTED` |
| `attempts` | Attempt[] | yes | Contiguous `seq`, unique `attempt_id`s; costs may be null |
| `deciding_attempt_id` | string | settled only | Attempt whose verification determined the status |
| `human_review_cost_usd` | number | when human touched | Never imputed |
| `verified_by` | string | settled only | Independent verifier identity; `== executor` reads as self-verified |
| `verified_at` / `occurred_at` | RFC3339 | recommended | Maturation accounting |
| `provenance` | string | recommended | Source system / export identity |

Validation (`ValidateRecord`) rejects only structurally unusable rows
(bad chains, duplicate attempts, unknown enums, settled rows without a
decider or verifier). Missing measurements are valid input and surface in
the feasibility report as explicitly missing data.

## 3. Missing-data rules (frozen for pilots)

- Missing costs are **excluded**, never zero-filled or imputed.
- `UNKNOWN` outcomes are **censored**: excluded from learning and from
  metric numerators/denominators, covered by the worst-case sensitivity.
- `PENDING` jobs are open and never move any readout.
- Corrections supersede: latest version wins; exact duplicates are
  idempotent; conflicting duplicates and version gaps fail the assessment
  loudly instead of being repaired.
- Historical observations are **observational**: they are never presented
  as causal proof that Thompson outperforms an alternative policy.

## 4. Feasibility assessment

`exp-run feasibility --in records.jsonl [--out report.json] [--source NAME] [--format json|text]`

Read-only. Answers the ten questions (IDs, independent verification,
`UNKNOWN` share, cost completeness, retry/fallback observability,
eligibility + task mix, comparison support, cost variance, sizing data,
pilot blockers) as machine-readable JSON (`feasibility-v1`) plus a human
summary. Rejected rows are counted with reasons and never enter any
denominator.

## 5. Sensitive data

Prefer stable references and content hashes over retaining raw customer
documents or prompts. Verdicts and costs are auditable without payloads;
see `docs/design/PILOT_DATA_HANDLING_V1.md` for the mandatory protections
before any production-derived data is accepted.

## 6. Freezing a pilot

1. Export a versioned workload definition; record its `workload_version`.
2. Copy the example pilot config, fill all fields (see
   `go/cmd/exp-run/pilot_config.go`, `PilotConfig`).
3. Run `exp-run pilot-check --config pilot.json --manifest manifest.json`.
4. Sign the printed frozen hash (`acknowledgement.by/at/config_hash`).
5. `exp-run run --pilot-config pilot.json --acknowledge <hash> …` refuses
   to start on anything else.
