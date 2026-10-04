package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// ProviderOutcome is the measured result of executing against one arm.
// Latency is measured by the router; success is derived from provider response.
// Token counts and cost are optional — nil means unavailable and must be
// persisted as JSON null, never synthesized.
type ProviderOutcome struct {
	Success      bool
	InputTokens  *int
	OutputTokens *int
	CostUSD      *float64
	// StatusCode is kept for debugging; not persisted as separate evidence field.
	StatusCode int
	// ResponseBody is not persisted to evidence (privacy); kept only for proxying to caller.
	ResponseBody   []byte
	ResponseHeader http.Header
}

// Provider is the minimal abstraction for executing an inference request against
// a specific arm/provider-model. Implementations must be stateless and
// side-effect scoped to a single request; they must not mutate global state.
type Provider interface {
	// ID returns the arm identifier this provider handles (e.g. "openai/gpt-4").
	ID() string
	// Invoke executes the request. The original *http.Request is provided for
	// header/body forwarding; provider must read r.Body without assuming it
	// has already been consumed more than once. Router will have buffered the
	// body before calling Invoke for shadow-safety future; V0 buffers for
	// single execution.
	Invoke(ctx context.Context, r *http.Request) (ProviderOutcome, error)
}

// HTTPProviderConfig holds per-provider HTTP target and optional price table.
type HTTPProviderConfig struct {
	URL string
	// Pricing: cost per 1k tokens (input+output) if known. Nil means cost unavailable.
	CostPer1KInput  *float64
	CostPer1KOutput *float64
}

// HTTPProvider forwards to a real HTTP inference endpoint.
// It is the smallest abstraction that exercises a real provider.
// For OpenAI-compatible APIs it attempts to parse usage from JSON response:
// {"usage": {"prompt_tokens": ..., "completion_tokens": ...}}.
// Cost is computed only if pricing is configured and usage is present.
// If either is missing, CostUSD remains nil.
type HTTPProvider struct {
	id     string
	config HTTPProviderConfig
	client *http.Client
}

// NewHTTPProvider creates a provider for the given arm ID.
func NewHTTPProvider(id string, cfg HTTPProviderConfig) *HTTPProvider {
	return &HTTPProvider{id: id, config: cfg, client: &http.Client{}}
}

func (p *HTTPProvider) ID() string { return p.id }

