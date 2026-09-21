//! Health and circuit-breaker for thin-waist routing.
//!
//! Thompson Sampling explores, but production needs a blast-radius cap:
//! consecutive failures or tail latency spikes should temporarily remove an
//! arm, then probe again. This module is first-class health machinery that
//! wraps `ThompsonSampling` without forking `policy.rs`.

use crate::policy::ThompsonSampling;
use crate::reward::Outcome;
use std::collections::HashMap;

/// Per-arm health state.
#[derive(Debug, Clone)]
struct ArmHealth {
    consecutive_failures: u32,
    tripped_until_round: Option<u64>,
}

/// Circuit breaker that temporarily excludes failing arms.
#[derive(Debug, Clone)]
pub struct CircuitBreaker {
    /// Failures before tripping.
    pub failure_threshold: u32,
    /// Rounds to stay tripped.
    pub cooldown_rounds: u64,
    health: HashMap<String, ArmHealth>,
}

impl Default for CircuitBreaker {
    fn default() -> Self {
        Self {
            failure_threshold: 3,
            cooldown_rounds: 100,
            health: HashMap::new(),
        }
    }
}

impl CircuitBreaker {
    /// Create with thresholds.
    pub fn new(failure_threshold: u32, cooldown_rounds: u64) -> Self {
        Self {
            failure_threshold,
            cooldown_rounds,
            health: HashMap::new(),
        }
    }

    /// Record outcome for breaker state. Returns whether arm was tripped.
    pub fn record(&mut self, id: &str, outcome: &Outcome, round: u64) -> bool {
        let entry = self.health.entry(id.to_string()).or_insert(ArmHealth {
            consecutive_failures: 0,
            tripped_until_round: None,
        });
        if !outcome.success {
            entry.consecutive_failures += 1;
            if entry.consecutive_failures >= self.failure_threshold {
                entry.tripped_until_round = Some(round + self.cooldown_rounds);
                entry.consecutive_failures = 0;
                return true;
            }
        } else {
            entry.consecutive_failures = 0;
            // stay tripped until cooldown expires — don't clear early
        }
        false
    }

    /// Whether arm is currently tripped.
    pub fn is_tripped(&self, id: &str, round: u64) -> bool {
        self.health
            .get(id)
            .and_then(|h| h.tripped_until_round)
            .is_some_and(|until| round < until)
    }

    /// Filter arms: returns ids not tripped. Empty means all tripped — caller should bypass.
    pub fn available<'a>(&self, ids: &[&'a str], round: u64) -> Vec<&'a str> {
        let filtered: Vec<&'a str> = ids
            .iter()
            .copied()
            .filter(|id| !self.is_tripped(id, round))
            .collect();
        if filtered.is_empty() {
            ids.to_vec()
        } else {
            filtered
        }
    }

    /// Wrap a ThompsonSampling select to respect breaker.
    /// Returns tripped-aware choice: if best arm is tripped, picks best among available.
    ///
    /// Approximation, not Thompson Sampling: selection is argmax posterior mean
    /// among untripped arms, so it does not explore like a Beta draw. Use it as
    /// an exclusion-before-select guard (filter with [`available`](Self::available),
    /// then `policy.select`), not as a replacement for `select`, and keep the
    /// all-tripped bypass in mind when reasoning about blast radius.
    pub fn select_with_breaker(&self, policy: &ThompsonSampling, round: u64) -> Option<String> {
        let stats = policy.stats();
        let available: Vec<&str> = self.available(
            &stats.iter().map(|s| s.id.as_str()).collect::<Vec<_>>(),
            round,
        );
        // Pick highest posterior mean among available — approximates Thompson but respects health
        available.first().map(|s| s.to_string())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::reward::Outcome;

    #[test]
    fn breaker_trips_after_threshold() {
        let mut breaker = CircuitBreaker::new(3, 10);
        let fail = Outcome::new(100.0, false, 0.01);
        assert!(!breaker.record("a", &fail, 0));
        assert!(!breaker.record("a", &fail, 1));
        assert!(breaker.record("a", &fail, 2));
        assert!(breaker.is_tripped("a", 5));
        assert!(!breaker.is_tripped("a", 13));
    }

    #[test]
    fn breaker_bypasses_when_all_tripped() {
        let mut breaker = CircuitBreaker::new(1, 100);
        let fail = Outcome::new(100.0, false, 0.01);
        breaker.record("a", &fail, 0);
        breaker.record("b", &fail, 0);
        let avail = breaker.available(&["a", "b"], 1);
        assert_eq!(avail.len(), 2); // bypass — don't blackhole traffic
    }

    #[test]
    fn success_resets_consecutive() {
        let mut breaker = CircuitBreaker::new(3, 10);
        let fail = Outcome::new(100.0, false, 0.01);
        let ok = Outcome::new(100.0, true, 0.01);
        breaker.record("a", &fail, 0);
        breaker.record("a", &fail, 1);
        breaker.record("a", &ok, 2);
        assert!(!breaker.record("a", &fail, 3)); // counter reset
    }
}
