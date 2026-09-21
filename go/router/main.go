// Router binary — deployable Go router for Thompson Sampling V0.
// Build: go build -o router ./go/router
// Run:   PORT=8080 EVIDENCE_PATH=./evidence.jsonl ARMS=openai/gpt-4,anthropic/claude-3-opus go run ./go/router
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe /health and exit (for Docker HEALTHCHECK)")
	flag.Parse()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	if *healthcheck {
		client := http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get("http://localhost:" + port + "/health")
		if err != nil {
			log.Printf("healthcheck failed: %v", err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Printf("healthcheck status: %d", resp.StatusCode)
			os.Exit(1)
		}
		os.Exit(0)
	}
	evidencePath := os.Getenv("EVIDENCE_PATH")
	if evidencePath == "" {
		evidencePath = "./evidence.jsonl"
	}
	armsEnv := os.Getenv("ARMS")
	var arms []string
	if armsEnv == "" {
		arms = []string{"openai/gpt-4", "anthropic/claude-3-opus"}
	} else {
		for _, a := range strings.Split(armsEnv, ",") {
			a = strings.TrimSpace(a)
			if a != "" {
				arms = append(arms, a)
			}
		}
	}

	policy := thompson.NewDefault(arms...)

	writer, err := gateway.NewFileEvidenceWriter(evidencePath)
	if err != nil {
		log.Fatalf("evidence writer: %v", err)
	}
	defer writer.Close()

	registry := gateway.NewProviderRegistry()
	// Register providers: if PROVIDER_URLS env is JSON, parse it; else use Fake for local.
	// For V0, we support PROVIDER_URL_<ARM> env vars like PROVIDER_URL_OPENAI_GPT_4
	// or fall back to FakeProvider that echoes request body.
	for _, arm := range arms {
		envKey := "PROVIDER_URL_" + strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(arm, "/", "_"), "-", "_"))
		url := os.Getenv(envKey)
		if url != "" {
			log.Printf("arm %s -> HTTP %s", arm, url)
			registry.Register(gateway.NewHTTPProvider(arm, gateway.HTTPProviderConfig{URL: url}))
		} else {
			// Fake provider for local verification; returns success with no tokens/cost
			log.Printf("arm %s -> FakeProvider (no real provider configured, set %s)", arm, envKey)
			registry.Register(gateway.NewFakeProvider(arm))
		}
	}

	shadowRate := 0.0
	if v := os.Getenv("SHADOW_SAMPLE_RATE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			shadowRate = f
		}
	}
	shadowTimeout := 5 * time.Second
	if v := os.Getenv("SHADOW_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			shadowTimeout = d
		}
	}
	shadowConc := 5
	if v := os.Getenv("SHADOW_MAX_CONCURRENCY"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			shadowConc = i
		}
	}
	log.Printf("shadow: rate=%.2f timeout=%s conc=%d (eligible via X-Shadow-Eligible:true, kill switch SHADOW_SAMPLE_RATE=0)", shadowRate, shadowTimeout, shadowConc)

	router, err := gateway.NewRouter(gateway.RouterConfig{
		Policy:                 policy,
		Registry:               registry,
		Writer:                 writer,
		ShadowEligibility:      gateway.HeaderShadowEligibility{},
		ShadowSampleRate:       shadowRate,
		ShadowTimeout:          shadowTimeout,
		ShadowMaxConcurrency:   shadowConc,
	})
	if err != nil {
		log.Fatalf("router: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", router.HealthHandler)
	// Infer requests hit router directly; path /v1/chat/completions, /infer, or /
	mux.Handle("/", router)
	// Also expose metrics placeholder
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("# HELP router_requests_total placeholder\n"))
	})

	addr := ":" + port
	log.Printf("router listening on %s evidence=%s arms=%v", addr, evidencePath, arms)
	log.Printf("ownership: single Policy instance per process (sync.Mutex in Policy, single replica V0)")
	log.Fatal(http.ListenAndServe(addr, mux))
}
