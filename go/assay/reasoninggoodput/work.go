package reasoninggoodput

import (
	"time"
)

// work.go: agent-like expensive work without fake sleeps. Deterministic
// CPU-expensive transformations over witnessed bytes; exact logical work
// units recorded alongside wall time; parameterized cost regimes (~1x/10x/
// 100x vs validation overhead). Same inputs deterministically produce the
// same output (asserted). Synthetic CPU work is NOT evidence of model-market
// demand — only a controlled assay for abort/replay economics.

// WorkSpec parameterizes one reasoning slice's cost.
type WorkSpec struct {
	// BurnRounds scales CPU: 1x ≈ validation-scale, 10x/100x expensive.
	BurnRounds int
	// Label names the slice kind (metrics only, never identity).
	Label string
}

// ExpensiveTransform runs burnRounds of hashing over input (deterministic).
// Returns output bytes and exact work units (rounds executed).
func ExpensiveTransform(data []byte, spec WorkSpec) ([]byte, int64) {
	t0 := time.Now()
	out := WorkHash(data, spec.BurnRounds)
	_ = t0
	return out, int64(spec.BurnRounds)
}

// SliceSpec declares one reasoning slice: inputs, cost, output derivation.
type SliceSpec struct {
	ID     string
	Inputs []Val
	Work   WorkSpec
	Make   func(inputs [][]byte) []byte // deterministic derivation
}

// RunSlice executes the slice, binding output to input premises.
func RunSlice(spec SliceSpec) (ReasoningSlice, time.Duration) {
	t0 := time.Now()
	ins := make([][]byte, len(spec.Inputs))
	for i, v := range spec.Inputs {
		ins[i] = v.Bytes
	}
	made := spec.Make(ins)
	out, units := ExpensiveTransform(made, spec.Work)
	dt := time.Since(t0)
	joined := Transform(out, spec.Inputs...)
	return ReasoningSlice{
		ID:        spec.ID,
		Premises:  joined.Premises,
		WorkUnits: units,
		Output:    out,
	}, dt
}

// ValidatePremises checks every premise against live witnesses:
// same resource, same authoritative version → covered; moved → stale;
// unresolvable → UNKNOWN (fail-closed). Returns stale premise or nil.
// Callers build the live map keyed by premise Resource identity.
func ValidatePremises(need PremiseSet, live func(resource string) (StateWitness, bool)) (stale *Premise, unknown bool) {
	for _, q := range need {
		w, ok := live(q.Resource)
		if !ok {
			return nil, true
		}
		if w.Version == "" || w.Version != q.Witness {
			c := q
			return &c, false
		}
	}
	return nil, false
}
