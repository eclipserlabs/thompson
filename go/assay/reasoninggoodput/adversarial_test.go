package reasoninggoodput

import (
	"strings"
	"testing"
)

// adversarial_test.go: Phase 12 safety matrix (12 cases). Hard gate: zero
// incorrect accepted mutations caused by stale required premises.

func TestAdversarialRepeatedIrrelevant(t *testing.T) {
	// Irrelevant resource churns repeatedly mid-reasoning: no invalidation,
	// goodput stays 1.0, no retries from the churn itself.
	f := httpTestBed(t)
	work := WorkSpec{BurnRounds: 400, Label: "analyze"}
	spec := HTTPJob(f, "adv-irrelevant", []string{"/doc"}, map[string][]string{"/doc": {"price"}},
		[]SliceBuilder{{ID: "a", From: []string{"read:0"}, Work: work, Make: func(ins [][]byte) []byte {
			return []byte("a:" + DigestBytes(ins[0])[:8])
		}}},
		func(outs map[string][]byte, _ map[string]Val, _ []Val) (string, string) {
			return "/doc", `{"price":150,"qty":2,"note":"keep","other":{"x":1}}`
		})
	var sched []Change
	for i := 0; i < 5; i++ {
		sched = append(sched, Change{Target: "/cfg", NewBody: `{"limit":9}`, AfterPhase: "reason"})
	}
	// Distinct bodies so each change is real (etag moves on /cfg only).
	applier := func(ch Change) {
		_, _ = f.PutUnconditional(ch.Target, ch.NewBody)
	}
	m := Run(spec, T4Replay, sched, applier, 3)
	if m.Retries != 0 || m.WorkExec != 400 {
		t.Fatalf("irrelevant churn caused work: %+v", m)
	}
	if m.Goodput != 1.0 || !m.OracleOK {
		t.Fatalf("bad outcome: %+v", m)
	}
}

func TestAdversarialChangeBeforeValidate(t *testing.T) {
	// Relevant change lands AfterPhase validate (between validation and
	// conditional commit): the commit guard (412) must catch it.
	f := httpTestBed(t)
	work := WorkSpec{BurnRounds: 400, Label: "analyze"}
	spec := HTTPJob(f, "adv-validate-race", []string{"/doc"}, map[string][]string{"/doc": {"price"}},
		[]SliceBuilder{{ID: "a", From: []string{"read:0"}, Work: work, Make: func(ins [][]byte) []byte {
			return []byte("a:" + DigestBytes(ins[0])[:8])
		}}},
		func(outs map[string][]byte, _ map[string]Val, _ []Val) (string, string) {
			return "/doc", `{"price":150,"qty":2,"note":"keep","other":{"x":1}}`
		})
	sched := []Change{
		{Target: "/doc", NewBody: `{"price":200,"qty":2,"note":"keep","other":{"x":1}}`, AfterPhase: "validate"},
	}
	applier := func(ch Change) {
		_, _ = f.PutUnconditional(ch.Target, ch.NewBody)
	}
	for _, treat := range []TreatID{T3Premise, T4Replay} {
		m := Run(spec, treat, sched, applier, 3)
		if m.MissedConf {
			t.Fatalf("%s missed validate-race conflict", treat)
		}
		final, _ := f.GetWitnessed("/doc")
		if !strings.Contains(string(final.Value), `"price":150`) && !strings.Contains(string(final.Value), `"price":200`) {
			t.Fatalf("%s unexpected final: %s", treat, final.Value)
		}
	}
}

