package reasoninggoodput

import (
	"fmt"
	"strings"
	"testing"
)

// scale_test.go: Phase 13 break-even and scaling. Sequential seeded runs
// (deterministic); workers dimension = independent task count (throughput
// lens — contention is per-task scheduled, never racy). Machine output:
// testdata/scale.json. Answers: break-even burn, broad-OCC degradation vs
// irrelevant reads, T3 premise-ratio behavior, T4-vs-T3 partial-invalidation
// value, bookkeeping growth, opaque-fraction cutoff.

type ScalePoint struct {
	Sweep     string  `json:"sweep"`
	Config    string  `json:"config"`
	Treatment string  `json:"treatment"`
	Exec      int64   `json:"exec_units"`
	Goodput   float64 `json:"goodput"`
	Retries   int     `json:"retries"`
	Overhead  int64   `json:"overhead_ns"`
	Wall      int64   `json:"wall_ns"`
	OracleOK  bool    `json:"oracle_ok"`
}

// scaleCase builds one parameterized case: fields on /doc, the first
// useFirst consumed round-robin by nSlices slices (opaqueFrac of them
// opaque), one background change to changeField, fixed propose.
func scaleCase(f *HTTPFixture, id string, fields []string, useFirst, nSlices int,
	opaqueFrac float64, burn int, changeBody string) (JobSpec, []Change) {
	var slices []SliceBuilder
	nOpaque := int(float64(nSlices)*opaqueFrac + 0.5)
	for i := 0; i < nSlices; i++ {
		i := i
		sb := SliceBuilder{
			ID:   fmt.Sprintf("s%d", i),
			From: []string{fmt.Sprintf("read:%d", i%useFirst)},
			Work: WorkSpec{BurnRounds: burn, Label: "scale"},
			Make: func(ins [][]byte) []byte {
				return []byte(fmt.Sprintf("s%d:", i) + DigestBytes(ins[0])[:8])
			},
		}
		if i < nOpaque {
			sb.Opaque = true
		}
		slices = append(slices, sb)
	}
	initVals := map[string]int{}
	for i, fld := range fields {
		initVals[fld] = 100 + i
	}
	spec := HTTPJob(f, id, []string{"/doc"}, map[string][]string{"/doc": fields},
		slices,
		func(outs map[string][]byte, _ map[string]Val, _ []Val) (string, string) {
			// Fixed-point propose: f0 bumped, every other field restored
			// to its frozen initial value (never clobbers by accident).
			parts := []string{`"f0":999`}
			for _, fld := range fields[1:] {
				parts = append(parts, `"`+fld+`":`+fmt.Sprintf("%d", initVals[fld]))
			}
			return "/doc", "{" + strings.Join(parts, ",") + "}"
		})
	return spec, []Change{{Target: "/doc", NewBody: changeBody, AfterPhase: "reason"}}
}

func runScalePoint(t *testing.T, sweep, config string, fields []string,
	mk func(f *HTTPFixture, fields []string) (JobSpec, []Change), treats []TreatID) []ScalePoint {
	t.Helper()
	var out []ScalePoint
	for _, treat := range treats {
		body := "{"
		for i, fld := range fields {
			if i > 0 {
				body += ","
			}
			body += `"` + fld + `":` + fmt.Sprintf("%d", 100+i)
		}
		body += "}"
		f := NewHTTPFixture(map[string]string{"/doc": body, "/cfg": `{"limit":5}`})
		t.Cleanup(f.Close)
		spec, sched := mk(f, fields)
		applier := func(ch Change) {
			_, _ = f.PutUnconditional(ch.Target, ch.NewBody)
		}
		m := Run(spec, treat, sched, applier, 3)
		out = append(out, ScalePoint{sweep, config, string(treat), m.WorkExec, m.Goodput, m.Retries, m.OverheadNS, m.WallNS, m.OracleOK})
		t.Logf("scale/%s/%s/%s exec=%d goodput=%.2f retries=%d over=%dus wall=%dms ok=%v",
			sweep, config, treat, m.WorkExec, m.Goodput, m.Retries, m.OverheadNS/1000, m.WallNS/1000000, m.OracleOK)
	}
	return out
}

func fieldSet(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("f%d", i)
	}
	return out
}

