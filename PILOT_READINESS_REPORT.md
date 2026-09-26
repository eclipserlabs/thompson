# PILOT_READINESS_REPORT (THOMPSON_PILOT_READINESS_V1)

- Baseline SHA (origin/main at start): `abf2d0e4f16d646e821d9ec5446997df93be236f`
  (tree identical to local `662b380`; `abf2d0e` is docs-only on top; working
  tree was clean).
- Implementation SHA (code, this PR): `ed017df4207b94822c804f86664deac926df1106`
  on branch `feat/pilot-readiness-v1`, stacked on the isolated race fix
  `24a4555`. This report is a docs-only commit on top; PR head is authoritative.
- Learning algorithm, reward formula (`BinaryStatusMapper`), and statistical
  charter (`harness.BuildReport` gates/verdicts) are unchanged.

## Tests run and results (all on the implementation commit unless noted)

- `go vet ./...`: clean at baseline; clean for all touched packages after.
- `go test -race -count=1 ./cmd/exp-run/`: PASS (~49s), incl. the former
  race reproducer `TestE2EMissingStorageFailsClosed` (3/3 repeated runs PASS
  after the fix; FAIL with race warning before).
- New unit tests under `-race`: `TestSyncBufferConcurrentUse`,
  `TestValidateRecord*`, `TestFullyLoadedCost*`, `TestHasFallback*`,
  `TestAssess*` (5), `TestPilot*` + `TestGatePilotConfigEnforced`: PASS.
- New acceptance tests under `-race`: `TestPilotAcceptanceFeasibilityIngestion`
  and `TestPilotAcceptanceEndToEnd` (ingestion, e2e run, report-refusal,
  resume-identity on a synthetic customer-shaped dataset): PASS.
- Manual CLI check: `exp-run feasibility` on a hand-built JSONL fixture
  produced the expected counts, blockers, and human summary.
- Rest of the race suite: `harness`, `outcome`, `thompson`, `propensity`,
  `gateway`, `router`, `cmd/exp-report`: PASS under `-race`.
  `opeval` passes under `-race` (304s; slow MonteCarlo calibration, needs a
  long timeout) and normally in ~22s.

## Remaining engineering blockers

1. **Branch protection is OFF** (`GET /branches/main/protection` → 404 on
   2026-09-26). Anyone with write access can push to `main` directly.
   Enable required reviews + no direct pushes before any pilot data lands.
2. **No per-record erasure.** Ledgers are append-only; deletion is
   directory-level. If the customer requires per-record erasure, do not
   accept their data (see `docs/design/PILOT_DATA_HANDLING_V1.md` §5).
3. **Concurrent local writer observed.** During this session an unrelated
   local process modified `go/harness/report_test.go` (+221 lines,
   referencing a non-existent `Comparison.ValidDraws`) and briefly
   `go/cmd/exp-report/main.go` (reverted). `origin/main` did not move and
   no branch was touched; the foreign change is preserved uncommitted in
   the working tree and bundled nowhere. Consequence: `go vet ./harness/`
   currently fails on that foreign test file; `go build ./...` passes.
   Resolve (revert or complete) that work separately before merging
   anything that runs the harness test package.
4. `opeval` under `-race` needs >120s (304s measured); CI must raise the
   timeout rather than treat it as a hang.

## What a customer must provide

1. Historical JSONL export per `REAL_WORKLOAD_CONTRACT_V1.md` §2
   (`job_id`, attempt tapes, verified statuses, observed costs or explicit
   nulls, verifier provenance, corrections as version chains).
2. Strategy eligibility per job and task classifications (`strata`).
3. Independent verifier identity and access for the pilot (Thompson never
   self-verifies).
4. Agreement on retention window + directory-level purge, loopback-only
   settlement, and per-deployment bearer tokens.
5. A named sign-off of the frozen pilot config (see §6 of the contract).

## What Thompson can currently measure

- Assignment → execution → independent verification → versioned settlement
  with resume-identity, per treatment, on the customer's eligibility sets.
- Fully-loaded cost per verified success (offline), with missing costs
  excluded and UNKNOWN censored; bootstrap CIs, worst-case-censoring and
  maturity sensitivities, and refusal gates (`NOT_RANKABLE`/`INCONCLUSIVE`
  instead of ranking on weak evidence).
- Whether a historical export can support sizing and what is missing,
  via `exp-run feasibility` (machine-readable + human summary).

## What Thompson cannot yet establish

- No causal claim from historical data: without randomized assignment with
  recorded probabilities, the feasibility report answers `comparable: false`
  and requires a randomized pilot.
- No per-record deletion; no multi-replica operation; no hosted SaaS or
  dashboard (all non-goals, unchanged).
- No regulatory compliance or enterprise security certification is claimed.

## Minimum requirements for the first paid pilot

1. Branch protection on `main`; retention/purge agreement signed.
2. Feasibility report on representative history with zero blockers, or a
   written waiver per blocker with a collection plan.
3. Frozen, acknowledged pilot config whose `workload_version` matches the
   pilot manifest; all runs launched with `--pilot-config` + `--acknowledge`.
4. Independent verifier live on every pilot job; quality floor and
   commercial threshold (`min_rel_effect`) frozen before the first assignment.
5. Success criterion fixed in advance: `CONCLUSIVE_T2_WINS` requires the
   full relative-improvement CI to clear the commercial bar with all
   sensitivities stable — anything less is reported, not rounded up.
