package livepilot

import (
	"os"
	"path/filepath"
	"testing"
)

func writeStream(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "stream.jsonl")
	var body string
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseStream(t *testing.T) {
	p := writeStream(t,
		`{"type":"step_start","timestamp":1,"sessionID":"s1","part":{"type":"step-start"}}`,
		`{"type":"text","timestamp":2,"sessionID":"s1","part":{"type":"text","text":"hello","tool":""}}`,
		`{"type":"x","timestamp":3,"sessionID":"s1","part":{"type":"tool-call","tool":"read","input":{"path":"calc/tax.go"}}}`,
		`{"type":"weird","timestamp":4,"sessionID":"s1","part":{"type":"mystery-box"}}`,
		`not json at all`,
	)
	s, err := ParseStream(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.SessionID != "s1" {
		t.Fatalf("session %q", s.SessionID)
	}
	var kinds []string
	for _, part := range s.Parts {
		kinds = append(kinds, part.Type)
	}
	if len(kinds) != 4 {
		t.Fatalf("parts %v", kinds)
	}
	if len(s.Unknown) != 1 {
		t.Fatalf("unknown %v", s.Unknown)
	}
	reads := s.ReadEvents()
	if len(reads) != 1 || reads[0].Tool != "read" {
		t.Fatalf("reads %v", reads)
	}
	if n := s.ModelCalls(); n != 1 {
		t.Fatalf("model calls %d", n)
	}
}

func TestCumulativePremises(t *testing.T) {
	// Two steps reading a.go then b.go: slice 0 premises {a}, slice 1
	// premises {a,b} (cumulative union — conservative conversational model).
	// An unresolvable path yields an UNKNOWN premise, never a silent drop.
	// Costs come from part timestamps (step spans).
	s := &Session{SessionID: "s", Parts: []Part{
		{Seq: 0, Type: "step-start", Stamp: 100, End: 110},
		{Seq: 1, Type: "tool-call", Tool: "read", InputJSON: `{"path":"a.go"}`, Stamp: 111, End: 120},
		{Seq: 2, Type: "step-start", Stamp: 121, End: 130},
		{Seq: 3, Type: "tool-call", Tool: "read", InputJSON: `{"path":"b.go"}`, Stamp: 131, End: 150},
		{Seq: 4, Type: "tool-call", Tool: "read", InputJSON: `{"path":"/nonexistent/zz.go"}`},
	}}
	wit := func(path string) (string, bool) {
		if path == "a.go" || path == "b.go" {
			return "w1:" + path, true
		}
		return "", false
	}
	calls := SegmentSteps(s, wit)
	if len(calls) != 2 {
		t.Fatalf("slices %d", len(calls))
	}
	if len(calls[0].Premises) != 1 || calls[0].Premises["a.go"] != "w1:a.go" {
		t.Fatalf("slice 0 premises %v", calls[0].Premises)
	}
	if len(calls[1].Premises) != 3 {
		t.Fatalf("slice 1 premises %v (want a+b+unknown)", calls[1].Premises)
	}
	if calls[1].Premises["b.go"] != "w1:b.go" {
		t.Fatalf("slice 1 missing b premise: %v", calls[1].Premises)
	}
	if calls[0].CostNS != 19 || calls[1].CostNS != 28 {
		t.Fatalf("costs %d %d", calls[0].CostNS, calls[1].CostNS)
	}
}

func TestSARFMath(t *testing.T) {
	full := map[string]string{"a": "w", "b": "w", "c": "w"}
	calls := []CallSlice{
		{Index: 0, Premises: map[string]string{"a": "w"}, CostNS: 100},
		{Index: 1, Premises: map[string]string{"a": "w", "b": "w", "c": "w"}, CostNS: 300},
	}
	r := SARF(calls, full)
	if r.SARF != 0.25 {
		t.Fatalf("sarf %v", r.SARF)
	}
	if r.MedianPremise != 3 {
		t.Fatalf("median %d", r.MedianPremise)
	}
}

func TestCounterfactualMath(t *testing.T) {
	calls := []CallSlice{
		{Index: 0, Premises: map[string]string{"a": "w"}, CostNS: 100},
		{Index: 1, Premises: map[string]string{"a": "w", "b": "w"}, CostNS: 300},
	}
	l1, l2, d3, p3 := Counterfactual(calls, map[string]bool{"b": true})
	if l1 != 400 || l2 != 400 || d3 != 300 || p3 != 100 {
		t.Fatalf("got %d %d %d %d", l1, l2, d3, p3)
	}
	l1, l2, d3, p3 = Counterfactual(calls, map[string]bool{})
	if l1 != 0 || l2 != 0 || d3 != 0 || p3 != 0 {
		t.Fatalf("clean run must zero counterfactuals: %d %d %d %d", l1, l2, d3, p3)
	}
}
