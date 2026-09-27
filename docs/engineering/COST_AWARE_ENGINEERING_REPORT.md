# Cost-Aware Engineering Report (V1)

## Baseline and implementation SHAs
- Baseline `origin/main`: `d7a33e68e9484732fad40561f21f038811540ca8` (no open PRs; frozen `PR3B_CHARTER.md` untouched).
- Feature branch: `feat/cost-aware-v1` (worktree `~/Documents/thompson-costaware`).
- Baseline merge `f26342c`: integrated `origin/review/correctness-d` (PRs A–D stack); 3 conflicts resolved by keeping newer HEAD versions.
- This mission's delta (uncommitted at report time; to be committed as one reviewable PR):
  - `go/harness/report.go`: deterministic gate ordering (sorted treatment iteration).
  - `go/thompson/costaware.go` (+test): v1 selection rule, identities, config.
  - `go/outcome/costbook.go` (+test): versioned per-arm cost ledger.
  - `go/harness/costaware.go` (+4 test files): T3 strategy/treatment, 4-treatment experiment, adversarial matrix, concurrency, perf.
  - `docs/engineering/COST_AWARE_BASELINE.md`, `docs/design/COST_AWARE_OBJECTIVE_V1.md`, `docs/engineering/COST_AWARE_CHARTER_V1.md`, this report.

## Exact changed files
```
go/harness/report.go
go/thompson/costaware.go
go/thompson/costaware_test.go
go/outcome/costbook.go
go/outcome/costbook_test.go
go/harness/costaware.go
go/harness/costaware_test.go
go/harness/costaware_concurrent_test.go
go/harness/costaware_experiment_test.go
go/harness/costaware_adversarial_test.go
go/harness/costaware_helpers_test.go
go/harness/costaware_perf_test.go
docs/engineering/COST_AWARE_BASELINE.md
docs/design/COST_AWARE_OBJECTIVE_V1.md
docs/engineering/COST_AWARE_CHARTER_V1.md
docs/engineering/COST_AWARE_ENGINEERING_REPORT.md
```

## Selected method and rationale
Method 1 (quality-constrained selection with separate success/cost estimates). Reuses audited Beta-Bernoulli posterior + Thompson samples for quality; independent `CostBookV1` for fully-loaded cost means; rule `min mean_cost/max(sample,eps)` among floor-or-cold-qualified arms with known costs; explicit flagged fallback otherwise. Rejected Method 2 (bounded scalar into same posterior): breaks conjugacy, needs fragile normalization, admits silent floor erosion.

Math assumptions: independent verifier; additive USD costs; observable missingness; assignment ignorability via RNG separation; deciding-arm attribution for multi-attempt chains (documented limitation; v1 experiment single-attempt-dominant).

## Test results
- Phase 0 baseline `go test ./...`: 9/9 ok (pre-change).
- New unit tests: thompson 6/6 (distinguish, gate, cold-start, fallback-explicit, config validation, sampler parity); outcome 7/7 (fallback exact-once, missing-never-zero, unknown/pending, correction, duplicate, snapshot/replay, malformed rejection).
- Harness integration: prefer-cheap, block-cheap-low-quality, identity-bound, replay-deterministic, cost-blind-preserved, concurrent-safe, 4-treatment experiment, drift-regression-exposed, 7-scenario × 2-seed adversarial matrix — all pass.
- Full `go test -race ./...`: 9/9 ok. `go vet ./...` clean. Go 1.22 compat suite (thompson/outcome/harness via GOTOOLCHAIN=go1.22.0): all 3 packages ok.
- Protocol conformance `TestProtocol*`: pass. Rust `control-plane storage` (merge-touched): 2/2 pass; `cargo fmt --check` clean.
- Cost-blind benchmarks unchanged: full suites green with no fixture edits; T2 identity still `exact-thompson-v1`.

## Cases where the new policy underperforms
Cost-drift scenario (cheap arm 25x spike mid-experiment): T3 over-commits to learned cheap preference and loses to competent static T1 on cost-per-success. The report exposes it (`t3-t1 Wins=false`, verdict refuses rankable claim). Documented as expected behavior under nonstationarity, not a bug.

## Backward compatibility
Additive only: existing `Policy`, `BinaryStatusMapper`, `Learner`, outcome schema v1, protocol fixtures, 3-treatment charter untouched. Historical posteriors never reinterpreted. Gateway router policy interface unchanged (T3 runs harness-local in v1; gateway cost-aware routing is documented future work requiring a router policy-interface change — smallest compatible alternative, not implemented here).

## Remaining production risks
1. Deciding-arm attribution approximates multi-attempt chains; per-attempt cost credit is future work.
2. Cost-mean unprotected against outliers (no winsorization by design).
3. Human-cost under-reporting and unmetered-gaming bounded by gates, not eliminated.
4. No multi-replica coordination / hosted control plane (out of scope); single-writer limits documented below.
5. `main` still lacks branch protection (prior mission blocker); real-workload testing NOT justified until repository controls land.

## Performance (measured, darwin, single-writer experimental)
decision_latency=2.8µs | settlement_latency=13.4ms (fsync-dominated) | checkpoint_save=5.3ms | recovery=327µs | storage_per_job≈1183B extra (decisions.jsonl). Limits: decision path easily interactive; settlement throughput ≈75 jobs/s single-writer on this machine; no replica coordination implemented.

## Real-workload testing justified?
No — technically feasible (contract + feasibility CLI + acceptance suites pass on synthetic) but NOT YET SAFE: blocked solely on repository controls (branch protection, required CI), not on code correctness. All results SYNTHETIC; no commercial savings claimed.
