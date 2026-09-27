# Cost-Aware Validation Baseline (Phase 0)

## Repository state (fetched 2026-09-27)
- `origin/main`: `d7a33e68e9484732fad40561f21f038811540ca8` (unchanged from known baseline).
- `origin/feat/cost-aware-v1`: `7c3c4c92be558ee7f9723165033b694eae396988` (matches reported implementation SHA).
- Open PRs: none.
- Review worktree: `~/Documents/thompson-validation`, branch `review/cost-aware-validation-v1`, detached from `7c3c4c9`.
- Unrelated state preserved: main-checkout `M go/harness/report_test.go` untouched; all other worktrees/branches untouched; no push to main, no force-push, no merge, no deploy.

## Documents read
`COST_AWARE_BASELINE.md`, `COST_AWARE_OBJECTIVE_V1.md`, `COST_AWARE_CHARTER_V1.md`,
`COST_AWARE_ENGINEERING_REPORT.md`, plus `OUTCOME_CONTRACT_V1.md`,
`PR3B_CHARTER.md` (frozen, untouched), `PR3B_EXPERIMENT_SPEC.md`.

## Correctness stack on main?
YES — already merged. Markers verified on `origin/main`:
`bootstrapCompare` 4, `checkAttribution` 3, `armInGenesisLocked` 3,
`storage_from_kind` 4, `syncBuffer` 5, `NoopMapper` 3.

## Commit dependency graph
```
d7a33e6 (origin/main: pilot readiness #19, incl. #24 squash 18b738b + #19)
  └─ f26342c (baseline merge of origin/review/correctness-d — EMPTY diff vs d7a33e6)
       └─ 7c3c4c9 (cost-aware v1 implementation, +2059/-2 across 16 files)
```
`git diff d7a33e6 f26342c` is empty: the correctness-stack merge duplicated
content main already carried via PR #24. The reviewable implementation delta is
exactly `f26342c..7c3c4c9` (gate-ordering fix + costaware/costbook/harness +
4 test files + 4 docs). Validation fixes must build on `7c3c4c9` and must not
re-apply the correctness stack.

## Baseline results (pre-change, validation worktree)
- `go test -race -count=1 ./...`: launched background (`sh_0e10b90cf0016YwcGX4fJaT55T`); result: 9/9 ok (VAL_RACE_EXIT=0; slowest opeval 158s).
- Pre-fix audit measurement (scratch, seed 100, cheap p=0.10 vs floor 0.50,
  200 jobs): unsafe picks 18/200 (9.0%), only 2 flagged fallback.
- vet / Go 1.22 / Rust / four-treatment suites: green per implementation
  report; re-run post-fix in Phase 7.
