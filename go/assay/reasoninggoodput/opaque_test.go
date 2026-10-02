package reasoninggoodput

import (
	"testing"
)

// opaque_test.go: Phase 10 — the opaque-model-boundary problem, tested
// directly. Three pipelines over the same broad context:
// structured (narrow slices only), broad (one opaque call over everything),
// mixed (deterministic narrowing slices feed one opaque call over the
// narrowed value). Question: does the mixed pipeline recover selectivity
// without workload-specific hand engineering?

func opaquePipelines(t *testing.T, work WorkSpec, narrowFirst bool, opaqueInputs int) (JobSpec, *HTTPFixture, []Change, func(Change)) {
	t.Helper()
	f := NewHTTPFixture(map[string]string{
		"/doc": `{"price":100,"qty":2,"note":"keep","other":{"x":1}}`,
	})
	t.Cleanup(f.Close)
	fields := []string{"price", "qty", "note"}
	reads := []string{"/doc"}
	var slices []SliceBuilder
	if !narrowFirst {
		// Broad: two chained opaque calls over the full context (successive
		// opaque reasoning steps, the agent-like shape). Any field change
		// invalidates everything downstream.
		opq := func(id string, from []string) SliceBuilder {
			return SliceBuilder{
				ID: id, From: from, Work: work, Opaque: true,
				Make: func(ins [][]byte) []byte {
					return []byte("opaque:" + DigestBytes(joinAll(ins))[:8])
				},
			}
		}
		slices = []SliceBuilder{
			opq("opaque1", []string{"read:0", "read:1", "read:2"}),
			opq("opaque2", []string{"opaque1"}),
		}
	} else {
		// Mixed: deterministic narrow slice extracts price, opaque call
		// consumes ONLY the narrowed value (single input).
		slices = []SliceBuilder{
			{ID: "narrow", From: []string{"read:0"}, Work: work,
				Make: func(ins [][]byte) []byte {
					return append([]byte("narrowed:"), ins[0]...)
				}},
			{ID: "opaque", From: []string{"narrow"}, Work: work, Opaque: true,
				Make: func(ins [][]byte) []byte {
					return []byte("opaque:" + DigestBytes(joinAll(ins))[:8])
				}},
		}
		_ = opaqueInputs
	}
	spec := HTTPJob(f, "opaque-pipe", reads, map[string][]string{"/doc": fields},
		slices,
		func(outs map[string][]byte, _ map[string]Val, _ []Val) (string, string) {
			return "/doc", `{"price":150,"qty":2,"note":"keep","other":{"x":1}}`
		})
	sched := []Change{
		{Target: "/doc", NewBody: `{"price":100,"qty":2,"note":"changed","other":{"x":1}}`, AfterPhase: "reason"},
	}
	applier := func(ch Change) {
		_, _ = f.PutUnconditional(ch.Target, ch.NewBody)
	}
	return spec, f, sched, applier
}

func TestOpaquePipelines(t *testing.T) {
	work := WorkSpec{BurnRounds: 400, Label: "analyze"}
	// Broad opaque: union invalidates → full replay (T4 == T2).
	specB, _, schedB, appB := opaquePipelines(t, work, false, 3)
	mB := Run(specB, T4Replay, schedB, appB, 3)
	// Mixed: narrowing slice reuses (price unchanged → same narrowed bytes
	// → same opaque key); opaque slice reuses too. Only the narrow slice's
	// premise check runs against the moved etag... price premise STALE
	// (shared coarse etag!) → narrow re-executes; opaque key: inputs =
	// narrowed bytes. Narrow output on unchanged price bytes = identical
	// bytes → opaque key IDENTICAL → premise check on narrowed value's
	// premises (price@v2 vs live v2 → valid) → REUSE.
	specM, _, schedM, appM := opaquePipelines(t, work, true, 1)
	mM := Run(specM, T4Replay, schedM, appM, 3)
	t.Logf("broad: exec=%d retries=%d | mixed: exec=%d retries=%d",
		mB.WorkExec, mB.Retries, mM.WorkExec, mM.Retries)
	if mB.WorkExec <= mM.WorkExec {
		t.Fatalf("mixed pipeline should preserve more than broad: broad=%d mixed=%d",
			mB.WorkExec, mM.WorkExec)
	}
	if mM.MissedConf || !mM.OracleOK {
		t.Fatalf("mixed pipeline unsafe: %+v", mM)
	}
}
