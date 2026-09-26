// Router binary — deployable Go router for Thompson Sampling.
// Build: go build -o router ./go/router
// Run (legacy): PORT=8080 EVIDENCE_PATH=./evidence.jsonl ARMS=a,b go run ./go/router
// Run (verified): ROUTER_MODE=verified STRATEGY_ID=... DECISIONS_PATH=... OUTCOMES_PATH=...
//
//	SETTLE_TOKEN=... SETTLE_ADDR=127.0.0.1:8081 go run ./go/router
package main

import (
	"crypto/subtle"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/gateway"
	"github.com/wiramahendra/thompson-sampling/go/outcome"
	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// appConfig is the full process configuration, loaded from the environment
// so it can be validated (and refused) before anything serves traffic.
type appConfig struct {
	port         string
	evidencePath string
	arms         []string
	providerURLs map[string]string

	shadowRate    float64
	shadowTimeout time.Duration
	shadowMaxConc int

	mode       gateway.RouterMode
	strategyID string

	decisionsPath string
	outcomesPath  string
	settleToken   string
	settleAddr    string
}

// loadConfig reads the environment. It never touches the network or disk:
// callers validate the result before opening stores or listeners.
func loadConfig(getenv func(string) string) (appConfig, error) {
	var c appConfig
	c.port = getenv("PORT")
	if c.port == "" {
		c.port = "8080"
	}
	c.evidencePath = getenv("EVIDENCE_PATH")
	if c.evidencePath == "" {
		c.evidencePath = "./evidence.jsonl"
	}
	armsEnv := getenv("ARMS")
	if armsEnv == "" {
		c.arms = []string{"openai/gpt-4", "anthropic/claude-3-opus"}
	} else {
		for _, a := range strings.Split(armsEnv, ",") {
			a = strings.TrimSpace(a)
			if a != "" {
				c.arms = append(c.arms, a)
			}
		}
	}
	if len(c.arms) == 0 {
		return c, fmt.Errorf("router: no arms configured")
	}
	c.providerURLs = make(map[string]string)
	for _, arm := range c.arms {
		key := "PROVIDER_URL_" + strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(arm, "/", "_"), "-", "_"))
		if url := getenv(key); url != "" {
			c.providerURLs[arm] = url
		}
	}

	c.shadowRate = 0.0
	if v := getenv("SHADOW_SAMPLE_RATE"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return c, fmt.Errorf("router: bad SHADOW_SAMPLE_RATE: %w", err)
		}
		c.shadowRate = f
	}
	c.shadowTimeout = 5 * time.Second
	if v := getenv("SHADOW_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("router: bad SHADOW_TIMEOUT: %w", err)
		}
		c.shadowTimeout = d
	}
	c.shadowMaxConc = 5
	if v := getenv("SHADOW_MAX_CONCURRENCY"); v != "" {
		i, err := strconv.Atoi(v)
		if err != nil {
			return c, fmt.Errorf("router: bad SHADOW_MAX_CONCURRENCY: %w", err)
		}
		c.shadowMaxConc = i
	}

	switch m := getenv("ROUTER_MODE"); m {
	case "", "legacy":
		c.mode = gateway.LegacyMode
	case "verified":
		c.mode = gateway.VerifiedMode
	default:
		return c, fmt.Errorf("router: unknown ROUTER_MODE %q", m)
	}
	c.strategyID = getenv("STRATEGY_ID")

	if c.mode == gateway.VerifiedMode {
		// Fail closed: every required verified input must be present before
		// any store opens or any listener binds.
		c.decisionsPath = getenv("DECISIONS_PATH")
		c.outcomesPath = getenv("OUTCOMES_PATH")
		c.settleToken = getenv("SETTLE_TOKEN")
		if c.decisionsPath == "" || c.outcomesPath == "" || c.settleToken == "" {
			return c, fmt.Errorf("router: verified mode requires DECISIONS_PATH, OUTCOMES_PATH and SETTLE_TOKEN")
		}
		c.settleAddr = getenv("SETTLE_ADDR")
		if c.settleAddr == "" {
			c.settleAddr = "127.0.0.1:8081"
		}
	}
	return c, nil
}