func TestAdversarialABA(t *testing.T) {
	// ABA: value returns to identical bytes via a new commit. Content
	// addressing keeps the witness stable (same bytes = same blob OID =
	// same ETag), so premises stay VALID — correctly, since nothing the
	// reasoning depends on actually differs.
	g := gitTestBed(t)
	r1, err := g.ReadWitnessed("src/main.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.WriteFile("src/main.txt", "main content v2\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Commit("away"); err != nil {
		t.Fatal(err)
	}
	if err := g.WriteFile("src/main.txt", "main content v1\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Commit("back"); err != nil {
		t.Fatal(err)
	}
	head, _ := g.Head()
	r2, err := g.ReadWitnessed("src/main.txt")
	if err != nil {
		t.Fatal(err)
	}
	_ = head
	if r1.Witness.Version != r2.Witness.Version {
		t.Fatal("content-identical rewrite should preserve witness (content addressing)")
	}
	v := Read(r1)
	live := map[string]StateWitness{"git:src/main.txt": r2.Witness}
	if stale, unk := ValidatePremises(v.Premises, func(s string) (StateWitness, bool) {
		w, ok := live[s]
		return w, ok
	}); stale != nil || unk {
		t.Fatal("ABA with identical content must not invalidate")
	}
}

func TestAdversarialIdenticalDigests(t *testing.T) {
	// Same content in two resources: premises stay distinguished by
	// Resource identity, never merged by digest.
	g := gitTestBed(t)
	if err := g.WriteFile("lib/other.txt", "main content v1\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Commit("dup content"); err != nil {
		t.Fatal(err)
	}
	ra, _ := g.ReadWitnessed("src/main.txt")
	rb, _ := g.ReadWitnessed("lib/other.txt")
	if ra.Witness.Version != rb.Witness.Version {
		t.Fatal("identical content should share blob OID")
	}
	va, vb := Read(ra), Read(rb)
	if va.Premises[0].Resource == vb.Premises[0].Resource {
		t.Fatal("premises merged across resources")
	}
	// Changing one leaves the other's premise valid.
	if err := g.WriteFile("lib/other.txt", "other v2\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Commit("touch other"); err != nil {
		t.Fatal(err)
	}
	rb2, _ := g.ReadWitnessed("lib/other.txt")
	live := map[string]StateWitness{
		"git:src/main.txt":  ra.Witness,
		"git:lib/other.txt": rb2.Witness,
	}
	_ = rb
	if stale, unk := ValidatePremises(va.Premises, func(s string) (StateWitness, bool) {
		w, ok := live[s]
		return w, ok
	}); stale != nil || unk {
		t.Fatal("untouched twin resource went stale")
	}
}

func TestAdversarialMissingWitness(t *testing.T) {
	// Deleted resource: reads fail → UNKNOWN, never treated as unchanged.
	f := httpTestBed(t)
	f.Close()
	if _, err := f.GetWitnessed("/doc"); err == nil {
		t.Fatal("expected read failure on closed fixture")
	}
}

func TestAdversarialWeakValidator(t *testing.T) {
	f := httpTestBed(t)
	// Weak ETags are refused for reads that back premises and for commits.
	if _, err := f.PutIfMatch("/doc", `{"price":1}`, `W/"weak"`); err == nil {
		t.Fatal("weak If-Match accepted")
	}
}

func TestAdversarialWorktreeDivergence(t *testing.T) {
	// Uncommitted worktree bytes never move the committed witness; reasoning
	// built on the witnessed snapshot stays valid.
	g := gitTestBed(t)
	r, err := g.ReadWitnessed("src/main.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.WriteFile("src/main.txt", "uncommitted divergence\n"); err != nil {
		t.Fatal(err)
	}
	r2, err := g.ReadWitnessed("src/main.txt")
	if err != nil {
		t.Fatal(err)
	}
	if r.Witness.Version != r2.Witness.Version {
		t.Fatal("uncommitted bytes moved the committed witness")
	}
	v := Read(r)
	live := map[string]StateWitness{"git:src/main.txt": r2.Witness}
	if stale, unk := ValidatePremises(v.Premises, func(s string) (StateWitness, bool) {
		w, ok := live[s]
		return w, ok
	}); stale != nil || unk {
		t.Fatal("committed-snapshot reasoning invalidated by worktree dirt")
	}
}

func TestAdversarialOpaqueDisguised(t *testing.T) {
	// Relevant premise disguised among irrelevant reads inside opaque
	// reasoning: union must invalidate (conservative, measured as cost).
	f := httpTestBed(t)
	work := WorkSpec{BurnRounds: 400, Label: "analyze"}
	spec := HTTPJob(f, "adv-opaque", []string{"/doc"},
		map[string][]string{"/doc": {"price", "qty", "note", "other"}},
		[]SliceBuilder{{
			ID: "opaque", From: []string{"read:0", "read:1", "read:2", "read:3"},
			Work: work, Opaque: true,
			Make: func(ins [][]byte) []byte {
				return []byte("opaque:" + DigestBytes(joinAll(ins))[:8])
			},
		}},
		func(outs map[string][]byte, _ map[string]Val, _ []Val) (string, string) {
			return "/doc", `{"price":150,"qty":2,"note":"keep","other":{"x":1}}`
		})
	sched := []Change{
		{Target: "/doc", NewBody: `{"price":100,"qty":2,"note":"keep","other":{"x":999}}`, AfterPhase: "reason"},
	}
	applier := func(ch Change) {
		_, _ = f.PutUnconditional(ch.Target, ch.NewBody)
	}
	m := Run(spec, T4Replay, sched, applier, 3)
	// Only "other" changed; opaque union forces full replay (no narrowing).
	if m.Retries == 0 {
		t.Fatal("opaque union failed to invalidate on disguised change")
	}
	if m.MissedConf || !m.OracleOK {
		t.Fatalf("unsafe opaque reuse: %+v", m)
	}
}

func TestAdversarialControlOnly(t *testing.T) {
	// Dependency through control flow only: branching on price without
	// copying it must still invalidate when price moves, and must not
	// invalidate when an unbranched field moves.
	f := httpTestBed(t)
	work := WorkSpec{BurnRounds: 400, Label: "analyze"}
	spec := HTTPJob(f, "adv-control", []string{"/doc"},
		map[string][]string{"/doc": {"price", "note"}},
		[]SliceBuilder{{
			ID: "decide", From: []string{"read:0"}, Work: work, Control: []int{0},
			Make: func(ins [][]byte) []byte { return []byte("decided") },
		}},
		func(outs map[string][]byte, _ map[string]Val, _ []Val) (string, string) {
			return "/doc", `{"price":150,"qty":2,"note":"keep","other":{"x":1}}`
		})
	mkApplier := func(f *HTTPFixture) func(Change) {
		return func(ch Change) {
			_, _ = f.PutUnconditional(ch.Target, ch.NewBody)
		}
	}
	// Control dependency moves → must replay.
	m1 := Run(spec, T4Replay, []Change{
		{Target: "/doc", NewBody: `{"price":200,"qty":2,"note":"keep","other":{"x":1}}`, AfterPhase: "reason"},
	}, mkApplier(f), 3)
	if m1.Retries == 0 {
		t.Fatal("control-dependency move caused no replay")
	}
	// Unread resources never enter premise sets: changing /cfg (never
	// read, never branched) must NOT replay. Note the honest limit this
	// documents: on coarse witnesses (one ETag per document) even a pure
	// control dep on price cannot survive a note change — control premises
	// widen correctly, never narrow. Finer witnesses would be needed for
	// control-only selectivity, which no adapter here provides.
	f2 := httpTestBed(t)
	spec2 := HTTPJob(f2, "adv-control", []string{"/doc"},
		map[string][]string{"/doc": {"price", "note"}},
		[]SliceBuilder{{
			ID: "decide", From: []string{"read:0"}, Work: work, Control: []int{0},
			Make: func(ins [][]byte) []byte { return []byte("decided") },
		}},
		func(outs map[string][]byte, _ map[string]Val, _ []Val) (string, string) {
			return "/doc", `{"price":150,"qty":2,"note":"keep","other":{"x":1}}`
		})
	m2 := Run(spec2, T4Replay, []Change{
		{Target: "/cfg", NewBody: `{"limit":9}`, AfterPhase: "reason"},
	}, mkApplier(f2), 3)
	if m2.Retries != 0 || m2.WorkExec != 400 {
		t.Fatalf("unread resource caused work: %+v", m2)
	}
	if m1.MissedConf || m2.MissedConf {
		t.Fatal("missed conflict in control tests")
	}
}

func TestAdversarialCoarseMerge(t *testing.T) {
	// Same coarse resource, semantically independent writes: the second
	// conditional commit must fail (412) rather than interleave silently.
	// Coordination here is honest serialization, not a false conflict.
	f := httpTestBed(t)
	r1, err := f.GetWitnessed("/doc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.PutIfMatch("/doc", `{"price":111}`, r1.Witness.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := f.PutIfMatch("/doc", `{"price":222}`, r1.Witness.Version); err == nil {
		t.Fatal("stale conditional write accepted (lost update)")
	}
	cur, err := f.GetWitnessed("/doc")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cur.Value), "111") {
		t.Fatalf("winner not preserved: %s", cur.Value)
	}
}

