package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func minManifest() string {
	return `{"experiment_id":"e1","workload_name":"w","seed":1,"maturation_hours":24,"synthetic":true,
"treatments":[{"id":"t0","arms":["a"],"max_attempts":1},{"id":"t1","arms":["a"],"max_attempts":1}],
"jobs":[{"job_id":"j1","strata":"s","arms":{"a":{"success_p":0.5,"cost_usd":0.01,"latency_ms":100}}}]}`
}

func TestLoadManifestVersionsAndValidates(t *testing.T) {
	m, err := LoadManifest(writeManifest(t, minManifest()))
	if err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if m.WorkloadVersion == "" {
		t.Fatal("version not stamped")
	}
	if m.Jobs[0].Behavior != BehaviorNormal {
		t.Fatal("default behavior not normalized")
	}
	// Stable across loads.
	m2, err := LoadManifest(writeManifest(t, minManifest()))
	if err != nil {
		t.Fatal(err)
	}
	// Same content → same version even across files.
	if m.WorkloadVersion != m2.WorkloadVersion {
		t.Fatal("version unstable")
	}
	// Tamper with content but keep declared version → rejected.
	var raw map[string]any
	rawBytes, _ := os.ReadFile(writeManifest(t, minManifest()))
	_ = rawBytes
	_ = raw
	tampered := strings.Replace(minManifest(), `"seed":1`, `"seed":2`, 1)
	// Inject the ORIGINAL version into tampered content.
	tm, err := LoadManifest(writeManifest(t, minManifest()))
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(tampered, `"workload_name":"w"`, `"workload_name":"w","workload_version":"`+tm.WorkloadVersion+`"`, 1)
	if _, err := LoadManifest(writeManifest(t, bad)); err == nil {
		t.Fatal("version mismatch accepted")
	}
}

func TestLoadManifestRejects(t *testing.T) {
	base := `{"experiment_id":"e1","workload_name":"w","seed":1,"maturation_hours":24,"synthetic":true,
"treatments":[{"id":"t0","arms":["a"],"max_attempts":1}],
"jobs":[{"job_id":"j1","strata":"s","arms":{"a":{"success_p":0.5,"cost_usd":0.01,"latency_ms":100}}}]}`
	cases := map[string]func(string) string{
		"dup job": func(s string) string {
			return strings.Replace(s, `"job_id":"j1"`, `"job_id":"j1"},{"job_id":"j1","strata":"s","arms":{"a":{"success_p":0.5}}}`, 1)
		},
		"bad behavior": func(s string) string { return strings.Replace(s, `"arms":{"a"`, `"behavior":"nope","arms":{"a"`, 1) },
		"bad treatment": func(s string) string {
			return strings.Replace(s, `"strata":"s","arms"`, `"strata":"s","eligible":["ghost"],"arms"`, 1)
		},
		"bad p":  func(s string) string { return strings.Replace(s, `"success_p":0.5`, `"success_p":1.5`, 1) },
		"no id":  func(s string) string { return strings.Replace(s, `"experiment_id":"e1"`, `"experiment_id":""`, 1) },
		"no mat": func(s string) string { return strings.Replace(s, `"maturation_hours":24`, `"maturation_hours":0`, 1) },
	}
	for name, mut := range cases {
		if _, err := LoadManifest(writeManifest(t, mut(base))); err == nil {
			t.Fatalf("%s: invalid manifest accepted", name)
		}
	}
}
