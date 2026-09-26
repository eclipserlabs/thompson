package thompson

import (
	"math"
	"math/rand/v2"
	"sync"
	"testing"
	"time"
)

// hostileStrategy tries to corrupt the policy through the strategy interface:
// it mutates the arms it receives, reorders the order slice, and retains
// pointers for later abuse.
type hostileStrategy struct {
	kept map[string]*Arm
}

func (s *hostileStrategy) Name() string { return "hostile" }

func (s *hostileStrategy) Select(rng *rand.Rand, arms map[string]*Arm, order []string, sampler Sampler, totalPulls uint64) string {
	for _, a := range arms {
		a.Posterior.Alpha = 1000
		a.Posterior.Beta = 0.001
		a.CumulativeReward = 1e9
	}
	if len(order) > 1 {
		order[0], order[1] = order[1], order[0]
	}
	if s.kept == nil {
		s.kept = make(map[string]*Arm)
	}
	for id, a := range arms {
		s.kept[id] = a
	}
	return order[0]
}

// A hostile strategy must not mutate live policy state, and retained
// pointers must be detached copies.
func TestSelectWithIsolatesHostileStrategy(t *testing.T) {
	p := NewDefault("a", "b", "c")
	before := p.Snapshot()
	hostile := &hostileStrategy{}
	for i := 0; i < 5; i++ {
		if _, err := p.SelectWith(newRNG(uint64(i)), hostile); err != nil {
			t.Fatalf("select: %v", err)
		}
	}
	after := p.Snapshot()
	if len(after.Arms) != len(before.Arms) {
		t.Fatal("arm set changed")
	}
	for i := range after.Arms {
		if after.Arms[i].Posterior != before.Arms[i].Posterior ||
			after.Arms[i].CumulativeReward != before.Arms[i].CumulativeReward {
			t.Fatalf("live posterior mutated via strategy: %+v", after.Arms[i])
		}
	}
	// Retained pointers point at detached copies: abusing them later still
	// cannot reach the policy. Mutate them and re-verify.
	for _, a := range hostile.kept {
		a.Posterior.Alpha = -5
	}
	again := p.Snapshot()
	for i := range again.Arms {
		if again.Arms[i].Posterior != before.Arms[i].Posterior {
			t.Fatal("retained pointer reaches live state")
		}
	}
	// Selection still functions over the intact policy.
	if _, err := p.Select(newRNG(99)); err != nil {
		t.Fatalf("select after hostile: %v", err)
	}
}

// unknownStrategy returns an arm that does not exist.
type unknownStrategy struct{}

func (unknownStrategy) Name() string { return "unknown" }
func (unknownStrategy) Select(rng *rand.Rand, arms map[string]*Arm, order []string, sampler Sampler, totalPulls uint64) string {
	return "ghost"
}

func TestSelectWithRejectsUnknownArm(t *testing.T) {
	p := NewDefault("a")
	if _, err := p.SelectWith(newRNG(1), unknownStrategy{}); err == nil {
		t.Fatal("unknown arm selection accepted silently")
	}
	if _, err := p.SelectWith(newRNG(1), nil); err == nil {
		t.Fatal("nil strategy accepted silently")
	}
}

// reentrantObserver calls back into the policy from inside notifications.
// Under-lock notification would self-deadlock here. Re-entry is bounded
// (one nested level) to exercise the lock path without recursing forever.
type reentrantObserver struct {
	policy *Policy
	calls  int
	depth  int
}

func (o *reentrantObserver) OnSelect(chosen string, scores map[string]float64) {
	o.calls++
	if o.depth > 0 {
		return
	}
	o.depth++
	defer func() { o.depth-- }()
	_ = o.policy.Stats()
	if _, err := o.policy.Select(newRNG(7)); err != nil {
		panic(err)
	}
}
func (o *reentrantObserver) OnRecord(arm string, reward float64, posterior Posterior) {
	o.calls++
	if o.depth > 0 {
		return
	}
	o.depth++
	defer func() { o.depth-- }()
	_ = o.policy.Stats()
}
func (o *reentrantObserver) OnArmAdded(id string, warmStarted bool) {}
func (o *reentrantObserver) OnDiscount(factor float64)              {}

func TestObserverReentrancyDoesNotDeadlock(t *testing.T) {
	p := NewDefault("a", "b")
	obs := &reentrantObserver{policy: p}
	p.SetObserver(obs)
	done := make(chan bool, 1)
	go func() {
		rng := newRNG(1)
		for i := 0; i < 20; i++ {
			id, err := p.Select(rng)
			if err != nil {
				t.Errorf("select: %v", err)
				break
			}
			if err := p.Record(rng, id, 0.7); err != nil {
				t.Errorf("record: %v", err)
				break
			}
		}
		done <- true
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock: reentrant observer blocked policy progress")
	}
	if obs.calls == 0 {
		t.Fatal("observer never notified")
	}
}

