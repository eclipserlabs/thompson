# Thompson Component Status

Baseline: post-PR-40 `origin/main` (`713874d`). Inspected directly;
status is not inferred from filenames. No code is deleted or promoted
by this file. Passing tests do not make a component a product; see
`STATUS.md` for the research verdicts.

## ACTIVE_PRIMITIVE

Tested components that remain genuinely reusable as libraries or tools.
None is a product thesis.

- **Artifact resolver** (`go/artifactresolver/`,
  `docs/artifactresolver/`) — salvaged from the stranded
  `feat/artifact-resolver-v1` branch into the product tree: split-identity
  (ComputationKey vs VerificationKey) artifact resolution with
  deterministic REUSE|VERIFY|RECOMPUTE|UNKNOWN verdicts, append-only
  verification claims, idempotent revocation, stdlib-only. Frozen parity
  fixture proves zero semantic discrepancies vs the assay it was
  extracted from. Explicitly a technical primitive, not a product thesis.

- **Rust Thompson Sampling policy** (`crates/thompson-sampling/`) —
  Beta-Bernoulli bandit core with pluggable samplers, warm-start priors,
  snapshot persistence; protocol conformance + stress tests. The
  `linear.rs` contextual extension is an explicit scaffold, not the
  validated path.
- **Go Thompson policy** (`go/thompson/`) — Go port with identical
  select/record/snapshot semantics and cross-language wire fixtures;
  extensive tests including safety and protocol conformance.
- **Gateway** (`go/gateway/`, wiring `go/router/main.go`) — HTTP
  decision/execution/learning service with verified and legacy learning
  modes, evidence ledgers, shadow evaluation, safety/OPE gates; ~25 test
  files. Complexity is real but documented; durability limits
  (single-writer, local storage) are stated in-code, not hidden.
- **Durable outcomes** (`go/outcome/`) — versioned append-only JSONL
  outcome ledger with idempotent submit rules feeding verified-mode
  learning; store/learner/costbook tests pass. Useful as a primitive;
  the outcome-ledger *product* was killed (see `STATUS.md`).
- **Offline evaluation / propensity tooling** (`go/opeval/`,
  `go/propensity/`, `go/gateway/ope.go`, `docs/propensity-audit/`) —
  IPS/SNIPS evaluator with Monte-Carlo vs independent numerical-reference
  reconstruction, ESS/sensitivity/rankability gating that refuses
  unsupported comparisons. Stated limits (exact-Thompson-argmax logs
  only, sub-unit Beta shapes weakest) are gates, not breakage.
- **Control plane / protocol interop** (`crates/control-plane/`,
  `protocol/`) — thin-waist select/record + frozen Snapshot v1 contract
  with in-memory/file snapshot registry and bidirectional Rust↔Go
  fixtures. S3/Postgres are explicit non-implementations that fail
  loudly. Deliberately narrow; healthy within its contract.

## RESEARCH_ONLY

Complete experiments. Keep for reproducibility; do not build on as
products.

- **Reasoning-goodput, live-agent-premise, and real-replay assays**
  (REMOVED from the working tree with `go/assay` in the substrate
  cleanup; verdict memos retained under `docs/research/`, full evidence
  in git history) — see `STATUS.md` decisions 7–9 and `ARCHIVE_NOTE.md`.
- **Journal prototype** (REMOVED from the working tree with `go/assay`
  in the substrate cleanup; record preserved in git history) — SQLite-WAL
  single-log prototype with lifecycle, matched-comparison, and fault
  tests. Verdict was CONTINUE INVESTIGATION toward a gateway pilot, which
  was never authorized. The experimental `JOURNAL_PATH` SQLite backend was
  de-wired from `go/router` and journal-authority support removed from
  `go/cmd/exp-run` (unknown `StorageBackend` now fails closed). Compaction
  was unimplemented (documented).

## LEGACY

Shipped or half-integrated paths superseded by verified mode but still
present and tested. Do not extend.

- **Gateway legacy transport-based learning mode** — learns from
  transport-derived observations; retained beside verified mode with the
  executable defaulting to legacy. Documented as not-evidence.
- **Single-replica/raw JSONL evidence paths** — earlier persistence
  shapes retained for compatibility behind store interfaces.

## BROKEN/UNVERIFIED

- **Artifact resolver / verified-artifact machinery: PRESENT as a
  primitive.** `go/artifactresolver/` (split-identity resolution,
  append-only claims, frozen parity fixture; tests pass) with contracts
  under `docs/artifactresolver/`. Salvaged into the product tree from
  the stranded `feat/artifact-resolver-v1` branch during the substrate
  cleanup. The closest *gateway-integrated* machinery remains
  verified-mode settlement (`go/gateway/settle.go`, `evidence.go`,
  `go/outcome/`), classified ACTIVE_PRIMITIVE above.
- **Pre-existing hygiene failures (repository, not components):**
  `gofmt -l` flags 18 files outside `go/assay/realreplay/` (pre-existing
  on main, none introduced by PR #40); CI's pinned Rust 1.75 clippy
  reports `clippy::box_default` in `thompson-sim` while the local
  toolchain is clean. Both are fixed or documented in the closeout
  branch; neither reflects component breakage.
