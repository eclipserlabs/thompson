# PR 3A Decision Lifecycle (Phase 1 — P0 contract resolution)

Traced on base `d6029af`. All line references are `go/gateway/router.go` unless
noted. This document resolves the six required invariants into concrete
mechanisms implemented in Phases 2–4.

## 1. Request trace (as-built)

1. **Identity** (`:158-165`): `canonicalID = generateCanonicalID()` (crypto-random,
   `:98-104`); optional external ID from `X-Decision-ID`/`X-Request-ID` (`:106-129`).
   No persistence yet; no idempotency.
2. **Selection** (`:173`): one atomic `SelectSnapshot` (PR 1) — eligible, chosen,
   true scores, per-arm posteriors, pulls, config+hash from a single instant.
3. **Decision persistence** (`:256`): `WriteDecisionStarted` (eligible, selected,
   scores, `posterior_before`, `eligible_arm_state`, derived logging-policy ID).
   Write failure → 500, no execution. **Gap:** the decision lives only in the
   evidence JSONL and process memory; there is no decision store, no lookup by
   ID, no survival contract across restart (evidence file *usually* survives,
   but nothing indexes it by decision and nothing prevents ID reuse conflicts).
4. **Execution** (`:276-313`): provider invoke; `success` = provider 2xx (or
   `false` on transport error, `:288-291`); HTTP status/body proxied to caller.
5. **Observation** (`:337`): `WriteExecutionObserved` (latency, success,
   tokens, cost). Failure → 500 **after the caller already got a response**.
6. **Learning** (`:343-366`): dead-code-adjacent duplicate guard (`:343`), then
   `computeReward(latency, success, cost)` (`:142-153,347`) → **`Record`
   (`:348`)** → `DecisionLearned`. This is transport-derived learning: HTTP
   200 with a garbage body learns success-flavored reward; a timeout learns
   confident failure. **This is the P0 being replaced in verified mode.**
7. **Shadow** (`:362+`): isolated RNG, budget-gated, diagnostics only — no
   `Record` anywhere in `shadow.go`. No change required except shared identity
   (done, PR 2).

Other learning paths: `middleware.go:80` `RecordOutcome` (thin-waist legacy);
`outcome/learner.go:247` (sanctioned verified fold). Cancellation
(client disconnect mid-flight) still executes steps 5–6 with whatever the
provider returned — transport outcome learned as task outcome.

## 2. Answers to the Phase 1 questions

- **Every path that can learn from transport:** (a) `ServeHTTP :348` — HTTP
  status/latency/cost, incl. timeout (`success=false` + worst-case latency) and
  cancellation remnants; (b) `middleware.go:80` — `Forward`-returned outcome
  with `Success` overwritten from HTTP status (`middleware.go:68`); (c) shadow
  execution: none (verify by grep — clean); (d) fallback/retry inside
  middleware (`:64-75`): retries collapse to one `outcome`, learned once —
  acceptable, stays legacy-only.
- **Decision identification/persistence/recovery today:** random ID per request,
  persisted once in evidence JSONL, never indexed, never re-loaded, never
  matched to anything later. Recovery = none.
- **Can PR 2's store+learner recover a gateway decision?** No — not yet. The
  outcome ledger keys on `job_id`, which the gateway never mints, persists, or
  returns. The join key does not exist. Phase 2 creates it.
- **Gaps vs OUTCOME_CONTRACT_V1:** (1) no `job_id`/`strategy_id` on decisions;
  (2) no decision store/lookup; (3) no settlement API; (4) no PENDING/UNKNOWN
  representation on the live path (timeout ⇒ confident failure, violating §1.3
  and invariant 6); (5) `DecisionStarted` additive fields
  (`selection_kind`, `policy_version`, …) deferred from PR 2 — now due;
  (6) no mode flag: legacy learning is unconditional.

## 3. Invariant resolution (binding for Phases 2–4)

1. **Committed decision ⇒ immutable identity + coherent snapshot.** Mechanism:
   new `DecisionStore` (gateway package, file-backed JSONL + fsync + `flock`,
   reusing `FileOutcomeStore` machinery patterns, separate file): `Commit`
   persists `{decision_id, job_id, strategy_id, selected, eligible,
   eligible_state, scores+score_kind, logging_policy_id, config_hash,
   policy_version, occurred_at}` and returns it; `Lookup(id)` reloads after
   restart. `Commit` is called with the PR 1 snapshot **before** provider
   dispatch; commit failure ⇒ 500, no execution (fail-closed).
2. **Durable outcome references a recoverable committed decision.**
   Settlement validates `decision_id` → `DecisionStore.Lookup` must hit (else
   reject); `job_id` must equal the decision's bound job (else reject).
   `job_id` derivation: deterministic from decision (`job-<decision_id>`)
   unless the caller supplies an idempotency key that maps to it — one job per
   decision in V1 (multi-attempt chains append attempts to the job's versions).
3. **Rejected/unknown IDs mutate nothing.** Settlement of an unknown
   `decision_id`, or any event failing `Validate`, returns an error before
   touching store, learner, or policy. Enforced by ordering: lookup →
   validate → `Submit` → `Apply` (via `outcome.Settle`).
4. **Verified mode never learns from HTTP status.** New `Mode` on the router
   (`legacy` default, `verified` opt-in): verified mode **deletes the
   `computeReward`+`Record` block from the request path** — the handler records
   transport observation only (attempt tape), returns `decision_id`+`job_id` to
   the caller, and learns exclusively via the settlement endpoint. HTTP 200 ⇒
   `transport: ok`; task verdict arrives only via settlement.
5. **No dual learning.** Mode is per-Router (single policy owner). Verified
   mode never calls `Record` outside `Learner.Apply`; legacy mode never
   touches the outcome ledger/learner. A runtime guard: the verified handler
   holds no direct `Record` call (assert by test + code review; `grep` gate in
   test). Mode switch does not reset posteriors; switching with unapplied
   ledger events requires `Resume` first (documented cutover).
6. **Ambiguous timeout ⇒ UNKNOWN until resolved.** Verified mode maps timeout/
   transport-error/cancelled execution to an initial `PENDING` job record
   (attempt tape persisted, no verdict); settlement to UNKNOWN/ACCEPTED/
   REJECTED arrives later with independent provenance. The timeout path learns
   nothing — verified by mandatory test.

## 4. Smallest viable amendment (no PR 2 redesign needed)

PR 2 is sufficient as-is: `OutcomeEvent` already carries `DecisionID`+`JobID`
+versions+attempts+provenance; `Submit`/`Settle`/`Resume`/checkpoint already
enforce the ordering this contract needs. The only additions are gateway-side:
`DecisionStore`, request-path plumbing, settlement endpoint, mode flag.
`policy_version` (contract §1.1) is implemented as a ledger-side monotonic
`seq` on the decision record, not a policy mutation counter — same
same-version assertion power for readers, zero changes to `thompson.Policy`.
