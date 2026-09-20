// Package propensity provides an estimator-independent reference for the
// Thompson Sampling action distribution, plus the Monte-Carlo estimator it is
// used to audit.
//
// # Why this package exists
//
// Off-policy evaluation divides an observed reward by the probability that the
// logging policy would have taken the observed action. For Thompson Sampling
// that probability has no closed form in the router, so it is reconstructed
// offline. Reconstructing it by Monte Carlo and then *validating* it by
// evaluating a Monte-Carlo Thompson candidate against a Monte-Carlo Thompson
// logger is circular: the same estimated vector appears in numerator and
// denominator, so every importance weight is exactly 1 whatever the estimator's
// bias. That check is an implementation sanity check, not a validation.
//
// This package supplies the independent leg. [Reference] computes the Thompson
// action probability by one-dimensional numerical integration of the exact
// order-statistic identity
//
//	P_i = int_0^1 f_i(x) * prod_{j != i} F_j(x) dx
//
// where f_i is the Beta(alpha_i, beta_i) density of arm i and F_j the
// regularized incomplete beta function. It shares no code, no random number
// stream and no distributional approximation with [MonteCarlo].
//
// # Scope and validity
//
// The identity above is the action distribution of "draw one independent sample
// per arm, take the argmax". That is exactly what
// thompson.ThompsonStrategy/argmaxSampled does with thompson.ExactSampler. It is
// NOT the action distribution of the UCB-regularized or phased selection kinds,
// nor of the approximate samplers (mean+gaussian, mean+uniform, deterministic,
// concentration-switched). Callers must confirm the logging policy actually ran
// exact Thompson selection before treating [Reference] as ground truth; see
// [CheckLoggingPolicy].
//
// # Placement
//
// Everything here is offline evaluation tooling. Numerical integration must
// never run on the router hot path: the router package does not import this
// package, and a test enforces that.
package propensity
