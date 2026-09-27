# Failure-Injection Results (Phase 6)

All scenarios run against both implementations where applicable; each
names the observed behavior, not the hoped-for one.

## Journal prototype
- **SIGKILL mid-commit**: 59-event committed prefix recovered, exploration
  budgets consistent, no torn rows, no phantom decisions. WAL recovery is
  prefix-closed by construction.
- **Competing writers**: second live writer fails loudly at first
  contended transaction (`SQLITE_BUSY`, documented refusal contract).
  Open-time refusal is impossible under SQLite semantics (connections are
  cheap); the boundary moved from open to first contended write —
  recorded as an explicit architectural delta from the flock ledgers.
- **WAL corruption**: byte flips in WAL/db surface as open/replay refusal
  or truncation; replay never invents history (test records the outcome
  per run; page-level corruption is rarer but less transparent than a
  bad JSON line — stated trade-off).
- **8-racer commit storm**: 200/200 commits land exactly once; budgets sum
  exactly; single-connection pool serializes in-process work.
- **Corrupt checkpoint**: refused; full replay always available.

## Existing implementation (prior missions, re-verified here by suite)
- Crash at every ledger boundary converges via replay/rebuild; duplicates
  idempotent; gaps/conflicts refused; cost sidecar corruption cannot block
  recovery; safety torn-tail truncates (operator retries: no ack ever
  promised commit).

## Test-harness lesson (recorded so nobody re-pays it)
Go's runtime kills processes whose only goroutine blocks in bare
`select {}` ("all goroutines are asleep"). Fault-injection holders MUST
sleep-loop instead — three debugging hours burned on holders that died
before holding anything, masquerading as a locking failure. The same
artifact also explains flaky background-process deaths between tool
calls: holders live only inside a single command invocation.
