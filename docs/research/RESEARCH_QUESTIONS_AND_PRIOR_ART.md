# Research Questions and Prior Art (Phase 1)

## Prior art (established techniques; nothing below is claimed as novel)
- **Thompson Sampling**: Thompson (1933); Russo & Van Roy (2014–2016) information-theoretic analysis; Chapelle & Li (2011) empirical evaluation. The Beta-Bernoulli sampler used here is textbook.
- **Delayed / censored feedback**: Joulani, György & Szepesvári (2013) on delayed bandits; Vernade, Cappé & Perchet (2017, 2020) on delayed and partially observable rewards. Maturation windows and censoring gates are standard practice.
- **Conservative / safe bandits**: Kazerouni et al. (2017) conservative bandits; Wu et al. (2016) safe exploration; recent conservative contextual work. Quality floors, pre-qualified sets, and fallback policies are instances of this family.
- **Off-policy evaluation**: Horvitz–Thompson/IPW propensities; Dudík, Langford & Li (2011) doubly robust estimation. The propensity-validity gate here (exact-Thompson denominator or refuse) follows this literature.
- **Event sourcing / durable execution**: Fowler (2005) event sourcing; ARIES write-ahead logging; Temporal / Durable Functions for crash recovery. Append-only outcome ledgers with replay are standard engineering.
- **Cost-aware model routing**: FrugalGPT (Chen et al., 2023) cascades with cost-quality trade-offs; RouteLLM (Ong et al., 2024) learned routing; static single-model and cheapest-qualified routing as industry baselines.

## Candidate contribution (defensible, narrow)
Not a new bandit algorithm and not a safety theorem. The defensible claim,
if measurements support it, is a *systems* one: an integrated architecture
in which independently verified outcomes, correction-safe dual (quality +
cost) learning, deterministic replay, operator safety interlocks, and
refusal-gated statistical reporting compose without silent semantic breaks —
plus measured costs, measured failure modes, and retained negative results.
Each mechanism above exists elsewhere; the composition under test, with its
ablation and adversarial battery, is what is being evaluated.

## Research questions, hypotheses, and nulls
- **RQ1 (correctness):** does crash/duplicate/correction handling reconstruct
  identical authoritative state? H: yes across all tested boundaries. Null:
  at least one boundary diverges silently or refuses spuriously. Method:
  replay-equivalence + crash matrix (S1–S3, S5). Negative result = any
  silent divergence (mission-critical finding, not a footnote).
- **RQ2 (overhead):** what does durability cost vs a minimal in-memory
  learner? H: persistence (fsync per commit) dominates; policy math is
  negligible; correction support adds replay cost only on correction.
  Null: durability overhead is indistinguishable from noise (would imply
  over-engineering) or dominates to the point of uselessness. Ablation S4.
- **RQ3 (economics):** under which workload properties does cost-aware
  adaptation beat competent statics net of ALL operating costs? H: wins
  when quality gaps are small and cost gaps large and stable; loses under
  nonstationarity, heavy missingness, and human-correction traps. Null:
  no measured scenario shows net advantage (kills the adaptive product
  case) or statics never win (suspicious — indicates weak baselines).
- **RQ4 (interpretability):** how do missingness, delay, deterioration,
  and interventions affect verdicts? H: gates refuse exactly when the
  comparison stops supporting conclusions. Null: gates fire on healthy
  runs (false-alarm problem) or stay silent on corrupted ones.

## What synthetic fixtures can and cannot support
Can: correctness (exact replay), overhead attribution, mechanism behavior
under adversarial regimes, gate calibration, negative-result existence.
Cannot: commercial savings, real workload distributions, human-verifier
fidelity, production failure rates. Anything beyond synthetic scope is
labeled as such; representative external workloads require the documented
de-identified interface (Phase 3), not extrapolation.
