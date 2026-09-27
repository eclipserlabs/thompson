# Journal Assay Baseline and Experiment Plan (Phase 0)

## Baseline (fetched 2026-09-27)
- `origin/main`: `a433a08` (PRs #25, #26 MERGED; branch protection still
  OFF — 404).
- Assay worktree: `~/Documents/thompson-assay`, branch
  `research/transactional-journal-assay-v1`, at `a433a08`, clean.
- Main-checkout `M go/harness/report_test.go` preserved; all worktrees
  untouched; no production modifications will be made (additive
  `go/assay/` + `docs/research/journal/` only).
- Pre-change suite `go test -count=1 ./...` launched; result in the final
  assay report (post-change matrix re-run there).

## Comparison methodology (frozen before building)
- Same workload events + seeds through both implementations; compare
  AUTHORITATIVE STATE (policy/cost/exploration/safety reconstructions +
  subsequent decisions under controlled RNG), never API responses alone.
- Durability matched: SQLite WAL + synchronous FULL vs per-write fsync
  JSONL. Unmatched settings are reported as unmatched, never averaged away.
- Benchmark-harness overhead measured separately (no-op comparator runs).
- Machine: MacBook Pro x86_64 i5-7360U 2.3GHz, 8GB, macOS 13.7.8, APFS;
  go1.27.1 (compat go1.22.0); Linux in golang:1.22-bookworm via Docker.
- Thresholds: equivalence = byte-identical reconstructions + identical
  subsequent decisions; refusal = same rejections on the same inputs;
  performance = p50/p95/p99 + distributions over repeated trials with
  randomized run order; no single-favorable-run claims.

## Acceptance (from mission decision framework)
Continue only if: equivalence + refusals hold everywhere tested; net
deletable machinery is meaningful; performance acceptable under matched
durability; complexity reduced or demonstrably justified. Otherwise retain
(and say which smaller change captures the value) or call inconclusive
with the blocking evidence named.
