package gateway

import (
	"fmt"

	"github.com/wiramahendra/thompson-sampling/go/propensity"
)

// PropensityEstimator reconstructs the logging policy's action distribution for
// one historical decision, and says how much to trust the result.
//
// Two implementations exist and they share no numerical machinery:
// [ReferenceEstimator] integrates the Beta order statistic, [MCEstimator]
// simulates Thompson draws. That separation is the point -- an importance
// weight whose numerator and denominator come from the same estimator is 1 by
// construction and validates nothing.
type PropensityEstimator interface {
	// ID names the estimator in reports and gate records.
	ID() string
	// Reconstruct returns per-arm propensity estimates for the given posterior
	// snapshot.
	Reconstruct(state []EligibleArmState) propensity.Reconstruction
}

// ArmsFromState converts the persisted decision-time snapshot into the arm form
// the propensity package works in.
func ArmsFromState(state []EligibleArmState) []propensity.Arm {
	arms := make([]propensity.Arm, 0, len(state))
	for _, s := range state {
		arms = append(arms, propensity.Arm{ID: s.ArmID, Alpha: s.Alpha, Beta: s.Beta})
	}
	return arms
}

// ReferenceEstimator uses the numerical reference as the propensity source.
// This is the ground truth the audit compares Monte Carlo against.
type ReferenceEstimator struct {
	Options    propensity.ReferenceOptions
	Thresholds propensity.Thresholds
}

// NewReferenceEstimator returns a ReferenceEstimator with audit defaults.
func NewReferenceEstimator() ReferenceEstimator {
	return ReferenceEstimator{
		Options:    propensity.DefaultReferenceOptions(),
		Thresholds: propensity.DefaultThresholds(),
	}
}

// ID implements PropensityEstimator.
func (ReferenceEstimator) ID() string { return "numerical-reference-v1" }

// Reconstruct implements PropensityEstimator.
func (e ReferenceEstimator) Reconstruct(state []EligibleArmState) propensity.Reconstruction {
	return propensity.ReconstructReference(ArmsFromState(state), e.Options, e.Thresholds)
}

// MCEstimator uses Monte-Carlo Thompson simulation as the propensity source.
// This is the estimator that was already in the OPE path and that this work
// exists to audit.
type MCEstimator struct {
	Draws int
	Seed  uint64
	// WithReference additionally computes the numerical reference, purely so the
	// Monte-Carlo estimate can be scored against it. The denominator handed to
	// IPS stays the Monte-Carlo number either way.
	WithReference bool
	Options       propensity.ReferenceOptions
	Thresholds    propensity.Thresholds
}

// NewMCEstimator returns an MCEstimator with audit defaults.
func NewMCEstimator(draws int, seed uint64) MCEstimator {
	return MCEstimator{
		Draws: draws, Seed: seed,
		Options:    propensity.DefaultReferenceOptions(),
		Thresholds: propensity.DefaultThresholds(),
	}
}

// WithReferenceScoring returns a copy that also computes the reference for
// scoring.
func (e MCEstimator) WithReferenceScoring() MCEstimator { e.WithReference = true; return e }

// ID implements PropensityEstimator.
func (e MCEstimator) ID() string {
	return fmt.Sprintf("monte-carlo-v1(draws=%d,seed=%d)", e.Draws, e.Seed)
}

// Reconstruct implements PropensityEstimator.
func (e MCEstimator) Reconstruct(state []EligibleArmState) propensity.Reconstruction {
	return propensity.ReconstructMonteCarlo(ArmsFromState(state), e.Draws, e.Seed, e.WithReference, e.Options, e.Thresholds)
}
