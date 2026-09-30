# Representation Analysis (Phase 14)

Question: does Thompson create any resource version separate from the
native authority, or only derived provenance?

## Audit

- Resource state authority: Git OIDs come from `git rev-parse`/`cat-file`;
  HTTP versions come from server `ETag` headers. The assay never mints,
  stores-then-trusts, or supersedes a version. `StateWitness.Version` is
  always a copy of a natively observed value, compared by equality only.
- PremiseSet: derived dependency facts (resource + witness + rule label),
  recomputed from access provenance on every validation. Regenerable from
  trace/access records (the durable log replays slice inputs + outputs).
- No mutable facts are represented both by Thompson and the resource: the
  assay owns no version table, no staleness flags on resources, no cached
  "current" etags used as authority (validation always re-reads where the
  comparator requires freshness; the T4 slice cache is keyed by input
  digests and revalidated, never trusted).
- Selective replay needs no durable shadow world-state: the crash log holds
  slice outputs + premises (derived data), and resume re-reads live
  witnesses before any reuse. Deleting the log only costs re-execution.

## Verdict

Desired result holds: exactly one authority per resource state; Thompson
stores derived provenance linking reasoning work to authoritative witnesses.
The conservation-law thesis survives — kill condition not triggered.