func changeBodyFor(fields []string, changed string, val int) string {
	parts := []string{}
	idx := map[string]int{}
	for i, f := range fields {
		idx[f] = 100 + i
	}
	idx[changed] = val
	for _, f := range fields {
		parts = append(parts, `"`+f+`":`+fmt.Sprintf("%d", idx[f]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func TestScaleSweeps(t *testing.T) {
	var all []ScalePoint
	T := []TreatID{T2Broad, T3Premise, T4Replay}

	// a. Break-even burn: 2 slices, one stale (f1 changes), burn sweep.
	for _, burn := range []int{4, 40, 400, 4000} {
		burn := burn
		fields := fieldSet(2)
		all = append(all, runScalePoint(t, "burn", fmt.Sprintf("burn=%d", burn), fields,
			func(f *HTTPFixture, fs []string) (JobSpec, []Change) {
				spec, sched := scaleCase(f, "burn", fs, 2, 2, 0, burn, changeBodyFor(fs, "f1", 999))
				_ = sched
				return spec, []Change{{Target: "/doc", NewBody: changeBodyFor(fs, "f1", 999), AfterPhase: "reason"}}
			}, T)...)
	}

	// b. Broad OCC vs irrelevant reads: 1 used field + K irrelevant; the
	// change hits one irrelevant field. T2 must replay fully every time.
	for _, k := range []int{0, 4, 16, 63} {
		k := k
		n := k + 1
		fields := fieldSet(n)
		changed := fmt.Sprintf("f%d", n-1)
		all = append(all, runScalePoint(t, "irrelevant", fmt.Sprintf("irrelevant=%d", k), fields,
			func(f *HTTPFixture, fs []string) (JobSpec, []Change) {
				spec, _ := scaleCase(f, "irr", fs, 1, 2, 0, 400, changeBodyFor(fs, changed, 999))
				return spec, []Change{{Target: "/doc", NewBody: changeBodyFor(fs, changed, 999), AfterPhase: "reason"}}
			}, T)...)
	}

	// c. T4 vs T3 across slice counts, exactly one stale slice group.
	for _, ns := range []int{1, 2, 4, 8} {
		ns := ns
		fields := fieldSet(2)
		all = append(all, runScalePoint(t, "slices", fmt.Sprintf("slices=%d", ns), fields,
			func(f *HTTPFixture, fs []string) (JobSpec, []Change) {
				spec, _ := scaleCase(f, "sl", fs, 2, ns, 0, 400, changeBodyFor(fs, "f1", 999))
				return spec, []Change{{Target: "/doc", NewBody: changeBodyFor(fs, "f1", 999), AfterPhase: "reason"}}
			}, T)...)
	}

	// d. Opaque fraction: share of slices consuming the BROAD context (all
	// fields) vs one narrow field. Provenance labels alone change nothing
	// (union == union); what kills selectivity is broad inputs. At frac=1.0
	// every slice sees the moved witness, so T4 must equal T2 (collapse).
	for _, frac := range []float64{0, 0.5, 1.0} {
		frac := frac
		fields := fieldSet(2)
		all = append(all, runScalePoint(t, "opaque", fmt.Sprintf("frac=%.1f", frac), fields,
			func(f *HTTPFixture, fs []string) (JobSpec, []Change) {
				var slices []SliceBuilder
				nBroad := int(4*frac + 0.5)
				for i := 0; i < 4; i++ {
					i := i
					from := []string{fmt.Sprintf("read:%d", i%2)}
					opaque := false
					if i < nBroad {
						from = []string{"read:0", "read:1"}
						opaque = true
					}
					slices = append(slices, SliceBuilder{
						ID:     fmt.Sprintf("s%d", i),
						From:   from,
						Work:   WorkSpec{BurnRounds: 400, Label: "scale"},
						Opaque: opaque,
						Make: func(ins [][]byte) []byte {
							return []byte(fmt.Sprintf("s%d:", i) + DigestBytes(joinAll(ins))[:8])
						},
					})
				}
				spec := HTTPJob(f, "op", []string{"/doc"}, map[string][]string{"/doc": fs},
					slices,
					func(outs map[string][]byte, _ map[string]Val, _ []Val) (string, string) {
						return "/doc", `{"f0":999,"f1":101}`
					})
				return spec, []Change{{Target: "/doc", NewBody: changeBodyFor(fs, "f1", 999), AfterPhase: "reason"}}
			}, T)...)
	}

	// e. Contention depth: 0, 1, 2 sequential changes (second lands in
	// validate phase, exercising the conditional-commit guard).
	for _, nc := range []int{0, 1, 2} {
		nc := nc
		fields := fieldSet(2)
		all = append(all, runScalePoint(t, "contention", fmt.Sprintf("changes=%d", nc), fields,
			func(f *HTTPFixture, fs []string) (JobSpec, []Change) {
				spec, _ := scaleCase(f, "ct", fs, 2, 2, 0, 400, changeBodyFor(fs, "f1", 999))
				var sched []Change
				if nc >= 1 {
					sched = append(sched, Change{Target: "/doc", NewBody: changeBodyFor(fs, "f1", 999), AfterPhase: "reason"})
				}
				if nc >= 2 {
					sched = append(sched, Change{Target: "/doc", NewBody: changeBodyFor(fs, "f1", 998), AfterPhase: "validate"})
				}
				return spec, sched
			}, T)...)
	}

	emitFixture(t, "scale.json", all)
	// Structural gate: T4 must never execute MORE than T3 on any point
	// (selective replay is a strict refinement of whole-task premise OCC).
	byPoint := map[string]map[string]int64{}
	for _, p := range all {
		k := p.Sweep + "/" + p.Config
		if byPoint[k] == nil {
			byPoint[k] = map[string]int64{}
		}
		byPoint[k][p.Treatment] = p.Exec
		if !p.OracleOK {
			t.Errorf("scale %s/%s/%s oracle failed", p.Sweep, p.Config, p.Treatment)
		}
	}
	for k, m := range byPoint {
		if m["T4"] > m["T3"] {
			t.Errorf("scale %s: T4 exec %d > T3 exec %d", k, m["T4"], m["T3"])
		}
	}
}
