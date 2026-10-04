# Frozen Decision Thresholds (Phase 16 pre-registration)

Frozen 2026-10-01 BEFORE held-out treatment comparisons are built or run.
These thresholds must not be weakened based on observed performance. A clean
KILL is an acceptable result.

## CONTINUE_TO_LIVE_AGENT_PILOT requires ALL of:

1. Zero stale-premise correctness violations (no accepted mutation with a
   stale required premise; no oracle mismatch) in held-out tests.
2. T3 or T4 preserves ≥40% more reasoning work than T2 broad OCC under
   meaningful contention, in at least two witness systems. "Preserves X%
   more" = (T2_discarded − T3/T4_discarded) / T2_discarded ≥ 0.40, measured
   on scenarios where T2 discards non-trivially (>1 slice of work) and at
   least one conflicting change actually lands.
3. T4 materially outperforms T3 where partial invalidation exists:
   T4_discarded ≤ 0.5 × T3_discarded on the disjoint/field-independent
   scenarios (proves selective replay, not just smaller read sets).
4. ≥80% of the premise bindings responsible for the measured benefit are
   mechanically derived (field/blob-path premises from SelectJSON/Read),
   not whole-resource unions: count premise entries by provenance across
   the deciding validations; `field`+`git`+`direct` ≥ 0.8.
5. Useful at realistic opaque-context fraction: at least one opaque scenario
   where T3/T4 beat T2, OR a mixed pipeline recovering selectivity —
   opaque collapse in every opaque scenario fails this gate.
6. Overhead check: validation/provenance/replay bookkeeping ≤ 1/9th of
   preserved work in the ≥10x regime (i.e., overhead does not erase the
   gain where reasoning costs ≥10× validation).
7. No independent mutable world-state/version authority introduced
   (representation audit).

## KILL / NOT_GENERAL_ENOUGH / KEEP_WITNESS_PROTOCOL_ONLY

Per mission definitions. In particular: opaque collapse everywhere →
NOT_GENERAL_ENOUGH (not a tuning problem); any missed stale premise →
KILL_REASONING_GOODPUT; T3/T4 ≈ T2 → KILL; benefit only via whole-resource
unions → NOT_GENERAL_ENOUGH.
