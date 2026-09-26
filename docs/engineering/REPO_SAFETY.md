# Repository Safety Findings

## What happens (evidence)

- After implementation sessions, one commit per touched file appears locally
  (`Create X` / `Update X`, author `Wira Mahendra <wiramahendra@proton.me>`,
  seconds apart — e.g. 12 commits in 17s on 2026-09-26), followed by a push
  that fast-forwards `origin/main` with **no PR and no review** (PR 3A range
  `d6029af..7b63a7e` is all auto-commits; `gh pr list` shows no PR 3A).
  PRs #1–#3 were merged by a human via GitHub; the PR 3A bypass was not.
- No git hooks were installed (only `.sample` files); no cron/launchd sync
  agent found; nothing in the executor daemon logs references git. The
  driver is the outer session orchestrator snapshotting the workspace, not
  any in-repo mechanism — stated as inference, because its code is outside
  this repository and not inspectable from here.
- Aggravating facts: `origin/main` has **no branch protection** (GitHub API
  404), so direct pushes succeed silently; the working branch
  `feat/live-outcomes-v1` was configured to track `origin/main`, inviting
  bare-push accidents.

## Harm assessment

No PR 3A content was lost (range verified: 15 files, +2194/−63, remote SHA
equals local, full suite green on the pushed tree). The harm is procedural:
unreviewed code on `main`, fragmented one-file commits, and no reviewable
PR artifact for PR 3A. Nothing requires history surgery — rewriting
published history is explicitly out of scope and would make this worse.

## Defenses installed (this mission, in-repo only)

1. **`.git/hooks/pre-push`** (local, executable, simulation-tested: blocks
   `refs/heads/main|master` with exit 1, allows feature branches). Limits:
   untracked by git (lost on fresh clone), bypassable with `--no-verify`,
   does not stop the orchestrator from committing locally.
2. **Upstream correction**: `feat/live-outcomes-v1` now tracks
   `refs/heads/feat/live-outcomes-v1` instead of `origin/main`.
3. **Recommended (needs human, not done here):** enable GitHub branch
   protection on `main` (require PR + passing CI) — the only defense that
   works regardless of local tooling; document the feature-branch → PR →
   merge workflow where the human already practices it (PRs #1–#3).

## Workflow going forward

Feature branch per mission → local commits (mine or the daemon's) →
`gh pr create` → human merge. The daemon may keep committing; review
happens at the PR, and `main` can no longer be fast-forwarded from a
workstation (hook) or the server (protection, once enabled).
