package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// Wrong-process contacts fail loudly (409) instead of landing decisions in
// a foreign ledger. Unconfigured instances keep legacy behavior.
func TestInstanceMismatchRefused(t *testing.T) {
	pol := thompson.NewDefault("a", "b")
	reg := NewProviderRegistry()
	reg.Register(NewFakeProvider("a"))
	reg.Register(NewFakeProvider("b"))
	mk := func(id string) *Router {
		rt, err := NewRouter(RouterConfig{
			InstanceID: id, Policy: pol, Registry: reg, Writer: &MemoryEvidenceWriter{},
		})
		if err != nil {
			t.Fatal(err)
		}
		return rt
	}
	rt := mk("proc-A")
	// No header on a configured instance: refused.
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
	if rec.Code != http.StatusConflict {
		t.Fatalf("missing instance header must 409, got %d", rec.Code)
	}
	// Wrong header: refused.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.Header.Set("X-Expect-Instance", "proc-B")
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("foreign instance must 409, got %d", rec.Code)
	}
	// Right header: served.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.Header.Set("X-Expect-Instance", "proc-A")
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("own instance must serve, got %d", rec.Code)
	}
	// Unconfigured instance: legacy open behavior preserved.
	plain := mk("")
	rec = httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
	if rec.Code != http.StatusOK {
		t.Fatalf("unconfigured instance must serve openly, got %d", rec.Code)
	}
	// Health reports the identity for boot-time binding.
	rec = httptest.NewRecorder()
	rt.HealthHandler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Header().Get("X-Instance-ID") != "proc-A" {
		t.Fatal("health must report instance identity")
	}
}
