package reasoninggoodput

import (
	"strings"
	"testing"
)

// adapters_test.go: Git witnesses are real OIDs; HTTP witnesses are strong
// ETags with If-Match semantics. No Thompson sequence numbers anywhere.

type testTmp struct{ t *testing.T }

func (t testTmp) TempDir() string { return t.t.TempDir() }

func TestGitWitnesses(t *testing.T) {
	g, err := NewGitRepo(testTmp{t})
	if err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	defer g.Close()
	if err := g.WriteFile("a.txt", "alpha\n"); err != nil {
		t.Fatal(err)
	}
	if err := g.WriteFile("sub/b.txt", "beta\n"); err != nil {
		t.Fatal(err)
	}
	head1, err := g.Commit("init")
	if err != nil {
		t.Fatal(err)
	}
	if len(head1) != 40 {
		t.Fatalf("head not an OID: %q", head1)
	}
	ra, err := g.ReadWitnessed("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(ra.Witness.Version) != 40 {
		t.Fatalf("blob witness not an OID: %q", ra.Witness.Version)
	}
	if string(ra.Value) != "alpha\n" {
		t.Fatalf("exact bytes not preserved: %q", ra.Value)
	}
	// Unrelated change: head moves, a.txt blob does not.
	if err := g.WriteFile("sub/b.txt", "beta-CHANGED\n"); err != nil {
		t.Fatal(err)
	}
	head2, err := g.Commit("touch b")
	if err != nil {
		t.Fatal(err)
	}
	if head1 == head2 {
		t.Fatal("head did not move")
	}
	ra2, err := g.ReadWitnessed("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if ra2.Witness.Version != ra.Witness.Version {
		t.Fatal("unrelated change moved an unrelated blob witness")
	}
	// Relevant change: blob witness moves.
	if err := g.WriteFile("a.txt", "alpha-CHANGED\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Commit("touch a"); err != nil {
		t.Fatal(err)
	}
	ra3, err := g.ReadWitnessed("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if ra3.Witness.Version == ra.Witness.Version {
		t.Fatal("content change did not move blob witness")
	}
	// Working-tree vs witnessed-blob distinction (adversarial support).
	if err := g.WriteFile("a.txt", "uncommitted\n"); err != nil {
		t.Fatal(err)
	}
	wt, _ := g.WorktreeBytes("a.txt")
	if string(wt) == string(ra3.Value) {
		t.Fatal("worktree/test confusion")
	}
	still, err := g.ReadWitnessed("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if still.Witness.Version != ra3.Witness.Version {
		t.Fatal("uncommitted bytes moved the committed witness")
	}
}

func TestHTTPWitnesses(t *testing.T) {
	f := NewHTTPFixture(map[string]string{
		"/doc": `{"price":100,"note":"keep","other":"untouched"}`,
		"/cfg": `{"limit":5}`,
	})
	defer f.Close()
	r1, err := f.GetWitnessed("/doc")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r1.Witness.Version, `"`) || !strings.HasSuffix(r1.Witness.Version, `"`) {
		t.Fatalf("not a strong ETag: %q", r1.Witness.Version)
	}
	// Irrelevant-field change moves the document ETag (coarse resource).
	if _, err := f.PutIfMatch("/doc", `{"price":100,"note":"changed","other":"untouched"}`, r1.Witness.Version); err != nil {
		t.Fatal(err)
	}
	r2, err := f.GetWitnessed("/doc")
	if err != nil {
		t.Fatal(err)
	}
	if r2.Witness.Version == r1.Witness.Version {
		t.Fatal("content change did not move ETag")
	}
	// Stale If-Match → 412 (no lost update, no silent overwrite).
	if _, err := f.PutIfMatch("/doc", `{"price":1}`, r1.Witness.Version); err == nil {
		t.Fatal("stale If-Match accepted")
	}
	// Weak validator refused for mutation (428, never treated as match).
	if _, err := f.PutIfMatch("/doc", `{"price":1}`, "W/\"weak\""); err == nil {
		t.Fatal("weak If-Match accepted")
	}
	// Independent resource untouched.
	if _, err := f.GetWitnessed("/cfg"); err != nil {
		t.Fatal(err)
	}
}
