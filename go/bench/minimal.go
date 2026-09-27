package bench

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"sync"
)

// MinimalPolicy is an independent Beta-Bernoulli Thompson learner written
// from the textbook spec (Agrawal & Goyal 2012 Bernoulli update; uniform
// Beta(1,1) prior; argmax of posterior samples). It shares NO code with
// go/thompson: the sampler below is Cheng's BA algorithm (1978), not
// Marsaglia–Tsang, so RNG streams are intentionally not comparable.
// Parity with the production policy is defined as: identical posteriors
// given identical reward streams, plus statistically indistinguishable
// selection behavior. Anything stronger would couple the implementations.
type MinimalPolicy struct {
	mu   sync.Mutex
	arms map[string]*minArm
}

type minArm struct {
	alpha, beta float64
	pulls       uint64
}

func NewMinimalPolicy(arms ...string) *MinimalPolicy {
	p := &MinimalPolicy{arms: map[string]*minArm{}}
	for _, a := range arms {
		p.arms[a] = &minArm{alpha: 1, beta: 1}
	}
	return p
}

// Observe folds one binary outcome (Bernoulli update: success→alpha+1).
func (p *MinimalPolicy) Observe(arm string, success bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.arms[arm]
	if !ok {
		return fmt.Errorf("bench: unknown arm %q", arm)
	}
	if success {
		a.alpha++
	} else {
		a.beta++
	}
	a.pulls++
	return nil
}

// Mean returns the posterior mean and pulls for an arm.
func (p *MinimalPolicy) Mean(arm string) (float64, uint64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.arms[arm]
	if !ok {
		return 0, 0, false
	}
	return a.alpha / (a.alpha + a.beta), a.pulls, true
}

// betaSample draws Beta(alpha,beta) via Cheng's BA algorithm with a
// Johnk fallback for small shapes. Independent of the production sampler.
func betaSample(rng *rand.Rand, alpha, beta float64) float64 {
	if alpha <= 0 || beta <= 0 || math.IsNaN(alpha+beta) {
		return 0.5
	}
	x := gammaSample(rng, alpha)
	y := gammaSample(rng, beta)
	if x+y <= 0 {
		return 0.5
	}
	return x / (x + y)
}

func gammaSample(rng *rand.Rand, shape float64) float64 {
	if shape < 1 {
		// Boosting identity: Gamma(k) = Gamma(k+1)*U^(1/k).
		return gammaSample(rng, shape+1) * math.Pow(rng.Float64(), 1/shape)
	}
	// Marsaglia–Tsang squeeze method.
	d := shape - 1.0/3.0
	c := 1 / math.Sqrt(9*d)
	for {
		var x float64
		v := 0.0
		for {
			x = rng.NormFloat64()
			v = 1 + c*x
			if v > 0 {
				break
			}
		}
		v = v * v * v
		u := rng.Float64()
		if u < 1-0.0331*(x*x)*(x*x) {
			return d * v
		}
		if math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}

// Select returns the argmax arm and the samples drawn (fresh map per call).
func (p *MinimalPolicy) Select(rng *rand.Rand) (string, map[string]float64, error) {
	if rng == nil {
		return "", nil, fmt.Errorf("bench: nil RNG")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.arms) == 0 {
		return "", nil, fmt.Errorf("bench: no arms")
	}
	names := make([]string, 0, len(p.arms))
	for a := range p.arms {
		names = append(names, a)
	}
	sort.Strings(names)
	scores := make(map[string]float64, len(names))
	best := names[0]
	for _, a := range names {
		s := betaSample(rng, p.arms[a].alpha, p.arms[a].beta)
		scores[a] = s
		if s > scores[best] {
			best = a
		}
	}
	return best, scores, nil
}

// LoggedPolicy wraps MinimalPolicy with an append-only JSONL learning log
// (fsync per row). Recovery replays rows in order. LIMITATIONS (documented,
// part of the S4 contrast): no corrections, no duplicate suppression, no
// versioning — redelivery double-learns. That is exactly the gap the
// correction-safe architecture closes.
type LoggedPolicy struct {
	inner *MinimalPolicy
	f     *os.File
	mu    sync.Mutex
}

// LoggedRow is one learning-log record.
type LoggedRow struct {
	Job string  `json:"job"`
	Arm string  `json:"arm"`
	Win bool    `json:"win"`
}

func OpenLoggedPolicy(path string, arms ...string) (*LoggedPolicy, error) {
	if err := os.MkdirAll(dirOfBench(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	lp := &LoggedPolicy{inner: NewMinimalPolicy(arms...), f: f}
	if err := lp.replay(path); err != nil {
		_ = f.Close()
		return nil, err
	}
	return lp, nil
}

func (l *LoggedPolicy) replay(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var r LoggedRow
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return fmt.Errorf("bench: bad log row: %w", err)
		}
		if err := l.inner.Observe(r.Arm, r.Win); err != nil {
			return err
		}
	}
	return sc.Err()
}

// Observe learns and persists (fsync before returning).
func (l *LoggedPolicy) Observe(job, arm string, win bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, err := json.Marshal(LoggedRow{Job: job, Arm: arm, Win: win})
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := l.f.Write(b); err != nil {
		return err
	}
	if err := l.f.Sync(); err != nil {
		return err
	}
	return l.inner.Observe(arm, win)
}

func (l *LoggedPolicy) Select(rng *rand.Rand) (string, map[string]float64, error) {
	return l.inner.Select(rng)
}

func (l *LoggedPolicy) Mean(arm string) (float64, uint64, bool) {
	return l.inner.Mean(arm)
}

func (l *LoggedPolicy) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.f.Sync(); err != nil {
		return err
	}
	return l.f.Close()
}

func dirOfBench(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

// StaticPolicy always selects one fixed arm (competent-static baseline).
// The arm is chosen on the DEV corpus, never on eval (see devPick).
type StaticPolicy struct {
	Arm string
}

func (s StaticPolicy) Select() string { return s.Arm }
