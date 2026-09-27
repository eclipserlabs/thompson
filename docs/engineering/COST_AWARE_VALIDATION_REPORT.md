# Cost-Aware Validation Report (V1)

## Baseline and final SHA
- Baseline `origin/main`: `d7a33e68e9484732fad40561f21f038811540ca8` (unchanged; no open PRs).
- Implementation under review: `origin/feat/cost-aware-v1` = `7c3c4c9`.
- Validation branch: `review/cost-aware-validation-v1` on isolated worktree
  `~/Documents/thompson-validation`.
- Dependency graph: `d7a33e6` → (empty merge `f26342c`, correctness stack
  already on main via #24, zero diff) → `7c3c4c9` (+2059/-2) → this
  validation delta. No duplicated merges; frozen charters untouched.

## Findings and dispositions
- F1 High (sample≠floor; measured 9% unsafe picks as "optimum"): FIXED by
  RuleV2 mean-gate post-cold + `RuleVersion` bound to every decision.
  Post-fix 6/200 (3%), all cold-phase bounded; legacy RuleV1 path preserved
  for exact replay of old ledgers.
- F2 High (malformed cost partially applied): FIXED by `ValidateCosts`
  pre-check before `Settle`; refusal is atomic (learner + ledger untouched).
- F3 Medium (book/evaluator population mismatch on human legs): FIXED by
  aligning the book to the report's conservative rule + parity test
  (`TestCostBookReportParity`). Evaluator frozen deliberately (baseline +
  active report-test work elsewhere). Known conservatism documented:
  fully-accounted human-reviewed jobs still trip the missing-cost gate.
- F4 Medium (weak hash, config not persisted): FIXED (sha256 + `config.json`
  at open + `readCostAwareConfig` + resume config-match refusal).
- F5 Medium (no T3 checkpoint/resume): FIXED (`SaveCheckpoint`,
  `ResumeCostAwareTreatment` ledger-replay recovery, config-match refusal,
  crash test) + dir fsync in `SaveCostCheckpoint`.
- F6 Medium (truth couldn't express missing/unresolved): FIXED
  (`MissingCostArms`, `Unresolved`, nil-default preserving legacy behavior;
  all suites green unchanged) + experiment-level missing/censoring coverage.
- F7 Low (known-without-mean dropped): FIXED (joins flagged fallback pool).
- F8 Info (selection/truth RNG coupling): documented, unchanged.

## Test results
- Baseline race (pre-change): 9/9 ok.
- Post-fix: `go test -race -count=1 ./...` 9/9 ok (FINAL_RACE_EXIT=0;
  slowest opeval 159s), `go vet ./...` clean, gofmt clean on touched files,
  `TestProtocol*` pass, Rust `control-plane storage` 2/2 (only Rust area
  touched is the no-op merge).
- New regression tests (all pass): mean-gate, RuleV1 replay, F7 fallback,
  RuleV2 cold, unsafe-fraction bound, atomic refusal, parity, config
  binding, crash resume.
- Existing suites: thompson/outcome/harness green unchanged (incl.
  4-treatment experiment, drift exposure, 10-scenario adversarial matrix).
- Go 1.22 compat (GOTOOLCHAIN=go1.22.0, thompson/outcome/harness): 3/3 ok.

## Before/after (frozen seeds)
- Unsafe picks (p=0.10 vs floor 0.50, 200 jobs): 18 (16 optimum-labeled) →
  6 (5 cold-phase optimum-labeled, 1 fallback). Residual is the bounded cold
  budget, not a leak.
- Deterioration (collapse at job 80, windows of 40): picks
  80 | 40 40 0 0 0 — full commitment for ~80 post-collapse jobs (posterior
  inertia + cost advantage), then complete exclusion. Detection delay ≈ 80
  jobs (≈72 excess failures). Shared with cost-blind Thompson; operational
  consequence below.
- Missing-blinds-learner (90% unmetered cheap): T3 $0.0532 vs T1 $0.0022
  (24× loss), comparison diff +0.0510, wins=false, INCONCLUSIVE — exposed.
- 4-treatment equal-quality scenario: unchanged pass (rule rarely binds at
  0.75≫0.30 floor).

## Quality-safety guarantees and limitations
Guarantees: post-cold optimum picks provably exclude arms whose posterior
mean is below floor; cold exploration bounded (~arms×ColdStartPulls) and
flagged; all-arms-below-floor stays flagged fallback; report gates refuse
rankability independently. Limitations: deterioration detection ≈80 jobs in
the measured regime (needs windowed accept-rate alarming + human response in
pilot ops); human-fixed jobs are invisible to arm learning (inherited
semantics — perpetual flagged exploration, never silent optimum); missingness
on the best arm strands the learner conservatively; means unprotected against
heavy tails (documented non-goal); selection/truth RNG coupling makes
trajectories sensitive to selection-path edits (deterministic, not robust).

## Performance (darwin; label platform, no Linux env available)
decision 10.5µs | settlement 14.6ms (fsync) | checkpoint 13.2ms (two files)
| recovery 429µs | storage ≈1225B/job extra. Single-writer only; no replica
coordination (out of scope). RuleV2 adds ~8µs vs V1 — negligible.

## Remaining risks
Branch protection still disabled (operational blocker, unchanged — report,
don't compensate). Evaluator's human-leg conservatism (F3 note) needs a
customer decision before human-heavy pilots. Deferred baseline-pin needs a
customer-designated baseline arm. Overlapping-file check: main-checkout
`M go/harness/report_test.go` belongs to another agent — untouched; no
`report.go` semantic changes made (only additive consumers).

## Real-workload suitability
Suitable for a REPRESENTATIVE real-workload experiment ONLY with: branch
protection enabled, windowed quality/deterioration alarming with human
response, pre-qualified arm sets, missing-cost budgets per arm, and the
understanding that human-heavy workloads will trip the missing-cost gate by
design. Not suitable for unsupervised production (detection delay,
heavy tails, human-trap invisibility). All synthetic claims only; no
commercial savings asserted.
