# Threats to Validity

1. **Benchmark representativeness.** All workloads are synthetic with
   author-chosen parameters. Real task difficulty, cost structures, and
   verifier behavior may differ arbitrarily. Mitigation: documented
   generative assumptions per scenario; de-identified external-data
   interface specified; no extrapolation claimed.
2. **Hardware effects.** Absolute latencies vary 3–4× with machine load;
   fsync behavior is filesystem-specific (APFS vs overlayfs measured;
   production filesystems unmeasured). Mitigation: distributions reported,
   guardrails generous, Linux cross-checked.
3. **Selection bias.** Scenario authors knew the policy design. Mitigation:
   dev/eval seed split, frozen parameters, retained negatives, and the
   static-matches scenario designed to favor the baseline.
4. **Simplified verification.** Synthetic verifiers are oracles with lags;
   real validators abstain adversarially and err systematically.
   Mitigation: UNKNOWN/PENDING paths exercised, but fidelity unproven.
5. **Warm-start confounding.** Production defaults borrow across model
   families; the research baseline pins textbook priors. Comparisons
   against production defaults mix both effects (documented, with a
   dedicated difference test).
6. **Short horizons.** n=160/arm≈40 leaves bootstrap CIs wide (hence
   INCONCLUSIVE rather than false ranks — the honest outcome, but also
   low power by construction). Longer horizons would sharpen both wins
   and losses.
7. **Single-writer scope.** All recovery claims assume one writer;
   multi-replica behavior is entirely untested.
