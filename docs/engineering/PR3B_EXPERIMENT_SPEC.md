# PR 3B Experiment Spec (DESIGN ONLY — not implemented in PR 3A)

## 1. Question and arms

Does adaptive Thompson routing lower the **fully loaded cost per verified
successful job at an agreed quality floor** versus static alternatives?

Three concurrent treatments, assigned per job by an external randomizer (not
by the bandit):

- **T0 fixed:** the customer's existing static routing policy. Reference for
  both quality and cost.
- **T1 cheapest-satisfying:** the cheapest static strategy that meets the
  agreed quality threshold (e.g. cheapest model with validation + bounded
  retries). Cost-floor reference.
- **T2 thompson-adaptive:** the PR 3A verified-mode gateway learning from
  settled outcomes only.

Each treatment runs as its own `strategy_id` on the same gateway binary with
its own decision/outcome files (or a shared file with `strategy_id`
partitioning — implementation choice, PR 3B). Treatments never share a policy
object or learner cursor.

## 2. Primary metric (business, offline)

```
fully_loaded_cost_per_verified_success =
    Σ over jobs in window (inference + retries + validation + human_review, where observable)
    / # jobs with final status ACCEPTED at quality ≥ floor
```

- Costs come from settled attempt tapes (`cost_usd`, `human_review_cost_usd`);
  unmetered attempts propagate as unmetered counts, never zero-filled
  (contract §1.5). Jobs with null totals are reported separately, not imputed.
- Quality floor: pre-registered (e.g. validator pass rate ≥ X on a held-out
  audit sample). ACCEPTED below the floor does not count as success.
- Censored (UNKNOWN) jobs are excluded from numerator and denominator and
  reported as `censored_fraction` overall and per treatment; per-arm
  censoring rates are part of the readout (contract §4).

## 3. Cost-sensitive optimization vs the cost-blind learner (explicit)

PR 3A's `BinaryStatusMapper` (ACCEPTED→1, REJECTED→0) is deliberately
cost-blind: the bandit learns *quality*, and cost is accounted offline. Two
consequences for PR 3B:

1. T2 optimizes success probability, not cost directly. The experiment tests
   whether success-probability learning *plus* the strategy's own retry/
   fallback structure beats static strategies on cost — a fair test of the
   shipped system, not of a hypothetical cost-aware bandit.
2. A cost-aware mapper (reward as a function of status + cost) is **not**
   specified here and must not be smuggled into PR 3B as a "tuning" change:
   it alters the estimand. If later proposed, it needs its own contract
   amendment, offline counterfactual evaluation, and a fresh experiment.

## 4. Randomization and validity

- **Unit:** one job (`job_id`). Randomize per job, concurrently across all
  three treatments for the whole window — never sequential eras (audit D2:
  drift confounds era comparisons).
- **Overlap:** all treatments eligible for the same job mix; log the
  assignment probability per job (stratified if traffic classes differ).
- **Analysis:** difference in primary metric T2−T0 and T2−T1 with bootstrap
  95% CIs over jobs (clustered by any session/customer grouping); IPS/SNIPS
  diagnostics over the T2 ledger as a consistency check (not the decision
  metric); rankability-style refusal when overlap/precision/censoring gates
  fail — including `censored_fraction > 0.05` (tunable) as NOT_RANKABLE.
- **Stopping:** pre-registered window (jobs or calendar time, whichever first)
  plus a minimum usable-jobs floor per treatment; no peeking-driven early
  stopping without an alpha-spending rule declared up front.
- **Shift monitoring:** reward-by-time and eligibility-mix plots per
  treatment; a detected mid-window distribution shift triggers a
  non-stationarity flag, not a winner declaration (audit D2).

## 5. Minimal external interface (to be built in PR 3B)

1. `POST /route` (exists): returns `decision_id` + `job_id` (+ chosen arm).
2. `POST /v1/outcomes` (exists, PR 3A): accepts PENDING/UNKNOWN/ACCEPTED/
   REJECTED with attempt tape + provenance.
3. New in PR 3B only: treatment assignment + strategy plumbing (randomizer,
   per-treatment `strategy_id` files, offline metric job). No new reward
   formula, no new benchmark, no gateway replacement.

## 6. Entry criteria (PR 3A must hold)

Verified mode green on all Phase 5 mandatory tests; single-writer deployment
per §2 of PR3A_OPERATIONS; verification pipeline (validator/judge/human)
producing provenance-labeled outcomes with measured censoring; quality floor
agreed and auditable. If any fails, PR 3B does not start.
