# Supervised Pilot Baseline (Phase 0)

## Repository state (fetched 2026-09-27)
- `origin/main`: `d7a33e6` (unchanged). Open PRs: none.
- `origin/feat/cost-aware-v1`: `7c3c4c9` (unchanged).
- `origin/review/cost-aware-validation-v1`: `bf8822d` (unchanged).
- Feature worktree: `~/Documents/thompson-pilot`, branch
  `feat/supervised-pilot-v1`, at `bf8822d`, clean.

## Validation diff inspected
`origin/review/cost-aware-validation-v1` CONTAINS the entire cost-aware
implementation (`merge-base --is-ancestor` cost-aware → validation: yes).
No explicit dependency needed: base the pilot directly on `bf8822d`.
Prior correctness/consolidation merges are already on main (empty
`f26342c`); nothing is duplicated by basing on the validation head.
Regression tests present: RuleV1/V2 replay, mean-gate bound, atomic
refusal, parity, config binding, crash resume, 4-treatment experiment,
drift exposure, 10-scenario adversarial matrix, validation battery
(deterioration/delayed/human-trap/missing-blinds).

## Worktrees and unrelated work
Six pre-existing worktrees untouched. Main-checkout `M
go/harness/report_test.go` (another agent) preserved — no contact.
README branches (`docs/readme-*`) untouched.

## Isolation and push path
- Local pre-push hook present (blocks `main` pushes from this machine).
- Remote branch protection: DISABLED (API 404, re-verified). The hook is
  local-only: an external synchronizer with network credentials could push
  to main bypassing it. Reported as operational blocker; mitigation here is
  feature-branch-only pushes plus never checking out main for writes.
- This worktree has never touched main; `git status` clean at base.

## Baseline verification (pre-change)
- `go test -race -count=1 ./...` launched in background at base; result
  recorded in the engineering report.
- vet / protocol / adversarial suites re-run post-change in Phase 8.
