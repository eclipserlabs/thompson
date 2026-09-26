package thompson

import (
	"fmt"
	"math"
)

// Outcome is the observed result of a single request.
type Outcome struct {
	LatencyMs float64
	Success   bool
	CacheHit  bool
	CostUSD   float64
	// Quality is a score in [0, 1] from a judge or heuristic. HasQuality
	// distinguishes "scored zero" from "not scored", which matters: an absent
	// score forfeits its weight rather than counting against the arm.
	Quality    float64
	HasQuality bool
}

// NewOutcome returns a successful or failed outcome with no quality score.
func NewOutcome(latencyMs float64, success bool, costUSD float64) Outcome {
	return Outcome{LatencyMs: latencyMs, Success: success, CostUSD: costUSD}
}

// WithQuality attaches a quality score, clamped to [0, 1].
func (o Outcome) WithQuality(q float64) Outcome {
	o.Quality = clamp01(q)
	o.HasQuality = true
	return o
}

// Cached marks the outcome as a cache hit.
func (o Outcome) Cached() Outcome {
	o.CacheHit = true
	return o
}

// Weights are the relative importance of each reward component. They need not
// sum to one; they are normalised at evaluation time.
type Weights struct {
	Latency float64
	Success float64
	Cache   float64
	Cost    float64
	Quality float64
}

// RewardPolicy defines normalisation bounds and weights for collapsing an
// Outcome into the single scalar a Beta posterior can consume.
type RewardPolicy struct {
	Weights         Weights
	TargetLatencyMs float64
	MaxLatencyMs    float64
	TargetCostUSD   float64
	MaxCostUSD      float64
	// FailureIsZero scores a failed request at zero regardless of other
	// components. Usually what you want: without it, a provider that fails
	// instantly and for free can out-score a working one on latency and cost.
	FailureIsZero bool
}

// DefaultRewardPolicy returns a balanced policy weighted toward success.
func DefaultRewardPolicy() RewardPolicy {
	return RewardPolicy{
		Weights: Weights{
			Latency: 0.25,
			Success: 0.40,
			Cache:   0.05,
			Cost:    0.15,
			Quality: 0.15,
		},
		TargetLatencyMs: 500,
		MaxLatencyMs:    10000,
		TargetCostUSD:   0.001,
		MaxCostUSD:      0.10,
		FailureIsZero:   true,
	}
}

// SuccessOnlyWeights scores only whether the request succeeded.
func SuccessOnlyWeights() Weights { return Weights{Success: 1} }

// Validate rejects non-finite weights. NaN slips through every comparison
// (`NaN <= 0` is false), so without this check a single NaN weight would be
// admitted by the collapse loop; the loop itself additionally skips
// non-positive-or-NaN weights as absent (see Reward). Negative weights keep
// their established absent treatment.
func (w Weights) Validate() error {
	for _, c := range []struct {
		name  string
		value float64
	}{
		{"latency", w.Latency},
		{"success", w.Success},
		{"cache", w.Cache},
		{"cost", w.Cost},
		{"quality", w.Quality},
	} {
		if math.IsNaN(c.value) || math.IsInf(c.value, 0) {
			return fmt.Errorf("thompson: non-finite %s weight %v", c.name, c.value)
		}
	}
	return nil
}

// rampDown scores 1.0 at or below target and 0.0 at or above max.
func rampDown(value, target, max float64) float64 {
	if value != value || value <= target { // NaN or below target
		return 1
	}
	if value >= max || max <= target {
		return 0
	}
	return 1 - (value-target)/(max-target)
}

// Reward scores an outcome, returning a value in [0, 1].
func (rp RewardPolicy) Reward(o Outcome) float64 {
	if rp.FailureIsZero && !o.Success {
		return 0
	}

	success, cache := 0.0, 0.0
	if o.Success {
		success = 1
	}
	if o.CacheHit {
		cache = 1
	}

	components := []struct {
		weight  float64
		value   float64
		present bool
	}{
		{rp.Weights.Latency, rampDown(o.LatencyMs, rp.TargetLatencyMs, rp.MaxLatencyMs), true},
		{rp.Weights.Success, success, true},
		{rp.Weights.Cache, cache, true},
		{rp.Weights.Cost, rampDown(o.CostUSD, rp.TargetCostUSD, rp.MaxCostUSD), true},
		{rp.Weights.Quality, clamp01(o.Quality), o.HasQuality},
	}

	weighted, totalWeight := 0.0, 0.0
	for _, c := range components {
		// `!(weight > 0)` (not `weight <= 0`): NaN fails every comparison,
		// so the naive test admits a NaN weight, which then poisons the
		// whole weighted sum to NaN. A non-finite weight is treated as
		// absent, exactly like a zero weight.
		if !(c.weight > 0) || !c.present {
			continue
		}
		weighted += c.weight * c.value
		totalWeight += c.weight
	}

	if totalWeight <= 0 {
		return success
	}
	return clamp01(weighted / totalWeight)
}