// bearerAuth returns a settlement auth hook comparing against a fixed token
// in constant time. Empty token never authenticates.
func bearerAuth(token string) func(r *http.Request) bool {
	return func(r *http.Request) bool {
		got := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(got, prefix) {
			return false
		}
		got, want := got[len(prefix):], token
		if len(got) == 0 || len(want) == 0 || len(got) != len(want) {
			return false
		}
		return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
	}
}

// buildRouter opens stores, registers providers, and constructs the Router.
// Verified mode refuses to start unless every durable dependency opens.
func buildRouter(cfg appConfig) (*gateway.Router, func(), error) {
	policy := thompson.NewDefault(cfg.arms...)

	writer, err := gateway.NewFileEvidenceWriter(cfg.evidencePath)
	if err != nil {
		return nil, nil, fmt.Errorf("evidence writer: %w", err)
	}
	cleanup := func() { _ = writer.Close() }

	registry := gateway.NewProviderRegistry()
	for _, arm := range cfg.arms {
		if url, ok := cfg.providerURLs[arm]; ok {
			log.Printf("arm %s -> HTTP %s", arm, url)
			registry.Register(gateway.NewHTTPProvider(arm, gateway.HTTPProviderConfig{URL: url}))
		} else {
			log.Printf("arm %s -> FakeProvider (no real provider configured)", arm)
			registry.Register(gateway.NewFakeProvider(arm))
		}
	}

	rc := gateway.RouterConfig{
		Policy:               policy,
		Registry:             registry,
		Writer:               writer,
		StrategyID:           cfg.strategyID,
		Mode:                 cfg.mode,
		ShadowEligibility:    gateway.HeaderShadowEligibility{},
		ShadowSampleRate:     cfg.shadowRate,
		ShadowTimeout:        cfg.shadowTimeout,
		ShadowMaxConcurrency: cfg.shadowMaxConc,
	}
	if cfg.mode == gateway.VerifiedMode {
		decisions, err := gateway.NewFileDecisionStore(cfg.decisionsPath)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("decision store: %w", err)
		}
		outcomes, err := outcome.NewFileOutcomeStore(cfg.outcomesPath)
		if err != nil {
			_ = decisions.Close()
			cleanup()
			return nil, nil, fmt.Errorf("outcome store: %w", err)
		}
		cleanup = func() {
			_ = outcomes.Close()
			_ = decisions.Close()
			_ = writer.Close()
		}
		rc.Decisions = decisions
		rc.Outcomes = outcomes
		rc.SettleAuth = bearerAuth(cfg.settleToken)
	}
	router, err := gateway.NewRouter(rc)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("router: %w", err)
	}
	if cfg.mode == gateway.VerifiedMode {
		if err := router.RecoverVerifiedLearning(checkpointPath(cfg)); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("recovery: %w", err)
		}
	}
	return router, cleanup, nil
}

func checkpointPath(cfg appConfig) string {
	if cfg.outcomesPath == "" {
		return ""
	}
	return cfg.outcomesPath + ".checkpoint.json"
}

// publicMux serves inference traffic. Settlement is never mounted here:
// /v1/outcomes is explicitly refused so a misdirected settlement can never
// be mistaken for (or executed as) an inference request. Outcome writes
// arrive only on the internal listener.
func publicMux(router *gateway.Router) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", router.HealthHandler)
	mux.HandleFunc("/v1/outcomes", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "settlement is not served on the public listener", http.StatusNotFound)
	})
	mux.Handle("/", router)
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("# HELP router_requests_total placeholder\n"))
	})
	return mux
}

// internalMux serves the settlement API on a loopback-restricted listener.
func internalMux(router *gateway.Router) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", router.HealthHandler)
	mux.HandleFunc("/v1/outcomes", router.SettleHandler)
	return mux
}

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

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	router, cleanup, err := buildRouter(cfg)
	if err != nil {
		log.Fatalf("startup: %v", err)
	}
	defer cleanup()

	if cfg.mode == gateway.VerifiedMode {
		go func() {
			log.Printf("settlement listening on %s (internal only)", cfg.settleAddr)
			log.Fatal(http.ListenAndServe(cfg.settleAddr, internalMux(router)))
		}()
	}

	addr := ":" + cfg.port
	log.Printf("router listening on %s mode=%s strategy=%s arms=%v", addr, cfg.mode, cfg.strategyID, cfg.arms)
	log.Printf("ownership: single Policy instance per process (sync.Mutex in Policy, single replica V0)")
	log.Fatal(http.ListenAndServe(addr, publicMux(router)))
}
