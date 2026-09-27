# Performance Results (Phase 4)

Method: matched CLI (`go/assay/cmd/bench`, digest-pinned workloads),
same Linux host+filesystem for the matched pair, WAL + synchronous FULL
vs per-write fsync JSONL, randomized run order (journal-first here;
order-swapped rerun noted below), harness overhead measured separately
(~50ns minimal learner op; negligible).

## Matched n=1000 (Linux, strace-counted syncs)
| | journal | JSONL |
|---|---|---|
| syncs/job (fsync+fdatasync) | 2.04 (2039/1000) | 3.00 (3002/1000) |
| steady-state bytes/job | 782 | 1250 |
| full replay (1k) | 0.20s | 0.07s |
| workload digest | identical (`assay-bench-v1-6b6ad…`) | identical |

Wall times under strace are tracer-inflated and machine-dependent
(journal ~63ms, JSONL ~36ms per commit+settle in that run — an artifact
of `-f` tracing overhead, not an architectural ranking); the durable
comparison rests on sync counts and bytes, which strace does not distort.
Darwin (no tracer): journal ~2.4ms vs JSONL ~16.2ms per commit+settle.

## Matched n=1000 (darwin)
| | journal | JSONL |
|---|---|---|
| commit+settle/job | 2.4ms | 16.2ms |
| steady-state bytes/job | 782 | 1250 |
| full replay (1k) | 50–90ms | 14ms |

## Scaling
| n (Linux) | journal commit+settle | jsonl commit+settle | journal B/job | jsonl B/job | journal replay | jsonl replay |
|---|---|---|---|---|---|---|
| 1,000 | ~63ms* | ~36ms* | 782 | 1250 | 0.20s | 0.07s |
| 10,000 | 21.0ms | 16.9ms | 740 | 1251 | 1.52s | 0.37s |

\* strace-inflated wall times; the untraced order-swapped rerun ties
them (jsonl 15.9ms vs journal 15.7ms per commit+settle at n=1000):
per-settle latency does not distinguish the designs on this box. The
durable comparison rests on sync counts (2.04 vs 3.00/job) and bytes
(782 vs 1250/job), which tracing does not distort. Untraced darwin:
journal 2.4ms vs JSONL 16.2ms per commit+settle at 1k (APFS fsync
latency profiles differ per platform; architectural counts transfer,
absolutes do not).

## Sync operations per completed job (strace, Linux)
2.04/job journal (1 decision txn + 1 outcome txn) vs 3.00/job JSONL
(assignment + outcome + decision rows). Both counted, not estimated.

## Sustained throughput
Not pushed to refusal: single-job loops measure latency, not arrival
capacity. The fsync-per-job counts above bound single-writer throughput
analytically (~1/sync-latency); a sustained arrival sweep to explicit
refusal is UNMEASURED — recorded as a gap.

## Variance
Two runs per cell (journal-first and order-swapped) plus darwin/Linux
pairs; strace vs untraced disagreement analyzed above, not averaged
away. Single-favorable-run claims excluded by construction.
