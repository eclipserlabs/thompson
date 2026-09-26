package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func envGetter(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestLoadConfigLegacyDefaults(t *testing.T) {
	cfg, err := loadConfig(envGetter(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.port != "8080" || cfg.evidencePath != "./evidence.jsonl" || len(cfg.arms) != 2 {
		t.Fatalf("bad defaults: %+v", cfg)
	}
	if cfg.mode != "legacy" {
		t.Fatalf("mode=%q want legacy", cfg.mode)
	}
}

func TestLoadConfigVerifiedRequiresSecrets(t *testing.T) {
	base := map[string]string{"ROUTER_MODE": "verified", "STRATEGY_ID": "t2"}
	if _, err := loadConfig(envGetter(base)); err == nil {
		t.Fatal("verified without paths/token loaded (must fail closed)")
	}
	full := map[string]string{
		"ROUTER_MODE": "verified", "STRATEGY_ID": "t2",
		"DECISIONS_PATH": "/tmp/d.jsonl", "OUTCOMES_PATH": "/tmp/o.jsonl",
		"SETTLE_TOKEN": "s3cret",
	}
	cfg, err := loadConfig(envGetter(full))
	if err != nil {
		t.Fatalf("full verified config rejected: %v", err)
	}
	if cfg.settleAddr != "127.0.0.1:8081" {
		t.Fatalf("settleAddr=%q", cfg.settleAddr)
	}
	if _, err := loadConfig(envGetter(map[string]string{"ROUTER_MODE": "nope"})); err == nil {
		t.Fatal("unknown mode accepted")
	}
	if _, err := loadConfig(envGetter(map[string]string{"SHADOW_SAMPLE_RATE": "x"})); err == nil {
		t.Fatal("bad shadow rate accepted")
	}
}

func TestBuildRouterVerifiedOpensDurableStores(t *testing.T) {
	dir := t.TempDir()
	cfg, err := loadConfig(envGetter(map[string]string{
		"ROUTER_MODE": "verified", "STRATEGY_ID": "t2",
		"ARMS":           "a,b",
		"EVIDENCE_PATH":  filepath.Join(dir, "ev.jsonl"),
		"DECISIONS_PATH": filepath.Join(dir, "d.jsonl"),
		"OUTCOMES_PATH":  filepath.Join(dir, "o.jsonl"),
		"SETTLE_TOKEN":   "s3cret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	rt, cleanup, err := buildRouter(cfg)
	if err != nil {
		t.Fatalf("verified build: %v", err)
	}
	defer cleanup()
	// Second builder on the same files fails: single-writer enforced.
	if _, _, err := buildRouter(cfg); err == nil {
		t.Fatal("second verified builder opened locked files")
	}

	// Settlement reachable ONLY on the internal mux, auth-gated.
	pub, priv := publicMux(rt), internalMux(rt)

	rec := httptest.NewRecorder()
	pub.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/outcomes", strings.NewReader(`{}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("public settle=%d want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	priv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/outcomes", strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated settle=%d want 401", rec.Code)
	}
}

// TestProductionRouteSettlement exercises the real mux wiring: serve on the
// public mux, settle on the internal mux, policy learns exactly once.
func TestProductionRouteSettlement(t *testing.T) {
	dir := t.TempDir()
	cfg, err := loadConfig(envGetter(map[string]string{
		"ROUTER_MODE": "verified", "STRATEGY_ID": "t2",
		"ARMS":           "a,b",
		"EVIDENCE_PATH":  filepath.Join(dir, "ev.jsonl"),
		"DECISIONS_PATH": filepath.Join(dir, "d.jsonl"),
		"OUTCOMES_PATH":  filepath.Join(dir, "o.jsonl"),
		"SETTLE_TOKEN":   "s3cret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	rt, cleanup, err := buildRouter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	pub, priv := publicMux(rt), internalMux(rt)

	srec := httptest.NewRecorder()
	pub.ServeHTTP(srec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)))
	if srec.Code != http.StatusOK {
		t.Fatalf("serve=%d", srec.Code)
	}
	decisionID := srec.Header().Get("X-Decision-ID")
	jobID := srec.Header().Get("X-Job-ID")

	arm := "a"
	body, _ := json.Marshal(map[string]any{
		"schema_version": 1, "event_type": "JobSettled",
		"decision_id": decisionID, "job_id": jobID, "strategy_id": "t2",
		"outcome_version": 1, "supersedes": 0, "status": "ACCEPTED",
		"attempts": []map[string]any{{
			"attempt_id": "a1", "seq": 0, "executor_id": arm, "arm_id": arm,
			"transport": "ok", "latency_ms": 120,
			"validation": "pass", "verified": "success", "verified_by": "checker:t",
		}},
		"deciding_attempt_id": "a1",
		"occurred_at":         "2026-09-27T00:00:00Z",
		"verified_by":         "checker:t",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/outcomes", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer s3cret")
	rec := httptest.NewRecorder()
	priv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("settle=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["learned"] != true {
		t.Fatalf("not learned: %v", resp)
	}
}

// TestBinaryBootsVerifiedAndSettles builds the actual binary and runs it:
// missing secrets must fail fast; a full serve+settle cycle must learn once.
func TestBinaryBootsVerifiedAndSettles(t *testing.T) {
	if testing.Short() {
		t.Skip("binary test skipped in short mode")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "router-bin")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}

	// Missing SETTLE_TOKEN: fail fast, non-zero exit.
	bad := exec.Command(bin)
	bad.Env = append(os.Environ(),
		"ROUTER_MODE=verified", "ARMS=a",
		"EVIDENCE_PATH="+filepath.Join(dir, "ev.jsonl"),
		"DECISIONS_PATH="+filepath.Join(dir, "d.jsonl"),
		"OUTCOMES_PATH="+filepath.Join(dir, "o.jsonl"),
	)
	if out, err := bad.CombinedOutput(); err == nil {
		t.Fatalf("booted without token: %s", out)
	}

	// Full boot on fixed ports.
	pub, settle := "18081", "18082"
	proc := exec.Command(bin)
	proc.Env = append(os.Environ(),
		"ROUTER_MODE=verified", "STRATEGY_ID=t2", "ARMS=a,b",
		"PORT="+pub, "SETTLE_ADDR=127.0.0.1:"+settle,
		"EVIDENCE_PATH="+filepath.Join(dir, "ev.jsonl"),
		"DECISIONS_PATH="+filepath.Join(dir, "d.jsonl"),
		"OUTCOMES_PATH="+filepath.Join(dir, "o.jsonl"),
		"SETTLE_TOKEN=s3cret",
	)
	proc.Stdout, proc.Stderr = &strings.Builder{}, &strings.Builder{}
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Process.Kill(); _, _ = proc.Process.Wait() }()

	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := client.Get("http://127.0.0.1:" + pub + "/health")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			break
		}
		if err == nil {
			resp.Body.Close()
		}
		if time.Now().After(deadline) {
			t.Fatal("binary did not become healthy")
		}
		time.Sleep(200 * time.Millisecond)
	}

	sresp, err := client.Post("http://127.0.0.1:"+pub+"/", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	sresp.Body.Close()
	if sresp.StatusCode != http.StatusOK {
		t.Fatalf("serve=%d", sresp.StatusCode)
	}
	decisionID, jobID := sresp.Header.Get("X-Decision-ID"), sresp.Header.Get("X-Job-ID")
	if decisionID == "" || jobID == "" {
		t.Fatal("missing identity headers")
	}
	// Public listener must not settle.
	badSettle, _ := client.Post("http://127.0.0.1:"+pub+"/v1/outcomes", "application/json", strings.NewReader(`{}`))
	badSettle.Body.Close()
	if badSettle.StatusCode != http.StatusNotFound {
		t.Fatalf("public settle=%d want 404", badSettle.StatusCode)
	}
	// Internal listener settles with auth.
	payload := fmt.Sprintf(`{"schema_version":1,"event_type":"JobSettled","decision_id":%q,"job_id":%q,"strategy_id":"t2","outcome_version":1,"supersedes":0,"status":"ACCEPTED","attempts":[{"attempt_id":"a1","seq":0,"executor_id":"a","arm_id":"a","transport":"ok","latency_ms":120,"validation":"pass","verified":"success"}],"deciding_attempt_id":"a1","occurred_at":"2026-09-27T00:00:00Z"}`,
		decisionID, jobID)
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+settle+"/v1/outcomes", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer s3cret")
	sresp2, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sresp2.Body.Close()
	if sresp2.StatusCode != http.StatusOK {
		t.Fatalf("settle=%d", sresp2.StatusCode)
	}
	var resp map[string]any
	if err := json.NewDecoder(sresp2.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["learned"] != true {
		t.Fatalf("binary did not learn: %v", resp)
	}
}
