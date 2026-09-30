# Baseline and Prior Freeze (Phase 0)

Mission: `THOMPSON_REASONING_GOODPUT_ASSAY_V1`. Systems hypothesis only:
preserving speculative work across external-state movement. No merge, no
deploy. Branch `research/reasoning-goodput-assay-v1` from `origin/main`
(`814d2b2`) in isolated worktree `thompson-goodput`. PRs #29–#35 all OPEN,
untouched. Toolchain go1.27.1; go.mod declares go 1.22.

## Main baseline (pre-assay)

`go build ./...` OK; `go/thompson`, `go/outcome` PASS (`-count=1`).
Full-tree and race verification run with the assay code (Phase 10).

## Frozen priors (must not be rebuilt as theses)

- Model routing as company thesis: rejected (no consistent economic win).
- Generic verified runtime / DAG materialization / physical-plan optimizer /
  runtime replanning: rejected or narrowed (graph added cost without
  eliminating operations; replanning value confined to shape changes).
- Outcome ledger product: KILLED (incumbent + warehouse reconstructs).
- Artifact resolver as standalone company thesis: extracted as a technical
  primitive only (PR #34), explicitly not a product.
- Generic agent eval platform; trace compiler as company thesis: rejected.
- Retained vocabulary only: exact content identity, evidence-bound reuse,
  append-only correction trails, offline economics analysis.

## Scope of this assay

Treat agent reasoning as expensive speculative computation over versioned
external state. Test whether lower-layer witnesses + mechanical premises
preserve work safely under contention. No coordinator, scheduler, lock
service, workflow engine, agent framework, LLM judge, or second state
authority will be built; observing any of them emerge stops the mission.
