//! Beta samplers: one exact reference and several cheap approximations.
//!
//! Thompson Sampling needs a draw from each arm's `Beta(alpha, beta)`
//! posterior. Drawing exactly costs two Gamma variates, which is not free, so
//! production routers frequently substitute something cheaper — usually the
//! posterior mean plus noise. Those substitutions are the object of study in
//! this crate, so they are implemented faithfully in [`legacy`] rather than
//! quietly fixed, and the harness in `thompson-sim` measures what they cost.
//!
//! Use [`Exact`] unless you are running an experiment.

use crate::posterior::Posterior;
use rand::RngCore;
use std::f64::consts::PI;

/// Draws a value in `[0, 1]` standing in for a sample from a Beta posterior.
pub trait BetaSampler: Send + Sync + std::fmt::Debug {
    /// Stable identifier used in benchmark output.
    fn name(&self) -> &'static str;

    /// Draw one value. Implementations must return a finite value in `[0, 1]`.
    fn sample(&self, rng: &mut dyn RngCore, posterior: &Posterior) -> f64;
}

impl BetaSampler for Box<dyn BetaSampler> {
    fn name(&self) -> &'static str {
        (**self).name()
    }
    fn sample(&self, rng: &mut dyn RngCore, posterior: &Posterior) -> f64 {
        (**self).sample(rng, posterior)
    }
}

/// A standard normal variate via the Box-Muller transform.
fn standard_normal(rng: &mut dyn RngCore) -> f64 {
    let u1 = open_unit(rng);
    let u2 = rng.next_u64() as f64 / u64::MAX as f64;
    (-2.0 * u1.ln()).sqrt() * (2.0 * PI * u2).cos()
}

/// A uniform draw in `(0, 1]`, excluding zero so `ln` stays finite.
fn open_unit(rng: &mut dyn RngCore) -> f64 {
    let u = (rng.next_u64() >> 11) as f64 / (1u64 << 53) as f64;
    if u <= 0.0 {
        f64::MIN_POSITIVE
    } else {
        u
    }
}

/// Exact Beta sampling via two Gamma variates: `X / (X + Y)` where
/// `X ~ Gamma(alpha, 1)` and `Y ~ Gamma(beta, 1)`.
///
/// Gamma draws use Marsaglia & Tsang (2000), "A Simple Method for Generating
/// Gamma Variables", with the boosting identity
/// `Gamma(k) = Gamma(k + 1) * U^(1/k)` for shapes below 1.
///
/// This is the reference implementation. Every other sampler in this crate is
/// measured against it.
#[derive(Debug, Clone, Copy, Default)]
pub struct Exact;

impl Exact {
    /// Draw from `Gamma(shape, 1)`.
    ///
    /// The squeeze constant `0.0331` and the transform constant
    /// `c = 1 / sqrt(9d)` are jointly tuned; changing either without the other
    /// silently biases the output. See `docs/FINDINGS.md`.
    pub fn gamma(rng: &mut dyn RngCore, shape: f64) -> f64 {
        debug_assert!(shape > 0.0 && shape.is_finite());

        if shape < 1.0 {
            let u = open_unit(rng);
            return Self::gamma(rng, shape + 1.0) * u.powf(1.0 / shape);
        }

        let d = shape - 1.0 / 3.0;
        let c = 1.0 / (9.0 * d).sqrt();

        loop {
            let x = standard_normal(rng);
            let v = 1.0 + c * x;
            if v <= 0.0 {
                continue;
            }
            let v3 = v * v * v;
            let u = open_unit(rng);

            // Cheap squeeze that accepts the overwhelming majority of draws.
            if u < 1.0 - 0.0331 * x * x * x * x {
                return d * v3;
            }
            // Exact acceptance test for the remainder.
            if u.ln() < 0.5 * x * x + d * (1.0 - v3 + v3.ln()) {
                return d * v3;
            }
        }
    }
}

impl BetaSampler for Exact {
    fn name(&self) -> &'static str {
        "exact"
    }

    fn sample(&self, rng: &mut dyn RngCore, posterior: &Posterior) -> f64 {
        let x = Self::gamma(rng, posterior.alpha);
        let y = Self::gamma(rng, posterior.beta);
        let denom = x + y;
        if denom <= 0.0 || !denom.is_finite() {
            // Both Gamma draws underflowed, which needs alpha and beta both
            // far below 1. The posterior mean is the best available answer.
            return posterior.mean();
        }
        x / denom
    }
}

