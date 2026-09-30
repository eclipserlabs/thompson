package reasoninggoodput

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// matrix_test.go: frozen comparison matrix. Every treatment × every Git +
// HTTP scenario with fixed seeds/schedules, full TaskMetrics plus final
// content digests. Machine output: testdata/goodput_matrix.json. This file
// is the evidentiary basis for the Phase 16 decision gates.

type MatrixRow struct {
	Workload  string      `json:"workload"`
	Scenario  string      `json:"scenario"`
	Treatment string      `json:"treatment"`
	Metrics   TaskMetrics `json:"metrics"`
	Final     string      `json:"final_digest"`
}

func TestGoodputMatrix(t *testing.T) {
	work := WorkSpec{BurnRounds: 400, Label: "analyze"}
	var rows []MatrixRow

	// Git scenarios.
	for _, sc := range GitScenarioSet(work) {
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
			t.Fatalf("git/%s: serial reference failed", sc.Name)
		}
		refContent, err := gRef.WorktreeBytes(sc.Target)
		if err != nil {
			t.Fatalf("git/%s: no reference output", sc.Name)
		}
		for _, treat := range []TreatID{T0Serial, T1Late, T2Broad, T3Premise, T4Replay} {
			g := gitTestBed(t)
			spec := GitJob(g, sc.Name, sc.Reads, sc.Slices, sc.Target, sc.Derive)
			applier := func(ch Change) {
				_ = g.WriteFile(ch.Target, ch.NewBody)
				_, _ = g.Commit("env " + ch.Target)
			}
			m := Run(spec, treat, sc.Schedule, applier, 3)
			final, ferr := g.WorktreeBytes(sc.Target)
			digest := ""
			if ferr == nil {
				digest = DigestBytes(final)
			}
			rows = append(rows, MatrixRow{"git", sc.Name, string(treat), m, digest})
			_ = refContent
		}
	}

	// HTTP scenarios.
	for _, sc := range HTTPScaffold(work) {
		fRef := httpTestBed(t)
		for _, ch := range sc.Schedule {
			if _, err := fRef.PutUnconditional(ch.Target, ch.NewBody); err != nil {
				t.Fatal(err)
			}
		}
		refSpec := HTTPJob(fRef, sc.Name+"-ref", sc.Reads, sc.Fields, sc.Slices, sc.Propose)
		refM := Run(refSpec, T0Serial, nil, func(Change) {}, 0)
		if !refM.OracleOK {
			t.Fatalf("http/%s: serial reference failed", sc.Name)
		}
		refBody, _ := fRef.GetWitnessed("/doc")
		for _, treat := range []TreatID{T0Serial, T1Late, T2Broad, T3Premise, T4Replay} {
			f := httpTestBed(t)
			spec := HTTPJob(f, sc.Name, sc.Reads, sc.Fields, sc.Slices, sc.Propose)
			if sc.Name == "invariant-shared-value" {
				spec.OracleVerify = invariantOracle(f)
			}
			applier := func(ch Change) {
				_, _ = f.PutUnconditional(ch.Target, ch.NewBody)
			}
			m := Run(spec, treat, sc.Schedule, applier, 3)
			final, ferr := f.GetWitnessed("/doc")
			digest := ""
			if ferr == nil {
				digest = DigestBytes(final.Value)
			}
			rows = append(rows, MatrixRow{"http", sc.Name, string(treat), m, digest})
			_ = refBody
		}
	}

	raw, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("testdata/goodput_matrix.json", raw, 0o644); err != nil {
		t.Fatal(err)
	}
	// Gate-relevant assertions live in the decision test (decision_test.go);
	// this test asserts structural invariants only.
	for _, r := range rows {
		if r.Metrics.Treatment == "T1" {
			continue // informational by design
		}
		if r.Metrics.MissedConf {
			t.Errorf("%s/%s/%s missed conflict", r.Workload, r.Scenario, r.Treatment)
		}
	}
}

// invariantOracle enforces the fixture invariant (price never below 175
// once the concurrent change landed) for invariant-shared-value.
func invariantOracle(f *HTTPFixture) func() error {
	return func() error {
		cur, err := f.GetWitnessed("/doc")
		if err != nil {
			return err
		}
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
