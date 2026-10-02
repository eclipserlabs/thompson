# Evidence Erratum — #36 gate 6, and audit of #37

Date: 2026-10-02. Audited tree: `main` @ `e395df3` (contains #36 `c94e786`
and #37 via #38). This is an erratum. It does not rewrite #36 or #37. Their
documents, fixtures and recorded verdicts are unchanged and keep their
historical status. Nothing here changes a threshold or a metric. Where
another reading of a gate is evaluated, it is labeled as a reading, not as
a replacement.

## Conclusions

- **#36: `#36_GATE6_SPEC_AMBIGUOUS`.** The gate as implemented and
  evaluated **fails deterministically** on #36's own committed evidence.
  The implementation does not represent the pre-registered prose. The prose
  itself is too underspecified to evaluate mechanically without choices it
  never made. As recorded, **CONTINUE_TO_LIVE_AGENT_PILOT was not supported
  by all seven #36 gates**: gate 6 as evaluated by #36 fails on the
  committed fixture.
- **#37: `INSUFFICIENT_EVIDENCE`.** None of #37's conclusions depend
  logically on #36 gate 6, and its eight frozen conditions hold as frozen.
  But #37 does not measure the economic question gate 6 asked, so it does
  not supersede it. And in 3 of its 4 held-out conflict runs, the preserved
  work behind its ≥40% result is exactly the first model call, an uncached
  prompt prefill. See §5.

## 1. Gate 6 as pre-registered (frozen prose)

`DECISION_THRESHOLDS.md`, frozen 2026-10-01 (unchanged):

> 6. Overhead check: validation/provenance/replay bookkeeping ≤ 1/9th of
>    preserved work in the ≥10x regime (i.e., overhead does not erase the
>    gain where reasoning costs ≥10× validation).

## 2. Gate 6 as implemented and evaluated in #36

`go/assay/reasoninggoodput/decision_test.go` (unchanged), on the matrix
contention cells where `T2.WorkExec − T4.WorkExec > 0`:

```go
if t4.Metrics.WallNS >= t2.Metrics.WallNS { overOK = false }
```

The in-code comment calls this an "Operationalization (same intent, honest
units)" that uses end-to-end wall time on both sides.

## 3. Committed fixture values (`testdata/goodput_matrix.json`, #36)

| Cell | T2 exec | T4 exec | Preserved units | T2 wall | T4 wall | T2 overhead | T4 overhead |
|---|---|---|---|---|---|---|---|
| git/premise-blob-change | 800 | 800 | 0 (gate N/A) | 633.2 ms | 453.0 ms | 88.17 ms | 66.7 µs |
| git/disjoint-slices | 1600 | 1200 | 400 | **820.1 ms** | **1203.3 ms** | 181.67 ms | 35.37 ms |
| http/used-field-change | 800 | 800 | 0 (gate N/A) | 1.74 ms | 1.43 ms | 441.8 µs | 5.5 µs |
| http/two-independent-fields | 1600 | 1200 | 400 | 4.26 ms | 1.81 ms | 748.5 µs | 11.0 µs |

The `a837740` (#36 head) and `c94e786` (#36 squash on main) trees are
identical, so this is the fixture #36 shipped.

## 4. Results

**Originally reported** (`ASSAY_DECISION.md`): "6. Overhead: PASS (T4 wall
beats T2 wall where work is preserved)". Decision: "All seven hold" →
CONTINUE_TO_LIVE_AGENT_PILOT.

**Deterministic re-evaluation of the implemented gate on the committed
fixture: FAIL.** git/disjoint-slices has T4 1203 ms ≥ T2 820 ms. The result
was reproduced on a clean checkout of `a837740` by running the exact command
recorded in #37's `BASELINE.md`
(`go test ./assay/reasoninggoodput/ -run 'TestDecisionGates|TestPremiseHeldOut'`)
and on `main`. It fails on every run. That contradicts the "7/7 gates PASS
on first run" recorded in #37 `BASELINE.md`, which also records this exact
1203/820 pair as a load flake.

**Why it previously appeared green.** Two defects combined:

1. *The gate read data from another run.* `TestDecisionGates`
   (`decision_test.go`) reads `testdata/goodput_matrix.json`.
   `TestGoodputMatrix` (`matrix_test.go`) **rewrote that file on every run**
   with fresh measurements. Go runs a package's tests in file order, so the
   gate ran first and evaluated whatever matrix the previous run had left on
   disk. A green gate therefore never certified the numbers that were
   committed. A plausible history, inferred and not verified because #36 is
   a single squashed commit: a run passed the gate while reading an earlier
   file, then overwrote the file with the 1203/820 sample, and that sample
   was committed.
