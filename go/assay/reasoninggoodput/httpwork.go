package reasoninggoodput

import (
	"strconv"
	"strings"
)

// httpwork.go: Phase 6 HTTP_WORKLOAD scenarios. Workers read versioned JSON
// resources, run slices, and propose conditional mutations. Oracles: serial
// reference outcome, fixture invariants, no lost updates, no acceptance
// against invalid required premises.

// HTTPJob builds a JobSpec over fixture paths with FIELD-LEVEL reads: each
// named field becomes one indexed Val carrying an http:path#field premise,
// so validation distinguishes used fields from irrelevant ones. Slices
// reference "read:<i>" over the flattened field list.
func HTTPJob(f *HTTPFixture, id string, reads []string, fields map[string][]string,
	slices []SliceBuilder, propose func(outs map[string][]byte, vals map[string]Val, reads []Val) (path, body string)) JobSpec {
	spec := JobSpec{ID: id}
	var baseETags map[string]string
	// flatFields fixes the read index order deterministically.
	var flatFields [][2]string
	for _, p := range reads {
		for _, fld := range fields[p] {
			flatFields = append(flatFields, [2]string{p, fld})
		}
	}
	spec.Reads = func() ([]Val, error) {
		baseETags = map[string]string{}
		var out []Val
		for _, ff := range flatFields {
			p, fld := ff[0], ff[1]
			r, err := f.GetWitnessed(p)
			if err != nil {
				return nil, err
			}
			baseETags[p] = r.Witness.Version
			sel, err := SelectJSON(Read(r), "http:"+p, r.Witness.Version, fld)
			if err != nil {
				return nil, err
			}
			out = append(out, sel[fld])
		}
		return out, nil
	}
	spec.Slices = slices
	spec.Propose = func(outs map[string][]byte, vals map[string]Val, reads []Val) MutationCandidate {
		path, body := propose(outs, vals, reads)
		var premises PremiseSet
		for _, v := range vals {
			premises = append(premises, v.Premises...)
		}
		var sliceIDs []string
		for _, sb := range slices {
			sliceIDs = append(sliceIDs, sb.ID)
		}
		return MutationCandidate{Op: "http-put:" + path, Args: []byte(body),
			Premises: premises.Normalize(), Slices: sliceIDs,
			Touched: []string{"http:" + path}}
	}
	spec.Commit = func(m MutationCandidate, conditional bool) (bool, error) {
		path := m.Op[len("http-put:"):]
		if !conditional {
			// Blind apply (T1): unconditional write, then post-validate.
			newETag, err := f.PutUnconditional(path, string(m.Args))
			if err != nil {
				return false, err
			}
			baseETags[path] = newETag
			return true, nil
		}
		etag, ok := baseETags[path]
		if !ok {
			cur, err := f.CurrentETag(path)
			if err != nil {
				return false, err
			}
			etag = cur
		}
		newETag, err := f.PutIfMatch(path, string(m.Args), etag)
		if err != nil {
			return false, err
		}
		baseETags[path] = newETag
		return true, nil
	}
	spec.Compensate = func(m MutationCandidate) error {
		// Assay affordance: restore is not part of HTTP semantics; the
		// treatment records the cost and retries from fresh reads. The
		// blind write stands (last-writer-wins is T1's honest outcome);
		// correctness is judged by the oracle, not by cleanup.
		return nil
	}
	spec.Live = func() map[string]StateWitness {
		out := map[string]StateWitness{}
		for _, p := range reads {
			etag, err := f.CurrentETag(p)
			if err != nil {
				continue
			}
			out["http:"+p] = StateWitness{Ref: ResourceRef{Authority: "http", ID: p}, Version: etag}
			for _, fld := range fields[p] {
				out["http:"+p+"#"+fld] = StateWitness{Ref: ResourceRef{Authority: "http", ID: p}, Version: etag}
			}
		}
		return out
	}
	spec.OracleVerify = func() error { return nil }
	spec.SliceInputs = map[string][]string{}
	return spec
}

// HTTPScenario is one frozen HTTP workload case.
type HTTPScenario struct {
	Name     string
	Reads    []string
	Fields   map[string][]string
	Slices   []SliceBuilder
	Propose  func(map[string][]byte, map[string]Val, []Val) (string, string)
	Schedule []Change
}

