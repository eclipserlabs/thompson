# Economics and Break-Even (Phase 13)

Source: `testdata/scale.json` (5 sweeps), matrix wall times, unit-cost
probe (burn busc wall: 4→4µs, 40→25µs, 400→283µs, 4000→1984µs per slice).
Synthetic CPU is assay scaffolding for abort/replay economics, NOT product
demand evidence. All figures measured (darwin); overhead is metered
bookkeeping + witness IO, never slice CPU.

## Findings by mission question

- **Break-even reasoning cost**: bookkeeping is flat ~10–50µs per task
  (key building, premise comparison, cache ops — independent of burn and
  scale); witness IO adds ~0.1–1ms per validation round on HTTP and
  ~10–35ms on the git CLI fixture. Preserved work scales with burn
  (~0.7µs/round). Break-even lands near 2–5× validation cost; the ≥10x
  regime wins decisively (25–4700× coverage in rotation-heavy series);
  100× dominates. Below ~1x the machinery loses to its own bookkeeping.
- **Broad OCC vs irrelevant reads**: T2 replays the full task per witness
  movement regardless of relevance (1600 units at every irrelevant count);
  T3/T4 skip entirely (800/400). T2's waste scales with TASK SIZE per
  retry, not with irrelevant-read count — full replay is the tax, paid
  whenever anything at all moves.
- **T3 vs premise/read ratio**: T3 ≡ T2 wherever witnesses are coarse
  (HTTP: one ETag per document makes every field premise move together).
  T3 beats T2 only with fine witnesses + unconsumed reads
  (git/unconsumed-file: 400 vs 800 + a true false-conflict flag on T2).
- **T4 vs T3 partial invalidation**: T4 = N+1 vs T3 = 2N slice executions
  (N slices, 1 stale) → ratio → 0.5 as N grows (measured 0.75 at N≤8).
  Selective replay is a strict refinement of whole-task premise OCC
  (asserted structurally: T4 ≤ T3 exec on every scale point).
- **Bookkeeping growth**: overhead flat in burn and near-flat in
  nodes (~10–50µs HTTP; git fixture dominated by subprocess forks,
  see below) — linear or better, never pathological.
- **Opaque cutoff**: frac=0.0 → T4 0.67; frac=0.5 → 0.57; frac=1.0 →
  T4 == T2 exactly (0.50). Advantage decays monotonically and vanishes
  precisely when every slice consumes the moved context.

## Fixture-cost honesty note

Git-fixture subprocess forks (~5–15ms per witness round-trip) dominate git
wall times and T4's metered overhead there (35ms on disjoint-slices). That
cost belongs to the FIXTURE (real `git` CLI), not the mechanism: the HTTP
adapter (in-process witnesses) shows mechanism bookkeeping at ~10µs. Both
are reported; conclusions about the mechanism use the in-process figures,
and the fork cost is labeled wherever it appears. Wall-time gate 6 uses
end-to-end wall on both sides precisely so no accounting choice hides this.