2. *The comparison is below the noise floor.* The HTTP cells compare walls
   of about 1–4 ms. In 5 fresh regenerations the HTTP order flipped once
   (T4 1.83 ms vs T2 1.68 ms). The git walls are dominated by `git`
   subprocess forks (ECONOMICS.md's fixture-cost note), not by the
   mechanism or the work: the preserved work is 400 rounds ≈ 0.28 ms
   (ECONOMICS.md calibration, 283 µs per burn-400 slice), against
   300–1200 ms of wall time. On fresh runs, git T4 < T2 usually held
   (≈270 vs ≈335 ms) but not always (766 vs 763 ms under load).

**No semantic difference was found.** Across 5 fresh regenerations, every
non-timing field of `goodput_matrix.json` and `scale.json` (work units,
discards, digests, premises, oracle verdicts, retries) was identical to the
committed fixtures. Only `wall_ns` and `overhead_ns` vary. Gates 1–5 and 7
are deterministic and unaffected.

## 5. Is the implementation consistent with the prose? (A vs B)

**Answer: B. The implementation is inconsistent with the pre-registered
text.** It differs on every element:

| Element | Prose | Implementation |
|---|---|---|
| Quantity compared | bookkeeping (validation/provenance/replay) | end-to-end wall (work + bookkeeping + fixture forks + noise) |
| Threshold | bookkeeping ≤ 1/9 × preserved work | T4 wall < T2 wall (in effect a 1/1 ratio, not 1/9) |
| Scope | only the ≥10x regime (reasoning ≥ 10× validation) | every cell with preserved work, regardless of regime |

So "A — the implementation correctly represented the frozen gate" does not
hold. The two readings, both evaluated on the committed fixture:

**Reading I: as implemented and evaluated by #36. FAIL** (§4). This is the
gate #36's verdict actually used.

**Reading II: the prose, made mechanical.** The prose does not define the
units of "preserved work", how the "≥10x regime" is decided, or whether
witness IO counts as "validation". Evaluating it requires all three choices.
One set of choices, stated as **assumptions not present in the
pre-registration**: preserved work = preserved units × 0.7075 µs/round
(ECONOMICS.md calibration); bookkeeping = T4 `overhead_ns` (metered
validation and replay bookkeeping including witness IO, as ECONOMICS.md
defines it); regime = per-slice reasoning cost / T4 overhead.

| Cell | T4 overhead / preserved work | ≤ 0.111? | Regime (reasoning/validation) | In ≥10x regime? |
|---|---|---|---|---|
| git/disjoint-slices | 35 371 µs / 283 µs = 125 | no | 0.008× | **no → gate not applicable** |
| http/two-independent-fields | 11.0 µs / 283 µs = 0.039 | yes | 25.7× | yes |

Under these assumptions Reading II passes on **one** HTTP cell, and no git
cell is in the regime. The result depends on the assumptions. If witness IO
(git forks) were excluded from "validation", the git cell would enter the
regime, and its mechanism-only bookkeeping is **not recorded separately** in
the fixture, so Reading II could not be evaluated. Reading II is not chosen
here. It is shown only to show that the prose does not determine a verdict.

**Mechanical re-evaluation of the #36 decision.** The gate #36 actually
evaluated fails on #36's committed evidence. The pre-registered prose
yields a verdict only under assumptions #36 never froze. The statement
"All seven hold" in `ASSAY_DECISION.md` is therefore **not supported** by
the committed evidence. Historically, CONTINUE_TO_LIVE_AGENT_PILOT rested
on six supported gates and one gate (6) that is unsupported as evaluated and
ambiguous as specified. The historical record keeps #36's verdict as
written; this erratum records that it was not supported by all seven gates.

## 6. Audit of #37 (live-agent premise pilot)

#37 is a separate experiment and is judged on its own frozen measurements.
#36 gate 6 does not invalidate it, and it is not used here to turn #36 into
a PASS.

**What depends logically on #36 gate 6:**
- *Nothing in #37's results.* #37's eight CONTINUE conditions (MANIFEST
  "Metrics and gates (frozen)") are zero violations, ≥40% L3-over-L2
  preservation, >1 family, transparency, SARF, mechanical provenance and
  fresh-L0 equivalence. None of them involves overhead, wall time or #36
  gate 6. The inherited kill threshold (`NEXT_LIVE_AGENT_PILOT.md`) is
  "missed stale premise OR preservation <40%", with no overhead term.
