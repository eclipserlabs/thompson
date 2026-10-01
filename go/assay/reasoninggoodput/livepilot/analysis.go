// Analysis: cumulative-union premises and L1/L2/L3 counterfactual costing.
//
// Premise model (frozen): each model invocation (stream step) sees all
// conversation history, so slice N's premises = union of all witnessed
// reads in steps 1..N. This is conservative (never claims less than the
// harness demonstrably showed) and never uses hidden reasoning. A slice is
// narrow iff earlier steps hadn't yet read what later steps did — early
// slices are naturally selective; late slices are broad. SARF measures how
// much expensive work sits in narrow slices.
package livepilot

import (
	"sort"
	"strings"
)

// CallSlice is one model invocation with mechanical premises.
type CallSlice struct {
	Index    int
	Premises map[string]string // resource -> witness digest/oid
	CostNS   int64             // wall time from part timestamps (fallback units)
	HasTools bool
}

// SegmentSteps groups stream parts into model invocations: a new slice
// starts at each step-start; text/tool parts attach to the open slice.
// readPaths maps tool-call parts to witnessed file paths (parser v1:
// exact "path"/"file"/"filePath" input fields ending in known suffixes).
func SegmentSteps(s *Session, witnessOf func(path string) (witness string, ok bool)) []CallSlice {
	var out []CallSlice
	cur := -1
	seen := map[string]map[string]string{} // slice idx -> resource->witness
	_ = seen
	for _, p := range s.Parts {
		if p.Type == "step-start" {
			out = append(out, CallSlice{Index: len(out), Premises: map[string]string{}})
			cur++
			continue
		}
		if cur < 0 {
			continue
		}
		if p.Type == "tool-call" {
			out[cur].HasTools = true
			for _, path := range toolPaths(p) {
				if w, ok := witnessOf(path); ok {
					out[cur].Premises[path] = w
				} else {
					out[cur].Premises[path] = "UNKNOWN"
				}
			}
		}
	}
	// Cumulative union: slice N inherits all premises of slices < N.
	// Cost accumulates wall time from part timestamps (parts without
	// timestamps contribute nothing; SARF falls back to unit weights).
	accum := map[string]string{}
	for i := range out {
		for k, v := range out[i].Premises {
			if _, exists := accum[k]; !exists {
				accum[k] = v
			}
		}
		for k, v := range accum {
			out[i].Premises[k] = v
		}
	}
	// Second pass: attribute part wall times to slices by sequence order.
	// (Parts were consumed in order above; recompute slice boundaries.)
	boundaries := []int{}
	for idx, part := range s.Parts {
		if part.Type == "step-start" {
			boundaries = append(boundaries, idx)
		}
	}
	for i := range out {
		start := 0
		if i < len(boundaries) {
			start = boundaries[i]
		}
		end := len(s.Parts)
		if i+1 < len(boundaries) {
			end = boundaries[i+1]
		}
		var cost int64
		for _, part := range s.Parts[start:end] {
			if part.End > part.Stamp {
				cost += part.End - part.Stamp
			}
		}
		out[i].CostNS = cost
	}
	return out
}

// toolPaths extracts candidate file paths from a tool-call part (v0:
// string input fields that look like repo paths; refined after dev runs).
func toolPaths(p Part) []string {
	var out []string
	lower := strings.ToLower(p.InputJSON)
	_ = lower
	// Best-effort: quoted strings ending in code suffixes.
	in := p.InputJSON
	for _, tok := range strings.FieldsFunc(in, func(r rune) bool {
		return r == '"' || r == '\'' || r == ' ' || r == ',' || r == ':' || r == '{' || r == '}' || r == '[' || r == ']'
	}) {
		if (strings.HasSuffix(tok, ".go") || strings.HasSuffix(tok, ".md") || strings.HasSuffix(tok, ".mod")) && !strings.Contains(tok, "\n") && len(tok) < 128 {
			tok = strings.TrimPrefix(tok, "./")
			tok = strings.TrimPrefix(tok, "/")
			out = append(out, tok)
		}
	}
	sort.Strings(out)
	return dedup(out)
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// SARFResult holds the selectivity measurement.
type SARFResult struct {
	SARF          float64
	OpaqueFrac    float64
	MedianPremise int
	TotalReads    int
	UnknownFrac   float64
	ByCall        []float64 // per-call premise count (audit)
}

// SARF computes the selectively-addressable reasoning fraction: cost of
// slices whose premise set is STRICTLY smaller than the run's full context,
// over total slice cost. Cost = per-call wall ns (metadata fallback pending
// dev-run inspection; source recorded per run).
func SARF(calls []CallSlice, fullContext map[string]string) SARFResult {
	var total, narrow, opaqueCost, totalCost int64
	var counts []float64
	unk := 0
	uniq := map[string]bool{}
	for _, c := range calls {
		for k := range c.Premises {
			uniq[k] = true
		}
	}
	for _, c := range calls {
		counts = append(counts, float64(len(c.Premises)))
		w := c.CostNS
		if w <= 0 {
			w = 1
		}
		totalCost += w
		total++
		strict := false
		for k := range fullContext {
			if _, ok := c.Premises[k]; !ok {
				strict = true
				break
			}
		}
		if strict && len(c.Premises) > 0 {
			narrow += w
		}
		// Opaque = premises cover the full run context (nothing narrowed).
		if len(c.Premises) > 0 && len(c.Premises) >= len(fullContext) {
			opaque := true
			for k := range fullContext {
				if _, ok := c.Premises[k]; !ok {
					opaque = false
					break
				}
			}
			if opaque {
				opaqueCost += w
			}
		}
		for _, v := range c.Premises {
			if v == "UNKNOWN" {
				unk++
				break
			}
		}
	}
	r := SARFResult{ByCall: counts, TotalReads: len(uniq)}
	if totalCost > 0 {
		r.SARF = float64(narrow) / float64(totalCost)
		r.OpaqueFrac = float64(opaqueCost) / float64(totalCost)
	}
	if total > 0 {
		r.UnknownFrac = float64(unk) / float64(total)
	}
	sort.Float64s(counts)
	if len(counts) > 0 {
		r.MedianPremise = int(counts[len(counts)/2])
	}
	return r
}

// Counterfactual costs L1/L2/L3 for one contended run given the stale set
// (premise resources whose witnesses moved). All units are the run's own
// cost metric (no invented dollars). With no stale premises all four are
// zero (nothing to discard, nothing to preserve-versus-discard).
func Counterfactual(calls []CallSlice, stale map[string]bool) (l1redo, l2discard, l3discard, l3preserved int64) {
	anyStale := false
	for _, c := range calls {
		for k := range c.Premises {
			if stale[k] {
				anyStale = true
				break
			}
		}
		if anyStale {
			break
		}
	}
	if !anyStale {
		return 0, 0, 0, 0
	}
	for _, c := range calls {
		w := c.CostNS
		if w <= 0 {
			w = 1
		}
		l1redo += w
		l2discard += w
		staleSlice := false
		for k := range c.Premises {
			if stale[k] {
				staleSlice = true
				break
			}
		}
		if staleSlice {
			l3discard += w
		} else {
			l3preserved += w
		}
	}
	return l1redo, l2discard, l3discard, l3preserved
}
