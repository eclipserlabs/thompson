# Thompson Research Status (canonical)

**Current status: Thompson's product-research lineage is closed.**
See `CLOSEOUT.md` for the terminal record.
The adaptive-routing and incremental-replay product theses were not
supported by the experiments in this repository. The codebase remains as
a collection of tested engineering primitives and reproducible systems
experiments, not as a validated product.

## Vocabulary

These words mean different things in this file:

- **Working mechanism** — code that demonstrably does what it claims under
  test (e.g. commit-time premise validation refuses stale commits).
  A working mechanism is not a product.
- **Useful library primitive** — a tested component others can reuse
  (e.g. the Thompson Sampling policy libraries, the offline evaluation
  tooling). Reuse value is not a product thesis.
- **Research result** — a pre-registered measurement with a recorded
  verdict, including negative results. A KILL verdict is a result, and it
  ends its thesis; it does not imply a replacement.
- **Product thesis** — a claim that a mechanism solves a repeated user
  problem well enough to build a company or product on. **No Thompson
  product thesis is currently authorized.** Do not promote a mechanism or
  primitive into one without new external evidence (see `CLOSEOUT.md`).

## Decision index (chronological)

1. **Adaptive/static economics** — the original adaptive-routing company
   thesis. No rankable Thompson-over-competent-static win in 8 evaluated
   scenarios; ties where the static is optimal; losses under missing
   costs, tails, and traps. Decision: concentrate on the evidence and
   evaluation library; pause adaptive-execution expansion.
   Authoritative: `docs/research/ADAPTIVE_ECONOMICS_RESULTS.md`,
   `docs/research/PRODUCT_EVIDENCE_DECISION.md`.
2. **Earlier systems bets (frozen priors)** — model routing as company
   thesis rejected; generic verified runtime / DAG materialization /
   physical-plan optimizer rejected or narrowed; outcome-ledger product
   KILLED; artifact resolver extracted as a technical primitive only,
   explicitly not a product; generic agent-eval platform and trace
   compiler rejected as company theses.
   Authoritative: `docs/research/reasoning-goodput/BASELINE.md`
   ("Frozen priors").
3. **Journal (transactional SQLite-WAL prototype)** — a single
   transactional log can replace the gateway's multiple ledgers with
   byte-identical posteriors and exact budgets, at stated operational
   cost (slower replay at 10k, WAL/backup discipline). Decision:
   CONTINUE INVESTIGATION toward a gateway-scoped pilot, not a product.
   Authoritative: `docs/research/journal/ASSAY_DECISION.md`.
4. **Materialized execution / DAG** — covered under decision 2: the graph
   added cost without eliminating operations; replanning value confined
   to shape changes. Not a product direction.
5. **Verify-resolution / artifact resolver** — covered under decision 2:
   technical primitive only, explicitly not a product thesis. The
   primitive itself now lives in the product tree at
   `go/artifactresolver/` (salvaged from the stranded
   `feat/artifact-resolver-v1` branch during the substrate cleanup).
   The closest gateway-integrated machinery is verified-mode settlement
   in `go/gateway` (`settle.go`, `evidence.go`) plus `go/outcome`, which
   learns only from settled, independently verified outcomes.
6. **Outcome ledger** — covered under decision 2: KILLED as a product
   (incumbent + warehouse reconstructs). The ledger implementation in
   `go/outcome` remains a useful primitive.
7. **Reasoning-goodput assay (#36) and erratum** — synthetic-work
   evidence that premise-tracked selective replay preserves work, with a
   recorded CONTINUE_TO_LIVE_AGENT_PILOT. The later erratum records that
   gate 6 as implemented fails deterministically on the committed
   fixture, and that the prose gate was too underspecified to evaluate;
   the historical verdict stands as written but was not supported by all
   seven gates. The assay never measured validation/replay bookkeeping
   cost against preserved work.
   Authoritative: `docs/research/reasoning-goodput/ASSAY_DECISION.md`,
   `docs/research/reasoning-goodput/EVIDENCE_ERRATUM.md`.
8. **Live-agent premise pilot (#37)** — transparent premise capture on
   real agent runs with 8/8 frozen conditions holding, verdict
   CONTINUE_TO_REASONING_TRANSACTION_PROTOTYPE as recorded. The erratum
   classifies it INSUFFICIENT_EVIDENCE for economic purposes: preservation
   was counterfactual (not actual replay), mostly the initial uncached
   prefill rather than reasoning slices, with no validation-cost
   measurement and no re-derivable premises (worktrees not in repo).
   Authoritative: `docs/research/live-agent-premise/ASSAY_DECISION.md`,
   `docs/research/reasoning-goodput/EVIDENCE_ERRATUM.md` (§6).
9. **Real-replay economics assay (#40, final)** — actual selective replay
   executed for real on Git and HTTP. Decision:
   **`KILL_INCREMENTAL_REPLAY_ECONOMICS`** (Guard mode retained as a
   mechanism, not a product). Observable-only replay forfeits provider
   prompt-cache locality: resumed sessions re-pay ~8–11k uncached input
   tokens of prefill, so selective replay executed more new model work
   than full restart in every qualifying Git scenario (G1 −99.5%,
   G1b −32.8%); the one positive number (H1 +47.3%) came with zero
   post-first-call preservation and R1-side cache misses, not from
   preservation. HTTP scored runs refused to commit because the agent
   fetched through unclassified tools (transparent-capture coverage gap).
   Correctness evidence held wherever results were accepted (Git R2 == R0
   oracle, zero stale accepts, race probes rejected).
   Authoritative: `docs/research/real-replay-economics/PROTOCOL.md`,
   `docs/research/real-replay-economics/RESULTS.md`,
   `docs/research/real-replay-economics/decision.json`.
   The `NEXT_REASONING_TRANSACTION.md` design in the live-agent-premise
   directory is superseded by this KILL: no ReasoningTransaction
   prototype is authorized.

## What this means

- The adaptive-routing/company thesis did not demonstrate a durable
  economic advantage. It is not revived by any later result.
- Selective/incremental replay as an economically justified product
  mechanism was killed by the final assay. The proposed
  ReasoningTransaction direction is not the next step.
- Verified-artifact machinery, the outcome ledger implementation, the
  policy libraries, and the evaluation tooling may remain useful
  engineering components. None of them is currently a standalone
  product thesis.
- Guard behavior (commit-time native-witness validation) produced
  positive correctness evidence across the final assay, but Guard has
  not earned a product continuation decision. Do not start a Guard
  coverage assay or build Guard product work under this lineage.
- Per-component standing is inventoried in `COMPONENT_STATUS.md`.
- The terminal record, including conditions to reopen, is `CLOSEOUT.md`.
  Terminal status: `THOMPSON_PRODUCT_RESEARCH_CLOSED`.
