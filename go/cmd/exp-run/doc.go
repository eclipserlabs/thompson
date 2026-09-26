// Command exp-run executes a deterministic randomized experiment end to end:
// workload manifest → per-job assignment → gateway execution per treatment →
// independent verification → versioned settlement → progress persistence →
// resume → offline report. All generated results are synthetic unless the
// manifest says otherwise.
package main
