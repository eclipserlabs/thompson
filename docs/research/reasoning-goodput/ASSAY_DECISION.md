# Assay Decision (Phase 16)

Thresholds frozen 2026-10-01 in DECISION_THRESHOLDS.md; applied unmoved.
Machine evidence: testdata/goodput_matrix.json, testdata/scale.json.

## Gate tally

1. Zero stale-premise violations (matrix + held-out): PASS.
2. ≥40% more preserved work than T2, ≥2 witness systems: PASS (git 50%,
   http 50% — narrow, exact, deterministic).
3. T4 ≤ 0.5× T3 discarded on partial invalidation: PASS (50% both —
   boundary-inclusive).
4. ≥80% mechanical bindings: PASS (12/12 counted from accepted candidates).
5. Opaque usefulness: PASS (broad collapse + mixed recovery, both measured).
6. Overhead: PASS (T4 wall beats T2 wall where work is preserved).
7. No second authority: PASS.

## Decision: CONTINUE_TO_LIVE_AGENT_PILOT

All seven hold. Negative findings retained (they bound, not break, the thesis):

- T3 ≡ T2 wherever witnesses are coarse or reads fully consumed; premise
  minimization pays only with fine witnesses + unconsumed reads.
- Opaque collapse is total at 100% broad context; mixed pipelines are the
  only demonstrated rescue, and they require narrowing BEFORE the opaque
  call — an application architecture constraint, not a free lunch.
- Below ~2–5× reasoning-to-validation cost, the machinery loses to its own
  bookkeeping; the thesis is economical only where reasoning is expensive
  (which, per the assay's own limitation note, is assumed — not proven —
  for model reasoning).
- Margins on gates 2–3 are exactly at the structural values the workload
  geometry dictates (50%), not comfortably above threshold.

## Smallest justified next task

The live-agent pilot designed in NEXT_LIVE_AGENT_PILOT.md (design only,
fixed budget, kill threshold pre-registered). No product infrastructure.
