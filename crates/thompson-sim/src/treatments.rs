//! The policy configurations under comparison.
//!
//! Each group varies exactly one axis and holds the rest fixed, so a difference
//! in regret is attributable. The controls are deliberately unflattering: the
//! samplers and update rules reproduced here are the ones found in shipped
//! routers, not strawmen.

use crate::experiment::Treatment;
use thompson_sampling::posterior::UpdateRule;
use thompson_sampling::sampler::{legacy, BetaSampler};
use thompson_sampling::warm_start::InformedPrior;
use thompson_sampling::{Config, Exact, Selection, ThompsonSampling, WarmStart};

/// Configuration shared by every treatment unless the group varies it.
fn baseline() -> Config {
    Config {
        // Bernoulli is the theoretically sound rule for `[0, 1]` rewards and
        // is a no-op in a Bernoulli environment, so it does not confound the
        // sampler comparison.
        update_rule: UpdateRule::Bernoulli,
        // Cold start everywhere except the warm-start group, so cold-start
        // behaviour is not silently mixed into the other comparisons.
        warm_start: WarmStart::Cold,
        selection: Selection::Thompson,
        discount: None,
        ..Config::default()
    }
}

fn treatment(
    group: &'static str,
    label: &str,
    config: Config,
    sampler: impl Fn() -> Box<dyn BetaSampler> + 'static,
) -> Treatment {
    Treatment {
        label: label.to_string(),
        group,
        build: Box::new(move || ThompsonSampling::new(config, sampler())),
    }
}

/// Varies the Beta sampler, holding the policy fixed.
pub fn sampler_group() -> Vec<Treatment> {
    vec![
        treatment("sampler", "exact", baseline(), || Box::new(Exact)),
        treatment("sampler", "mean+gaussian", baseline(), || {
            Box::new(legacy::MeanPlusGaussian)
        }),
        treatment("sampler", "mean+uniform", baseline(), || {
            Box::<legacy::MeanPlusUniform>::default()
        }),
        treatment("sampler", "concentration-switched", baseline(), || {
            Box::new(legacy::ConcentrationSwitched::production_default())
        }),
        treatment("sampler", "miscoded-gamma", baseline(), || {
            Box::new(legacy::MiscodedGamma)
        }),
        treatment("sampler", "deterministic", baseline(), || {
            Box::new(legacy::Deterministic)
        }),
    ]
}

/// Varies how a newly-added arm is initialised. Only meaningful on scenarios
/// where an arm arrives mid-run.
pub fn warm_start_group() -> Vec<Treatment> {
    let with = |ws: WarmStart| Config {
        warm_start: ws,
        ..baseline()
    };

    vec![
        treatment("warm-start", "cold", with(WarmStart::Cold), || {
            Box::new(Exact)
        }),
        treatment(
            "warm-start",
            "fixed-optimistic",
            with(WarmStart::Fixed(InformedPrior::new(4.0, 1.0))),
            || Box::new(Exact),
        ),
        treatment(
            "warm-start",
            "family-similarity",
            with(WarmStart::FamilySimilarity {
                discount: 0.2,
                fallback: InformedPrior::default(),
            }),
            || Box::new(Exact),
        ),
    ]
}

/// The same warm-start comparison run under an approximate sampler.
///
/// Warm-start machinery and approximate samplers tend to appear in the same
/// codebases, and the pairing is not a coincidence: a sampler that
/// under-explores makes cold start genuinely expensive, which makes an
/// informed prior look valuable. This group exists to test whether the value
/// survives once the sampler is exact — compare it against `warm-start`.
pub fn warm_start_approx_group() -> Vec<Treatment> {
    warm_start_group()
        .into_iter()
        .map(|t| Treatment {
            label: t.label,
            group: "warm-start-approx",
            build: Box::new(move || {
                // Rebuild with the same config but a deployed-style sampler.
                let reference = (t.build)();
                let config = *reference.config();
                ThompsonSampling::new(config, Box::<legacy::MeanPlusUniform>::default())
            }),
        })
        .collect()
}

/// Varies the selection strategy layered on top of sampling.
pub fn selection_group() -> Vec<Treatment> {
    let with = |s: Selection| Config {
        selection: s,
        ..baseline()
    };

    vec![
        treatment("selection", "thompson", with(Selection::Thompson), || {
            Box::new(Exact)
        }),
        treatment(
            "selection",
            "ucb-regularized",
            with(Selection::UcbRegularized {
                c: 2.0,
                until_pulls: 30,
            }),
            || Box::new(Exact),
        ),
        treatment(
            "selection",
            "phased",
            with(Selection::Phased {
                bootstrap: 10,
                min_pulls_for_exploit: 50,
            }),
            || Box::new(Exact),
        ),
    ]
}

