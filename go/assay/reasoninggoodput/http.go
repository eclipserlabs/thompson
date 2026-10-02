package reasoninggoodput

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// http.go: HTTP witness fixture shaped by RFC 9110 strong validators.
// Resources are JSON documents with deliberately independent fields. ETags
// are strong (quoted content hash — change iff bytes change; weak ETags are
// never issued and never accepted for mutation validation). Mutations
// require If-Match (412 on mismatch); unconditional writes are refused.
// This exercises OCC-shaped conditional mutation, not a bespoke protocol.

// HTTPResource is one versioned JSON resource.
type HTTPResource struct {
	Body string
	ETag string // strong validator, quoted
	Rev  int
}

func strongETag(body string) string { return `"` + DigestString(body) + `"` }

// HTTPFixture is a local versioned resource server.
type HTTPFixture struct {
	mu        sync.Mutex
	resources map[string]*HTTPResource
	Server    *httptest.Server
	URL       string
}

// NewHTTPFixture serves initial (path -> JSON body) resources.
func NewHTTPFixture(initial map[string]string) *HTTPFixture {
	f := &HTTPFixture{resources: map[string]*HTTPResource{}}
	for p, b := range initial {
		f.resources[p] = &HTTPResource{Body: b, ETag: strongETag(b), Rev: 1}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", f.serve)
	f.Server = httptest.NewServer(mux)
	f.URL = f.Server.URL
	return f
}

func (f *HTTPFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res, ok := f.resources[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if inm := r.Header.Get("If-None-Match"); inm != "" && inm == res.ETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", res.ETag)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, res.Body)
	case http.MethodPut:
		// Conditional mutation only: If-Match required, strong comparison.
		match := r.Header.Get("If-Match")
		if match == "" || strings.HasPrefix(match, "W/") {
			http.Error(w, "If-Match with strong validator required", http.StatusPreconditionRequired)
			return
		}
		if match != res.ETag {
			http.Error(w, "precondition failed", http.StatusPreconditionFailed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		res.Body = string(body)
		res.ETag = strongETag(res.Body)
		res.Rev++
		w.Header().Set("ETag", res.ETag)
		_, _ = io.WriteString(w, res.Body)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// Close shuts the server down.
func (f *HTTPFixture) Close() { f.Server.Close() }

// GetWitnessed GETs path, binding the body to its strong ETag witness.
func (f *HTTPFixture) GetWitnessed(path string) (WitnessedRead, error) {
	resp, err := http.Get(f.URL + path)
	if err != nil {
		return WitnessedRead{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return WitnessedRead{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return WitnessedRead{}, fmt.Errorf("http: GET %s: %d", path, resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" || strings.HasPrefix(etag, "W/") {
		return WitnessedRead{}, fmt.Errorf("http: %s missing strong validator", path)
	}
	return WitnessedRead{
		Value: body,
		Witness: StateWitness{
			Ref:        ResourceRef{Authority: "http", ID: path},
			Version:    etag,
			ObservedAt: nowRFC3339(),
		},
		Path: path,
	}, nil
}

// CurrentETag returns the live ETag without body transfer (diagnostic;
// validation always re-reads or conditions, never trusts cache).
func (f *HTTPFixture) CurrentETag(path string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res, ok := f.resources[path]
	if !ok {
		return "", fmt.Errorf("http: unknown %s", path)
	}
	return res.ETag, nil
}

// PutUnconditional writes without any precondition (last-writer-wins).
// Exists ONLY so T1 late-conflict can exercise systems that do not
// condition mutations. Conditional treatments never call it.
func (f *HTTPFixture) PutUnconditional(path, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res, ok := f.resources[path]
	if !ok {
		return "", fmt.Errorf("http: unknown %s", path)
	}
	res.Body = body
	res.ETag = strongETag(body)
	res.Rev++
	return res.ETag, nil
}

// PutIfMatch conditionally writes body; 412 signals a lost race honestly.
func (f *HTTPFixture) PutIfMatch(path, body, etag string) (string, error) {
	req, err := http.NewRequest(http.MethodPut, f.URL+path, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusPreconditionFailed {
		return "", fmt.Errorf("http: 412 precondition failed for %s", path)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http: PUT %s: %d", path, resp.StatusCode)
	}
	_ = out
	return resp.Header.Get("ETag"), nil
}
