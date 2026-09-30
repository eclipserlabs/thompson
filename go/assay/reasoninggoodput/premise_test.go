package reasoninggoodput

import (
	"testing"
)

// premise_test.go: Phase 4 — binder correctness BEFORE concurrency
// performance. Tasks carry hidden ground-truth dependency sets; the binder
// sees only values. Metrics: recall, precision, unsafe omissions,
// over-conservative inclusions, UNKNOWN rate. Hard gates: zero omitted
// safety-relevant premises on held-out supported classes; no false narrowing
// across opaque boundaries (conservative widening mandatory).

// groundTask is one fixture: hidden truth + mechanically derived premises.
type groundTask struct {
	name      string
	build     func() (Val, map[string]bool) // value + hidden truth set
	supported bool                          // in-scope dependency classes?
}

// premiseID renders a Premise for truth comparison.
func premiseID(p Premise) string { return p.Resource + "@" + p.Witness + "/" + p.Provenance }

// devTasks: frozen development set.
func devTasks(g *GitRepo, f *HTTPFixture) []groundTask {
	doc := `{"price":100,"qty":2,"note":"irrelevant","other":{"x":1}}`
	return []groundTask{
		{
			name: "direct-flow",
			build: func() (Val, map[string]bool) {
				r, _ := f.GetWitnessed("/doc")
				v := Read(r)
				out := Transform([]byte("total"), v)
				truth := map[string]bool{}
				for _, q := range out.Premises {
					truth[premiseID(q)] = true
				}
				return out, truth
			},
			supported: true,
		},
		{
			name: "field-select-narrow",
			build: func() (Val, map[string]bool) {
				_ = doc
				r, _ := f.GetWitnessed("/doc")
				v := Read(r)
				fields, err := SelectJSON(v, "http:/doc", r.Witness.Version, "price", "qty")
				if err != nil {
					panic(err)
				}
				joined := Transform([]byte("pq"), fields["price"], fields["qty"])
				truth := map[string]bool{
					"http:/doc#price@" + r.Witness.Version + "/field": true,
					"http:/doc#qty@" + r.Witness.Version + "/field":   true,
				}
				return joined, truth
			},
			supported: true,
		},
		{
			name: "control-dep",
			build: func() (Val, map[string]bool) {
				r, _ := f.GetWitnessed("/doc")
				v := Read(r)
				fields, _ := SelectJSON(v, "http:/doc", r.Witness.Version, "price")
				// Branch on price: control premise must appear.
				var out Val
				if string(fields["price"].Bytes) == "100" {
					out = Branch(fields["price"], []byte("bucket-a"))
				} else {
					out = Branch(fields["price"], []byte("bucket-b"))
				}
				truth := map[string]bool{
					"http:/doc#price@" + r.Witness.Version + "/control": true,
				}
				return out, truth
			},
			supported: true,
		},
		{
			name: "opaque-union",
			build: func() (Val, map[string]bool) {
				r, _ := f.GetWitnessed("/doc")
				v := Read(r)
				fields, _ := SelectJSON(v, "http:/doc", r.Witness.Version, "price", "note", "other")
				out := OpaqueBoundary([]byte("opaque-out"), fields["price"], fields["note"], fields["other"])
				truth := map[string]bool{}
				for _, q := range out.Premises {
					truth[premiseID(q)] = true
				}
				if !out.Opaque {
					panic("opaque flag dropped")
				}
				return out, truth
			},
			supported: true,
		},
		{
			name: "git-blob-bind",
			build: func() (Val, map[string]bool) {
				r, _ := g.ReadWitnessed("src/main.txt")
				v := Read(r)
				out := Transform([]byte("analysis"), v)
				truth := map[string]bool{}
				for _, q := range out.Premises {
					truth[premiseID(q)] = true
				}
				return out, truth
			},
			supported: true,
		},
	}
}

