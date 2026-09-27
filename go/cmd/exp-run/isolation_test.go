package main

import (
	"fmt"
	"testing"
	"time"
)

// A squatter on our ports (stale gateway from a killed run, or a sibling
// test process) must fail the boot loudly instead of serving foreign
// ledgers as healthy.
func TestBootRefusesForeignProcess(t *testing.T) {
	dir := t.TempDir()
	pubBase, settleBase := nextPortBases()
	pub := fmt.Sprintf("127.0.0.1:%d", pubBase)
	settle := fmt.Sprintf("127.0.0.1:%d", settleBase)
	g1, err := SpawnGateway(routerBin(t), "t0", dir, pub, settle,
		"tok", "fixed", "t0", "noop", "", 20*time.Second)
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}
	defer g1.Kill()
	// Second boot on the SAME addresses hits g1's health endpoint, sees a
	// foreign instance identity, kills nothing, and fails loudly.
	_, err = SpawnGateway(routerBin(t), "t0", dir, pub, settle,
		"tok", "fixed", "t0", "noop", "", 5*time.Second)
	if err == nil {
		t.Fatal("boot against a foreign process must fail, not cross-talk")
	}
	t.Logf("refusal: %v", err)
	// The original gateway is unharmed (the failed boot killed only its own
	// child, which never started serving: the failure happens before adoption).
	if resp, err := g1.client.Get("http://" + pub + "/health"); err != nil || resp.StatusCode != 200 {
		t.Fatalf("original gateway harmed: %v", err)
	} else {
		resp.Body.Close()
	}
}
