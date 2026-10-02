# Next Live-Agent Pilot (Phase 17 — design only, conditional on CONTINUE)

No paid/model-heavy work is executed in this mission. This document is the
complete design for the follow-up, if funded.

## Workloads and runtimes

- Coding-agent workload: two compared runtimes (names fixed at funding time) performing bounded multi-file
  edits against a frozen monorepo snapshot with a real CI gate.
- API/tool-using workload: same two runtimes against a versioned HTTP
  fixture family (same ETag discipline as this assay).

## Witnessing without chain-of-thought access

Reads are witnessed at tool-call boundaries only: file/blob OIDs for
repository reads (via the harness's file tool responses, hashed
client-side), ETags for HTTP GETs. Reasoning cost measured from observable
tokens per call + tool latency + verification CPU — never from hidden
reasoning traces. Premises derive from tool-call arguments and response
bytes consumed by deterministic post-processing; whole responses passed
verbatim into the next model call are opaque-boundary unions.

## Contention, oracle, budget, kill threshold

- Contention injection: background fixture commits/PUTs on a frozen
  schedule at read/reason/validate phases (same phase model as this assay).
- Serial correctness oracle: per-task serial reference on quiescent final
  state + fixture invariants (merge cleanliness, no lost update, price
  monotonicity analogues per domain).
- Fixed budget: 200 USD total paid inference + fixture compute, metered.
- Kill threshold: the pilot KILLS the thesis if EITHER held-out live gate
  fails (any missed stale premise) OR T4-equivalent selective preservation
  is <40% over broad OCC on the live workloads. Threshold frozen here.
- No product infrastructure is built regardless of outcome.
