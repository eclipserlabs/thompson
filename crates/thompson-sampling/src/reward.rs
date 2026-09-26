//! Turning per-request outcomes into a scalar reward in `[0, 1]`.
//!
//! A router optimises several things at once — latency, spend, success,
//! response quality — but a Beta-Bernoulli bandit consumes a single number.
//! This module does the collapse explicitly so the trade-off being optimised is
//! visible and configurable rather than buried in the update path.

use crate::error::{Error, Result};
use serde::{Deserialize, Serialize};

/// Observed outcome of a single request.
#[derive(Debug, Clone, Copy, PartialEq, Serialize, Deserialize)]
pub struct Outcome {
    /// End-to-end latency in milliseconds.
    pub latency_ms: f64,
    /// Whether the request completed successfully.
    pub success: bool,
    /// Whether the response was served from cache.
    pub cache_hit: bool,
    /// Cost in USD.
    pub cost_usd: f64,
    /// Optional quality score in `[0, 1]` from a judge or heuristic.
    pub quality: Option<f64>,
}

impl Outcome {
    /// A successful, uncached outcome with the given latency and cost.
    pub fn new(latency_ms: f64, success: bool, cost_usd: f64) -> Self {
        Outcome {
            latency_ms,
            success,
            cache_hit: false,
            cost_usd,
            quality: None,
        }
    }

    /// Attach a quality score, clamped to `[0, 1]`. NaN clamps to 0.0.
    pub fn with_quality(mut self, quality: f64) -> Self {
        self.quality = Some(clamp01(quality));
        self
    }

    /// Mark the outcome as a cache hit.
    pub fn cached(mut self) -> Self {
        self.cache_hit = true;
        self
    }
}

/// Relative importance of each reward component.
///
/// Weights need not sum to one; they are normalised at evaluation time. A
/// component whose weight is zero is skipped entirely, and if `quality` is
/// weighted but absent from an outcome, its weight is redistributed across the
/// remaining components rather than scored as zero.
#[derive(Debug, Clone, Copy, PartialEq, Serialize, Deserialize)]
pub struct Weights {
    /// Weight on the latency score.
    pub latency: f64,
    /// Weight on success.
    pub success: f64,
    /// Weight on cache hits.
    pub cache: f64,
    /// Weight on cost efficiency.
    pub cost: f64,
    /// Weight on the quality score.
    pub quality: f64,
}

impl Default for Weights {
    fn default() -> Self {
        Weights {
            latency: 0.25,
            success: 0.40,
            cache: 0.05,
            cost: 0.15,
            quality: 0.15,
        }
    }
}

impl Weights {
    /// Score only whether the request succeeded.
    pub fn success_only() -> Self {
        Weights {
            latency: 0.0,
            success: 1.0,
            cache: 0.0,
            cost: 0.0,
            quality: 0.0,
        }
    }

    /// Reject non-finite weights. NaN slips through every comparison, so
    /// without this check a single NaN weight is admitted by the collapse
    /// loop below (which additionally skips non-`> 0.0` weights as absent).
    /// Negative weights keep their established absent treatment.
    pub fn validate(&self) -> Result<()> {
        for (name, value) in [
            ("latency", self.latency),
            ("success", self.success),
            ("cache", self.cache),
            ("cost", self.cost),
            ("quality", self.quality),
        ] {
            if !value.is_finite() {
                return Err(Error::InvalidParameter {
                    parameter: format!("weights.{name}"),
                    value,
                });
            }
        }
        Ok(())
    }
}

/// Normalisation bounds and weights defining the reward function.
#[derive(Debug, Clone, Copy, PartialEq, Serialize, Deserialize)]
pub struct RewardPolicy {
    /// Component weights.
    pub weights: Weights,
    /// Latency at or below this scores 1.0.
    pub target_latency_ms: f64,
    /// Latency at or above this scores 0.0.
    pub max_latency_ms: f64,
    /// Cost at or below this scores 1.0.
    pub target_cost_usd: f64,
    /// Cost at or above this scores 0.0.
    pub max_cost_usd: f64,
    /// If true, a failed request scores 0.0 regardless of other components.
    ///
    /// Usually what you want: a fast, cheap failure is not a good outcome, and
    /// without this a consistently-failing provider can out-score a working one
    /// on latency and cost alone.
    pub failure_is_zero: bool,
}

impl Default for RewardPolicy {
    fn default() -> Self {
        RewardPolicy {
            weights: Weights::default(),
            target_latency_ms: 500.0,
            max_latency_ms: 10_000.0,
            target_cost_usd: 0.001,
            max_cost_usd: 0.10,
            failure_is_zero: true,
        }
    }
}

/// Clamp to `[0, 1]`, mapping NaN to 0.0.
///
/// `f64::clamp` propagates NaN rather than clamping it, which would let one
/// unscorable component poison the whole reward and surface as a spurious
/// [`Error::RewardOutOfRange`](crate::Error::RewardOutOfRange) from
/// `record_outcome` — an error about a reward the caller never supplied. The Go
/// port clamps the same way.
fn clamp01(value: f64) -> f64 {
    if value.is_nan() {
        0.0
    } else {
        value.clamp(0.0, 1.0)
    }
}

