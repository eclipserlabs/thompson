# Platform and Sibling-Boundary Analysis (Phase 15)

## Is this a harness feature?

The mechanism (witness-carrying reads, premise validation, conditional
commit, input-keyed slice reuse) uses only resource-native identities
(Git OIDs, HTTP ETags) plus deterministic bookkeeping. Any agent harness
COULD reproduce it — but only by implementing the same provenance discipline
per workload; the assay's contribution is showing WHICH discipline is safe
(field/blob premises, opaque union, control marking, coarse-witness limits),
not a runtime only Thompson can host. Value, if any, is protocol-shaped
(witness/validation conventions across runtimes), never vendor-privileged:
no hidden chain-of-thought, no model-specific internals are read anywhere.

## Sibling overlap check (required boundary respected)

- Igris consequential-action execution: untouched (assay mutations are
  fixture writes; no real actions authorized or executed).
- Marshall tool-policy enforcement: untouched (no policy checks exist).
- btree control-flow execution: untouched (no flow engine; slices are
  assay-declared order, not a workflow product).
- Proofline source-code review: untouched (no review product; git content
  is test data, not a review subject).
- Multiple unrelated runtimes could use the same witness/validation layer
  (adapters speak only Git/HTTP).

## Verdict

No overlap found. The assay validates speculative-reasoning premises and
does nothing else. Whether that validation is worth productizing is the
Phase 16 decision, not a boundary question.
