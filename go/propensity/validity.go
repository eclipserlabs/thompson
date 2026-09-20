package propensity

import "fmt"

// ExactThompsonLoggingPolicyIDs are the logging-policy identifiers whose action
// distribution is "one independent exact Beta draw per arm, take the argmax" --
// the only distribution [Reference] computes.
var ExactThompsonLoggingPolicyIDs = map[string]bool{
	"exact-thompson-v1": true,
	"thompson-v1":       true,
}

// CheckLoggingPolicy reports whether the numerical reference is a valid ground
// truth for decisions logged under policyID.
//
// This guard exists because the integral is not a general-purpose propensity
// oracle. thompson.UCBRegularized adds a pull-count bonus before the argmax,
// thompson.PhasedSelection forces the least-pulled arm below a quota, and the
// approximate samplers (mean+gaussian, mean+uniform, deterministic,
// concentration-switched) do not draw from the Beta posterior at all. For any
// of those, both the integral and the Monte-Carlo reconstruction in this
// package describe a policy that did not run.
func CheckLoggingPolicy(policyID string) error {
	if ExactThompsonLoggingPolicyIDs[policyID] {
		return nil
	}
	return fmt.Errorf("propensity: logging policy %q is not exact Thompson argmax; "+
		"the Beta order-statistic reference does not describe its action distribution", policyID)
}
