# Premise Binding (Phase 3)

Dependencies derive from actual access and dataflow through the assay's
value wrappers. Workers compose `Read`, `Transform`, `SelectJSON`, `Branch`,
`OpaqueBoundary` — they never hand-list premises, and no LLM is asked what
mattered.

## Supported classes and derivation

- DIRECT_VALUE_FLOW: `Transform` unions input premise sets (taint through
  deterministic maps/filters/formats).
- STRUCTURED_FIELD_ACCESS: `SelectJSON` records exact dotted paths as
  `resource#path@witness` premises. Unparseable docs or missing paths error
  (fail-closed); only provably accessed paths are recorded.
- GIT_CONTENT_ACCESS: `Read` on a git witnessed-read binds the exact blob
  OID consumed (`git:path@oid`).
- OPAQUE_REASONING_BOUNDARY: `OpaqueBoundary` inherits the UNION of all
  input premises, re-provenanced `opaque`. No smaller set is ever claimed.

## Rules (enforced)

Copies propagate taint; control dependence enters only via explicit `Branch`
(a copied value alone never clears control premises); opaque/unknown flags
propagate and never clear; incomplete provenance returns `ErrUnknown` from
`PremisesOf`, forcing callers to widen or UNKNOWN — silent omission is
structurally impossible (there is no API for asserting premises).
