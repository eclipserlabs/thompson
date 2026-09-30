package reasoninggoodput

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// premise.go: mechanical premise binder. Dependencies derive from actual
// access and dataflow through assay value wrappers — workers never hand-list
// premises, no LLM is asked what mattered, and incomplete provenance widens
// to UNKNOWN rather than silently omitting.

// Val is bytes carrying mechanically derived premises.
type Val struct {
	Bytes    []byte
	Premises PremiseSet
	// Opaque marks transit through an opaque reasoning boundary.
	Opaque bool
	// Unknown marks unclassifiable provenance (fail-closed downstream).
	Unknown bool
	// Parents carries the immediate input Vals (read or slice outputs) that
	// produced this value. The T4 reuse path revalidates premises against
	// these current inputs so a cache hit can never smuggle a stale witness
	// past validation when inputs match but the world moved.
	Parents []Val
}

// Read lifts a witnessed read into a value (premise = resource@witness).
func Read(r WitnessedRead) Val {
	id := r.Witness.Ref.Authority + ":" + r.Witness.Ref.ID
	if r.Path != "" && r.Path != r.Witness.Ref.ID {
		id += "#" + r.Path
	}
	return Val{Bytes: append([]byte(nil), r.Value...), Premises: PremiseSet{{
		Resource: id, Witness: r.Witness.Version, Provenance: provenanceFor(r),
	}}.Normalize()}
}

func provenanceFor(r WitnessedRead) string {
	if r.Witness.Ref.Authority == "git" {
		return "git"
	}
	if r.Path != "" {
		return "field"
	}
	return "direct"
}

// Transform applies a deterministic function, unioning input premises
// (DIRECT_VALUE_FLOW). Opaque/Unknown flags propagate (never clear).
// Parents records the immediate inputs for premise-freshness checks.
func Transform(out []byte, ins ...Val) Val {
	var p PremiseSet
	opaque, unknown := false, false
	for _, in := range ins {
		p = append(p, in.Premises...)
		opaque = opaque || in.Opaque
		unknown = unknown || in.Unknown
	}
	return Val{Bytes: out, Premises: p.Normalize(), Opaque: opaque, Unknown: unknown, Parents: append([]Val(nil), ins...)}
}

// SelectJSON extracts exact dotted paths from a JSON document, recording
// STRUCTURED_FIELD_ACCESS premises per path (resource#path@witness).
// Unparseable input or missing paths yield Unknown (fail-closed widening
// happens at premise-set comparison, never silent omission here — the value
// carries what WAS provably accessed).
func SelectJSON(doc Val, resource string, witness string, paths ...string) (map[string]Val, error) {
	var raw interface{}
	if err := json.Unmarshal(doc.Bytes, &raw); err != nil {
		return nil, fmt.Errorf("select: unparseable: %w", err)
	}
	out := map[string]Val{}
	for _, path := range paths {
		v, ok := jsonPath(raw, strings.Split(path, "."))
		if !ok {
			return nil, fmt.Errorf("select: missing path %q", path)
		}
		b, _ := json.Marshal(v)
		out[path] = Val{Bytes: b, Premises: PremiseSet{{
			Resource: resource + "#" + path, Witness: witness, Provenance: "field",
		}}.Normalize(), Opaque: doc.Opaque, Unknown: doc.Unknown}
	}
	return out, nil
}

func jsonPath(raw interface{}, parts []string) (interface{}, bool) {
	cur := raw
	for _, p := range parts {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil, false
		}
		cur, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// Branch records a CONTROL dependency: the result depends on cond having
// taken this path, so cond's premises join as control premises. A copied
// value establishes data dependence; Branch is how control dependence enters
// mechanically (workers call it at every data-dependent branch).
func Branch(cond Val, out []byte) Val {
	var p PremiseSet
	for _, q := range cond.Premises {
		q.Provenance = "control"
		p = append(p, q)
	}
	return Val{Bytes: out, Premises: p.Normalize(), Opaque: cond.Opaque, Unknown: cond.Unknown}
}

// OpaqueBoundary runs fn over inputs whose internals are not mechanically
// visible (OPAQUE_REASONING_BOUNDARY): the output conservatively inherits
// the UNION of all input premises, marked opaque. A smaller set is never
// claimed unless proven — and this binder proves nothing smaller.
func OpaqueBoundary(out []byte, ins ...Val) Val {
	v := Transform(out, ins...)
	v.Opaque = true
	var p PremiseSet
	for _, q := range v.Premises {
		q.Provenance = "opaque"
		p = append(p, q)
	}
	v.Premises = p.Normalize()
	return v
}

// CurrentPremisesOf rebuilds the premise set a value WOULD carry if derived
// from the given current inputs, without executing anything. The T4 reuse
// path compares this against live witnesses so validation always reflects
// the present world, never metadata stored at production time.
func CurrentPremisesOf(inVals []Val, opaque bool) (PremiseSet, error) {
	probe := Transform(nil, inVals...)
	if probe.Unknown {
		return nil, ErrUnknown
	}
	if opaque {
		var p PremiseSet
		for _, q := range probe.Premises {
			q.Provenance = "opaque"
			p = append(p, q)
		}
		return p.Normalize(), nil
	}
	return probe.Premises.Normalize(), nil
}

// UnknownVal marks a value whose provenance cannot be established.
func UnknownVal(out []byte) Val {
	return Val{Bytes: out, Unknown: true}
}

// PremisesOf returns the normalized premise set, or an error when the value
// is UNKNOWN (callers must fall back conservatively).
func PremisesOf(v Val) (PremiseSet, error) {
	if v.Unknown {
		return nil, ErrUnknown
	}
	return v.Premises.Normalize(), nil
}

// WorkHash is the deterministic expensive-transform helper used by the work
// model: repeated hashing with output bound to inputs (slices record the
// digest; determinism asserted in tests).
func WorkHash(data []byte, rounds int) []byte {
	out := sha256.Sum256(data)
	for i := 1; i < rounds; i++ {
		out = sha256.Sum256(append(out[:], byte(i), byte(i>>8)))
	}
	return out[:]
}