/// Varies how a reward is folded into the posterior. Only informative on
/// scenarios with continuous rewards.
pub fn update_rule_group() -> Vec<Treatment> {
    let with = |r: UpdateRule| Config {
        update_rule: r,
        ..baseline()
    };

    vec![
        treatment(
            "update-rule",
            "binarize@0.6",
            with(UpdateRule::Binarize { threshold: 0.6 }),
            || Box::new(Exact),
        ),
        treatment(
            "update-rule",
            "bernoulli",
            with(UpdateRule::Bernoulli),
            || Box::new(Exact),
        ),
        treatment(
            "update-rule",
            "fractional",
            with(UpdateRule::Fractional),
            || Box::new(Exact),
        ),
    ]
}

/// Varies posterior discounting. Only informative on drifting scenarios.
pub fn discount_group() -> Vec<Treatment> {
    let with = |d: Option<f64>| Config {
        discount: d,
        ..baseline()
    };

    vec![
        treatment("discount", "none", with(None), || Box::new(Exact)),
        treatment("discount", "0.999", with(Some(0.999)), || Box::new(Exact)),
        treatment("discount", "0.99", with(Some(0.99)), || Box::new(Exact)),
    ]
}

/// Every group, in report order.
pub fn all_groups() -> Vec<(&'static str, Vec<Treatment>)> {
    vec![
        ("sampler", sampler_group()),
        ("warm-start", warm_start_group()),
        ("warm-start-approx", warm_start_approx_group()),
        ("selection", selection_group()),
        ("update-rule", update_rule_group()),
        ("discount", discount_group()),
    ]
}

/// Which scenarios each group is informative on. A group run against a
/// scenario that cannot distinguish its treatments produces a table of noise,
/// so the pairing is declared rather than left to the reader.
pub fn scenarios_for(group: &str) -> &'static [&'static str] {
    match group {
        "sampler" => &["easy", "hard", "drift", "churn"],
        "warm-start" => &["churn", "treadmill"],
        "warm-start-approx" => &["churn", "treadmill"],
        "selection" => &["easy", "hard", "churn"],
        "update-rule" => &["graded"],
        "discount" => &["drift"],
        _ => &[],
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashSet;

    #[test]
    fn labels_are_unique_within_a_group() {
        for (name, group) in all_groups() {
            let labels: HashSet<_> = group.iter().map(|t| t.label.clone()).collect();
            assert_eq!(labels.len(), group.len(), "duplicate label in group {name}");
        }
    }

    #[test]
    fn every_group_has_declared_scenarios() {
        for (name, _) in all_groups() {
            assert!(
                !scenarios_for(name).is_empty(),
                "group {name} has no scenarios"
            );
        }
    }

    #[test]
    fn declared_scenarios_exist() {
        let known: HashSet<_> = crate::env::scenarios()
            .into_iter()
            .map(|s| s.name)
            .collect();
        for (name, _) in all_groups() {
            for scenario in scenarios_for(name) {
                assert!(
                    known.contains(scenario),
                    "group {name} names unknown scenario {scenario}"
                );
            }
        }
    }

    #[test]
    fn each_group_varies_exactly_one_axis() {
        // Guards against a treatment quietly changing two things at once,
        // which would make its regret difference uninterpretable.
        let base = baseline();
        for (name, group) in all_groups() {
            for t in &group {
                let c = (t.build)();
                let cfg = *c.config();
                let differences = [
                    (cfg.update_rule != base.update_rule, "update_rule"),
                    (cfg.warm_start != base.warm_start, "warm_start"),
                    (cfg.selection != base.selection, "selection"),
                    (cfg.discount != base.discount, "discount"),
                ]
                .iter()
                .filter(|(differs, _)| *differs)
                .count();

                assert!(
                    differences <= 1,
                    "treatment {}/{} varies {differences} config axes",
                    name,
                    t.label
                );
            }
        }
    }

    #[test]
    fn build_returns_a_fresh_policy_each_call() {
        let t = &sampler_group()[0];
        let mut first = (t.build)();
        first.add_arm("a".into());
        assert_eq!(first.len(), 1);

        let second = (t.build)();
        assert_eq!(second.len(), 0, "state leaked between runs");
    }
}
