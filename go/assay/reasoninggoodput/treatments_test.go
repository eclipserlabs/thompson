package reasoninggoodput

import (
	"encoding/json"
	"fmt"
	"testing"
)

// treatments_test.go: Phases 6-8. All five treatments over the frozen Git +
// HTTP scenarios with frozen change schedules. Oracles: serial reference
// (T0 on the quiescent final state where applicable), exact content,
// fixture invariants. Machine output: testdata/goodput_matrix.json.

type MatrixEntry struct {
	Workload  string      `json:"workload"`
	Scenario  string      `json:"scenario"`
	Treatment string      `json:"treatment"`
	Metrics   TaskMetrics `json:"metrics"`
}

func gitTestBed(t *testing.T) *GitRepo {
	t.Helper()
	g, err := NewGitRepo(testTmp{t})
	if err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })
	files := map[string]string{
		"src/main.txt":   "main content v1\n",
		"lib/util.txt":   "util content v1\n",
		"lib/other.txt":  "other content v1\n",
		"docs/notes.txt": "notes content v1\n",
	}
	for p, b := range files {
		if err := g.WriteFile(p, b); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.Commit("base"); err != nil {
		t.Fatal(err)
	}
	return g
}

func httpTestBed(t *testing.T) *HTTPFixture {
	t.Helper()
	f := NewHTTPFixture(map[string]string{
		"/doc": `{"price":100,"qty":2,"note":"keep","other":{"x":1}}`,
		"/cfg": `{"limit":5}`,
	})
	t.Cleanup(f.Close)
	return f
}

func TestTreatmentsGit(t *testing.T) {
	work := WorkSpec{BurnRounds: 400, Label: "analyze"}
	for _, sc := range GitScenarioSet(work) {
		// Serial reference: apply all changes first, then run T0.
		gRef := gitTestBed(t)
		for _, ch := range sc.Schedule {
			if err := gRef.WriteFile(ch.Target, ch.NewBody); err != nil {
				t.Fatal(err)
			}
			if _, err := gRef.Commit("env " + ch.Target); err != nil {
				t.Fatal(err)
			}
		}
		refSpec := GitJob(gRef, sc.Name+"-ref", sc.Reads, sc.Slices, sc.Target, sc.Derive)
		refM := Run(refSpec, T0Serial, nil, func(Change) {}, 0)
		if !refM.OracleOK {
			t.Fatalf("%s: serial reference failed", sc.Name)
		}
		refContent, err := gRef.WorktreeBytes(sc.Target)
		if err != nil {
			t.Fatalf("%s: no reference output: %v", sc.Name, err)
		}
		for _, treat := range []TreatID{T0Serial, T1Late, T2Broad, T3Premise, T4Replay} {
			g := gitTestBed(t)
			spec := GitJob(g, sc.Name, sc.Reads, sc.Slices, sc.Target, sc.Derive)
			applier := func(ch Change) {
				_ = g.WriteFile(ch.Target, ch.NewBody)
				_, _ = g.Commit("env " + ch.Target)
			}
			m := Run(spec, treat, sc.Schedule, applier, 3)
			finalContent, ferr := g.WorktreeBytes(sc.Target)
			// Oracle: final content must equal serial reference, except
			// premise-blob-change (serial ran on changed input by design —
			// still comparable) — all scenarios converge by construction
			// except T1 blind-overwrite races, which the oracle must catch.
			match := ferr == nil && string(finalContent) == string(refContent)
			t.Logf("git/%s/%s exec=%d goodput=%.2f retries=%d late=%v false=%v missed=%v oracle=%v match=%v",
				sc.Name, treat, m.WorkExec, m.Goodput, m.Retries, m.LateConf,
				m.FalseConf, m.MissedConf, m.OracleOK, match)
			if treat == T0Serial && !match {
				t.Errorf("git/%s/T0 diverged from serial reference", sc.Name)
			}
			// T2/T3/T4 must converge to the serial reference (bounded
			// retries on a finite schedule always reach quiescence here).
			// T1 is informational: blind writes may diverge (late cost).
			if (treat == T2Broad || treat == T3Premise || treat == T4Replay) && !match {
				t.Errorf("git/%s/%s failed to converge", sc.Name, treat)
			}
			if (treat == T2Broad || treat == T3Premise || treat == T4Replay) && m.MissedConf {
				t.Errorf("git/%s/%s missed conflict", sc.Name, treat)
			}
		}
	}
}

func TestTreatmentsHTTP(t *testing.T) {
	work := WorkSpec{BurnRounds: 400, Label: "analyze"}
	for _, sc := range HTTPScaffold(work) {
		// Serial reference on quiescent final state.
		fRef := httpTestBed(t)
		for _, ch := range sc.Schedule {
			if _, err := fRef.PutUnconditional(ch.Target, ch.NewBody); err != nil {
				t.Fatal(err)
			}
		}
		refSpec := HTTPJob(fRef, sc.Name+"-ref", sc.Reads, sc.Fields, sc.Slices, sc.Propose)
		refM := Run(refSpec, T0Serial, nil, func(Change) {}, 0)
		if !refM.OracleOK {
			t.Fatalf("%s: serial reference failed", sc.Name)
		}
		refBody, _ := fRef.GetWitnessed("/doc")
		for _, treat := range []TreatID{T0Serial, T1Late, T2Broad, T3Premise, T4Replay} {
			f := httpTestBed(t)
			spec := HTTPJob(f, sc.Name, sc.Reads, sc.Fields, sc.Slices, sc.Propose)
			if sc.Name == "invariant-shared-value" {
				spec.OracleVerify = func() error {
					cur, err := f.GetWitnessed("/doc")
					if err != nil {
						return err
					}
					// Fixture invariant: once the concurrent change landed
					// (price=175), the settled price must never go below
					// it. A blind stale bid (150) violates it.
					var doc struct {
						Price int `json:"price"`
					}
					if err := json.Unmarshal(cur.Value, &doc); err != nil {
						return err
					}
					if doc.Price < 175 {
						return fmt.Errorf("invariant violated: price=%d < 175", doc.Price)
					}
					return nil
				}
			}
			applier := func(ch Change) {
				_, _ = f.PutUnconditional(ch.Target, ch.NewBody)
			}
			m := Run(spec, treat, sc.Schedule, applier, 3)
			final, ferr := f.GetWitnessed("/doc")
			match := ferr == nil && string(final.Value) == string(refBody.Value)
			t.Logf("http/%s/%s exec=%d goodput=%.2f retries=%d late=%v false=%v missed=%v oracle=%v match=%v",
				sc.Name, treat, m.WorkExec, m.Goodput, m.Retries, m.LateConf,
				m.FalseConf, m.MissedConf, m.OracleOK, match)
			if treat == T0Serial && !match {
				t.Errorf("http/%s/T0 diverged", sc.Name)
			}
			if (treat == T2Broad || treat == T3Premise || treat == T4Replay) && !match {
				t.Errorf("http/%s/%s failed to converge", sc.Name, treat)
			}
			if (treat == T2Broad || treat == T3Premise || treat == T4Replay) && m.MissedConf {
				t.Errorf("http/%s/%s missed conflict", sc.Name, treat)
			}
		}
	}
}

// Matrix JSON output is produced by the dedicated comparison test
// (matrix_test.go); this file holds scenario execution only.