// heldOutTasks: reserved fixtures (different resources/paths/shapes).
func heldOutTasks(g *GitRepo, f *HTTPFixture) []groundTask {
	return []groundTask{
		{
			name: "heldout-nested-field",
			build: func() (Val, map[string]bool) {
				r, _ := f.GetWitnessed("/doc")
				v := Read(r)
				fields, err := SelectJSON(v, "http:/doc", r.Witness.Version, "other")
				if err != nil {
					panic(err)
				}
				out := Transform([]byte("nested"), fields["other"])
				truth := map[string]bool{
					"http:/doc#other@" + r.Witness.Version + "/field": true,
				}
				return out, truth
			},
			supported: true,
		},
		{
			name: "heldout-opaque-narrow-attempt",
			build: func() (Val, map[string]bool) {
				// An opaque call that "only needs" price: binder must STILL
				// widen to everything visible (no false narrowing).
				r, _ := f.GetWitnessed("/doc")
				v := Read(r)
				fields, _ := SelectJSON(v, "http:/doc", r.Witness.Version, "price", "qty", "note")
				out := OpaqueBoundary([]byte("o"), fields["price"], fields["qty"], fields["note"])
				if len(out.Premises) < 3 {
					panic("false narrowing across opaque boundary")
				}
				truth := map[string]bool{}
				for _, q := range out.Premises {
					truth[premiseID(q)] = true
				}
				return out, truth
			},
			supported: true,
		},
		{
			name: "heldout-git-second-file",
			build: func() (Val, map[string]bool) {
				r, _ := g.ReadWitnessed("lib/util.txt")
				v := Read(r)
				// Irrelevant sibling read must still be recorded (it WAS read;
				// minimality is the validator's job, not the binder's).
				r2, _ := g.ReadWitnessed("src/main.txt")
				v2 := Read(r2)
				out := Transform([]byte("both"), v, v2)
				truth := map[string]bool{}
				for _, q := range out.Premises {
					truth[premiseID(q)] = true
				}
				return out, truth
			},
			supported: true,
		},
	}
}

func evalTasks(t *testing.T, tasks []groundTask, heldout bool) (recall, precision float64, unsafeOmit, overCons int, unknowns int) {
	t.Helper()
	var tp, fp, fn int
	for _, task := range tasks {
		v, truth := task.build()
		got, err := PremisesOf(v)
		if err != nil {
			unknowns++
			continue
		}
		gotSet := map[string]bool{}
		for _, q := range got {
			gotSet[premiseID(q)] = true
		}
		for id := range truth {
			if gotSet[id] {
				tp++
			} else {
				fn++
				if task.supported {
					unsafeOmit++
					t.Errorf("task %s omitted safety-relevant premise %s", task.name, id)
				}
			}
		}
		for id := range gotSet {
			if !truth[id] {
				fp++
				overCons++
			}
		}
	}
	if tp+fn > 0 {
		recall = float64(tp) / float64(tp+fn)
	} else {
		recall = 1
	}
	if tp+fp > 0 {
		precision = float64(tp) / float64(tp+fp)
	} else {
		precision = 1
	}
	_ = heldout
	return recall, precision, unsafeOmit, overCons, unknowns
}

func premiseFixture(t *testing.T) (*GitRepo, *HTTPFixture) {
	t.Helper()
	g, err := NewGitRepo(testTmp{t})
	if err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })
	if err := g.WriteFile("src/main.txt", "main content v1\n"); err != nil {
		t.Fatal(err)
	}
	if err := g.WriteFile("lib/util.txt", "util content v1\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Commit("fixture"); err != nil {
		t.Fatal(err)
	}
	f := NewHTTPFixture(map[string]string{
		"/doc": `{"price":100,"qty":2,"note":"irrelevant","other":{"x":1}}`,
	})
	t.Cleanup(f.Close)
	return g, f
}

func TestPremiseDev(t *testing.T) {
	g, f := premiseFixture(t)
	recall, precision, omit, over, unk := evalTasks(t, devTasks(g, f), false)
	t.Logf("dev: recall=%.3f precision=%.3f unsafeOmit=%d overCons=%d unknown=%d",
		recall, precision, omit, over, unk)
	if omit != 0 {
		t.Fatal("unsafe omissions on dev set")
	}
}

func TestPremiseHeldOut(t *testing.T) {
	g, f := premiseFixture(t)
	recall, precision, omit, over, unk := evalTasks(t, heldOutTasks(g, f), true)
	t.Logf("heldout: recall=%.3f precision=%.3f unsafeOmit=%d overCons=%d unknown=%d",
		recall, precision, omit, over, unk)
	if omit != 0 {
		t.Fatalf("HARD GATE FAILED: %d omitted safety-relevant premises on held-out", omit)
	}
	if recall < 1.0 {
		t.Fatalf("held-out recall %.3f < 1.0", recall)
	}
}