// Invoke forwards the request body to p.config.URL and captures observables.
// Success is true for 2xx, false otherwise. Body is returned for caller proxying.
// Token usage is extracted best-effort from JSON; absence yields nil tokens/cost.
func (p *HTTPProvider) Invoke(ctx context.Context, r *http.Request) (ProviderOutcome, error) {
	var bodyBytes []byte
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return ProviderOutcome{Success: false}, fmt.Errorf("read request body: %w", err)
		}
		bodyBytes = b
		// Restore body for potential retries (V0: single execution, but safe)
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, p.config.URL, bytes.NewReader(bodyBytes))
	if err != nil {
		return ProviderOutcome{Success: false}, err
	}
	// Forward relevant headers (auth is handled separately; we forward content-type)
	for k, vv := range r.Header {
		// Do not forward hop-by-hop headers that break proxying
		if k == "Host" || k == "Content-Length" {
			continue
		}
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return ProviderOutcome{Success: false}, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	out := ProviderOutcome{
		Success:        resp.StatusCode >= 200 && resp.StatusCode < 300,
		StatusCode:     resp.StatusCode,
		ResponseBody:   respBody,
		ResponseHeader: resp.Header.Clone(),
	}
	// Attempt to parse OpenAI-compatible usage; non-JSON or missing usage => nil tokens
	var envelope struct {
		Usage *struct {
			PromptTokens     *int `json:"prompt_tokens"`
			CompletionTokens *int `json:"completion_tokens"`
			InputTokens      *int `json:"input_tokens"`
			OutputTokens     *int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(respBody, &envelope) == nil && envelope.Usage != nil {
		// Prefer prompt/completion naming; fall back to input/output
		if envelope.Usage.PromptTokens != nil {
			out.InputTokens = envelope.Usage.PromptTokens
		} else if envelope.Usage.InputTokens != nil {
			out.InputTokens = envelope.Usage.InputTokens
		}
		if envelope.Usage.CompletionTokens != nil {
			out.OutputTokens = envelope.Usage.CompletionTokens
		} else if envelope.Usage.OutputTokens != nil {
			out.OutputTokens = envelope.Usage.OutputTokens
		}
	}
	// Compute cost only when pricing + usage both present
	if p.config.CostPer1KInput != nil && p.config.CostPer1KOutput != nil && out.InputTokens != nil && out.OutputTokens != nil {
		c := float64(*out.InputTokens)*(*p.config.CostPer1KInput)/1000.0 + float64(*out.OutputTokens)*(*p.config.CostPer1KOutput)/1000.0
		out.CostUSD = &c
	} else if p.config.CostPer1KInput != nil && out.InputTokens != nil && out.OutputTokens == nil {
		// Single cost model (e.g. uniform per token) — only if output missing and caller used single field
		// Do not synthesize output token cost; leave nil to respect "missing remains missing".
		// V0: cost stays nil when output tokens unavailable.
	}
	return out, nil
}

// FakeProvider is a deterministic provider for integration tests.
// It never reaches the network; behavior is configured per arm.
type FakeProvider struct {
	id           string
	statusCode   int
	success      *bool // if nil, derived from statusCode 2xx
	responseBody []byte
	inputTokens  *int
	outputTokens *int
	costUSD      *float64
	err          error
}

type FakeProviderOption func(*FakeProvider)

func FakeWithTokens(in, out int) FakeProviderOption {
	return func(f *FakeProvider) {
		f.inputTokens = &in
		f.outputTokens = &out
	}
}
func FakeWithCost(c float64) FakeProviderOption {
	return func(f *FakeProvider) { f.costUSD = &c }
}
func FakeWithStatus(code int) FakeProviderOption {
	return func(f *FakeProvider) { f.statusCode = code }
}
func FakeWithError(err error) FakeProviderOption {
	return func(f *FakeProvider) { f.err = err }
}

// NewFakeProvider creates a FakeProvider that always reports success with
// 200 unless overridden. Token/cost nil by default to test placeholder handling.
func NewFakeProvider(id string, opts ...FakeProviderOption) *FakeProvider {
	f := &FakeProvider{id: id, statusCode: 200, responseBody: []byte(`{"choices":[]}`)}
	for _, o := range opts {
		o(f)
	}
	return f
}

func (f *FakeProvider) ID() string { return f.id }

func (f *FakeProvider) Invoke(ctx context.Context, r *http.Request) (ProviderOutcome, error) {
	if f.err != nil {
		return ProviderOutcome{Success: false, StatusCode: f.statusCode}, f.err
	}
	// Dry-run/test instrumentation ONLY: FakeProvider honors X-Fake-Delay-Ms
	// (capped) to simulate slow providers deterministically through the real
	// HTTP path. Real providers (HTTPProvider) never read this header.
	if v := r.Header.Get("X-Fake-Delay-Ms"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			if ms > 30000 {
				ms = 30000
			}
			select {
			case <-time.After(time.Duration(ms) * time.Millisecond):
			case <-ctx.Done():
				return ProviderOutcome{Success: false, StatusCode: 200}, ctx.Err()
			}
		}
	}
	success := f.statusCode >= 200 && f.statusCode < 300
	if f.success != nil {
		success = *f.success
	}
	return ProviderOutcome{
		Success:        success,
		StatusCode:     f.statusCode,
		ResponseBody:   f.responseBody,
		ResponseHeader: http.Header{"Content-Type": []string{"application/json"}},
		InputTokens:    f.inputTokens,
		OutputTokens:   f.outputTokens,
		CostUSD:        f.costUSD,
	}, nil
}

// Registry maps arm ID -> Provider. Used by Router to resolve selected arm.
type ProviderRegistry struct {
	providers map[string]Provider
}

func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{providers: make(map[string]Provider)}
}

func (r *ProviderRegistry) Register(p Provider) {
	r.providers[p.ID()] = p
}

func (r *ProviderRegistry) Get(id string) (Provider, bool) {
	p, ok := r.providers[id]
	return p, ok
}

func (r *ProviderRegistry) IDs() []string {
	out := make([]string, 0, len(r.providers))
	for k := range r.providers {
		out = append(out, k)
	}
	return out
}
