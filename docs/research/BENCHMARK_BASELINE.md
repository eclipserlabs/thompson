# Benchmark Baseline (Phase 0)

## Repository state (fetched 2026-09-27)
- `origin/main`: `d7a33e6` (unchanged). No open PRs except PR #25.
- PR #25 (`review/reliability-v1` → `main`): OPEN at `0ad84b7` (19 commits).
  CI: Go FAIL + Rust FAIL both at the **Format** step (pre-existing
  gofmt/rustfmt dirt also present on main) — Vet/Test never ran in CI.
  Reproduce-findings and replay jobs pass.
- Research worktree: `~/Documents/thompson-research`, branch
  `research/benchmark-v1`, at `0ad84b7`, clean. All prior missions'
  work preserved; main-checkout `M go/harness/report_test.go` untouched.

## Ancestry
PR #25 contains the complete stack (correctness A–D via main's squashes,
cost-aware implementation, validation fixes, supervised-pilot integration,
reliability hardening). The benchmark builds on it directly; nothing is
reimplemented.

## Baseline suites
`go test -count=1 ./...` launched pre-change; result recorded in the final
engineering report (post-change full matrix re-run there).

## Extracted Linux measurements (prior mission, independently re-run here
where load-bearing: build/vet/units green on Linux; fault/e2e suites
re-run in Phase 5)
- Build/vet clean; safety/monitor/cost suites green.
- Sustained selection: ~110/s Linux / ~72/s darwin @16 racers, fsync-bound.
- Faults: /dev/full refuses (Linux), read-only refuses (darwin non-root),
  budgets race-exact, supervised 60-job e2e green both platforms.
- Monitor fold ~8ms @10k outcomes, ~150ms @100k (darwin).

## Machine (this mission's numbers)
- MacBook Pro x86_64, Intel i5-7360U 2.3GHz, 8GB RAM, macOS 13.7.8, APFS.
- go1.27.1 darwin/amd64; rustc 1.90.0; Linux runs in golang:1.22-bookworm
  (go1.22.12 linux/amd64) via Docker, repo bind-mounted.
- Runtime: default GOMAXPROCS, no cgroup limits locally; container
  unconstrained except where fault tests impose limits explicitly.
