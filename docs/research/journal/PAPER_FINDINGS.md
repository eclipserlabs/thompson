# Paper Findings (Phase 7)

## Contribution status
A distinct systems-research contribution is SUPPORTED on current
evidence: a falsifiable representation-collapse hypothesis (single
transactional journal vs coordinated JSONL ledgers) tested through
authoritative-state equivalence, refusal preservation, fault injection,
and matched durability measurement — with retained negatives. This is
not a new bandit algorithm, safety theorem, or commercial claim, and
the draft must keep that boundary explicit.

## What the paper draft may now claim (with citations to evidence)
- Collapse removes ~500 coordination lines and three partial-commit
  failure classes, measured, not asserted (DELETION_AND_COMPLEXITY +
  fault battery).
- Durability costs fewer syncs (2.04 vs 3.00/job) and fewer bytes
  (782 vs 1250/job) with slower replay (4× at 10k) — a trade, reported
  whole (PERFORMANCE_RESULTS).
- All refusal and consistency guarantees preserved or strengthened;
  writer-contract boundary documented as different (open-time vs
  first-contention), not weaker.
- Negative results included: replay cost, strace wall-time disagreement
  analyzed not averaged, unresolved envelopes (sustained throughput,
  ledger rotation, multi-writer).

## What it must not claim
Faster settling as an architectural ranking (platform-dependent tie);
production readiness; customer economics; safety guarantees beyond the
tested envelope; multi-replica behavior.

## Suggested paper updates (not applied — draft frozen for review)
Replace the readout paragraphs in PAPER_DRAFT.md sections 5–7 with the
tables above; add the refusal-boundary difference to limitations.
