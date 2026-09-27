package gateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafetyStoreTornTailTruncates(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "safety.jsonl")
	s, err := NewSafetyStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(SafetyEvent{Actor: "op", Type: SafetyEmergencyStop, Reason: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate crash mid-write: partial last line, no trailing newline.
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"seq":1,"type":"ARM_SUSPEND`)
	f.Close()
	s2, err := NewSafetyStore(p)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := s2.Events()
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Type != SafetyEmergencyStop {
		t.Fatalf("torn tail not truncated: %+v", evs)
	}
	s2.Close()
}

func TestSafetyStoreMidFileCorruptionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "safety.jsonl")
	s, err := NewSafetyStore(p)
	if err != nil {
		t.Fatal(err)
	}
	s.Append(SafetyEvent{Actor: "op", Type: SafetyEmergencyStop, Reason: "r"})
	s.Append(SafetyEvent{Actor: "op", Type: SafetyEmergencyRelease, Reason: "r"})
	s.Close()
	// Corrupt the FIRST line (disk rot, not torn tail): must fail, not guess.
	raw, _ := os.ReadFile(p)
	lines := splitTestLines(raw)
	lines[0] = []byte(`{"seq":0,"type":BROKEN`)
	os.WriteFile(p, joinTestLines(lines), 0o600)
	if _, err := NewSafetyStore(p); err == nil {
		t.Fatal("mid-file corruption must fail closed")
	}
}

func splitTestLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == 0x0a {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	return out
}

func joinTestLines(ls [][]byte) []byte {
	var out []byte
	for _, l := range ls {
		out = append(out, l...)
		out = append(out, 0x0a)
	}
	return out
}
