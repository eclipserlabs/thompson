# Pilot data-handling and operational requirements (V1)

Scope: what Thompson persists when it touches customer-derived workload
data, and what must hold before a customer safely provides
production-derived records. No regulatory compliance or enterprise
security certification is claimed.

## 1. Ledger inventory (per treatment directory)

| File | Contents | Customer data? |
|---|---|---|
| `decisions.jsonl` | Committed decisions: decision/job/strategy IDs, eligible arms + posterior snapshots, sampled scores, config hash | IDs + model selection metadata. No prompts by default |
| `evidence.jsonl` | `DecisionStarted` / `ExecutionObserved` / `DecisionLearned` (+ shadow) events: arms, latency, success flags, token counts, costs | Transport/task facts. Token counts, never token text |
| `outcomes.jsonl` | Versioned `OutcomeEvent`s: full attempt tapes, verdicts, costs, corrections | Yes: the auditable outcome record |
| `assignments.jsonl` | Randomization rows: job ID, strata, treatment, probability, seed | Job IDs + strata labels |
| `jobmap.jsonl` | Manifest→gateway job binding | Mapping only |
| `progress.jsonl` (root) | Run/resume cursor per job | Job IDs + treatment |
| `outcomes.jsonl.checkpoint.json` | Policy snapshot + applied cursor | Model state, no customer payload |

Required vs optional: `job_id`, attempt tapes, verified status, observed
costs (or explicit nulls), verifier provenance are required for a pilot job
to count. Token counts, per-attempt latency percentiles, shadow evidence are
optional diagnostics.

## 2. Sensitive input/output retention

- Prefer stable references and hashes (job IDs, config hashes,
  `workload_version`) over retaining raw customer documents or prompts.
- The gateway evidence path records metadata (arm IDs, token *counts*,
  costs), not prompt/completion text. Do not add payload logging for a
  pilot without a documented retention window and a deletion procedure.
- Verification verdicts (`verified`, `verified_by`, `failure_category`)
  are retained; validator inputs beyond the verdict need explicit approval.

## 3. Credentials and settlement access

- Settlement (`POST /v1/outcomes` on the internal listener) requires a
  bearer token (`SETTLE_TOKEN`, constant-time comparison). The public
  listener serves `404` for settlement paths; unauthenticated writes get
  `401`.
- Tokens travel via environment only, never in files, manifests, or reports.
- The internal listener binds loopback in test/dry-run deployments.
  Binding it beyond loopback requires network policy review first.

## 4. Filesystem, permissions, backup

- New ledger files are created `0600`; treatment directories `0700`.
  Existing files keep their mode: tighten with `chmod 600` on upgrade.
- Every append is followed by `fsync`; checkpoints are atomic
  (tmp + rename + fsync).
- Each store enforces single-writer operation with a non-blocking `flock`:
  a second writer fails loudly instead of interleaving history.
- Backups must capture the full treatment directory (ledgers + checkpoint);
  a partial copy replays as a version gap and fails closed on resume.

## 5. Export and deletion limitations

- Ledgers are append-only: there is no per-job erase. Deletion today means
  file-level retention (expire whole treatment directories per policy) plus
  checkpoint rotation.
- **Mandatory before production-derived data**: define the retention window
  and the directory-level purge procedure with the customer, and confirm
  that append-only storage satisfies their policy. If per-record erasure is
  required, Thompson does not currently provide it — do not accept the data.

## 6. Single-writer deployment

- One process owns one treatment directory. No multi-replica architecture
  exists: multiple replicas must use separate files/directories, and their
  ledgers must never be merged by concatenation (Seq cursors would collide).
- Crash recovery replays from the checkpoint high-watermark; `SIGKILL`
  falls back to slower ledger replay with identical results.

## 7. Mandatory changes before production-derived data

1. Enable GitHub branch protection on `main` (required reviews, no direct
   pushes) — currently **not enabled** (verified 2026-09-26).
2. Agree retention window + directory-level purge; confirm append-only is
   acceptable (else: do not proceed).
3. Issue per-deployment `SETTLE_TOKEN`s; confirm the settlement listener
   stays loopback-only.
4. Freeze the pilot config (contract §6) and record the feasibility report
   source hash alongside it.
5. Confirm no prompt/completion text is logged anywhere in the path
   (evidence, verifier, or report inputs).
