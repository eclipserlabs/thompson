package reasoninggoodput

import (
	"testing"
)

// confluence_test.go: Phase 11 (P1, conditional — runs because premise
// validation shows material value). Question: can operation algebra from
// machine-readable resource semantics avoid coordination, or is useful
// confluence knowledge application-specific configuration? Rule: only
// resource-identity structure (same vs disjoint validators); no handwritten
// business invariants; no LLM judges; default COORDINATE/UNKNOWN.

// Confluence verdicts for a pair of pending mutations.
type Confluence string

const (
	SafeConcurrent    Confluence = "SAFE_CONCURRENT"
	Validate          Confluence = "VALIDATE"
	Coordinate        Confluence = "COORDINATE"
	ConfluenceUnknown Confluence = "UNKNOWN"
)

// ClassifyPair decides mergeability from resource identities alone:
// disjoint validators (different resources/URLs) with conditional commits
// can proceed concurrently; same coarse validator cannot prove field
// disjointness at commit time and must coordinate; unknown authority fails
// closed. This is the entire classifier — deliberately tiny.
func ClassifyPair(opA, opB MutationCandidate) Confluence {
	ra, rb := targetOf(opA), targetOf(opB)
	if ra == "" || rb == "" {
		return ConfluenceUnknown
	}
	if ra != rb {
		return SafeConcurrent
	}
	return Coordinate
}

func targetOf(m MutationCandidate) string {
	const httpPut = "http-put:"
	const gitWrite = "git-write:"
	if len(m.Op) > len(httpPut) && m.Op[:len(httpPut)] == httpPut {
		return "http:" + m.Op[len(httpPut):]
	}
	if len(m.Op) > len(gitWrite) && m.Op[:len(gitWrite)] == gitWrite {
		return "git:" + m.Op[len(gitWrite):]
	}
	return ""
}

func TestConfluenceDisjoint(t *testing.T) {
	// Two writers, disjoint URL resources, conditional commits: both succeed
	// without coordination (SAFE_CONCURRENT), verified by execution.
	f := httpTestBed(t)
	r1, err := f.GetWitnessed("/doc")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := f.GetWitnessed("/cfg")
	if err != nil {
		t.Fatal(err)
	}
	mA := MutationCandidate{Op: "http-put:/doc", Args: []byte(`{"price":111}`)}
	mB := MutationCandidate{Op: "http-put:/cfg", Args: []byte(`{"limit":6}`)}
	if ClassifyPair(mA, mB) != SafeConcurrent {
		t.Fatal("disjoint resources must classify SAFE_CONCURRENT")
	}
	if _, err := f.PutIfMatch("/doc", string(mA.Args), r1.Witness.Version); err != nil {
		t.Fatalf("concurrent disjoint write failed: %v", err)
	}
	if _, err := f.PutIfMatch("/cfg", string(mB.Args), r2.Witness.Version); err != nil {
		t.Fatalf("concurrent disjoint write failed: %v", err)
	}
}

func TestConfluenceSameResource(t *testing.T) {
	// Same coarse resource: even field-disjoint writes cannot prove safety
	// at commit time (one ETag covers all fields) → COORDINATE. Demonstrated:
	// the second conditional commit fails and the loser must retry.
	f := httpTestBed(t)
	r1, err := f.GetWitnessed("/doc")
	if err != nil {
		t.Fatal(err)
	}
	mA := MutationCandidate{Op: "http-put:/doc", Args: []byte(`{"price":111}`)}
	mB := MutationCandidate{Op: "http-put:/doc", Args: []byte(`{"price":222}`)}
	if ClassifyPair(mA, mB) != Coordinate {
		t.Fatal("same coarse resource must classify COORDINATE")
	}
	if _, err := f.PutIfMatch("/doc", string(mA.Args), r1.Witness.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := f.PutIfMatch("/doc", string(mB.Args), r1.Witness.Version); err == nil {
		t.Fatal("stale same-resource write accepted (lost update)")
	}
}

func TestConfluenceUnknown(t *testing.T) {
	m := MutationCandidate{Op: "mystery-op", Args: []byte("x")}
	if ClassifyPair(m, m) != ConfluenceUnknown {
		t.Fatal("unparseable op must classify UNKNOWN")
	}
}
