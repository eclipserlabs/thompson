# Cost-Aware Experiment Charter V1 (four-treatment)

Frozen for this mission. Changing any field restarts the experiment.
The existing `PR3B_CHARTER.md` (three-treatment cost-blind charter) is untouched.

- CharterID: `cost-aware-charter-v1`
- Objective version: `cost-aware-v1`
- Policy identities:
  - T0 `static-fixed-v1` (NoopMapper, fixed single arm, no learning)
  - T1 `static-cheap-v1` (NoopMapper, cheapest static, no learning)
  - T2 `exact-thompson-v1` (BinaryStatusMapper, cost-blind Thompson)
  - T3 `thompson-costaware-v1` (BinaryStatusMapper quality + CostBookV1 costs)
- Treatments: t0, t1, t2, t3; randomized assignment, weights [1,1,1,1].
- Assignment RNG independent from policy RNG (assigner hash `treatment-assignment-v1`; policy PCG streams).
- Workload: synthetic only (`synthetic-dry-run`, seed-pinned manifests); no customer data; no commercial claims.
- Arms: cheap / strong (/ fixed for T0); every job carries per-arm success_p, cost_usd (nil stays unknown), latency_ms.
- Primary metric: fully loaded cost per verified successful job at pre-registered quality floor (0.30 default; per-run value in report config).
- Secondary metrics: verified success rate, cost/assigned, retry/fallback frequency, human-review burden, censoring, missing-cost rate, p95 latency, exploration cost, recovery/settlement overhead.
- Quality floor is a gate (`quality-floor-*`); missing-cost gate `missing-cost-*` (default share 0.10); censoring gate; min-jobs gate; allocation check (A3).
- Comparison: bootstrap CIs (`bootstrapCompare`, fixed seed), validity fraction gate, `MeetsBar` requires win + validity + min effect.
- Verdicts: RANKABLE only when gates pass and CIs support; otherwise NOT_RANKABLE with reasons. Regressions must be exposed, never hidden.
- Isolation: per-treatment policy state, decision ledger (`decisions.jsonl` for T3), outcome ledger (`outcomes.jsonl`), assignment log, checkpoints. No cross-treatment reads.
- Reproducibility: frozen seeds, sorted gate emission, deterministic replay (quality posterior + cost book rebuild from ledger order).
- Stop rules: malformed costs (NaN/Inf/negative) refuse; version gaps refuse; all-arms-below-floor → explicit fallback flagged, report gate refuses conclusions.
