package reasoninggoodput

import (
	"fmt"
	"sort"
)

// gitwork.go: Phase 6 GIT_WORKLOAD scenarios. Workers read witnessed blobs,
// run reasoning slices, and propose a file mutation applied by
// compare-and-commit on premise-blob staleness. Oracles: exact content,
// repository integrity, serial reference.

// GitJob builds a JobSpec reading paths, analyzing with slices, proposing a
// mutation to targetPath derived from slice outputs.
func GitJob(g *GitRepo, id string, reads []string, slices []SliceBuilder, targetPath string,
	derive func(outs map[string][]byte) []byte) JobSpec {
	spec := JobSpec{ID: id}
	spec.Reads = func() ([]Val, error) {
		var out []Val
		for _, p := range reads {
			r, err := g.ReadWitnessed(p)
			if err != nil {
				return nil, err
			}
			out = append(out, Read(r))
		}
		return out, nil
	}
	spec.Slices = slices
	spec.Propose = func(outs map[string][]byte, vals map[string]Val, reads []Val) MutationCandidate {
		var premises PremiseSet
		for _, v := range vals {
			premises = append(premises, v.Premises...)
		}
		premises = premises.Normalize()
		body := derive(outs)
		var sliceIDs []string
		for _, sb := range slices {
			sliceIDs = append(sliceIDs, sb.ID)
		}
		return MutationCandidate{Op: "git-write:" + targetPath, Args: body,
			Premises: premises, Slices: sliceIDs,
			Touched: []string{"git:" + targetPath}}
	}
	spec.Commit = func(m MutationCandidate, conditional bool) (bool, error) {
		if conditional {
			// Premise-gated commit: only blobs named by the candidate's
			// premises must be unchanged (finer than whole-read-set when
			// slices consume a subset). Head movement alone never blocks
			// (content-addressed premises, not head pinning).
			for _, q := range m.Premises {
				if len(q.Resource) < 4 || q.Resource[:4] != "git:" {
					continue
				}
				p := q.Resource[4:]
				cur, err := g.BlobOID(p)
				if err != nil || cur != q.Witness {
					return false, fmt.Errorf("premise blob %s moved", p)
				}
			}
		}
		if err := g.WriteFile(targetPath, string(m.Args)); err != nil {
			return false, err
		}
		if _, err := g.Commit("assay " + id); err != nil {
			return false, err
		}
		return true, nil
	}
	spec.Compensate = func(m MutationCandidate) error {
		// Assay affordance: own commit is tip (single-threaded interleaving);
		// remove exactly it, preserving concurrent history.
		_, err := gitIn(g.Dir, "reset", "--hard", "HEAD~1")
		return err
	}
	spec.Live = func() map[string]StateWitness {
		out := map[string]StateWitness{}
		for _, p := range reads {
			oid, err := g.BlobOID(p)
			if err != nil {
				continue
			}
			out["git:"+p] = StateWitness{Ref: ResourceRef{Authority: "git", ID: p}, Version: oid}
		}
		return out
	}
	spec.OracleVerify = func() error {
		if _, err := gitIn(g.Dir, "fsck", "--no-dangling"); err != nil {
			return err
		}
		if _, err := gitIn(g.Dir, "status", "--porcelain"); err != nil {
			return err
		}
		return nil
	}
	spec.SliceInputs = map[string][]string{}
	return spec
}

// GitScenarios builds the six frozen GIT_WORKLOAD scenarios. Each returns
// job factory + change schedule + expected-final-content oracle info.
type GitScenario struct {
	Name     string
	Reads    []string
	Slices   []SliceBuilder
	Target   string
	Derive   func(map[string][]byte) []byte
	Schedule []Change
	// ExpectFinal validates final content (serial reference inside test).
	ExpectFinal func(t TB, g *GitRepo)
}

// TB abstracts *testing.T logging/failure.
type TB interface {
	Helper()
	Fatalf(string, ...interface{})
	Logf(string, ...interface{})
}

