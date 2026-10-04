//! Concurrent stress: many threads select/record with health breaker.

use rand::rngs::SmallRng;
use rand::SeedableRng;
use std::sync::{Arc, Mutex};
use thompson_sampling::health::CircuitBreaker;
use thompson_sampling::observer::PolicyObserver;
use thompson_sampling::posterior::Posterior;
use thompson_sampling::ThompsonSampling;

#[derive(Debug, Default)]
struct CountingObserver {
    selects: Mutex<usize>,
    records: Mutex<usize>,
}

impl PolicyObserver for CountingObserver {
    fn on_select(&self, _chosen: &str, _scores: &[(&str, f64)]) {
        *self.selects.lock().unwrap() += 1;
    }
    fn on_record(&self, _arm: &str, _reward: f64, _posterior: &Posterior) {
        *self.records.lock().unwrap() += 1;
    }
}

#[test]
fn concurrent_select_record_with_observer() {
    let policy = Arc::new(Mutex::new(
        ThompsonSampling::with_defaults(["a", "b", "c"])
            .with_observer(Box::<CountingObserver>::default()),
    ));
    let mut handles = Vec::new();
    for seed in 0..8 {
        let policy = Arc::clone(&policy);
        handles.push(std::thread::spawn(move || {
            let mut rng = SmallRng::seed_from_u64(seed);
            for _ in 0..500 {
                let id = {
                    let p = policy.lock().unwrap();
                    p.select(&mut rng).unwrap()
                };
                {
                    let mut p = policy.lock().unwrap();
                    p.record(&mut rng, &id, 0.5).unwrap();
                }
            }
        }));
    }
    for h in handles {
        h.join().unwrap();
    }
    assert_eq!(policy.lock().unwrap().total_pulls(), 4000);
}

#[test]
fn breaker_trips_under_concurrent_failures() {
    let mut breaker = CircuitBreaker::new(3, 100);
    let fail = thompson_sampling::Outcome::new(100.0, false, 0.01);
    for t in 0..10 {
        breaker.record("a", &fail, t);
    }
    assert!(breaker.is_tripped("a", 5));
}
