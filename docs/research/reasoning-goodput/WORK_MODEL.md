# Work Model (Phase 5)

Expensive work = deterministic CPU transforms (`WorkHash` rounds) over
witnessed bytes. No `time.Sleep` anywhere in the assay (grep-enforced).
Each `ReasoningSlice` records exact logical work units (hash rounds
executed) plus wall time; slices carry output digests; determinism
(same inputs → same output) is asserted, not assumed.

Cost regimes via burn factor: 1x ≈ validation-scale work, 10x/100x expensive
reasoning, so conflict economics are measured against validation overhead at
controlled ratios. Synthetic CPU work is NOT evidence of model-market
demand — only a controlled assay for abort/replay economics (stated here and
in every economics conclusion).
