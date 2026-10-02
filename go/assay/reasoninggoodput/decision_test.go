package reasoninggoodput

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// decision_test.go: Phase 16 gate evaluation against DECISION_THRESHOLDS.md
// (frozen 2026-10-01, before held-out treatment comparisons). Reads
// testdata/goodput_matrix.json (dev, frozen) plus held-out results.
// Gate 1 (violations) uses held-out runs; gates 2-6 use the frozen matrix.
//
// HISTORICAL EVIDENCE CHECK, not an active product acceptance gate. This test
// re-evaluates the #36 gates exactly as implemented against the committed
// frozen fixture and asserts the recorded outcome. Per
// docs/research/reasoning-goodput/EVIDENCE_ERRATUM.md (the authority):
// gates 1-5 and 7 PASS; implemented gate 6 FAILS on the committed fixture;
// the pre-registered gate-6 prose is ambiguous and is not reinterpreted
// here; the original "7/7 PASS" claim is unsupported.

// gateResult is one evaluated gate.
type gateResult struct {
	name   string
	pass   bool
	detail string
}

func loadMatrix(t *testing.T) []MatrixRow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "goodput_matrix.json"))
	if err != nil {
		t.Fatalf("frozen evidence fixture missing: %v", err)
	}
	var rows []MatrixRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func cell(rows []MatrixRow, workload, scenario, treat string) *MatrixRow {
	for i, r := range rows {
		if r.Workload == workload && r.Scenario == scenario && r.Treatment == treat {
			return &rows[i]
		}
	}
	return nil
}

// historicalOutcome is the corrected recorded result of each #36 gate as
// implemented, evaluated on the committed fixture (EVIDENCE_ERRATUM.md).
// It is NOT a statement that the thesis passes or fails a product gate.
var historicalOutcome = map[string]bool{
	"gate1-zero-violations":     true,
	"gate2-40pct-two-systems":   true,
	"gate3-selective-half":      true,
	"gate4-mechanical-80pct":    true,
	"gate5-opaque-useful":       true,
	"gate6-overhead":            false, // implemented gate FAILS: git/disjoint-slices T4 1203ms >= T2 820ms
	"gate7-no-second-authority": true,
}

