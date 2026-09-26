# PR 3A Review Findings (retrospective review of `d6029af..7b63a7e`)

Reviewed as one logical change: durable decision identity + verified
settlement in the live gateway. Method: full re-read of
`go/gateway/{settle,decision_store}.go` and the `router.go`/`evidence.go`/
`outcome/store.go` diffs; `Record`-call inventory by grep; independent test
run (results §4).

## Verdict: approve with required fixes F1–F2 (both small, both pre-pilot)

Core design and all six required invariants hold. No finding questions the
architecture; F1 blocks any deployment claim, F2 blocks schema misuse.

## Required fixes

**F1 — `SettleHandler` is unreachable in the shipped binary.**
`go/router/main.go` mounts `/health`, `/`, `/metrics` only; nothing routes
`/v1/outcomes`. All settlement tests call the handler directly, so the suite
is green while the deployable cannot settle. Fix (PR 3B scope accepted):
mount the handler on an internal-only listener with auth, or record an
explicit decision that `router/main.go` stays legacy-only and verified
deployments use a separate entrypoint. Either way the operations doc's
"internal listener" claim (§2) is currently aspirational.

**F2 — `DecisionExecution.Seq` shares the decision `Seq` namespace and means
nothing.** Assigned as `len(decisions)+len(executions)+1`
(`decision_store.go:233,376,457`), it collides with `CommittedDecision.Seq`
values and is read nowhere. A future reader will mistake it for a version.
Fix: replace with a per-decision monotonic marker number (or drop the field;
PR 3A is pre-pilot so the ledger schema is still cheap to change).
`CommittedDecision.Seq` itself is correct (ledger-monotonic, asserted by
restart test).

## Advisory (no change required before pilot, tracked)

- **A1:** `alreadyLatest` is read outside `settleMu` (`settle.go:121-123`).
  Impact is limited to the advisory `duplicate` flag; a concurrent correction
  in the gap correctly yields 409. Safe as-is; do not "fix" by widening the
  lock without measuring settle throughput first.
- **A2:** durability enforcement is type-based (`*MemoryDecisionStore`,
  `*MemoryOutcomeStore` assertions in `initVerifiedSettlement`). A custom
  in-memory `DecisionStore` implementation passes the gate. Acceptable: the
  gate stops accidents, not adversaries; custom stores are deployer code.
- **A3:** dead code — unused `settleRequest` alias (`settle.go:15`). Remove.
- **A4:** stale comment "Shadow execution only after live learning is
  complete" now guards a block that verified mode skips; shadow `sReward`
  remains diagnostic-only (no `Record` — grep-verified). Comment-only fix.
- **A5:** checkpoint tmp-file residue on crash mid-save is harmless
  (overwritten, never read) but unmentioned; one line in the ops doc.
- **A6:** correction-driven rebuilds are O(history) per correction with no
  endpoint rate limiting; acceptable behind auth + deployment-layer limits,
  but the deployment checklist (PR 3B plan) must name it.

## Positively verified

- **Recovery:** `Resume` restores checkpoint policy/cursor then replays only
  `Seq > LedgerLen`; double-`Recover` is safe (idempotent re-fold);
  no-checkpoint recovery rebuilds from genesis. Checkpoint `genesis` never
  advances, so post-checkpoint corrections to old jobs re-fold correctly.
- **Ordering:** commit → dispatched-mark → execute → observed-mark →
  (legacy learn | verified skip) → shadow; commit/dispatch failures are
  fail-closed pre-execution; post-execution marks are best-effort with the
  evidence JSONL as the durable observation. Persistence ordering matches
  PR3A_DECISION_LIFECYCLE §3.
- **Auth boundary:** deny-by-default (nil auth refuses construction);
  auth-checked before body decode; 405/404/401/400/409 mapping correct and
  every rejection path precedes any store/learner/policy mutation
  (lookup → validate → `Settle`, and `Submit`-before-`Apply` inside).
- **Idempotency:** exact-duplicate → 200 no-op; same-version-diff-content →
  409 with history byte-identical (tested); stale/gap → 409.
- **No dual learning:** verified ServeHTTP contains no `Record`
  (`grep '\.Record(' router.go` → line 465 only, inside
  `mode != VerifiedMode`); `shadow.go` has none; legacy+settlement wiring
  refused at construction; middleware thin-waist remains legacy-only and
  documented. Mode switch never touches posteriors (fresh-process cutover).
- **Propensity integrity:** committed `eligible_arm_state` reconstructs
  bit-identical propensities to live state (test); `scoreKindFor` covers
  Thompson/UCB/phased-forced/phased-sampled; UCB/approx refused at verified
  construction so no unevaluable evidence can be produced.
- **Legacy compatibility:** legacy request path logic is byte-identical
  (indentation-only diff inside the mode gate); all pre-existing gateway
  tests pass unmodified; evidence schema changes are two `omitempty` fields.

## Test results (independent run on range HEAD `7b63a7e`)

- `go test -race -count=1 ./...` (go1.27.1): all 5 packages **ok**
  (gateway 12.3s, opeval 87.7s, outcome 5.2s, propensity 14.4s,
  thompson 6.4s). `go vet ./...`: clean.
- Go 1.22 toolchain (`GOTOOLCHAIN=go1.22.0`, separate session run):
  `go vet` clean on gateway/thompson/outcome; `go test -race` ok on all
  three; `gofmt -l` confirms every file touched by PR 3A is clean under
  CI's toolchain (14 remaining dirty files are pre-existing).
- Rust `thompson-sampling`/`thompson-sim`: 107 passed (Go-only range).