// HTTPScaffold builds the six frozen scenarios (work = slice cost).
func HTTPScaffold(work WorkSpec) []HTTPScenario {
	priceSlices := func(from ...string) []SliceBuilder {
		return []SliceBuilder{{
			ID: "analyze", From: from, Work: work,
			Make: func(ins [][]byte) []byte {
				return []byte("a:" + DigestBytes(joinAll(ins))[:8])
			},
		}}
	}
	mkFields := func(docFields ...string) map[string][]string {
		return map[string][]string{"/doc": docFields}
	}
	proposePrice := func(outs map[string][]byte, _ map[string]Val, _ []Val) (string, string) {
		return "/doc", `{"price":150,"qty":2,"note":"keep","other":{"x":1}}`
	}
	return []HTTPScenario{
		{
			Name: "irrelevant-field-change", Reads: []string{"/doc"},
			Fields: mkFields("price"), Slices: priceSlices("read:0"),
			Propose: proposePrice,
			Schedule: []Change{
				{Target: "/doc", NewBody: `{"price":100,"qty":2,"note":"changed","other":{"x":1}}`, AfterPhase: "reason"},
			},
		},
		{
			Name: "used-field-change", Reads: []string{"/doc"},
			Fields: mkFields("price"), Slices: priceSlices("read:0"),
			Propose: proposePrice,
			Schedule: []Change{
				{Target: "/doc", NewBody: `{"price":200,"qty":2,"note":"keep","other":{"x":1}}`, AfterPhase: "reason"},
			},
		},
		{
			Name: "independent-resource", Reads: []string{"/doc"},
			Fields: mkFields("price"), Slices: priceSlices("read:0"),
			Propose: proposePrice,
			Schedule: []Change{
				{Target: "/cfg", NewBody: `{"limit":9}`, AfterPhase: "reason"},
			},
		},
		{
			Name: "two-independent-fields", Reads: []string{"/doc"},
			Fields: mkFields("price", "note"),
			Slices: []SliceBuilder{
				{ID: "s-price", From: []string{"read:0"}, Work: work, Make: func(ins [][]byte) []byte {
					return []byte("p:" + DigestBytes(ins[0])[:8])
				}},
				{ID: "s-note", From: []string{"read:1"}, Work: work, Make: func(ins [][]byte) []byte {
					return []byte("n:" + DigestBytes(ins[0])[:8])
				}},
			},
			Propose: func(outs map[string][]byte, _ map[string]Val, _ []Val) (string, string) {
				return "/doc", `{"price":150,"qty":2,"note":"updated","other":{"x":1}}`
			},
			Schedule: []Change{
				{Target: "/doc", NewBody: `{"price":100,"qty":2,"note":"changed-externally","other":{"x":1}}`, AfterPhase: "reason"},
			},
		},
		{
			Name: "invariant-shared-value", Reads: []string{"/doc"},
			Fields: mkFields("price"),
			Slices: []SliceBuilder{{
				ID: "bump", From: []string{"read:0"}, Work: work,
				Make: func(ins [][]byte) []byte {
					// State-dependent proposal: read the observed price and
					// bid +50. A stale read therefore bids stale value.
					p, err := strconv.Atoi(strings.TrimSpace(string(ins[0])))
					if err != nil {
						return []byte("bad-price")
					}
					return []byte(strconv.Itoa(p + 50))
				},
			}},
			Propose: func(outs map[string][]byte, _ map[string]Val, reads []Val) (string, string) {
				// qty/note/other are fixture constants in this scenario
				// (the frozen schedule moves price only); the bid derives
				// from the witnessed price read (+50). Slice outputs carry
				// only work-proof bytes, never semantic content.
				p, err := strconv.Atoi(strings.TrimSpace(string(reads[0].Bytes)))
				if err != nil {
					return "/doc", `{"price":"bad-price","qty":2,"note":"keep","other":{"x":1}}`
				}
				return "/doc", `{"price":` + strconv.Itoa(p+50) + `,"qty":2,"note":"keep","other":{"x":1}}`
			},
			Schedule: []Change{
				{Target: "/doc", NewBody: `{"price":175,"qty":2,"note":"keep","other":{"x":1}}`, AfterPhase: "reason"},
			},
		},
		{
			Name: "opaque-multi-field", Reads: []string{"/doc"},
			Fields: mkFields("price", "qty", "note"),
			Slices: []SliceBuilder{{
				ID: "opaque", From: []string{"read:0", "read:1", "read:2"}, Work: work, Opaque: true,
				Make: func(ins [][]byte) []byte {
					return []byte("opaque:" + DigestBytes(joinAll(ins))[:8])
				},
			}},
			Propose: proposePrice,
			Schedule: []Change{
				{Target: "/doc", NewBody: `{"price":100,"qty":2,"note":"changed","other":{"x":1}}`, AfterPhase: "reason"},
			},
		},
		{
			// Extension scenario (same rationale as git/unconsumed-file):
			// four fields read, reasoning consumes one.
			Name: "unconsumed-context", Reads: []string{"/doc"},
			Fields:  mkFields("price", "note", "other", "qty"),
			Slices:  priceSlices("read:0"),
			Propose: proposePrice,
			Schedule: []Change{
				{Target: "/doc", NewBody: `{"price":100,"qty":2,"note":"keep","other":{"x":999}}`, AfterPhase: "reason"},
			},
		},
	}
}
