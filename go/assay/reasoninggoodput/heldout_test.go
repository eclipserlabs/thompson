package reasoninggoodput

import (
	"testing"
)

// heldout_test.go: treatment-level held-out comparison for the Phase 16
// gates. Fresh seeds, distinct files/fields/values from every dev scenario,
// same frozen mechanics. Gates read from DECISION_THRESHOLDS.md; verdicts
// computed in decision_test.go from the matrix + these rows.

// TestHeldOutTreatments runs T0–T4 on unseen variants and asserts the safety
// floor (zero stale-premise violations, oracle agreement) directly.
func TestHeldOutTreatments(t *testing.T) {
	work := WorkSpec{BurnRounds: 400, Label: "heldout"}

	// Held-out git variant: different files change than any dev scenario.
	t.Run("git", func(t *testing.T) {
		g := gitTestBed(t)
		if err := g.WriteFile("lib/extra.txt", "extra v1\n"); err != nil {
			t.Fatal(err)
		}
		if _, err := g.Commit("extra file"); err != nil {
			t.Fatal(err)
		}
		sc := GitScenario{
			Name:  "heldout-unread-change",
			Reads: []string{"src/main.txt", "lib/util.txt"},
			Slices: []SliceBuilder{
				{ID: "s-main", From: []string{"read:0"}, Work: work, Make: func(ins [][]byte) []byte {
					return []byte("m:" + DigestBytes(ins[0])[:8])
				}},
				{ID: "s-util", From: []string{"read:1"}, Work: work, Make: func(ins [][]byte) []byte {
					return []byte("u:" + DigestBytes(ins[0])[:8])
				}},
			},
			Target: "out/heldout.txt",
			Derive: func(outs map[string][]byte) []byte {
				return []byte("result:" + DigestBytes([]byte(joinKeys(outs)))[:16] + "\n")
			},
			Schedule: []Change{
				{Target: "lib/extra.txt", NewBody: "extra v2\n", AfterPhase: "reason"},
			},
		}
		// Serial reference on quiescent final state.
		gRef := gitTestBed(t)
		if err := gRef.WriteFile("lib/extra.txt", "extra v1\n"); err != nil {
			t.Fatal(err)
		}
		if _, err := gRef.Commit("extra file"); err != nil {
			t.Fatal(err)
		}
		for _, ch := range sc.Schedule {
			if err := gRef.WriteFile(ch.Target, ch.NewBody); err != nil {
				t.Fatal(err)
			}
			if _, err := gRef.Commit("env"); err != nil {
				t.Fatal(err)
			}
		}
		refSpec := GitJob(gRef, sc.Name+"-ref", sc.Reads, sc.Slices, sc.Target, sc.Derive)
		refM := Run(refSpec, T0Serial, nil, func(Change) {}, 0)
		if !refM.OracleOK {
			t.Fatal("held-out reference failed")
		}
		refContent, _ := gRef.WorktreeBytes(sc.Target)
		for _, treat := range []TreatID{T0Serial, T2Broad, T3Premise, T4Replay} {
			g2 := gitTestBed(t)
			if err := g2.WriteFile("lib/extra.txt", "extra v1\n"); err != nil {
				t.Fatal(err)
			}
			if _, err := g2.Commit("extra file"); err != nil {
				t.Fatal(err)
			}
			spec := GitJob(g2, sc.Name, sc.Reads, sc.Slices, sc.Target, sc.Derive)
			applier := func(ch Change) {
				_ = g2.WriteFile(ch.Target, ch.NewBody)
				_, _ = g2.Commit("env " + ch.Target)
			}
			m := Run(spec, treat, sc.Schedule, applier, 3)
			final, ferr := g2.WorktreeBytes(sc.Target)
			if ferr != nil || string(final) != string(refContent) {
				t.Errorf("heldout git/%s diverged", treat)
			}
			if m.MissedConf || !m.OracleOK {
				t.Errorf("heldout git/%s unsafe: %+v", treat, m)
			}
			t.Logf("heldout git/%s exec=%d goodput=%.2f retries=%d", treat, m.WorkExec, m.Goodput, m.Retries)
		}
	})

	// Held-out HTTP variant: unseen field values + unseen changed field.
	t.Run("http", func(t *testing.T) {
		mkSpec := func(f *HTTPFixture) JobSpec {
			return HTTPJob(f, "heldout", []string{"/doc"},
				map[string][]string{"/doc": {"price", "qty"}},
				[]SliceBuilder{{
					ID: "analyze", From: []string{"read:0", "read:1"}, Work: work,
					Make: func(ins [][]byte) []byte {
						return []byte("a:" + DigestBytes(joinAll(ins))[:8])
					},
				}},
				func(outs map[string][]byte, _ map[string]Val, _ []Val) (string, string) {
					return "/doc", `{"price":777,"qty":2,"note":"keep","other":{"x":1}}`
				})
		}
		sched := []Change{
			{Target: "/doc", NewBody: `{"price":100,"qty":9,"note":"keep","other":{"x":1}}`, AfterPhase: "reason"},
		}
		fRef := httpTestBed(t)
		for _, ch := range sched {
			if _, err := fRef.PutUnconditional(ch.Target, ch.NewBody); err != nil {
				t.Fatal(err)
			}
		}
		refM := Run(mkSpec(fRef), T0Serial, nil, func(Change) {}, 0)
		if !refM.OracleOK {
			t.Fatal("held-out reference failed")
		}
		refBody, _ := fRef.GetWitnessed("/doc")
		for _, treat := range []TreatID{T0Serial, T2Broad, T3Premise, T4Replay} {
			f := httpTestBed(t)
			spec := mkSpec(f)
			applier := func(ch Change) {
				_, _ = f.PutUnconditional(ch.Target, ch.NewBody)
			}
			m := Run(spec, treat, sched, applier, 3)
			final, ferr := f.GetWitnessed("/doc")
			if ferr != nil || string(final.Value) != string(refBody.Value) {
				t.Errorf("heldout http/%s diverged", treat)
			}
			if m.MissedConf || !m.OracleOK {
				t.Errorf("heldout http/%s unsafe: %+v", treat, m)
			}
			t.Logf("heldout http/%s exec=%d goodput=%.2f retries=%d", treat, m.WorkExec, m.Goodput, m.Retries)
		}
	})
}
