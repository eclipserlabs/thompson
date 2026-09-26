# Engineering Baseline — THOMPSON_ENGINEERING_BASELINE_V1 (Phase 0)

- Date (UTC): 2026-09-26
- Baseline SHA (origin/main, fast-forwarded this session): `62f3de6b7cd90ab930ed4d5d6b3b127a9297f2ca`
- Previous local SHA before fetch: `c7237b7b1bfc275c5691445c590a7e689b6c7de7` (behind by 1 commit; `git pull --ff-only` applied, README-only delta)
- Branch: `main`, tracking `origin/main`
- Working tree at record time: clean (`git status`: nothing to commit)
- Remote: `https://github.com/wiramahendra/thompson` (local remote resolves to `wiramahendra/thompson-sampling`; treated as the mission repository)

## Toolchain versions (as-run, darwin/amd64)

| Tool | Version |
|---|---|
| rustc | 1.90.0 (1159e78c4 2025-09-14) |
| cargo | 1.90.0 (840b83a10 2025-07-30) |
| go | go1.27.1 darwin/amd64 |
| CI pins (not local) | Rust 1.75 + `rustfmt/clippy`; Go 1.22 (`actions/setup-go@v5`) |

Version skew note: local Go (1.27.1) is newer than CI's Go 1.22. `gofmt` findings below were produced locally and must be re-checked under CI's toolchain before being treated as blocking.

## CI configuration (`.github/workflows/`)

- `ci.yml` (`CI`): three jobs.
  - `rust`: `cargo fmt --all --check`, `cargo clippy --workspace --all-targets -- -D warnings`, `cargo test --workspace --release`, `cargo doc --workspace --no-deps`. `RUSTFLAGS: -D warnings`.
  - `go` (workdir `go`): `gofmt -l` must be empty, `go vet ./...`, `go test -race ./...`, `go test -run TestConformance -v ./...`, `helm lint ../helm/traverse` (soft-fail if helm missing).
  - `reproduce`: `cargo run --release -p thompson-sim -- --seeds 5` (smoke only, not the published 50-seed tables).
- `trace-replay.yml`: `cargo test --workspace`, `cargo check -p thompson-sampling --examples`, `cargo test -p thompson-sampling --test stress`, 50-seed sim CSV, and conditional trace replay (`traces/*.jsonl` → sim CSV + `go test -race -run TestTraceReplay`). No `traces/` directory exists in the repo, so the replay leg currently no-ops with "no traces, skipping replay".

## What was read

- `README.md` — workspace map, V0 status, quickstarts, gateway/OPE/helm overview.
- `docs/FINDINGS.md` — synthetic regret study (50 seeds/cell, 95% CI) across samplers, update rules, warm-start, selection, discount.
- `docs/PROPENSITY_VALIDATION.md`, `docs/propensity-audit/REPORT.md` (+ `matrix.txt`, `log-10k.txt`, `ladder-300.txt`) — propensity ground-truth verdict `PROPENSITY_GROUND_TRUTH_PASS`.
- `protocol/SPEC.md`, `protocol/schema.json` — frozen wire `v1` (thin-waist `select`/`record`, `Snapshot{version:1}`).
- Rust: `crates/thompson-sampling/src/` (`policy.rs`, `posterior.rs`, `reward.rs`, `sampler.rs`, `selection.rs`, `warm_start.rs`, `discount.rs`, `persistence.rs`, `observer.rs`, `arm.rs`, `context.rs`, `linear.rs`, `health.rs`), `crates/thompson-sim/src/` (`env.rs`, `experiment.rs`, `treatments.rs`, `trace.rs`, `main.rs`), `crates/control-plane/src/` (`lib.rs`, `server.rs`, `storage.rs`, `dashboard.rs`, `main.rs`).
- Go: `go/thompson/` (`policy.go`, `posterior.go`, `sampler.go`, `reward.go`, `selection.go`, `warmstart.go`, `discount.go`, `persistence.go`, `context.go`, `health.go`, `metrics.go`, `otel.go`), `go/gateway/` (`router.go`, `middleware.go`, `evidence.go`, `provider.go`, `ope.go`, `replay.go`, `propensity_source.go`, `shadow.go`, `auth.go`, `rankability.go`, `dashboard.go`), `go/propensity/`, `go/opeval/`, `go/harness/`, `go/router/main.go`, `go/cmd/{analyze,evaluate,propensity-audit}/`, `go/examples/`.
- `examples/lite_llm_adapter.py`, `load/k6.js`, `helm/router/`, `helm/traverse/`.

`docs/audit/`, `docs/design/`, `docs/engineering/` did not exist before this mission.

## Architecture (as-built)