func analysisSlices(work WorkSpec, from ...string) []SliceBuilder {
	return []SliceBuilder{{
		ID:   "analyze",
		From: from,
		Work: work,
		Make: func(ins [][]byte) []byte {
			h := "analysis:"
			for _, b := range ins {
				h += DigestBytes(b)[:8] + ";"
			}
			return []byte(h)
		},
	}}
}

func sortedKeys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// GitScenarioSet returns the six frozen scenarios (seeds fixed in tests).
func GitScenarioSet(work WorkSpec) []GitScenario {
	derive := func(outs map[string][]byte) []byte {
		return []byte("result:" + DigestBytes([]byte(joinKeys(outs)))[:16] + "\n")
	}
	return []GitScenario{
		{
			Name: "irrelevant-file-change", Reads: []string{"src/main.txt"},
			Slices: analysisSlices(work, "read:0"), Target: "out/result.txt",
			Derive: derive,
			Schedule: []Change{
				{Target: "lib/other.txt", NewBody: "changed-unrelated\n", AfterPhase: "reason"},
			},
		},
		{
			Name: "premise-blob-change", Reads: []string{"src/main.txt"},
			Slices: analysisSlices(work, "read:0"), Target: "out/result.txt",
			Derive: derive,
			Schedule: []Change{
				{Target: "src/main.txt", NewBody: "main content v2\n", AfterPhase: "reason"},
			},
		},
		{
			Name: "unrelated-never-read", Reads: []string{"src/main.txt"},
			Slices: analysisSlices(work, "read:0"), Target: "out/result.txt",
			Derive: derive,
			Schedule: []Change{
				{Target: "docs/notes.txt", NewBody: "notes\n", AfterPhase: "read"},
			},
		},
		{
			Name:  "disjoint-slices",
			Reads: []string{"src/main.txt", "lib/util.txt"},
			Slices: []SliceBuilder{
				{ID: "s-main", From: []string{"read:0"}, Work: work, Make: func(ins [][]byte) []byte {
					return []byte("m:" + DigestBytes(ins[0])[:8])
				}},
				{ID: "s-util", From: []string{"read:1"}, Work: work, Make: func(ins [][]byte) []byte {
					return []byte("u:" + DigestBytes(ins[0])[:8])
				}},
			},
			Target: "out/result.txt", Derive: derive,
			Schedule: []Change{
				{Target: "lib/util.txt", NewBody: "util content v2\n", AfterPhase: "reason"},
			},
		},
		{
			Name: "base-moves-premises-same", Reads: []string{"src/main.txt"},
			Slices: analysisSlices(work, "read:0"), Target: "out/result.txt",
			Derive: derive,
			Schedule: []Change{
				{Target: "lib/other.txt", NewBody: "other v2\n", AfterPhase: "read"},
				{Target: "docs/notes.txt", NewBody: "notes v2\n", AfterPhase: "reason"},
			},
		},
		{
			Name:  "opaque-multi-blob",
			Reads: []string{"src/main.txt", "lib/util.txt", "docs/notes.txt"},
			Slices: []SliceBuilder{{
				ID: "opaque", From: []string{"read:0", "read:1", "read:2"}, Work: work, Opaque: true,
				Make: func(ins [][]byte) []byte {
					return []byte("opaque:" + DigestBytes(joinAll(ins))[:8])
				},
			}},
			Target: "out/result.txt", Derive: derive,
			Schedule: []Change{
				{Target: "docs/notes.txt", NewBody: "notes v2\n", AfterPhase: "reason"},
			},
		},
		{
			// Extension scenario (added to isolate T3 premise-minimization
			// from T2 broad read-sets after observing T3≡T2 on the frozen
			// six): reads cover three files, reasoning consumes one.
			Name:   "unconsumed-file",
			Reads:  []string{"src/main.txt", "lib/util.txt", "lib/other.txt"},
			Slices: analysisSlices(work, "read:0"), Target: "out/result.txt",
			Derive: derive,
			Schedule: []Change{
				{Target: "lib/util.txt", NewBody: "util content v2\n", AfterPhase: "reason"},
			},
		},
	}
}

func joinKeys(m map[string][]byte) string {
	s := ""
	for _, k := range sortedKeys(m) {
		s += k + "=" + string(m[k]) + ";"
	}
	return s
}
