# Thompson Component Status

Baseline: post-PR-40 `origin/main` (`713874d`). Inspected directly;
status is not inferred from filenames. No code is deleted or promoted
by this file. Passing tests do not make a component a product; see
`STATUS.md` for the research verdicts.

## ACTIVE_PRIMITIVE

Tested components that remain genuinely reusable as libraries or tools.
None is a product thesis.

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

- **Reasoning-goodput assay** (`go/assay/reasoninggoodput/`,
  `docs/research/reasoning-goodput/`) — synthetic-work selective-replay
  harness with broad tests. Credibility damaged as recorded: implemented
  gate 6 fails deterministically on the committed fixture
  (`EVIDENCE_ERRATUM.md`); `TestDecisionGates` now asserts the truthful
  historical record (gates 1–5, 7 PASS, gate 6 FAIL). Evidence, not code
  to reuse.
- **Live-agent-premise assay** (`go/assay/reasoninggoodput/livepilot/`,
  `docs/research/live-agent-premise/`) — live-stream premise capture and
  counterfactual preservation analysis. Frozen gates held as frozen, but
  classified INSUFFICIENT_EVIDENCE for economics (counterfactual, not
  actual replay; preservation mostly initial prefill; premises not
  re-derivable from the repo). Its `NEXT_REASONING_TRANSACTION.md` design
  is superseded by the real-replay KILL.
- **Real-replay economics assay** (`go/assay/realreplay/`,
  `docs/research/real-replay-economics/`) — the terminal experiment.
  Harness tests pass; verdict `KILL_INCREMENTAL_REPLAY_ECONOMICS`.
  Preserve untouched for reproducibility.
- **Journal prototype** (`go/assay/journal/`,
  `docs/research/journal/`) — SQLite-WAL single-log prototype with
  lifecycle, matched-comparison, and fault tests. Verdict was CONTINUE
  INVESTIGATION toward a gateway pilot, which this closeout does not
  authorize. Research code; explicitly never touches the production
  gateway. Compaction unimplemented (documented).

## LEGACY

Shipped or half-integrated paths superseded by verified mode but still
present and tested. Do not extend.

- **Gateway legacy transport-based learning mode** — learns from
  transport-derived observations; retained beside verified mode with the
  executable defaulting to legacy. Documented as not-evidence.
- **Single-replica/raw JSONL evidence paths** — earlier persistence
  shapes retained for compatibility behind store interfaces.

## BROKEN/UNVERIFIED

- **Artifact resolver / verified-artifact machinery as a named
  component: ABSENT.** No file or type named artifact/resolver exists in
  `go/` or `crates/` (only generic "experiment artifact" vocabulary in
  comments). Do not cite it as shipped code. The closest real machinery
  is verified-mode settlement (`go/gateway/settle.go`, `evidence.go`,
  `go/outcome/`), classified ACTIVE_PRIMITIVE above.
- **Pre-existing hygiene failures (repository, not components):**
  `gofmt -l` flags 18 files outside `go/assay/realreplay/` (pre-existing
  on main, none introduced by PR #40); CI's pinned Rust 1.75 clippy
  reports `clippy::box_default` in `thompson-sim` while the local
  toolchain is clean. Both are fixed or documented in the closeout
  branch; neither reflects component breakage.
