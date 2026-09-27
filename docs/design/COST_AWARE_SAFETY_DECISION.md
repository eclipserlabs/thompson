# Cost-Aware Safety Decision (Phase 3)

## Problem (F1)
Post-evidence qualification uses a Thompson SAMPLE crossing the floor.
Measured: true-p=0.10 arm at floor 0.50 selected 9% over 200 jobs, 16 of 18
picks labeled "genuine optimum". A sample is not a guarantee, a bound, a
mean, or an observed rate.

## Options evaluated
1. **Pre-qualified approved set** (manifest arms). Already present; bounds
   WHERE exploration goes, not whether an approved arm may decay. Keeps.
2. **Bounded exploration budget**. Already effectively present: cold
   qualification ends at `ColdStartPulls` per arm (~10 jobs for 2 arms);
   fallbacks are counted (`Fallbacks`, persisted per decision). No new cap:
   a hard stop would need a designated baseline to pin to (customer input,
   ops integration) — deferred, documented below, not coded.
3. **Statistical qualification** (SELECTED): once `pulls >= ColdStartPulls`,
   qualify iff posterior MEAN >= floor (not sample). Guarantees: no
   post-evidence "optimal" pick of an arm whose current estimate is below
   floor. Does not guarantee: the true rate is above floor (estimates err;
   bounded by ongoing learning + report gate). Cold arms stay explorable but
   never optimal, still counted/flagged.
4. **Explicit baseline pin** when nothing qualifies. DEFERRED: current
   flagged max-sample fallback + report-gate refusal is the honest
   experimental behavior; pinning needs a customer-designated baseline arm
   (not a code default). Recorded as deployment prerequisite, not implemented.

## Decision
Implement (3) only: mean-gate post-cold. Cold rule, fallback flagging,
identities, metrics, gates, charters all unchanged. Same scientific question,
stricter compliance — no charter amendment, no redesign, no STOP trigger.

## Versioning
Behavior changes, so decisions must distinguish rules: add additive
`RuleVersion` (`1`=sample rule, `2`=mean rule) to `CostAwareResult` and
persisted decisions. Family `PolicyID` (`thompson-costaware-v1`) and
`CostAwareStateVersion` (state representation unchanged) stay put; ledgers
stay compatible; experiment restarts under the new SHA with before/after
results in Phase 5. If reviewers require a full `…-v2` identity instead, that
is a one-line follow-up plus charter note — flagged, not blocking.
