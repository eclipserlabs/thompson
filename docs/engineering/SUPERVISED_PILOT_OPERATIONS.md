# Supervised Pilot Operations (V1)

## Tested operating assumptions (single-writer experimental)
- One gateway process per treatment; no replica coordination. Second writer
  fails loudly (flock) on decisions/outcomes; safety log has no flock (add
  before multi-process operation — currently single-writer by deployment).
- Sequential traffic per gateway is assumed for budget exactness; concurrent
  requests are race-safe (mutex-ordered) but share budgets.
- Measured (darwin arm64, httptest loopback, FakeProviders): decision
  13.2ms (fsync-dominated: decision commit + evidence write), monitor fold
  46µs per arm over ~20 outcomes (linear in ledger size — re-evaluated per
  settlement), checkpoint 19ms (quality + cost sidecar), recovery 389µs,
  ~1.2KB/decision, ~0.6KB/outcome, safety log 0B on healthy runs (events
  only on transitions). Linux numbers: not measured here (no Linux env);
  fsync latency dominates, so expect similar shape, different constants.
- Maximum tested throughput: correctness suites only (not load-tested).
  Settlement throughput ≈ 1/(fsync × writes per settle) — order 50–100
  jobs/s on this machine. Do not exceed single-writer fsync capacity;
  backpressure is fail-closed (503/500), never silent.

## Failure matrix
| Failure | Behavior | Recovery |
|---|---|---|
| Disk full on ledger write | append/sync error → 500 + persist-issue flag; health degrades | free disk, restart (replay heals) |
| Missing store paths | construction refuses (fail closed, no listeners) | fix env, restart |
| Corrupt safety log (mid-file) | controller construction refuses; binary won't serve | restore safety.jsonl from backup, restart |
| Torn-tail safety write | truncated, event absent (as if never sent) | none needed; operator re-issues |
| Corrupt outcome checkpoint | recovery refuses | delete checkpoint (full ledger replay) |
| Shutdown during settlement | event either durable (then learned on replay) or absent (then unsettled) | restart replays; no half-learned state |
| Delayed verifier | jobs stay PENDING/immature; monitor reports AvgDelayH; no suspension on thin data | wait or investigate verifier |
| Safety store write fails live | controller admits fallback-or-nothing; monitor refuses (500) | fix store, restart |
| Operator credential leak | rotate OPERATOR_TOKEN, restart (no re-auth of past actions; log review) | audit safety.jsonl |
| Settle listener exposed beyond loopback | construction refuses without ALLOW_PUBLIC_SETTLE=1 | bind loopback |

## Operator runbook
1. **Investigate**: read per-arm health (safety events carry evidence JSON;
   `safety.jsonl` is the audit trail; decisions carry rule/fallback identity).
   Distinguish low rate (suspend-worthy) from thin/delayed verification
   (AvgDelayH high, matured < minObs → wait, do not suspend manually unless
   incident demands).
2. **Suspend**: `POST /v1/operator/suspend {"arm":"X","reason":"..."}` with
   `Authorization: Bearer <OPERATOR_TOKEN>:<your-id>` on the INTERNAL
   listener. Empty arm = emergency stop (fallback only). Verify 200 + event.
3. **Fallback check**: subsequent decisions select the fallback (or fail
   closed 503 when nothing is safe — expected, not an error to retry blindly).
4. **Resume**: only after the cause is fixed AND the frozen config still
   matches: `POST /v1/operator/resume` with reason. Disabled arms need
   re-approval (config change = new experiment). Rejected resumes are logged.
5. **Rollback**: ledgers are append-only — rollback means suspend/resume,
   never deletion. For analysis, the report gates already exclude/refuse
   properly; intent-to-treat keeps every randomized job.
6. **Budget sizing** (per experiment): budget ≥ ColdStartPulls + opening
   exploration + non-learning margin (PENDING/timeout picks spend budget
   without producing pulls). Undersized budgets fail closed mid-run (safe
   abort, loud refusal) — size from expected treatment traffic × attempts.

## What remains unsupported
Multi-replica coordination, hosted control plane, dashboards, load-tested
throughput ceilings, Linux-measured constants, disk-full live injection
(documented procedure only), customer-traffic authorization.
