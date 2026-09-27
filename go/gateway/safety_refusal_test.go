package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A fail-closed gateway commits nothing: the 503 carries headers but no
// decision exists to settle. Runners must treat it as refusal, never settle
// the phantom ID.
func TestSafetyRefusalCommitsNothing(t *testing.T) {
	f := newSafetyFixture(t, safetyTestConfig())
	defer f.close(t)
	nBefore := f.decStore.Len()
	if code := func() int {
		// Suspend both arms: nothing safe remains.
		if err := f.safety.Suspend("operator:test", "cheap", "test"); err != nil {
			t.Fatal(err)
		}
		if err := f.safety.Suspend("operator:test", "strong", "test"); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
		return rec.Code
	}(); code != 503 {
		t.Fatalf("double suspension must fail closed 503, got %d", code)
	}
	if f.decStore.Len() != nBefore {
		t.Fatal("refused selection committed a decision")
	}
}
