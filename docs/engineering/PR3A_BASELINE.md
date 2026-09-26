# PR 3A Baseline (Phase 0 — THOMPSON_LIVE_OUTCOMES_V1)

- Date (UTC): 2026-09-27
- Baseline SHA (`origin/main`, merged PRs #1–#3): `d6029afde5d10c06ea50f51e2f2cbdfcd8e81b88`
  - `3629226` Merge PR #2 (PR 1: atomic decision snapshot)
  - `d6029af` Merge PR #3 (PR 2: durable outcome learner)
- Note: mission named `feat/durable-learning-v1` as base, but both PR branches are
  now merged on `origin/main`; the new branch `feat/live-outcomes-v1` was cut from
  `origin/main` (= PR 1 + PR 2 content, verified: `go/outcome/` present,
  `LoggingPolicyID`/`SelectSnapshot`/`RestoreSnapshot` present in
  `go/thompson/policy.go`). Stacking on the merged base is strictly safer.
- Working tree at record time: clean. Branch: `feat/live-outcomes-v1` tracking `origin/main`.
- Toolchains as-run: go1.27.1 darwin/amd64, rustc/cargo 1.90.0. (CI pins Go 1.22 /
  Rust 1.75; Go 1.22 compat check is a Phase 5 command.)

## Docs read (contract verification)

- `docs/audit/CORRECTNESS.md` — findings A1 (fixed, PR 1), A3 (fixed, PR 2),
  B3/C1/C2 (contract + store/learner, PR 2). Open for this PR: B1 (transport vs
  task success conflated in the live path), B2 (gateway drops quality/cache),
  C3 (single-replica unenforced — PR 2 added `flock` on the outcome ledger only).
- `docs/design/OUTCOME_CONTRACT_V1.md` — normative design + 2 adopted V1
  deviations (REJECTED requires deciding attempt; additive `DecisionStarted`
  fields deferred to this PR's wiring).
- `docs/engineering/PR2_DURABLE_LEARNING.md` — storage design (ledger +
  checkpoint + cursor), recovery order, perf (~240 submits/s fsync-bound).

## Live and shadow policy mutations (complete inventory, `go/`)

| # | Location | Call | Provenance |
|---|---|---|---|
| 1 | `go/gateway/router.go:348` | `rt.policy.Record(rng, chosen, reward)` in `ServeHTTP` | transport-derived (HTTP status/latency/cost) — **the P0 target** |
| 2 | `go/gateway/middleware.go:80` | `m.Policy.RecordOutcome(rng, provider, outcome)` | transport-derived thin-waist — legacy surface |
| 3 | `go/outcome/learner.go:247` | `p.Record(rngFor(job,version), armID, reward)` | verified settlement — the sanctioned path |
| — | `go/gateway/shadow.go` | none (verified: no `Record`) | shadow never mutates live policy |
| — | `go/harness/harness.go:208`, `go/opeval/synthetic.go:107`, `go/examples/thin_waist.go:38` | offline/demo only | out of scope |
| — | tests | test-only | out of scope |

Decision identity today: `canonicalID` (crypto-random per request) + optional
external ID header; `DecisionStarted` persists eligible/selected/scores/
posteriors/config-hash/logging-policy-ID (PR 1 coherent, PR 2 derived) but has
**no decision store, no job_id, no lookup-after-restart, no settlement
endpoint**. `recorded map[string]bool` is per-process, unbounded, keyed by
fresh IDs (audit C2). Outcome ledger (`go/outcome`) cannot reference a
gateway decision — the join key does not exist yet. That gap is Phase 1/2.

## Focused tests on the base (before any PR 3A edits)

| Suite | Result |
|---|---|
| `go test -race -count=1 ./gateway/ ./thompson/ ./outcome/` | all `ok` |
| `go vet` on the same three packages | clean |

Known pre-existing issues (PR 1 baseline, not regressions unless they change):
control-plane env-var test races; repo-wide `gofmt` dirt (16 files incl.
`gateway/router.go` hunks unrelated to this work); `go test -run
TestConformance` matches zero tests in-tree.