- Rust policy core (`crates/thompson-sampling`): Beta posteriors, `RewardPolicy` collapse, `Selection::{Thompson,UcbRegularized,Phased}`, `WarmStart`, `DiscountPolicy`, `FileStore`/`MemoryStore` snapshots, optional OTel. `select(&self)` is non-mutating; `record(&mut self)` mutates. No interior mutability — no concurrent select+record without external synchronization.
- `crates/thompson-sim`: deterministic harness (scenarios easy/hard/drift/churn/treadmill/graded; treatments over samplers, update rules, warm-start, selection, discount).
- `crates/control-plane`: Axum snapshot registry + `/metrics`, per-tenant bearer auth, `memory|file|s3|postgres` storage (s3/postgres are loud stubs).
- Go port (`go/thompson`): same model; single `sync.Mutex` guards all policy state (`go/thompson/policy.go:110-118`).
- Go gateway (`go/gateway`): deployable path `Select → DecisionStarted → Execute → ExecutionObserved → Reward → Record → DecisionLearned → Shadow`. Evidence is append-only JSONL (`0600`, per-write `Sync`). Single-process, single-replica by design (`helm/router` `replicaCount: 1`, "do not scale without shared state").
- Offline evaluation: `propensity` (MC + independent Gauss-Kronrod reference), `ope.go` (IPS/SNIPS/ESS/bootstrap/rankability gates), `replay.go` (paired primary-vs-shadow comparison, explicitly not regret), `opeval` synthetic matrix + calibration + sweep CLIs.

## Decision → execution → outcome → evidence → update → replay map

1. **Decision**: `Policy::select` (`crates/thompson-sampling/src/policy.rs:233`) / `Policy.SelectWithScores` (`go/thompson/policy.go:297`). Gateway entry: `Router.ServeHTTP` (`go/gateway/router.go:157`) or thin-waist `Middleware.Handler` (`go/gateway/middleware.go:34`).
2. **Execution**: caller/gateway dispatches; `HTTPProvider.Invoke` (`go/gateway/provider.go:~100`) forwards HTTP, 2xx = success, best-effort OpenAI `usage` parse, cost only if pricing + usage present.
3. **Observed outcome**: `ExecutionObserved` event (`go/gateway/evidence.go:73-84`); token/cost pointers nullable, never synthesized.
4. **Evidence persistence**: `FileEvidenceWriter` append + `Sync` per event (`go/gateway/evidence.go:182-197`); `MemoryEvidenceWriter` for tests.
5. **Policy update**: `record` / `record_outcome` (`crates/thompson-sampling/src/policy.rs:426-473`; `go/thompson/policy.go:507-552`).
6. **Replay/OPE**: `LedgerFromFile` + `AnalyzePaired` (`go/gateway/replay.go`), `ToBanditLogWith` + `EvaluateOPE` (`go/gateway/ope.go:65-458`), rankability gate (`go/gateway/rankability.go`). No online mutation from offline path.

## Checks run (this baseline, before any mission edits)

| Check | Result |
|---|---|
| `cargo fmt --all --check` | PASS |
| `cargo clippy --workspace --all-targets` | PASS (no warnings) |
| `cargo test -p thompson-sampling -p thompson-sim` | PASS — 81 + 2 + 3 + 18 + 2 + 1 tests, 0 failed |
| `cargo test --workspace` | **FAIL — 2 pre-existing failures in `control-plane`**: `server::tests::per_tenant_authorized_tenant` (panics at `server.rs:278`, `unwrap()` on `None`) and `server::tests::per_tenant_scoping_filters_list` (assertion at `server.rs:336`, response contains `t2`). Both tests mutate `CONTROL_PLANE_TOKENS`/`CONTROL_PLANE_TOKEN` process env without inter-test serialization; under parallel execution they race each other. Unrelated to bandit correctness; left untouched per execution rules. |
| `go vet ./...` (in `go/`) | PASS |
| `gofmt -l .` (in `go/`) | **DIRTY — 16 files listed** (incl. `gateway/router.go`, `evidence.go`, `middleware.go`, `provider.go`, `shadow.go`, `thompson/context.go`, `metrics.go`, `observer.go`, `harness/harness.go`, `router/main.go`, …). Diffs are struct-field alignment/whitespace class (e.g. `router.go` `Router`/`RouterConfig` blocks). Pre-existing; local `gofmt` is Go 1.27.1 vs CI Go 1.22, so CI impact must be confirmed under the pinned toolchain before claiming CI is red. Left untouched except files this mission edits (which are normalized as a side effect — see final report). |
| `go test ./thompson/... ./propensity/... ./opeval/...` | PASS |
| `go test -race ./gateway/...` | PASS |

No test results or benchmarks in this file are invented: every entry above is the observed output of a command run in this session.
