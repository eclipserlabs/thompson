package reasoninggoodput

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// fixture_test.go: canonical result fixtures (testdata/goodput_matrix.json,
// testdata/scale.json) are frozen research evidence. An ordinary test run
// never writes them: it writes its fresh output to a temp dir and asserts
// the fresh run reproduces the frozen fixture on every non-timing field
// (work units, discards, digests, premises, oracle verdicts). Wall/overhead
// nanoseconds are host measurements and are excluded from the comparison.
//
// Regenerating canonical fixtures is an explicit command:
//
//	go test ./assay/reasoninggoodput -run 'TestGoodputMatrix|TestScaleSweeps' -update

var updateFixtures = flag.Bool("update", false, "rewrite canonical testdata result fixtures")

// timingKeys are the JSON fields holding host wall-clock measurements.
var timingKeys = map[string]bool{"wall_ns": true, "overhead_ns": true}

func stripTiming(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			if !timingKeys[k] {
				out[k] = stripTiming(e)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = stripTiming(e)
		}
		return out
	}
	return v
}

// emitFixture writes fresh results. With -update it rewrites the canonical
// testdata file; otherwise it writes under t.TempDir() and fails if the
// fresh non-timing content diverges from the frozen fixture.
func emitFixture(t *testing.T, name string, v any) {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join("testdata", name)
	if *updateFixtures {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(canonical, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.WriteFile(filepath.Join(t.TempDir(), name), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	frozenRaw, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("frozen fixture %s missing (regenerate explicitly with -update): %v", canonical, err)
	}
	var frozen, fresh any
	if err := json.Unmarshal(frozenRaw, &frozen); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fresh); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stripTiming(frozen), stripTiming(fresh)) {
		t.Errorf("fresh %s diverges from frozen fixture on non-timing fields", name)
	}
}