// Concurrent select/record/snapshot traffic must be race-clean and coherent.
func TestConcurrentSelectRecordSnapshot(t *testing.T) {
	p := NewDefault("a", "b", "c")
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			rng := newRNG(seed)
			for i := 0; i < 100; i++ {
				id, err := p.Select(rng)
				if err != nil {
					t.Errorf("select: %v", err)
					return
				}
				if err := p.Record(rng, id, 0.5); err != nil {
					t.Errorf("record: %v", err)
					return
				}
				if _, err := p.SelectSnapshot(rng); err != nil {
					t.Errorf("snapshot: %v", err)
					return
				}
			}
		}(uint64(w + 1))
	}
	wg.Wait()
}

func TestNilRNGRejected(t *testing.T) {
	p := NewDefault("a")
	if _, err := p.Select(nil); err != ErrNilRNG {
		t.Fatalf("select nil rng: %v", err)
	}
	if _, _, err := p.SelectWithScores(nil); err != ErrNilRNG {
		t.Fatalf("scores nil rng: %v", err)
	}
	if _, err := p.SelectSnapshot(nil); err != ErrNilRNG {
		t.Fatalf("snapshot nil rng: %v", err)
	}
	if _, err := p.SelectWith(nil, ThompsonStrategy{}); err != ErrNilRNG {
		t.Fatalf("selectwith nil rng: %v", err)
	}
	if err := p.Record(nil, "a", 0.5); err != ErrNilRNG {
		t.Fatalf("record nil rng: %v", err)
	}
	// Bernoulli consumes the RNG: nil must error, not panic.
	p2 := New(Config{UpdateRule: UpdateRule{Kind: Bernoulli}}, ExactSampler{})
	p2.AddArm("a")
	if err := p2.Record(nil, "a", 0.5); err != ErrNilRNG {
		t.Fatalf("bernoulli nil rng: %v", err)
	}
	// Fractional/Binarize do not consume randomness, but the API contract
	// is uniform: nil RNG is always rejected.
	p3 := New(Config{UpdateRule: UpdateRule{Kind: Fractional}}, ExactSampler{})
	p3.AddArm("a")
	if err := p3.Record(nil, "a", 0.5); err != ErrNilRNG {
		t.Fatalf("fractional nil rng: %v", err)
	}
}

func TestUCBNaNCoefficientRejected(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Selection = Selection{Kind: UCBRegularized, C: math.NaN(), UntilPulls: 10}
	p := New(cfg, ExactSampler{})
	p.AddArm("a")
	p.AddArm("b")
	if _, err := p.Select(newRNG(1)); err == nil {
		t.Fatal("NaN UCB coefficient selected silently")
	}
	if _, _, err := p.SelectWithScores(newRNG(1)); err == nil {
		t.Fatal("NaN UCB coefficient scored silently")
	}
	if _, err := p.SelectSnapshot(newRNG(1)); err == nil {
		t.Fatal("NaN UCB coefficient snapshotted silently")
	}
	inf := cfg
	inf.Selection = Selection{Kind: UCBRegularized, C: math.Inf(1), UntilPulls: 10}
	pi := New(inf, ExactSampler{})
	pi.AddArm("a")
	if _, err := pi.Select(newRNG(1)); err == nil {
		t.Fatal("+Inf UCB coefficient selected silently")
	}
}

func TestBinarizeNaNThresholdRejected(t *testing.T) {
	var post Posterior = Posterior{Alpha: 1, Beta: 1}
	if err := post.Observe(newRNG(1), 0.7, UpdateRule{Kind: Binarize, Threshold: math.NaN()}); err == nil {
		t.Fatal("NaN binarize threshold accepted (would fail every observation)")
	}
	if err := post.Observe(newRNG(1), 0.7, UpdateRule{Kind: Binarize, Threshold: 1.5}); err == nil {
		t.Fatal("out-of-range binarize threshold accepted")
	}
	if err := post.Observe(newRNG(1), 0.7, UpdateRule{Kind: Binarize, Threshold: 0.5}); err != nil {
		t.Fatalf("valid threshold rejected: %v", err)
	}
}

func TestNaNWeightsSkippedAndValidated(t *testing.T) {
	w := DefaultRewardPolicy().Weights
	w.Latency = math.NaN()
	rp := DefaultRewardPolicy()
	rp.Weights = w
	// Direct scoring treats the NaN component as absent (never NaN out).
	r := rp.Reward(Outcome{LatencyMs: 100, Success: true, CostUSD: 0.001})
	if math.IsNaN(r) {
		t.Fatal("NaN weight poisoned the reward")
	}
	// Validation rejects explicitly.
	if err := w.Validate(); err == nil {
		t.Fatal("NaN weight passed validation")
	}
	w2 := DefaultRewardPolicy().Weights
	w2.Cost = math.Inf(1)
	if err := w2.Validate(); err == nil {
		t.Fatal("+Inf weight passed validation")
	}
	// RecordOutcome enforces validation on the learning path.
	p := NewDefault("a")
	bad := DefaultConfig()
	bad.Reward.Weights.Latency = math.NaN()
	pb := New(bad, ExactSampler{})
	pb.AddArm("a")
	if err := pb.RecordOutcome(newRNG(1), "a", Outcome{LatencyMs: 100, Success: true}); err == nil {
		t.Fatal("RecordOutcome with NaN weights accepted")
	}
	_ = p
}