/// Linear score: 1.0 at or below `target`, 0.0 at or above `max`.
///
/// Only NaN short-circuits, because it is unorderable and there is nothing to
/// score. Infinities are ordered and must fall through to the comparisons
/// below: an infinite latency is a timeout, which is the worst outcome
/// available, not a perfect one.
fn ramp_down(value: f64, target: f64, max: f64) -> f64 {
    if value.is_nan() || value <= target {
        return 1.0;
    }
    if value >= max || max <= target {
        return 0.0;
    }
    1.0 - (value - target) / (max - target)
}

/// The individual components behind a composite reward, for inspection.
#[derive(Debug, Clone, Copy, PartialEq, Serialize, Deserialize)]
pub struct Breakdown {
    /// Latency score in `[0, 1]`.
    pub latency: f64,
    /// Success score, 1.0 or 0.0.
    pub success: f64,
    /// Cache score, 1.0 or 0.0.
    pub cache: f64,
    /// Cost efficiency in `[0, 1]`.
    pub cost: f64,
    /// Quality score in `[0, 1]`, if supplied.
    pub quality: Option<f64>,
    /// Weighted total in `[0, 1]`.
    pub total: f64,
}

impl RewardPolicy {
    /// Score an outcome, returning the total and its components.
    pub fn evaluate(&self, outcome: &Outcome) -> Breakdown {
        let latency = ramp_down(
            outcome.latency_ms,
            self.target_latency_ms,
            self.max_latency_ms,
        );
        let cost = ramp_down(outcome.cost_usd, self.target_cost_usd, self.max_cost_usd);
        let success = if outcome.success { 1.0 } else { 0.0 };
        let cache = if outcome.cache_hit { 1.0 } else { 0.0 };
        let quality = outcome.quality.map(clamp01);

        if self.failure_is_zero && !outcome.success {
            return Breakdown {
                latency,
                success,
                cache,
                cost,
                quality,
                total: 0.0,
            };
        }

        let w = self.weights;
        let mut weighted = 0.0;
        let mut total_weight = 0.0;

        for (weight, value) in [
            (w.latency, Some(latency)),
            (w.success, Some(success)),
            (w.cache, Some(cache)),
            (w.cost, Some(cost)),
            (w.quality, quality),
        ] {
            // NaN fails every comparison, so the naive `weight <= 0.0`
            // test admits a NaN weight, which then poisons the whole
            // weighted sum. Non-finite weights are treated as absent here;
            // record_outcome rejects them explicitly.
            if weight.is_nan() || weight <= 0.0 {
                continue;
            }
            // An absent quality score forfeits its weight instead of scoring
            // zero, so arms without a judge are not penalised for its absence.
            if let Some(v) = value {
                weighted += weight * v;
                total_weight += weight;
            }
        }

        let total = if total_weight > 0.0 {
            clamp01(weighted / total_weight)
        } else {
            success
        };

        Breakdown {
            latency,
            success,
            cache,
            cost,
            quality,
            total,
        }
    }

