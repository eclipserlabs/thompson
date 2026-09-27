# Engineering Reliability Baseline (Phase 0)

## Repository state (fetched 2026-09-27)
- `origin/main`: `d7a33e6` (unchanged). Open PRs: none.
- `origin/feat/supervised-pilot-v1`: `54270aa` (matches known head).
- Review worktree: `~/Documents/thompson-reliability`, branch
  `review/reliability-v1`, at `54270aa`, clean.
- Unrelated state preserved: main-checkout `M go/harness/report_test.go`
  untouched; all six pre-existing worktrees untouched; no main contact.

## Commit graph (pilot vs main)
`origin/main..origin/feat/supervised-pilot-v1` = PR A–D fixes, correctness
release record, empty correctness merge, cost-aware implementation,
validation fixes, and six supervised-pilot commits. Main already carries
the PR A–D content via squash merges; the pilot stack adds no duplicates —
final integration must be a single merge of the pilot branch (or a
same-content PR), never a replay of the review branches.

## Documents read
Supervised-pilot baseline, safety contract, operations, engineering report;
cost-aware objective, safety decision, charter, validation findings/report;
outcome contract, pilot-data-handling, PR3B charter/spec (frozen).

## Code reviewed (Phase 1 scope)
Gateway selection/commitment, safety controller/store/monitor/operator,
cost-aware policy (RuleV1–V3), cost book, learner, settlement, decision/
outcome stores, router binary wiring, exp-run runner (incl. phantom-
decision refusal), supervised manifest flow.

## Baseline results (pre-change)
`go test -count=1 ./...`: 9/9 ok (REL_BASE_EXIT=0). This run compiled the
pre-change tree (started before any edit).

## Fact separation
- Independently verified here: R1 missing flock (code + failing repro);
  sequential lock discipline (read); OPE allowlist refusal (read).
- Agent-reported, taken as claims to re-verify: perf numbers, detection
  delays, before/after experiment results (re-run where load-bearing).
- Untested assumptions: Linux constants, load ceilings, disk-full live
  behavior, multi-process operation (all labeled as such in prior docs).