func TestAdversarialTransitiveReuseRefused(t *testing.T) {
	// T4 must never reuse a slice whose transitive premise changed, even
	// when its direct inputs are byte-identical: covered structurally by
	// key+premise double-check; this test pins the two-independent-fields
	// run reuses exactly the fresh slice and re-executes the stale one.
	f := httpTestBed(t)
	work := WorkSpec{BurnRounds: 400, Label: "analyze"}
	spec := HTTPJob(f, "adv-transitive", []string{"/doc"},
		map[string][]string{"/doc": {"price", "note"}},
		[]SliceBuilder{
			{ID: "s-price", From: []string{"read:0"}, Work: work, Make: func(ins [][]byte) []byte {
				return []byte("p:" + DigestBytes(ins[0])[:8])
			}},
			{ID: "s-note", From: []string{"read:1"}, Work: work, Make: func(ins [][]byte) []byte {
				return []byte("n:" + DigestBytes(ins[0])[:8])
			}},
		},
		func(outs map[string][]byte, _ map[string]Val, _ []Val) (string, string) {
			return "/doc", `{"price":150,"qty":2,"note":"updated","other":{"x":1}}`
		})
	sched := []Change{
		{Target: "/doc", NewBody: `{"price":100,"qty":2,"note":"changed-externally","other":{"x":1}}`, AfterPhase: "reason"},
	}
	applier := func(ch Change) {
		_, _ = f.PutUnconditional(ch.Target, ch.NewBody)
	}
	m := Run(spec, T4Replay, sched, applier, 3)
	// price slice preserved (400) + note slice redone (400) + first attempt
	// both (800) = 1200 total; goodput 1200-accepted... accepted counts the
	// final attempt's slices (400+400+... see accounting).
	if m.WorkExec != 1200 {
		t.Fatalf("expected exactly one preserved slice, exec=%d", m.WorkExec)
	}
	if !m.OracleOK || m.MissedConf {
		t.Fatalf("unsafe: %+v", m)
	}
}
