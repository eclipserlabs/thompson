# State-Witness Contract (Phase 1)

Freshness is represented using identities already supplied by the resource
itself. Thompson authors no canonical version of anything external.

## Types

- **ResourceRef**: authority kind (`git`, `http`) + resource identity
  (repo-relative path / URL). A reference, never a version.
- **StateWitness**: ResourceRef + authoritative version/content identity
  (Git blob/commit OID; HTTP strong ETag) + observation timestamp kept for
  DIAGNOSTICS ONLY — timestamps never participate in identity or comparison.
- **WitnessedRead**: value bytes + the StateWitness observed with them +
  subresource path where applicable.
- **Premise**: resource/subresource identity + the witness reasoning was
  constructed against + mechanically derived provenance (which binder rule
  produced it).
- **ReasoningSlice**: slice identity + input premise set + measured work
  units + derived output digest. The unit of preserved/discarded work.
- **MutationCandidate**: operation + arguments + premise closure + producing
  slices. Commit requires revalidation of the closure against live witnesses.

## Invariants

Git OIDs remain Git's identity; HTTP ETags remain the resource's identity; a
Thompson identifier may reference a witness but cannot supersede it.
Wall-clock timestamps are not freshness witnesses. Missing authority →
UNKNOWN, never a manufactured version. A changed witness establishes that
state MOVED — never by itself whether reasoning went stale (premise
comparison decides that) nor whether concurrent operations merge safely.
