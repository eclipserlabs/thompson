# Reproducibility

## Baseline
Branch `research/benchmark-v1` on `review/reliability-v1@0ad84b7`
(main `d7a33e6`). Toolchains: go1.27.1 darwin/amd64 (compat go1.22.0),
rustc 1.90.0, golang:1.22-bookworm for Linux. Repo: the benchmark lives
entirely in `go/bench/` (new, additive) plus research docs.

## Commands (from a clean checkout)
```
cd go
go test -count=1 ./bench/ -v          # corpus, parity, S1-S5, ablation
go test -count=1 -run TestScalingLearners ./bench/ -v   # scaling to 1M
go test -count=1 -run TestEconomicsEvalSweep ./bench/ -v # 8-scenario sweep
go test -race -count=1 ./...          # full matrix incl. bench
GOTOOLCHAIN=go1.22.0 go test -count=1 ./thompson/ ./outcome/ ./harness/ ./gateway/ ./router/ ./cmd/exp-run/ ./bench/
docker run --rm -v $PWD/..:/src -w /src/go golang:1.22-bookworm \
  go test -count=1 -run 'TestScalingLearners|TestMonitorScaling' ./bench/ ./gateway/ -v
```

## Fixtures and digests
Every scenario carries a content digest (`corpus-v1-*`, recomputed by
`TestCorpusDeterministic`); dev seeds {11,22,33}, eval seeds
{101..105}; the sweep uses eval seed 101. Raw outputs are the test logs
themselves (machine-readable `t.Logf` rows); summary tables in
ADAPTIVE_ECONOMICS_RESULTS.md are transcribed, not computed, from them.

## External workload interface (for future de-identified data)
Implement `JobSpec` JSON per the schema in `go/bench/corpus.go`
(`job_id`, `strata`, per-arm `success_p` + nullable `cost_usd`,
`verify_at`, `missing_cost_arms`, `correct_to`, `duplicate`,
`unresolved`, `human_fix_cost`). Requirements: no prompt/completion text,
no user identifiers, per-arm measurements or explicit nulls, verifier
provenance per job. Validation: unmarshal + digest + seed-pinned
assignment; nothing else is assumed.