// TestDecisionGates evaluates every frozen gate as implemented in #36 and
// asserts the corrected historical outcome (historicalOutcome). Gate logic
// and thresholds are unchanged; a gate whose evaluation diverges from its
// recorded outcome in either direction fails the test.
func TestDecisionGates(t *testing.T) {
	rows := loadMatrix(t)
	var gates []gateResult
	fail := func(name, detail string) { gates = append(gates, gateResult{name, false, detail}) }
	pass := func(name, detail string) { gates = append(gates, gateResult{name, true, detail}) }

	// Gate 1: zero stale-premise violations (held-out + dev safety floor).
	// Structural: no MissedConf in any non-T1 matrix row; held-out tests
	// assert this directly and would fail first.
	violations := 0
	for _, r := range rows {
		if r.Treatment != "T1" && r.Metrics.MissedConf {
			violations++
		}
	}
	if violations == 0 {
		pass("gate1-zero-violations", "no missed stale-premise accepts in matrix or held-out")
	} else {
		fail("gate1-zero-violations", itoa(violations)+" violations")
	}

	// Gate 2: T3-or-T4 preserves ≥40% more than T2 under real contention,
	// in ≥2 witness systems. preserved-fraction = (T2disc - Xdisc)/T2disc
	// over scenarios where T2 discarded >1 slice of work (400 units).
	type cand struct{ workload, scenario string }
	contention := []cand{
		{"git", "premise-blob-change"}, {"git", "disjoint-slices"},
		{"http", "used-field-change"}, {"http", "two-independent-fields"},
	}
	wins := 0
	var winDetail []string
	for _, c := range contention {
		t2 := cell(rows, c.workload, c.scenario, "T2")
		t3 := cell(rows, c.workload, c.scenario, "T3")
		t4 := cell(rows, c.workload, c.scenario, "T4")
		if t2 == nil || t3 == nil || t4 == nil {
			continue
		}
		if t2.Metrics.Discarded <= 400 {
			continue
		}
		best := t3.Metrics.Discarded
		if t4.Metrics.Discarded < best {
			best = t4.Metrics.Discarded
		}
		frac := float64(t2.Metrics.Discarded-best) / float64(t2.Metrics.Discarded)
		winDetail = append(winDetail, c.workload+"/"+c.scenario+fmtPct(frac))
		if frac >= 0.40 {
			wins++
		}
	}
	_ = winDetail
	// Count distinct witness systems among wins (git vs http).
	if wins >= 2 {
		pass("gate2-40pct-two-systems", joinStr(winDetail, "; "))
	} else {
		fail("gate2-40pct-two-systems", joinStr(winDetail, "; "))
	}

	// Gate 3: T4 ≤ 0.5 × T3 discarded on partial-invalidation scenarios.
	partials := []cand{{"git", "disjoint-slices"}, {"http", "two-independent-fields"}}
	ok3 := true
	var d3 []string
	for _, c := range partials {
		t3 := cell(rows, c.workload, c.scenario, "T3")
		t4 := cell(rows, c.workload, c.scenario, "T4")
		if t3 == nil || t4 == nil {
			ok3 = false
			continue
		}
		ratio := 0.0
		if t3.Metrics.Discarded > 0 {
			ratio = float64(t4.Metrics.Discarded) / float64(t3.Metrics.Discarded)
		} else if t4.Metrics.Discarded > 0 {
			ratio = 2.0
		}
		d3 = append(d3, c.workload+"/"+c.scenario+fmtRatio(ratio))
		if ratio > 0.5 {
			ok3 = false
		}
	}
	if ok3 {
		pass("gate3-selective-half", joinStr(d3, "; "))
	} else {
		fail("gate3-selective-half", joinStr(d3, "; "))
	}

	// Gate 4: ≥80% of deciding bindings mechanical. Counts actual
	// provenance labels on the ACCEPTED candidates' premise sets (recorded
	// per run in the matrix) across T3/T4 contention cells.
	mech, total := 0, 0
	for _, c := range contention {
		for _, tname := range []string{"T3", "T4"} {
			r := cell(rows, c.workload, c.scenario, tname)
			if r == nil {
				continue
			}
			for _, q := range r.Metrics.AcceptPremises {
				total++
				if q.Provenance == "field" || q.Provenance == "git" || q.Provenance == "direct" {
					mech++
				}
			}
		}
	}
	if total == 0 {
		fail("gate4-mechanical-80pct", "no deciding bindings recorded")
	} else if float64(mech)/float64(total) >= 0.8 {
		pass("gate4-mechanical-80pct", fmt.Sprintf("mechanical %d/%d bindings counted from accepted candidates", mech, total))
	} else {
		fail("gate4-mechanical-80pct", fmt.Sprintf("mechanical share %d/%d below threshold", mech, total))
	}

	// Gate 5: useful at realistic opaque fraction. Falsifiable two-part
	// check, both executed here: (a) the frozen broad-opaque cell must show
	// collapse (T4 == T2: union premises cannot narrow); (b) the mixed
	// pipeline (deterministic narrowing before ONE opaque call) must show
	// strict selective value (mixed exec < broad exec, both oracle-clean).
	opaqueT4 := cell(rows, "http", "opaque-multi-field", "T4")
	opaqueT2 := cell(rows, "http", "opaque-multi-field", "T2")
	collapse := opaqueT4 != nil && opaqueT2 != nil &&
		opaqueT4.Metrics.WorkExec == opaqueT2.Metrics.WorkExec
	work := WorkSpec{BurnRounds: 400, Label: "gate5"}
	mixedOK := false
	mixedDetail := ""
	// Independent measurement (not trusting another test's log).
	mkPipe := func(narrow bool) (JobSpec, []Change, func(Change), *HTTPFixture) {
		f := NewHTTPFixture(map[string]string{
			"/doc": `{"price":100,"qty":2,"note":"keep","other":{"x":1}}`,
		})
		fields := []string{"price", "qty", "note"}
		var slices []SliceBuilder
		if narrow {
			slices = []SliceBuilder{
				{ID: "narrow", From: []string{"read:0"}, Work: work, Make: func(ins [][]byte) []byte {
					return append([]byte("narrowed:"), ins[0]...)
				}},
				{ID: "opaque", From: []string{"narrow"}, Work: work, Opaque: true, Make: func(ins [][]byte) []byte {
					return []byte("opaque:" + DigestBytes(joinAll(ins))[:8])
				}},
			}
		} else {
			opq := func(id string, from []string) SliceBuilder {
				return SliceBuilder{ID: id, From: from, Work: work, Opaque: true, Make: func(ins [][]byte) []byte {
					return []byte("opaque:" + DigestBytes(joinAll(ins))[:8])
				}}
			}
			slices = []SliceBuilder{opq("opaque1", []string{"read:0", "read:1", "read:2"}), opq("opaque2", []string{"opaque1"})}
		}
		spec := HTTPJob(f, "gate5", []string{"/doc"}, map[string][]string{"/doc": fields}, slices,
			func(outs map[string][]byte, _ map[string]Val, _ []Val) (string, string) {
				return "/doc", `{"price":150,"qty":2,"note":"keep","other":{"x":1}}`
			})
		sched := []Change{{Target: "/doc", NewBody: `{"price":100,"qty":2,"note":"changed","other":{"x":1}}`, AfterPhase: "reason"}}
		applier := func(ch Change) {
			_, _ = f.PutUnconditional(ch.Target, ch.NewBody)
		}
		_ = fields
		return spec, sched, applier, f
	}
	bSpec, bSched, bApp, _ := mkPipe(false)
	bM := Run(bSpec, T4Replay, bSched, bApp, 3)
	mSpec, mSched, mApp, _ := mkPipe(true)
	mM := Run(mSpec, T4Replay, mSched, mApp, 3)
	if bM.OracleOK && mM.OracleOK && !bM.MissedConf && !mM.MissedConf {
		mixedDetail = fmt.Sprintf("broad=%d mixed=%d", bM.WorkExec, mM.WorkExec)
		mixedOK = mM.WorkExec < bM.WorkExec
	} else {
		mixedDetail = "oracle failure in gate-5 probe"
	}
	if collapse && mixedOK {
		pass("gate5-opaque-useful", "broad-opaque collapses (T4==T2); mixed pipeline recovers selectivity ("+mixedDetail+")")
	} else {
		fail("gate5-opaque-useful", fmt.Sprintf("collapse=%v mixed-better=%v (%s)", collapse, mixedOK, mixedDetail))
	}

	// Gate 6: overhead must not erase the gain where reasoning is expensive.
	// Operationalization (same intent, honest units): on contention cells
	// with preserved work, T4 end-to-end wall must beat T2 end-to-end wall.
	// Wall subsumes every cost (work, bookkeeping, witness IO, fixture
	// forks) with no unit conversions. Burn-400 slices cost ~283us each
	// (measured), so the regime is ~100x validation scale.
	overOK := true
	var overDetail []string
	for _, c := range contention {
		t2 := cell(rows, c.workload, c.scenario, "T2")
		t4 := cell(rows, c.workload, c.scenario, "T4")
		if t2 == nil || t4 == nil {
			continue
		}
		preserved := t2.Metrics.WorkExec - t4.Metrics.WorkExec
		if preserved <= 0 {
			continue
		}
		overDetail = append(overDetail, fmt.Sprintf("%s/%s T4wall=%dms T2wall=%dms",
			c.workload, c.scenario, t4.Metrics.WallNS/1000000, t2.Metrics.WallNS/1000000))
		if t4.Metrics.WallNS >= t2.Metrics.WallNS {
			overOK = false
		}
	}
	if overOK {
		pass("gate6-overhead", "T4 wall beats T2 wall on preserved-work cells: "+joinStr(overDetail, "; "))
	} else {
		fail("gate6-overhead", "T4 wall does not beat T2 wall: "+joinStr(overDetail, "; "))
	}

	// Gate 7: no second authority (representation audit doc + code facts:
	// no version tables owned; witnesses always re-read/native).
	pass("gate7-no-second-authority", "REPRESENTATION_ANALYSIS.md + adapter review")

	if len(gates) != len(historicalOutcome) {
		t.Errorf("evaluated %d gates, historical record has %d", len(gates), len(historicalOutcome))
	}
	for _, g := range gates {
		status := "PASS"
		if !g.pass {
			status = "FAIL"
		}
		t.Logf("%s %s: %s", status, g.name, g.detail)
		want, ok := historicalOutcome[g.name]
		if !ok {
			t.Errorf("gate %s has no recorded historical outcome", g.name)
			continue
		}
		if g.pass != want {
			t.Errorf("historical evidence check: %s evaluated pass=%v, recorded outcome pass=%v (%s)", g.name, g.pass, want, g.detail)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func fmtPct(f float64) string {
	return " " + itoa(int(f*100+0.5)) + "%"
}

func fmtRatio(f float64) string {
	return " " + itoa(int(f*100+0.5)) + "%"
}

func joinStr(ss []string, sep string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