/// Faithful reproductions of approximations found in deployed routers.
///
/// These are preserved for measurement. Each carries a note describing where it
/// came from and how its behaviour departs from an exact draw.
pub mod legacy {
    use super::{open_unit, standard_normal, BetaSampler, Exact};
    use crate::posterior::Posterior;
    use rand::RngCore;

    /// Posterior mean plus Gaussian noise scaled by the posterior standard
    /// deviation, clamped to `[0, 1]`.
    ///
    /// Matches the first two moments of the Beta and is the most defensible of
    /// the approximations here: it does shrink exploration as the posterior
    /// concentrates. It still misses the Beta's skew, which is severe exactly
    /// where it matters — an arm with `Beta(1, 1)` has a flat posterior, but
    /// this sampler draws a bell around 0.5 and clamps the tails, so a
    /// never-tried arm is under-explored relative to a true draw.
    #[derive(Debug, Clone, Copy, Default)]
    pub struct MeanPlusGaussian;

    impl BetaSampler for MeanPlusGaussian {
        fn name(&self) -> &'static str {
            "mean+gaussian"
        }

        fn sample(&self, rng: &mut dyn RngCore, posterior: &Posterior) -> f64 {
            let sample = posterior.mean() + standard_normal(rng) * posterior.std_dev();
            sample.clamp(0.0, 1.0)
        }
    }

    /// Posterior mean plus uniform noise of fixed half-width, clamped to
    /// `[0, 1]`.
    ///
    /// The noise does not depend on the posterior at all, which is the defect:
    /// an arm with two observations and an arm with two thousand receive
    /// identical exploration pressure. The bandit cannot converge, and it also
    /// cannot explore a genuinely uncertain arm any harder than a settled one.
    #[derive(Debug, Clone, Copy)]
    pub struct MeanPlusUniform {
        /// Noise is drawn from `U(-half_width, +half_width)`.
        pub half_width: f64,
    }

    impl Default for MeanPlusUniform {
        fn default() -> Self {
            MeanPlusUniform { half_width: 0.1 }
        }
    }

    impl BetaSampler for MeanPlusUniform {
        fn name(&self) -> &'static str {
            "mean+uniform"
        }

        fn sample(&self, rng: &mut dyn RngCore, posterior: &Posterior) -> f64 {
            let u = open_unit(rng) - 0.5;
            (posterior.mean() + 2.0 * u * self.half_width).clamp(0.0, 1.0)
        }
    }

    /// Dispatches between two samplers on posterior concentration.
    ///
    /// Reproduces routers that special-case "enough data" and fall back to a
    /// crude rule below the threshold. The shape is worth studying on its own:
    /// the cheap branch governs precisely the early rounds where selection
    /// quality determines total regret.
    #[derive(Debug)]
    pub struct ConcentrationSwitched {
        /// Both `alpha` and `beta` must exceed this for the concentrated branch.
        pub threshold: f64,
        /// Used while the posterior is diffuse.
        pub diffuse: Box<dyn BetaSampler>,
        /// Used once the posterior is concentrated.
        pub concentrated: Box<dyn BetaSampler>,
    }

    impl ConcentrationSwitched {
        /// The configuration observed in production: uniform noise of
        /// half-width 0.1 below `alpha, beta = 100`, Gaussian above.
        pub fn production_default() -> Self {
            ConcentrationSwitched {
                threshold: 100.0,
                diffuse: Box::new(MeanPlusUniform { half_width: 0.1 }),
                concentrated: Box::new(MeanPlusGaussian),
            }
        }
    }

    impl BetaSampler for ConcentrationSwitched {
        fn name(&self) -> &'static str {
            "concentration-switched"
        }

        fn sample(&self, rng: &mut dyn RngCore, posterior: &Posterior) -> f64 {
            if posterior.alpha > self.threshold && posterior.beta > self.threshold {
                self.concentrated.sample(rng, posterior)
            } else {
                self.diffuse.sample(rng, posterior)
            }
        }
    }

    /// A closed-form shift of the posterior mean with no randomness whatsoever.
    ///
    /// Included because it is what a widely-copied implementation actually
    /// does: `mean + std_dev * 0.1 * f(pulls)`, where `f` steps down as pulls
    /// accumulate. It is deterministic in `(alpha, beta, pulls)`, so argmax
    /// over these values is a fixed function of state and the policy performs
    /// no exploration at all. It is the null treatment: whatever a bandit is
    /// worth, this is the baseline it must beat.
    #[derive(Debug, Clone, Copy, Default)]
    pub struct Deterministic;

    impl Deterministic {
        fn exploration_factor(pulls: u64) -> f64 {
            if pulls < 5 {
                2.0
            } else if pulls < 20 {
                1.0
            } else {
                0.1
            }
        }
    }

    impl BetaSampler for Deterministic {
        fn name(&self) -> &'static str {
            "deterministic"
        }

        fn sample(&self, _rng: &mut dyn RngCore, posterior: &Posterior) -> f64 {
            let shift = posterior.std_dev() * 0.1 * Self::exploration_factor(posterior.pulls);
            (posterior.mean() + shift).clamp(0.0, 1.0)
        }
    }

    /// Marsaglia & Tsang with the transform constant written as
    /// `1 / sqrt(3d)` instead of `1 / sqrt(9d)`.
    ///
    /// A single-character class of typo, and the resulting draws are not Gamma
    /// distributed: the proposal is widened by `sqrt(3)` while the squeeze
    /// constant and acceptance test remain tuned for the correct width. The
    /// output still looks plausible — bounded, unimodal, roughly the right
    /// location — which is why it survives casual inspection. Kept so the
    /// harness can quantify the bias rather than argue about it.
    #[derive(Debug, Clone, Copy, Default)]
    pub struct MiscodedGamma;

    impl MiscodedGamma {
        fn gamma(rng: &mut dyn RngCore, shape: f64) -> f64 {
            if shape < 1.0 {
                let u = open_unit(rng);
                return Self::gamma(rng, shape + 1.0) * u.powf(1.0 / shape);
            }

            let d = shape - 1.0 / 3.0;
            let c = 1.0 / (3.0 * d).sqrt(); // should be (9.0 * d).sqrt()

            for _ in 0..10_000 {
                let x = standard_normal(rng);
                let v = 1.0 + c * x;
                if v <= 0.0 {
                    continue;
                }
                let v3 = v * v * v;
                let u = open_unit(rng);

                if u < 1.0 - 0.0331 * x * x * x * x {
                    return d * v3;
                }
                if u.ln() < 0.5 * x * x + d * (1.0 - v3 + v3.ln()) {
                    return d * v3;
                }
            }
            // The widened proposal raises the rejection rate; bound the loop so
            // a pathological shape cannot hang a benchmark run.
            Exact::gamma(rng, shape)
        }
    }

    impl BetaSampler for MiscodedGamma {
        fn name(&self) -> &'static str {
            "miscoded-gamma"
        }

        fn sample(&self, rng: &mut dyn RngCore, posterior: &Posterior) -> f64 {
            let x = Self::gamma(rng, posterior.alpha);
            let y = Self::gamma(rng, posterior.beta);
            let denom = x + y;
            if denom <= 0.0 || !denom.is_finite() {
                return posterior.mean();
            }
            (x / denom).clamp(0.0, 1.0)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::legacy::*;
    use super::*;
    use rand::rngs::SmallRng;
    use rand::SeedableRng;

    fn moments(sampler: &dyn BetaSampler, alpha: f64, beta: f64, n: usize) -> (f64, f64) {
        let mut rng = SmallRng::seed_from_u64(0xC0FFEE);
        let p = Posterior::new(alpha, beta).unwrap();
        let mut sum = 0.0;
        let mut sum_sq = 0.0;
        for _ in 0..n {
            let s = sampler.sample(&mut rng, &p);
            assert!((0.0..=1.0).contains(&s), "{} produced {s}", sampler.name());
            sum += s;
            sum_sq += s * s;
        }
        let mean = sum / n as f64;
        let var = sum_sq / n as f64 - mean * mean;
        (mean, var)
    }

    #[test]
    fn exact_gamma_matches_closed_form_moments() {
        // Gamma(k, 1) has mean k and variance k.
        for k in [0.3_f64, 1.0, 2.5, 17.0, 250.0] {
            let mut rng = SmallRng::seed_from_u64(7);
            let n = 200_000;
            let mut sum = 0.0;
            let mut sum_sq = 0.0;
            for _ in 0..n {
                let x = Exact::gamma(&mut rng, k);
                sum += x;
                sum_sq += x * x;
            }
            let mean = sum / n as f64;
            let var = sum_sq / n as f64 - mean * mean;

            let tol = 0.02 * k.max(1.0);
            assert!((mean - k).abs() < tol, "k={k}: mean {mean} != {k}");
            assert!(
                (var - k).abs() < 0.1 * k.max(1.0),
                "k={k}: var {var} != {k}"
            );
        }
    }

    #[test]
    fn exact_matches_beta_moments() {
        for (a, b) in [(1.0, 1.0), (2.0, 3.0), (30.0, 5.0), (0.5, 0.5)] {
            let p = Posterior::new(a, b).unwrap();
            let (mean, var) = moments(&Exact, a, b, 200_000);
            assert!(
                (mean - p.mean()).abs() < 0.01,
                "Beta({a},{b}) mean {mean} != {}",
                p.mean()
            );
            assert!(
                (var - p.variance()).abs() < 0.01,
                "Beta({a},{b}) var {var} != {}",
                p.variance()
            );
        }
    }

    /// Closed-form raw moment of a Beta: `E[X^k] = prod_{i<k} (a+i)/(a+b+i)`.
    fn beta_raw_moment(alpha: f64, beta: f64, k: u32) -> f64 {
        (0..k)
            .map(|i| (alpha + i as f64) / (alpha + beta + i as f64))
            .product()
    }

    #[test]
    fn exact_matches_beta_moments_to_fourth_order() {
        // The first two moments do not pin down a shape, and shape is the whole
        // subject of this crate: `MeanPlusGaussian` matches mean and variance by
        // construction and is still wrong. Raw moments through the fourth
        // constrain skew and kurtosis as well, and need no special functions.
        for (a, b) in [(1.0, 1.0), (2.0, 8.0), (30.0, 5.0), (0.5, 0.5)] {
            let mut rng = SmallRng::seed_from_u64(0xD15EA5E);
            let p = Posterior::new(a, b).unwrap();
            let n = 200_000;
            let mut sums = [0.0_f64; 4];
            for _ in 0..n {
                let s = Exact.sample(&mut rng, &p);
                let mut power = 1.0;
                for sum in sums.iter_mut() {
                    power *= s;
                    *sum += power;
                }
            }
            for (idx, sum) in sums.iter().enumerate() {
                let k = idx as u32 + 1;
                let observed = sum / n as f64;
                let expected = beta_raw_moment(a, b, k);
                let relative = (observed - expected).abs() / expected;
                assert!(
                    relative < 0.02,
                    "Beta({a},{b}) raw moment {k}: {observed} != {expected} (relative {relative})"
                );
            }
        }
    }

    #[test]
    fn exact_explores_a_flat_posterior_uniformly() {
        // Beta(1,1) is uniform. Roughly a tenth of draws should land in each
        // decile; approximations that draw a bell around the mean will not.
        let mut rng = SmallRng::seed_from_u64(11);
        let p = Posterior::uninformative();
        let mut deciles = [0usize; 10];
        let n = 100_000;
        for _ in 0..n {
            let s = Exact.sample(&mut rng, &p);
            let idx = ((s * 10.0) as usize).min(9);
            deciles[idx] += 1;
        }
        for (i, count) in deciles.iter().enumerate() {
            let share = *count as f64 / n as f64;
            assert!(
                (share - 0.1).abs() < 0.01,
                "decile {i} share {share} deviates from uniform"
            );
        }
    }

    /// Returns a fixed value; used to observe which branch a composite sampler
    /// took without inferring it from statistics.
    #[derive(Debug)]
    struct Constant(f64);

    impl BetaSampler for Constant {
        fn name(&self) -> &'static str {
            "constant"
        }
        fn sample(&self, _rng: &mut dyn RngCore, _posterior: &Posterior) -> f64 {
            self.0
        }
    }

    #[test]
    fn deterministic_sampler_returns_the_same_bits_every_time() {
        let mut rng = SmallRng::seed_from_u64(1);
        let p = Posterior::new(30.0, 5.0).unwrap();
        let first = Deterministic.sample(&mut rng, &p);
        for _ in 0..1_000 {
            assert_eq!(
                Deterministic.sample(&mut rng, &p).to_bits(),
                first.to_bits(),
                "the deterministic sampler must not vary"
            );
        }
    }

    #[test]
    fn uniform_noise_does_not_shrink_as_evidence_accumulates() {
        let s = MeanPlusUniform::default();
        let (_, var_sparse) = moments(&s, 2.0, 2.0, 100_000);
        let (_, var_dense) = moments(&s, 2_000.0, 2_000.0, 100_000);
        // A correct sampler's variance would fall by three orders of magnitude
        // between these two posteriors. Here it is flat.
        let ratio = var_dense / var_sparse;
        assert!(
            ratio > 0.9,
            "expected flat exploration noise, got ratio {ratio}"
        );
    }

    #[test]
    fn gaussian_approximation_understates_flat_posterior_spread() {
        // Beta(1,1) has variance 1/12 ~ 0.0833. Clamping a Gaussian to [0,1]
        // loses spread precisely where exploration is most needed.
        let (_, var) = moments(&MeanPlusGaussian, 1.0, 1.0, 200_000);
        assert!(var < 0.075, "expected understated variance, got {var}");
    }

    #[test]
    fn miscoded_gamma_is_biased_against_the_closed_form() {
        // Documents the defect: at least one of the first two moments of the
        // induced Beta is off by more than an exact sampler ever would be.
        let p = Posterior::new(2.0, 3.0).unwrap();
        let (mean, var) = moments(&MiscodedGamma, 2.0, 3.0, 200_000);
        let mean_err = (mean - p.mean()).abs();
        let var_err = (var - p.variance()).abs();
        assert!(
            mean_err > 0.01 || var_err > 0.01,
            "expected measurable bias, got mean_err={mean_err} var_err={var_err}"
        );
    }

    #[test]
    fn switched_sampler_routes_on_concentration() {
        let s = ConcentrationSwitched {
            threshold: 100.0,
            diffuse: Box::new(Constant(0.25)),
            concentrated: Box::new(Constant(0.75)),
        };
        let mut rng = SmallRng::seed_from_u64(3);

        // Both parameters must clear the threshold, so an arm that is confident
        // in one direction only stays on the cheap branch.
        let cases = [
            ((1.0, 1.0), 0.25),
            ((100.0, 100.0), 0.25),
            ((101.0, 100.0), 0.25),
            ((1e6, 5.0), 0.25),
            ((101.0, 101.0), 0.75),
        ];

        for ((a, b), expected) in cases {
            let p = Posterior::new(a, b).unwrap();
            assert_eq!(
                s.sample(&mut rng, &p),
                expected,
                "Beta({a},{b}) took the wrong branch"
            );
        }
    }

    #[test]
    fn production_switch_crosses_over_at_the_documented_point() {
        let s = ConcentrationSwitched::production_default();
        assert_eq!(s.threshold, 100.0);

        // Below the threshold the spread is the flat 0.1 half-width regardless
        // of how concentrated the posterior actually is.
        let mut rng = SmallRng::seed_from_u64(3);
        let p = Posterior::new(99.0, 99.0).unwrap();
        let mut max_dev: f64 = 0.0;
        for _ in 0..10_000 {
            max_dev = max_dev.max((s.sample(&mut rng, &p) - p.mean()).abs());
        }
        // The true posterior std dev here is under 0.04, so 0.1-wide flat noise
        // is roughly triple the exploration an exact draw would give.
        assert!(p.std_dev() < 0.04, "std_dev was {}", p.std_dev());
        assert!(max_dev > 0.09, "max deviation was {max_dev}");
    }

    #[test]
    fn all_samplers_stay_in_range_on_extreme_posteriors() {
        let samplers: Vec<Box<dyn BetaSampler>> = vec![
            Box::new(Exact),
            Box::new(MeanPlusGaussian),
            Box::<MeanPlusUniform>::default(),
            Box::new(Deterministic),
            Box::new(MiscodedGamma),
            Box::new(ConcentrationSwitched::production_default()),
        ];
        let extremes = [(1e-3, 1e-3), (1e6, 1.0), (1.0, 1e6), (1e6, 1e6)];

        let mut rng = SmallRng::seed_from_u64(99);
        for s in &samplers {
            for (a, b) in extremes {
                let p = Posterior::new(a, b).unwrap();
                for _ in 0..2_000 {
                    let v = s.sample(&mut rng, &p);
                    assert!(
                        v.is_finite() && (0.0..=1.0).contains(&v),
                        "{} produced {v} for Beta({a},{b})",
                        s.name()
                    );
                }
            }
        }
    }
}
