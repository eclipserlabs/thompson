# Release Readiness Report (THOMPSON_INTEGRATION_RELEASE_V1)

## Release decision: READY_FOR_REVIEW, BLOCKED_FOR_CUSTOMER_DATA

Software checks pass; repository protection and pilot-data controls remain
human-owned blockers. No production-readiness or commercial claim authorized.

## Candidate
- Branch `release/integration-v1` (this worktree), base `origin/main abf2d0e`.
- Merge sequence (all local, no GitHub merges by this mission):
  `review/correctness-a` (PR #20) → `-b` (#21) → `-c` (#22) →
  `feat/pilot-readiness-v1` (#19) → `review/correctness-d` (#23),
  plus `openTestRunner` compatibility and STORAGE test-seam commits.
- One textual conflict (`cluster.go` duplicate race fix) resolved to the
  pilot `syncBuffer` variant with its unit test kept; one signature
  collision (`openTestRunner` ports) resolved via backward-compatible
  variadic. Full record: `docs/engineering/INTEGRATION_CONFLICTS.md`.

## Independent verification (all executed this mission, not cited)
- `go test -race -count=1 ./...`: all 9 packages ok.
- `go vet ./...`: clean. `gofmt -l`: only pre-existing dirt (none mine).
- Go 1.22: vet clean; tests ok on thompson/outcome/harness/gateway/router/exp-report/exp-run.
- Rust: workspace green (lib 85, protocol 6, control-plane 15, sim 18+);
  `fmt --check` clean; clippy 0 warnings. Control-plane suite 5/5
  consecutive (env flakes fixed).
- Protocol: `TestProtocol*` (7) + `tests/protocol.rs` (6) green both ways;
  CI `-run` patterns now match real tests; both helm charts linted in CI
  (helm binary absent locally — CI-owned check).
- Actual binary: router boot/settle conformance tests green; 300-job dry
  run + crash/resume below.
- Pilot acceptance + feasibility suites green on the integrated tree.

## Dry run + resume proof (integrated candidate)
- `exp-run all --seed 20260105 --n 300` → 300 jobs, all artifacts
  (manifest w/ version, per-treatment decisions/outcomes/evidence/jobmap,
  progress log, report.json/txt), verdict NOT_RANKABLE.
- Crash at job 100 (`--crash-after`, SIGKILL, no graceful checkpoint) +
  resume → **byte-identical metrics**: assignments (108/88/104),
  treatments, comparisons, verdict, reasons all equal.
- The refusal is the corrected behavior working: missing-cost gate fires
  (unmetered ~16% > 0.10 on all treatments). Under pre-correctness code
  this synthetic data would have produced an unsupported verdict.

## Invariants (each covered by a passing test on the candidate)
Unauthorized-arm updates rejected; duplicates/supersedes single-count;
recovery enforces version chains; reports refuse unsupported conclusions;
allocation checked; UNKNOWN/PENDING/unresolved censored from quality and
cost numerators; Rust/Go snapshots restore both ways; CI gates execute;
restart/resume preserves assignments, outcomes, learned state.

## Release-blocker register
1. **BLOCKED_FOR_CUSTOMER_DATA**: `main` has no branch protection
   (re-checked). Human steps: enable required-PR + required-CI ruleset.
2. Auto-commit/push mechanism persists; pre-push hook is local-only.
   Do not treat local guards as repository protection.
3. Pilot track (#19) overlaps `exp-report/main.go`, `harness/report_test.go`,
   `router/main.go` in uncommitted WIP — coordinate before landing either.
4. Helm lint relies on CI (no local helm); trace-replay leg needs traces/
   to exercise (skips cleanly without).

## Approved merge sequence (human executes, fresh tests per merge)
1. #20 → 2. #21 → 3. #22 → 4. #19 → 5. #23, each followed by its focused
   suite (harness/exp-report; thompson/outcome/gateway + Rust lib;
   protocol both languages; exp-run incl. pilot acceptance; full race).
   Then delete the integration branch. No tags, no deploy.
