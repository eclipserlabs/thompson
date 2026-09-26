// Command exp-run executes a deterministic randomized experiment end to end:
// workload manifest → per-job assignment → gateway execution per treatment →
// independent verification → versioned settlement → progress persistence →
// resume → offline report. All generated results are synthetic unless the
// manifest says otherwise.
//
// Subcommands: gen, run, all (synthetic dry runs); feasibility (read-only
// historical-workload assessment per REAL_WORKLOAD_CONTRACT_V1);
// pilot-check (frozen pilot configuration validation). A run gated with
// --pilot-config refuses to start without --acknowledge of the frozen hash.
package main