- *Procedural only:* #37 was launched on the strength of #36's CONTINUE.
  That affects why #37 was run, not what it measured.
- *A factual statement in #37 `BASELINE.md` is wrong:* "7/7 gates PASS on
  first run" is not reproducible on a clean `a837740` with the recorded
  command (§4). This statement does not feed any #37 gate.

**What #37 establishes independently, from its frozen measurements.** By the
frozen metric definitions (token cost = input + output + reasoning, cache
tokens excluded; cumulative-union premises; counterfactual L1/L2/L3), the
recorded `analysis.json` files support each held-out figure in
`RESULTS.md`: preserved fractions 0.635 / 0.500 / 0.589 / 0.733, SARF 0.603
overall, zero UNKNOWN in conflict runs, and oracle and probes green. The
historical verdict CONTINUE_TO_REASONING_TRANSACTION_PROTOTYPE stands as
recorded.

Limits on re-verification: `cmd/analyze` re-hashes files in the original
live worktrees, which are not in the repository. From the repo alone, the
premise sets cannot be re-derived; only the recorded analyses can be
audited internally.

**Does #37 supersede gate 6's economic question? No.**
- #37 measures *how much* token cost has a premise set that stays valid
  (counterfactually). It never measures the cost of the validation,
  provenance and replay machinery, so the bookkeeping-to-preserved-work
  ratio that gate 6 asks about is unmeasured in both #36 and #37.
- Its preservation is counterfactual (L2/L3 recomputed offline), not actual
  replay.
- **Finding (from #37's own committed streams):** in 3 of the 4 held-out
  conflict runs (local2, cross2, disjoint2), `l3_preserved_tokens` equals
  exactly the token count of **slice 0, the first model call**. Slice 0 is
  7 875–7 892 **uncached input** tokens (harness system prompt, tool
  schemas and task prompt; `cache.read` 1 521) plus 82 output tokens. Every
  later call reads ≥9 329 tokens from cache and is billed only 263–1 302
  uncached tokens. Because the frozen metric excludes cache tokens, the
  first call's prefill dominates both L2 discard and L3 preservation. With
  slice 0 removed, held-out preservation is 0.000 / 0.000 / 0.000 / 0.069.
  Slice 0 alone is also 0.50–0.79 of each held-out run's tokens, close to
  that run's SARF (0.50–0.82).
- This is **not** a re-scoring of #37. Removing slice 0 is a different
  metric and is not applied to #37's verdict. It establishes that #37's
  ≥40% preservation, as measured, is mostly the preservation of a cacheable
  prompt prefix, not of the agent's later reasoning. Whether a real replay
  would re-pay that prefill or hit the prompt cache was not measured.

**#37 classification: `INSUFFICIENT_EVIDENCE`.** #37 is independent of
#36 gate 6, and its frozen gates hold as frozen. But (a) it does not
measure the economic question gate 6 was meant to settle, and (b) its
headline preservation is attributable, in 3 of 4 held-out runs, to the
initial uncached prefill rather than to reasoning slices. So it neither
independently supports nor refutes a ReasoningTransaction prototype on the
grounds that prototype needs, namely actual preserved reasoning net of
validation and replay cost.

## 7. Consequences (no action taken here)

- Historical verdicts are unchanged: #36 CONTINUE_TO_LIVE_AGENT_PILOT, #37
  CONTINUE_TO_REASONING_TRANSACTION_PROTOTYPE.
- Proceeding to THOMPSON_REASONING_TRANSACTION_PROTOTYPE_V1 despite these
  findings would be a **new explicit decision**, not a consequence of either
  verdict. If taken, these findings bear on its pre-registration: an
  overhead gate needs pinned units and regime definitions; cost accounting
  must state how cached prefill is priced on replay; and preserved work must
  be reported both with and without the initial prefill.
- Harness repair already committed separately: tests no longer rewrite the
  frozen fixtures. Ordinary runs write to a temp dir and assert non-timing
  equality with the frozen fixture. Regeneration requires `-update`.
  `TestDecisionGates` now evaluates the committed fixture, so gate 6 **fails
  deterministically**. That is the truthful state, and it is left failing on
  purpose.
