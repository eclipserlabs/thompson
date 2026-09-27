# Engineering Reliability Final Report (V1)

## Scope and method
Independent review of the supervised-pilot stack at `review/reliability-v1`
(18 commits ahead of `origin/main@d7a33e6`, ~8300 added lines, no merges,
no deploys, synthetic data only). Every fix below was reproduced before
repair; every measurement is labeled by platform.

## Confirmed defects repaired
1. **R1 safety-store writer lock** — doc claimed an OS flock the code never
   took; reproduced duplicate seqs under concurrency. Now LOCK_EX|LOCK_NB
   like the other ledgers, released on close/error. Regression tests fail
   pre-fix, pass post-fix.
2. **Safety-config hash brittleness** — frozen identity hashed raw file
   bytes, so identical envelopes in different serializations (indented CLI
   vs compact test) refused legitimate resume mid-experiment. Now hashed
   over canonical struct JSON, with a serialization-invariance unit test.
   Old raw-bytes hashes rotate; no production state existed under them.
3. **Phantom decisions on refusal** (pre-existing runner defect, exposed by
   fail-closed gateways): non-2xx responses still carried decision headers
   into settlement, corrupting the join with 404s. Runner now aborts loudly
   with intent-to-treat rows intact. Boot failures now report the child
   exit status (this instrumentation is what exposed defect 2's fatal
   line hiding in wrapped test output).
4. **Test-process port collisions** — PID-scoped port blocks plus per-
   gateway instance identity (boot handshake + per-request 409 on
   mismatch). Proved by running the previously-failing race/1.22
   combination concurrently green, and by the handshake catching a real
   reboot-without-shutdown lifecycle bug in our own test.
5. **COSTAWARE-in-legacy half-enablement** — validation read in the wrong
   block; moved out with binary-level refusal tests.

## Proven sound (no change needed)
- R2 budget/commit boundary: uncommitted reservations forgiven on recovery,
  committed budgets replay exactly (safe direction, pinned by tests).
- R3 crash consistency: learner/book skew heals via ledger rebuild;
  corrupt quality checkpoint refuses, corrupt cost sidecar cannot block.
- Lock discipline sequential without inversion; OPE allowlist refuses the
  cost-aware identity automatically; correction/duplicate semantics hold.

## Operating envelope (tested, not extrapolated)
Single writer per treatment (OS-enforced everywhere now); monitor fold
comfortable to ~10k outcomes/ledger (~8ms), ~150ms at 100k (projection
specified, deferred); sustained selection ~110/s Linux / ~72/s darwin at
16-way concurrency; actual-binary supervised experiments green on both
platforms including SIGKILL crash, suspension, drift, and idempotent
resume with NOT_RANKABLE verdicts where warranted.

## Remaining risks and unverified items
- Branch protection DISABLED (owner actions: enable required-PR +
  required-CI ruleset on main; until then any green gate is advisory).
- CI Format gate is red on main itself (pre-existing gofmt dirt) —
  separate hygiene commit needed; this stack's files are all clean.
- Read-only-dir refusal proven on darwin only (container runs as root);
  live disk-full beyond construction refusal untested; load ceilings above
  measured numbers unknown; ledger rotation past ~100k undesigned.
- Monitor detects deterioration in ~5 jobs at minObs 6–8 in fixtures;
  posterior inertia (~80 jobs in the validation profile) remains the
  upstream bound — alarming + human response stay mandatory.
- A concurrent-suite stash incident (foreign WIP entries) was observed:
  never run `git stash` in shared-checkout automation; recovered without
  loss. No merges, no force-pushes, no main contact occurred.

## Verification record
- Full race suite: 9/9 packages green after one legacy-expectation update
  (unauthenticated-settle now asserts 409-unaddressed then 401
  addressed-but-unauthenticated; routing precedes auth by design).
- Go 1.22 full suite: result recorded at push time.
- Prior-mission port interference is closed: PID-scoped blocks plus the
  instance handshake let race and compat suites run concurrently green
  (targeted matrix on both toolchains).

## Real-workload readiness
Suitable for a CONTROLLED, supervised, single-writer real-workload
experiment under the runbook, once branch protection lands. Not
production, not autonomous, no commercial claims; all results synthetic.
