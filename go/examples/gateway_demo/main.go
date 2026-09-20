// Example gateway wiring Thompson Sampling middleware.
// Run: go run ./go/gateway
package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
	"github.com/wiramahendra/thompson-sampling/go/gateway"
)

func main() {
	policy := thompson.NewDefault("openai/gpt-4", "anthropic/claude-3-opus")
	// e.g. attach observer for Prometheus
	policy.SetObserver(thompson.NoopObserver{})

	// Map provider id -> upstream URL
	upstreams := map[string]string{
		"openai/gpt-4":             "https://api.openai.com",
		"anthropic/claude-3-opus": "https://api.anthropic.com",
	}

	mw := gateway.NewMiddleware(policy, func(provider string, w http.ResponseWriter, r *http.Request) (thompson.Outcome, error) {
		target, ok := upstreams[provider]
		if !ok {
			return thompson.NewOutcome(0, false, 0), nil
		}
		u, _ := url.Parse(target)
		proxy := httputil.NewSingleHostReverseProxy(u)
		start := time.Now()
		proxy.ServeHTTP(w, r)
		latency := float64(time.Since(start).Milliseconds())
		// Simplified: infer success from status written; real code should capture ResponseRecorder
		return thompson.NewOutcome(latency, true, 0.001), nil
	})

	http.Handle("/", mw.Handler(nil))
	log.Println("gateway listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
