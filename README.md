# Thompson

[![Rust 1.75+](https://img.shields.io/badge/Rust-1.75%2B-dea584?logo=rust)](https://www.rust-lang.org/)
[![Go 1.22+](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go)](https://go.dev/)
[![Protocol v1](https://img.shields.io/badge/wire%20protocol-v1-5b5bd6)](protocol/SPEC.md)
[![License: MIT OR Apache-2.0](https://img.shields.io/badge/license-MIT%20OR%20Apache--2.0-blue.svg)](LICENSE-MIT)

A research and engineering workspace for **Beta–Bernoulli Thompson Sampling**
for provider and model selection. It includes a Rust policy library and
simulator, a Rust snapshot control plane, and a Go HTTP gateway with durable
evidence and offline-policy-evaluation tools.

~~~text
select provider -> execute request -> record outcome
~~~

Selection does not mutate policy state. Learning occurs only after a completed
outcome is recorded, so cancelled or failed-to-dispatch requests are not learned
as observations.

> **Deployment status:** the Go gateway is single-process and single-replica.
> Its policy is in memory and not shared across replicas; its default Helm chart
> stores evidence on an emptyDir volume. It is a controlled V0 integration path,
> not a horizontally scalable production router.

## Repository map

| Path | Purpose |
| --- | --- |
| crates/thompson-sampling | Rust policy: posteriors, rewards, selection, warm starts, persistence, and optional OpenTelemetry. |
| crates/thompson-sim | Deterministic benchmark harness for regret, drift, churn, and sampler comparisons. |
| crates/control-plane | Axum service exposing policy snapshots and Prometheus-style metrics. |
| go/thompson | Go policy implementation and snapshot format. |
| go/gateway | HTTP router, provider adapters, evidence ledger, shadowing, replay, and OPE gates. |
| go/cmd | Evidence analysis, IPS/SNIPS evaluation, and propensity validation. |
| protocol | Frozen v1 wire contract and JSON schema. |
| docs | Benchmark findings, results, and propensity-validation report. |
| helm | Experimental Go router and separate control-plane charts. |

## Prerequisites and verification

- Rust **1.75+** (workspace MSRV)
- Go **1.22+** for the Go port and gateway
- Docker and Helm only to build images or render charts

~~~sh
cargo test --workspace
(cd go && go test -race ./...)
~~~

The race check is required because Go policy and gateway instances serve
concurrent HTTP handlers.

## Rust policy quick start

The caller owns request execution and must report the real result after routing.

~~~rust
use rand::{rngs::SmallRng, SeedableRng};
use thompson_sampling::{Outcome, ThompsonSampling};

let mut rng = SmallRng::seed_from_u64(42);
let mut policy = ThompsonSampling::with_defaults([
    "openai/gpt-4",
    "anthropic/claude-3-5-sonnet",
]);

let provider = policy.select(&mut rng).expect("policy has eligible arms");

// Dispatch to provider, then record what actually happened.
let outcome = Outcome::new(320.0, true, 0.0012).with_quality(0.87);
policy
    .record_outcome(&mut rng, &provider, &outcome)
    .expect("selected arm is still registered");
~~~

Outcome carries latency, success, cache hit, cost, and optional quality.
RewardPolicy converts that multi-objective result into the signal used to update
the Beta posterior. Do not record before a provider is reached, or invent a cost
when it is unknown.

Key extension points:

- WarmStart: initialise a related new arm from an informed prior.
- DiscountPolicy: discount old evidence when performance drifts.
- PartitionedPolicy / LinearPolicy: contextual routing.
- FileStore / MemoryStore: snapshot persistence.

The default sampler is the exact Gamma-based Beta sampler. Approximate samplers
are comparison baselines, not automatic performance optimizations.

## Simulation and findings

The harness uses synthetic environments with known rewards. Its regret figures
are reproducible engineering measurements, not production outcomes.

~~~sh
# Discover scenarios and treatment groups.
cargo run -p thompson-sim -- --list

# Focused run.
cargo run --release -p thompson-sim -- --group sampler --scenario hard --seeds 20

# Larger repeatable experiment.
cargo run --release -p thompson-sim -- --seeds 50 --csv docs/results.csv
~~~

Read [docs/FINDINGS.md](docs/FINDINGS.md) for interpretation and
[docs/results.csv](docs/results.csv) for the recorded table. The important
finding is that shortcuts can look good in easy stationary tests while degrading
under churn and drift.

## Snapshot control plane

The Rust control plane starts with in-memory storage; choose file storage when
local persistence is needed.

~~~sh
# Local development only: no token means snapshot routes are open.
PORT=8080 cargo run -p control-plane
curl http://localhost:8080/health

# File-backed, token-protected instance.
PORT=8080 STORAGE=file STORAGE_DIR=/var/lib/traverse \
  CONTROL_PLANE_TOKEN='replace-with-a-secret' \
  cargo run -p control-plane
curl -H 'Authorization: Bearer replace-with-a-secret' \
  http://localhost:8080/snapshots
~~~

| Endpoint | Auth | Description |
| --- | --- | --- |
| GET /health | No | Liveness response. |
| GET /metrics | No | Prometheus text metrics. |
| GET /snapshots | Required when a token is configured | Lists snapshots; tenant tokens are scoped. |
| GET /snapshots/:tenant | Required when a token is configured | Reads one snapshot; tenant tokens may read only their own tenant. |

Set CONTROL_PLANE_TOKENS to comma-separated tenant:token pairs for scoped
access; CONTROL_PLANE_TOKEN is a global token. Never use the open default
outside local development. Health and metrics stay public, so apply network
controls if tenant names or policy metadata are sensitive.

## Go gateway

The gateway performs Select -> persist decision -> execute -> persist outcome
-> Record. It exposes GET /health, GET /metrics, and sends all other
paths—including /v1/chat/completions—to the selected provider.

~~~sh
cd go

# Local smoke test: missing PROVIDER_URL_* values select FakeProvider.
EVIDENCE_PATH=./evidence.jsonl go run ./router

# Real provider configuration. Convert arm ID '/' and '-' to '_' and uppercase.
ARMS='openai/gpt-4,anthropic/claude-3-opus' \
PROVIDER_URL_OPENAI_GPT_4='https://provider-a.example/v1/chat/completions' \
PROVIDER_URL_ANTHROPIC_CLAUDE_3_OPUS='https://provider-b.example/v1/messages' \
EVIDENCE_PATH=/var/lib/router/evidence.jsonl \
go run ./router
~~~

The fake-provider fallback is for local verification only. The binary itself
does not configure request authentication: deploy it behind authenticated,
authorized ingress or compose the provided middleware with application-side
authentication. Do not expose it to untrusted clients.

The evidence file is append-only and each successful event write is synced to
the local filesystem. It supports replay and offline analysis, but it is not a
transactional or cross-replica ledger: a crash can leave an incomplete decision.
Use a durable volume with restrictive permissions; the current writer creates
new files with mode 0600 (tighten pre-existing files with `chmod 600`).

### Shadow execution

Shadowing runs one non-selected provider without updating the live policy. It
is disabled by default; enable it only for requests that are safe to duplicate.

~~~sh
SHADOW_SAMPLE_RATE=0.01 \
SHADOW_TIMEOUT=5s \
SHADOW_MAX_CONCURRENCY=5 \
go run ./router
~~~

Requests must include X-Shadow-Eligible: true. Set SHADOW_SAMPLE_RATE=0 as the
kill switch. Shadow observations are paired diagnostics, not full-information
regret data.

## Evidence replay and OPE

These commands read a collected ledger; they do not mutate the online policy.

~~~sh
cd go

go run ./cmd/analyze --evidence ./evidence.jsonl
go run ./cmd/evaluate \
  --evidence ./evidence.jsonl \
  --policy uniform-v1 \
  --propensity reference
go run ./cmd/propensity-audit --evidence ./evidence.jsonl
~~~

IPS/SNIPS needs support and precise logging-action propensities. The tooling
separates overlap failures, Monte-Carlo zero-win estimates, numerical-reference
failures, and low-precision rows. Candidates marked NOT_RANKABLE are refused
rather than reported as winners. A candidate and logging propensity generated by
the same estimator is IMPLEMENTATION_SANITY_ONLY, not a valid comparison.

Read [docs/PROPENSITY_VALIDATION.md](docs/PROPENSITY_VALIDATION.md) before using
an estimate to change routing policy.

## Interoperability and deployment

Rust and Go share this v1 snapshot shape:

~~~text
Snapshot { version: 1, config, arms, total_pulls }
~~~

JSON floats must be compared with tolerance, not bitwise equality. See
[protocol/SPEC.md](protocol/SPEC.md) and [protocol/schema.json](protocol/schema.json)
for types and conformance details.

- Dockerfile builds Rust simulator and control-plane binaries.
- go/Dockerfile builds the Go router.
- helm/router renders the one-replica experimental router with ephemeral
  evidence by default.
- helm/traverse is the separate control-plane chart; review storage, auth,
  image, and resources before applying it.

~~~sh
helm lint helm/router
helm template router helm/router
helm lint helm/traverse
helm template traverse helm/traverse
~~~

## Engineering checks

~~~sh
# Rust library, simulator, control plane, and doctests.
cargo test --workspace

# Go policy, gateway, OPE, and race checks.
(cd go && go test -race ./...)

# Optional Rust OpenTelemetry example.
cargo run -p thompson-sampling --features otel --example thin_waist
~~~

There is deliberately no CI-status badge: the badges above state versioned
repository facts, not build status. Checks live in `.github/workflows/`
(`ci.yml`: fmt/clippy/test; `trace-replay.yml`: regret + trace replay).

## License

Dual-licensed under [MIT](LICENSE-MIT) or [Apache-2.0](LICENSE-APACHE), at your
option.
