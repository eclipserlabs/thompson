# Research Benchmark Engineering Report (V1)

## Baseline and branch
- Base: `review/reliability-v1@0ad84b7` (PR #25 OPEN, CI red at Format on
  pre-existing dirt — Vet/Test never ran there).
- Branch: `research/benchmark-v1` (isolated worktree
  `~/Documents/thompson-research`; pushed; no main contact).
- Scope kept: new `go/bench/` package + research docs only. No production
  algorithm, charter, contract, or policy change. No novelty, savings, or
  customer-data claims anywhere.

## What was built
- Versioned corpus: 14 scenarios, content digests, dev/eval seed split.
- Independent baselines: minimal Beta-Bernoulli (own Cheng sampler) with
  posterior + distributional parity vs textbook-pinned production policy;
  logged ablation (limitation proven: redelivery double-learns); static
  baselines dev-picked. Warm-start difference documented as exclusion.
- S1–S5: replay equivalence, crash matrix (incl. scoped harness
  re-execution duplication finding), correction permutations, durability
  ablation, safety recovery.
- Scaling to 1M (minimal, O(1) memory) + Linux cross-checks.
- 8-scenario economics sweep with dev/eval separation; negatives retained.

## Measured findings
- Correctness: zero silent divergence anywhere tested.
- Overhead: ~50ns–1µs minimal vs ~4–6ms durable per settle (10^4–10^5×);
  correction safety ≈ +2ms over plain logging; ~39B vs ~468B+evidence/job.
- Economics: no rankable T3-over-T1 claim at n=160; ties at static-optimal;
  losses under missingness/heavy-tails/traps; universal ties under drift.
  Floor choice drives quality outcomes more than the optimizer.
- Monitor fold linearithmic (~8ms @10k, ~150ms @100k both platforms).

## Verification (recorded at push)
- Full race suite, Go 1.22 touched packages, protocol conformance, Rust
  untouched (no shared-contract changes), clean-checkout bench rerun,
  Linux build/vet/units/faults/scaling green. Exact commands + results in
  REPRODUCIBILITY.md; raw outputs are the test logs.

## Remaining gaps
CI Format red blocks all gating (owner hygiene commit first); load
ceilings above measured numbers unknown; ledger rotation past ~100k
undesigned; human-verifier fidelity unmodeled; multi-writer untested;
power too low (n=160) for tight economic ranking by construction.

## Recommendation
Per PRODUCT_EVIDENCE_DECISION.md: concentrate on the evidence/evaluation
library; pause adaptive expansion pending representative external
workloads with a pre-registered margin and an explicit second-null stop
rule. Manuscript drafted, unsubmitted, for independent review only.
