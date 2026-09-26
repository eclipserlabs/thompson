# Integration Conflicts (Phase 1)

Merge order: #20 (A) → #21 (B) → #22 (C) → #19 (pilot) → #23 (D).
All PRs open; bases verified (A←main, B←A, C←B, D←C, pilot←main).

## Direct (textual) conflicts

### C1. `go/cmd/exp-run/cluster.go` — duplicate race fix (only textual conflict)
- Pilot (#19, commit `24a4555`): `syncBuffer` type + call-site swap.
- Correctness D (#23): `lockedBuffer` type + call-site swap.
- The implementations are behaviorally identical (mutex-guarded bytes.Buffer
  for os/exec stderr). Resolution: keep pilot's `syncBuffer` (its
  `cluster_test.go` unit test references the name), discard the duplicate.
  Coverage preserved: `TestSyncBufferConcurrentUse` (unit) +
  `TestE2EMissingStorageFailsClosed` under `-race` (integration).
- No other file is textually modified by both sides (verified by file-set
  diff; the main-checkout `report_test.go`/`exp-run/main.go` edits are
  uncommitted pilot WIP, not in PR #19).

## Semantic overlap (no textual conflict — verified compatible)

### S1. Pilot acceptance vs corrected verdict semantics
- Pilot tests (`pilot_acceptance_test.go`) assert ingestion properties
  (rejection counts, UNKNOWN preservation, missing-cost visibility,
  correction collapsing, non-comparability of synthetic history) — none
  assert a CONCLUSIVE verdict, so PR A's stricter gates cannot break them.
- Pilot `feasibility.go`/`workload_contract.go` call `harness.BuildReport`
  with the post-E2E signature (incl. `jobMaps`): compatible with the
  extended signature (new `ReportConfig` fields default safely).
- `exp-run/main.go`: pilot adds subcommands/flags + `pilotConfig`/
  `acknowledge` fields; correctness-D does not touch this file. Clean merge.

### S2. Gateway/outcome interfaces
- Pilot uses `outcome.Settle`, learner, `LoadTreatmentDir` — none of the
  signatures changed by PRs B/D (additive: `Rebase`, `NoopMapper`,
  `NotReadyError`, `JobMap`). `SettleHandler` behavior tightened (B2/B4)
  in ways pilot tests do not contradict (single-arm/selected-arm
  settlements; no conflicting-version fixtures in pilot tests — verified
  by grep for `StatusConflict` expectations: none).

### S3. Protocol fixtures
- Pilot `testdata/pilot.example.json` is a workload manifest, not a
  snapshot: unaffected by canonical Config JSON. No action.

### S4. Statistical expectations
- Pilot's `customerFixtureToManifest` e2e run asserts version-chain
  contiguity, not verdicts — immune to PR A semantics.

## Reconciliation plan (execution)
1. Merge #20, test harness/exp-report.
2. Merge #21, test thompson/outcome/gateway + Rust lib.
3. Merge #22, test protocol suites both languages.
4. Merge #19, resolve `cluster.go` to `syncBuffer` variant, test exp-run.
5. Merge #23, full suite + 300-job dry run + resume proof.
No PR is merged on GitHub by this mission; all merges are local to
`release/integration-v1` for validation. Proposed GitHub merge order is
separate (final report).
