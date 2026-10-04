# Thompson Research Closeout

**Final product decision: every Thompson product thesis tested in this
repository was either rejected or killed by its own pre-registered
experiment. No Thompson product thesis is currently authorized.**

**Terminal status: `THOMPSON_PRODUCT_RESEARCH_CLOSED`**

- PR #40 merge SHA: `713874da6c7ac521af87bffed11c1628fdea1e8e`
  (real-replay economics assay, `KILL_INCREMENTAL_REPLAY_ECONOMICS`).
- Closeout branch: `docs/thompson-research-closeout-v1` (from
  post-PR-40 `origin/main`).
- Canonical status: `docs/research/STATUS.md`. Component inventory:
  `docs/research/COMPONENT_STATUS.md`.

## What was disproven

- **Adaptive routing as a product/company thesis.** No rankable
  Thompson-over-competent-static win in 8 evaluated scenarios; ties
  where the static is optimal; material losses under missing costs,
  heavy tails, and human traps (`ADAPTIVE_ECONOMICS_RESULTS.md`,
  `PRODUCT_EVIDENCE_DECISION.md`).
- **Selective/incremental replay as an economically justified product
  mechanism.** The final real-replay assay executed actual replay and
  found it loses to full restart on new model work in every qualifying
  scenario, because the observable-only continuation forfeits provider
  prompt-cache locality (~8–11k uncached prefill tokens per resume).
- **ReasoningTransaction incremental replay as the next product
  direction.** Killed with the mechanism above; the
  `NEXT_REASONING_TRANSACTION.md` design is superseded, not queued.
- **Reasoning-goodput as sufficient evidence of economic benefit.**
  Goodput favors replay even where replay executes more new work; it is
  reported but not gated, by frozen design.
- Earlier bets, recorded in the frozen priors: generic verified
  runtime / DAG materialization, outcome-ledger product, artifact
  resolver as company thesis, generic agent-eval platform, trace
  compiler as company thesis.

## What was technically demonstrated

- Actual selective replay implemented correctly on Git using observable
  state only; preserved model calls genuinely not re-executed.
- Fresh-state correctness matching the deterministic oracle in accepted
  Git runs; commit-time native-witness validation preventing stale
  accepted commits; Git CAS and HTTP precondition race probes correctly
  rejecting stale commits.
- A tested set of primitives: Thompson Sampling policy libraries
  (Rust + Go, wire-compatible), verified-mode settlement with an
  append-only outcome ledger, and refusal-gated offline evaluation /
  propensity tooling.
- Transparent premise capture that works mechanistically but has
  substantial tool-coverage gaps (`execute(fetch(...))`, `search`).

## What remains reusable

See `COMPONENT_STATUS.md`. In short: the policy libraries, gateway
(in verified mode within its stated single-writer limits), outcome
ledger implementation, offline evaluation/propensity tooling, and the
control-plane wire protocol — as primitives, not products. All assay
harnesses and evidence directories remain for reproducibility.

## What remains unproven

- Guard (commit-time validation) as a standalone product thesis:
  positive correctness evidence, no continuation decision earned.
- Journal single-log replacing gateway ledgers in production: prototype
  only, pilot not authorized under this lineage.
- Contextual routing: explicitly no evidence either way; do not build
  yet was the recorded gating, and this closeout does not reopen it.
- Anything resembling effect protocols, MCP transactions, multi-agent
  orchestration, or merge-set verification: never tested here, never
  authorized here.

## Why the research stops here

Each thesis in this lineage was given a pre-registered experiment with
a kill threshold, and each one hit its kill condition or never earned
continuation. The final assay closed the last open direction
(incremental replay) with a decisive negative on the honest cost
metric. Continuing to search inside this lineage would mean
reinterpreting negative evidence into a new thesis without new
external evidence — which this closeout explicitly refuses to do.
The useful code is preserved; the product search is over.

## Conditions required to reopen Thompson research

Do not reopen this lineage merely because a deeper abstraction can be
imagined. Reopening requires **new external evidence of a concrete
repeated user problem plus evidence that existing frontier
harnesses/platforms do not already absorb it**. A new thesis built on
these primitives must arrive with its own pre-registered protocol, its
own kill thresholds, and its own budget — and it starts as a new
lineage, not as a continuation of this one.
