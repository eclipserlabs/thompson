package gateway

import (
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ShadowEligibility determines whether a request is proven side-effect-free
// and thus safe to replay to a shadow provider.
// Default is deny. Callers must explicitly opt-in via header / config.
type ShadowEligibility interface {
	IsEligible(r *http.Request) bool
}

// HeaderShadowEligibility requires an explicit internal header to allow shadowing.
// This is the V0 implementation: only requests with X-Shadow-Eligible: true
// are considered safe. Do NOT infer safety from path like /v1/chat/completions.
// Documented opt-in: caller sets header `X-Shadow-Eligible: true` AND ensures
// workload has no tool calls, external actions, payments, writes, etc.
type HeaderShadowEligibility struct{}

func (h HeaderShadowEligibility) IsEligible(r *http.Request) bool {
	v := r.Header.Get("X-Shadow-Eligible")
	return strings.EqualFold(v, "true")
}

// DenyAllEligibility always denies.
type DenyAllEligibility struct{}

func (DenyAllEligibility) IsEligible(r *http.Request) bool { return false }

// MaxBodyBytes is the explicit limit for request body buffering.
// Bodies exceeding this disable shadowing cleanly (primary still works).
const MaxBodyBytes = 1 << 20 // 1 MiB for V0

// ShadowBudget controls hard rate and concurrency limits.
type ShadowBudget struct {
	mu sync.Mutex
	// concurrency semaphore
	sem chan struct{}
	// simple token bucket for rate (optional) — using atomic counter per second window
	// For V0, we enforce max concurrent only; rate is via sampling + optional limiter.
}

func NewShadowBudget(maxConcurrency int) *ShadowBudget {
	if maxConcurrency <= 0 {
		maxConcurrency = 5 // conservative default
	}
	return &ShadowBudget{sem: make(chan struct{}, maxConcurrency)}
}

func (b *ShadowBudget) TryAcquire() bool {
	select {
	case b.sem <- struct{}{}:
		return true
	default:
		return false
	}
}
func (b *ShadowBudget) Release() {
	select {
	case <-b.sem:
	default:
	}
}

// shadowSampler decides whether to shadow using an isolated RNG stream.
type shadowSampler struct {
	mu  sync.Mutex
	rng *rand.Rand
}

func newShadowSampler(seed uint64) *shadowSampler {
	return &shadowSampler{rng: rand.New(rand.NewPCG(seed, seed>>1))}
}

// shouldSample returns true with probability rate in [0,1] without consuming
// the primary policy RNG. Thread-safe.
func (s *shadowSampler) shouldSample(rate float64) bool {
	if rate <= 0 {
		return false
	}
	if rate >= 1 {
		return true
	}
	s.mu.Lock()
	v := s.rng.Float64()
	s.mu.Unlock()
	return v < rate
}

// selectShadowArm chooses exactly one non-primary arm deterministically.
// V0 rule: separate uniform sampling among eligible\{primary} using an isolated RNG.
// Never shadows primary against itself, never mutates policy.
func selectShadowArm(eligible []string, primary string, rnd *rand.Rand) (string, bool) {
	candidates := make([]string, 0, len(eligible)-1)
	for _, id := range eligible {
		if id != primary {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	if len(candidates) == 1 {
		return candidates[0], true
	}
	idx := rnd.IntN(len(candidates))
	return candidates[idx], true
}

// ShadowConfig holds tunable shadow V0 parameters with kill switch semantics.
type ShadowConfig struct {
	// SampleRate in [0,1], default 0. Isolated from Thompson selection RNG.
	SampleRate float64
	// Timeout for shadow execution, default 5s.
	Timeout time.Duration
	// MaxConcurrency hard limit, default 5.
	MaxConcurrency int
	// MaxBodyBytes body limit, default MaxBodyBytes (1MiB).
	MaxBodyBytes int64
	// ShadowRNGSeed for deterministic tests; 0 => time-seeded.
	ShadowRNGSeed uint64
}

// shadowState holds mutable shadow configuration with atomic kill switch.
type shadowState struct {
	mu      sync.RWMutex
	config  ShadowConfig
	sampler *shadowSampler
	budget  *ShadowBudget
	// shadowRNG for arm selection, isolated from primary
	shadowRNG *rand.Rand
}

func newShadowState(cfg ShadowConfig) *shadowState {
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 5
	}
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = MaxBodyBytes
	}
	seed := cfg.ShadowRNGSeed
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	return &shadowState{
		config:    cfg,
		sampler:   newShadowSampler(seed ^ 0x9e3779b97f4a7c15),
		budget:    NewShadowBudget(cfg.MaxConcurrency),
		shadowRNG: rand.New(rand.NewPCG(seed^0xdeadbeef, seed)),
	}
}

func (s *shadowState) getSampleRate() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.SampleRate
}
func (s *shadowState) setSampleRate(rate float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rate < 0 {
		rate = 0
	}
	if rate > 1 {
		rate = 1
	}
	s.config.SampleRate = rate
}
func (s *shadowState) getTimeout() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.Timeout
}
func (s *shadowState) getBudget() *ShadowBudget {
	return s.budget
}

// shadowMetrics holds aggregate counters for observability (not policy punishment).
type shadowMetrics struct {
	attempted   atomic.Uint64
	sampled     atomic.Uint64
	executed    atomic.Uint64
	timeout     atomic.Uint64
	rateLimited atomic.Uint64
}
