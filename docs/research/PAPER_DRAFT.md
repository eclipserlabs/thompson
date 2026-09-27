# Verifiable Adaptive Execution: Durable Outcome Learning and Reproducible Evaluation for AI Workloads
*(Technical paper draft — for independent review. NOT submitted, NOT published.)*

## Abstract
We describe and measure an architecture for adaptive execution of AI
workloads that treats independently verified outcomes as an append-only
ledger, folds them into quality and cost estimators through
correction-safe replay, and gates every comparative claim behind
pre-registered statistical refusal rules. On 14 deterministic synthetic
workload families we show: (1) crash, duplication, and retrospective
correction handling reconstruct byte-identical authoritative state;
(2) durability costs ~6ms per settlement vs ~50ns in-memory, with
correction safety adding ~2ms over plain logging; (3) cost-aware
adaptation matches but never supportably beats a competent cheap static
policy, and loses materially under missing costs, heavy tails, and
human-correction traps — all exposed, none hidden, by the same gates.
No new bandit algorithm is claimed; the contribution is the integrated,
measured, adversarially tested composition.

## 1. Introduction
AI workloads choose between models, retries, and human review per task.
Costs and quality vary; verification arrives late, sometimes never, and
sometimes gets revised. This paper asks what it costs — in systems terms —
to learn adaptively under those conditions without fooling ourselves.

## 2. Related work
Thompson Sampling (Thompson 1933; Russo & Van Roy; Chapelle & Li); delayed
bandits (Joulani et al.; Vernade et al.); conservative bandits (Kazerouni
et al.); OPE/IPW and doubly robust estimation (Dudík et al.); event
sourcing and durable execution (Fowler; ARIES; Temporal); cost-aware
cascades and routing (FrugalGPT; RouteLLM). Each mechanism used here comes
from this literature; see RESEARCH_QUESTIONS_AND_PRIOR_ART.md.

## 3. System design
Decision → atomic commit → dispatch → independently verified settlement →
dual estimators (Beta-Bernoulli quality, metered-cost means) → correction-
safe rebuild → operator safety interlocks (prequalification, budgets,
suspension, fallback) → refusal-gated reporting. Key invariants: no
dispatch without commitment; no learning from uncommitted records;
missing costs never zero-filled; samples never treated as guarantees.

## 4. Methodology
Versioned synthetic corpus (14 scenarios, content digests, dev/eval seed
split); independent minimal/logged/static baselines with proven parity;
frozen analyzer (bootstrap CIs, validity fractions, quality/missing/
censoring gates); identical traces, seeds recorded, harness overhead
measured separately. Full matrix in BENCHMARK_BASELINE.md.

## 5. Results
Correctness: zero divergence across replay/interruption/rebuild, seven
delivery permutations, crash matrix (SYSTEMS_CORRECTNESS_RESULTS.md).
Performance: ablation + scaling tables (SYSTEMS_PERFORMANCE_RESULTS.md).
Economics: sweep table with retained negatives (ADAPTIVE_ECONOMICS_RESULTS.md):
adaptation matches optimal statics, loses under missingness/tails/traps,
ties under drift; no rankable T3-over-T1 claim anywhere at n=160.

## 6. Limitations
All economic results synthetic; human-verifier fidelity unmodeled;
production failure rates unknown; single-writer only; monitor fold bounds
(~10k/ledger); absolute latencies machine-dependent. See
THREATS_TO_VALIDITY.md.

## 7. Conclusion
Durable, correction-safe adaptive execution is achievable at measured,
attributable cost — and the same measurement apparatus shows its economic
uplift over competent statics is, on current evidence, zero or negative
outside narrow stationary conditions. The evidence library (ledgers,
verification contract, refusal-gated reporting) retains value
independently of adaptive selection. Recommended next experiment:
representative external workloads through the de-identified interface,
powered for the pre-registered economic margin — or stop expanding the
runtime if none materializes.
