# Integration Baseline (THOMPSON_INTEGRATION_RELEASE_V1, Phase 0)

- Date (UTC): 2026-09-26.
- `origin/main`: `abf2d0e` ("docs: restore flow diagram embed removed in #15 (#18)"). Unchanged since the correctness mission.
- Integration worktree: `~/Documents/thompson-integration`, branch
  `release/integration-v1` cut from `origin/main`. Working tree clean.
- PR states (all OPEN, bases unchanged):
  - #20 `review/correctness-a` @ `8cfa1fc` ← main — statistical correctness.
  - #21 `review/correctness-b` @ `b28ddde` ← #20 — policy/learning integrity.
  - #22 `review/correctness-c` @ `4451aeb` ← #21 — protocol + CI.
  - #23 `review/correctness-d` @ `d7f89e9` ← #22 — runner/operational (+ baseline + ops docs).
  - #19 `feat/pilot-readiness-v1` @ `eee440f` ← main — pilot contract/feasibility/gate/acceptance (+ cluster.go race fix `24a4555`).
- Stack order verified: A ⊂ B ⊂ C ⊂ D by ancestry (each head contains its parent).
- Pilot branch (#19) is based on main WITHOUT the correctness stack; it will
  be merged after #22 per the required order.
- Branch protection on `main`: still DISABLED (GitHub API 404, re-checked).
  Local pre-push hook blocks direct `main` pushes from this machine.
- Other agents' state preserved: main checkout holds pilot-track uncommitted
  edits (`go/harness/report_test.go` modified, pilot files untracked) — not
  inspected beyond `git status`, not touched. README/docs branches untouched.
- Correctness worktree (`~/Documents/thompson-correctness`,
  `fix/correctness-pra-statistical`) left exactly as the correctness mission
  ended; no further commits will be made there.
- Pre-existing full-suite baseline (correctness mission, committed tree):
  `go test -race ./...` all green; Rust workspace green; clippy/fmt clean;
  Go 1.22 vet+tests green on touched packages.
