# Linux Reliability Report (Phase 5)

## Environment
- Image: `golang:1.22-bookworm` (go1.22.12 linux/amd64), Docker on darwin
  host; repo bind-mounted read-write (synthetic data only, no customer
  information, no production infrastructure touched).
- Method: same suites and harnesses as darwin; container-local loopback for
  spawned gateways; fault injection where the container permits.

## Build and unit results
- `go build ./...`: exit 0 (linux/amd64, go1.22.12).
- `go vet` (gateway/router/thompson/outcome/harness/cmd/exp-run): clean on
  darwin and Linux (go1.22.12).
- Safety/monitor/cost suites (`TestSafety*`, `TestR2/R3`, `TestArmHealth`,
  `TestCostAware*`, `TestCostBook*`): green on Linux.
- Concurrent fault suite (`TestLinux*`): green — see below.

## Throughput and latency
16-racer sustained selection through full ServeHTTP (commit + fsync'd
evidence per request), loopback, overlayfs:
- Linux: 800 selections, 110/s, p50 139ms, p95 195ms, p99 270ms.
- darwin (same test): 72/s, p50 214ms, p95 269ms, p99 450ms.
Single-writer fsync serialization dominates; concurrency adds queueing,
not throughput. Do not extrapolate production capacity from these
microbenchmarks; they bound the envelope, nothing more.
- Monitor fold, checkpoint, recovery, disk constants: darwin-measured
  (46µs/arm, 19ms, 389µs, ~1.2KB/decision); re-measure on Linux only if
  the deployment target changes the storage story.

## Fault injection
- Disk-full (`/dev/full` as safety path): construction refuses loudly.
  Proven on Linux (no `/dev/full` on darwin — skipped there by design).
- Read-only directory: all three stores refuse construction. Proven on
  darwin as non-root; SKIPPED in the Linux container (runs as root, which
  bypasses permission checks) — labeled UNVERIFIED on Linux, not passed.
- Budget exactness under 32 racers: exact on both platforms.
- Client cancellation: FakeProvider delay path returns before the deadline
  matters on the in-process path; ambiguity documented, real timeout
  coverage lives in the runner e2e (timeout-then-accept).
- fd exhaustion: safety refusal path passes under `ulimit -n 64`
  (fail-closed behavior holds with a constrained table; not a saturation
  proof — the harness itself consumes most of the 64).
- Process termination: covered by SIGKILL crash/resume e2e (darwin);
  Linux actual-binary supervised run (pending).

## Operating envelope (maximum TESTED, not extrapolated)
- Single writer per treatment directory (OS-enforced on all ledgers).
- Monitor fold linearithmic: comfortable to ~10k outcomes/ledger
  (~8ms/fold); 100k costs ~150ms/fold — sustained operation past ~10k
  needs the specified windowed projection or ledger rotation.
- Sustained selection: ~110/s (Linux) / ~72/s (darwin) at 16-way
  concurrency with p99 < 300ms/< 450ms; concurrency queueing, no scaling.
- Actual-binary 4-treatment supervised experiment (60 jobs + operator
  demo + idempotent resume): green on darwin and Linux.
- Crash recovery: SIGKILL at any point → ledger replay converges
  (suspension, budgets, quality, cost); tested with deterioration,
  drift, missing costs, and operator suspension in flight.
- NOT covered: multi-writer operation, load-tested ceilings above the
  above, Linux disk-full live injection beyond construction refusal,
  production traffic of any kind.
