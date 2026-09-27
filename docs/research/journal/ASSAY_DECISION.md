# Transactional Journal Assay — Decision Memorandum and Final Engineering Report (V1)

## Starting and ending SHAs
- Start: `origin/main@a433a08` (PRs #25, #26 merged; protection OFF).
- Branch: `research/transactional-journal-assay-v1` (additive
  `go/assay/` + `docs/research/journal/` + go.mod/go.sum SQLite lines
  only; zero production changes). Final SHA recorded at push.

## Exact changed files
- `go/assay/journal/journal.go` (open/schema/writer contract/checkpoints)
- `go/assay/journal/ops.go` (decision/outcome/safety/assignment ops)
- `go/assay/journal/validate.go` (outcome rules mirroring the contract)
- `go/assay/journal/replay.go` (projections, exploration verify)
- `go/assay/journal/journal_test.go` (lifecycle + production equivalence)
- `go/assay/journal/compare_test.go` (matched harness comparison)
- `go/assay/journal/fault_test.go` (kill/contention/corruption/storm)
- `go/assay/cmd/bench/main.go` (matched CLI, digests, JSON results)
- `go/go.mod`, `go/go.sum` (modernc.org/sqlite v1.28.0, pure Go)
- 8 docs under `docs/research/journal/`

## Schema and transactional boundaries
One `events` table (rowid commit sequence; unique idempotency keys;
job/version index) + `exploration` + `checkpoints` + `meta`. Decision+
budget and outcome+version commit atomically; safety persists before
effect; dispatch/verification/operator timing separations unchanged.

## Equivalence, crashes, concurrency
Byte-identical posteriors/costs/cursors on shared histories; all refusal
classes preserved; kill-mid-commit recovers clean prefixes; real
two-process contention refuses (BUSY); storm exactly-once with exact
budgets; corrupt WAL/checkpoint handled without invented history.

## Matched durability and performance
2.04 vs 3.00 syncs/job; 782 vs 1250 B/job; per-settle tied on Linux
(~16ms), journal faster on darwin (2.4 vs 16.2ms); replay 4× slower at
10k (1.52s vs 0.37s). Workload digests identical across impls.

## Removable vs new machinery
Removable: ~500 coordination lines, W3/W4/W6 partial states, 3 seq
counters, torn-tail readers, sidecar, re-scan, jobmap join. New: WAL
tuning, backup discipline, busy-contract handling, pure-Go driver dep.
Net: genuine reduction with a stated operational trade.

## Negative results and limitations
Replay cost; strace wall-time artifact analyzed; sustained-throughput
sweep unmeasured; ledger rotation, multi-writer, production traffic out
of scope; absolute latencies machine-dependent; synthetic workloads only.

## Decision: CONTINUE INVESTIGATION
All four framework conditions hold on current evidence. Smallest next
step: gateway-scoped pilot replacing one treatment's ledgers behind
existing store interfaces, JSONL fallback retained until production
verdicts reproduce. Paper contribution supported (falsifiable collapse
claim with measured costs and retained negatives); manuscript stays
unsubmitted.

## Verification (at push)
Race suite, vet, Go 1.22 (new packages), protocol/Rust unaffected (no
shared contracts touched), Linux build/vet/units green; raw outputs are
the test logs + results JSON files.