    /// Score an outcome, returning only the scalar reward.
    ///
    /// Always in `[0, 1]` and never NaN, whatever the outcome contains, so the
    /// result is always a legal input to
    /// [`Posterior::observe`](crate::Posterior::observe).
    pub fn reward(&self, outcome: &Outcome) -> f64 {
        self.evaluate(outcome).total
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use rand::rngs::SmallRng;
    use rand::SeedableRng;

    #[test]
    fn ramp_is_one_below_target_and_zero_above_max() {
        assert_eq!(ramp_down(100.0, 500.0, 10_000.0), 1.0);
        assert_eq!(ramp_down(500.0, 500.0, 10_000.0), 1.0);
        assert_eq!(ramp_down(20_000.0, 500.0, 10_000.0), 0.0);
        let mid = ramp_down(5_250.0, 500.0, 10_000.0);
        assert!((mid - 0.5).abs() < 1e-9, "midpoint was {mid}");
    }

    #[test]
    fn failure_scores_zero_when_configured() {
        let policy = RewardPolicy::default();
        let fast_failure = Outcome::new(10.0, false, 0.0);
        assert_eq!(policy.reward(&fast_failure), 0.0);
    }

    #[test]
    fn fast_failure_can_outscore_slow_success_without_the_guard() {
        let policy = RewardPolicy {
            failure_is_zero: false,
            ..RewardPolicy::default()
        };
        let fast_failure = policy.reward(&Outcome::new(10.0, false, 0.0));
        let slow_success = policy.reward(&Outcome::new(9_500.0, true, 0.09));
        // Documents exactly why `failure_is_zero` defaults to true.
        assert!(
            fast_failure > 0.0 && slow_success > 0.0,
            "both should be scored"
        );
        assert!(
            fast_failure > 0.2,
            "a free instant failure scores {fast_failure} on latency and cost alone"
        );
    }

    #[test]
    fn absent_quality_redistributes_its_weight() {
        let policy = RewardPolicy::default();
        let ideal_no_quality = Outcome {
            latency_ms: 0.0,
            success: true,
            cache_hit: true,
            cost_usd: 0.0,
            quality: None,
        };
        // Every present component is perfect, so the total must be 1.0 rather
        // than 0.85 (which is what scoring absent quality as zero would give).
        assert!((policy.reward(&ideal_no_quality) - 1.0).abs() < 1e-9);
    }

    #[test]
    fn an_infinite_measurement_scores_worst_not_best() {
        // Regression guard. A timeout recorded as an infinite latency, or an
        // unmetered request recorded as an infinite cost, is the worst outcome
        // available. Short-circuiting the ramp on `!is_finite()` scored both of
        // them 1.0 — a perfect score — and the Go port, whose guard covers only
        // NaN, disagreed.
        assert_eq!(ramp_down(f64::INFINITY, 500.0, 10_000.0), 0.0);
        assert_eq!(ramp_down(f64::NEG_INFINITY, 500.0, 10_000.0), 1.0);
        assert_eq!(ramp_down(f64::NAN, 500.0, 10_000.0), 1.0);

        let policy = RewardPolicy {
            failure_is_zero: false,
            ..RewardPolicy::default()
        };
        let breakdown = policy.evaluate(&Outcome::new(f64::INFINITY, true, f64::INFINITY));
        assert_eq!(breakdown.latency, 0.0);
        assert_eq!(breakdown.cost, 0.0);
        // Success is the only component left carrying anything: 0.40 of the
        // 0.85 weight present once quality is absent.
        assert!(
            (breakdown.total - 0.40 / 0.85).abs() < 1e-12,
            "total was {}",
            breakdown.total
        );
    }

    #[test]
    fn reward_is_never_nan() {
        // `f64::clamp` propagates NaN instead of clamping it, so a single
        // unscorable component used to make the whole reward NaN, which
        // `record_outcome` then rejected as out of range.
        let policy = RewardPolicy::default();
        let cases = [
            Outcome::new(320.0, true, 0.0012).with_quality(f64::NAN),
            Outcome::new(f64::NAN, true, f64::NAN).with_quality(f64::NAN),
            Outcome::new(f64::INFINITY, true, f64::INFINITY),
        ];
        for c in cases {
            let r = policy.reward(&c);
            assert!(
                !r.is_nan() && (0.0..=1.0).contains(&r),
                "reward {r} for {c:?}"
            );
        }
    }

    #[test]
    fn reward_is_bounded() {
        let policy = RewardPolicy::default();
        let cases = [
            Outcome::new(-5.0, true, -1.0),
            Outcome::new(f64::MAX, true, f64::MAX),
            Outcome::new(0.0, true, 0.0).with_quality(2.0),
            Outcome::new(1.0, true, 1.0).with_quality(-1.0),
        ];
        for c in cases {
            let r = policy.reward(&c);
            assert!(
                (0.0..=1.0).contains(&r),
                "reward {r} out of range for {c:?}"
            );
        }
    }

    #[test]
    fn zero_total_weight_falls_back_to_success() {
        let policy = RewardPolicy {
            weights: Weights {
                latency: 0.0,
                success: 0.0,
                cache: 0.0,
                cost: 0.0,
                quality: 0.0,
            },
            failure_is_zero: false,
            ..RewardPolicy::default()
        };
        assert_eq!(policy.reward(&Outcome::new(1.0, true, 0.0)), 1.0);
        assert_eq!(policy.reward(&Outcome::new(1.0, false, 0.0)), 0.0);
    }

    #[test]
    fn nan_weights_are_absent_in_scoring_but_rejected_by_validation() {
        // Direct scoring must never NaN out: the NaN component is treated
        // as absent (matches Go port).
        let policy = RewardPolicy {
            weights: Weights {
                latency: f64::NAN,
                ..Weights::default()
            },
            ..RewardPolicy::default()
        };
        let r = policy.reward(&Outcome::new(100.0, true, 0.001));
        assert!(!r.is_nan() && (0.0..=1.0).contains(&r));
        // Explicit validation rejects (learning path enforces via
        // record_outcome).
        assert!(policy.weights.validate().is_err());
        let bad_q = Weights {
            quality: f64::INFINITY,
            ..Weights::default()
        };
        assert!(bad_q.validate().is_err());
        assert!(Weights::default().validate().is_ok());
    }

    #[test]
    fn record_outcome_rejects_nonfinite_weights() {
        use crate::policy::Config;
        let weights = Weights {
            latency: f64::NAN,
            ..Weights::default()
        };
        let mut policy = crate::ThompsonSampling::new(
            Config {
                reward_policy: RewardPolicy {
                    weights,
                    ..RewardPolicy::default()
                },
                ..Config::default()
            },
            Box::new(crate::sampler::Exact),
        );
        policy.add_arm("a".into());
        let mut rng = SmallRng::seed_from_u64(3);
        let outcome = Outcome::new(100.0, true, 0.001);
        assert!(policy.record_outcome(&mut rng, "a", &outcome).is_err());
        assert_eq!(policy.total_pulls(), 0);
    }
}
