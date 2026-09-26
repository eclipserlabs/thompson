package thompson

import "testing"

// LoggingPolicyID must derive from the live configuration: only exact
// Thompson sampling earns the ID the offline estimators model. Every other
// combination gets a distinct, gate-refused identifier (audit A3).
func TestLoggingPolicyIDDerivesFromConfig(t *testing.T) {
	ucb := DefaultConfig()
	ucb.Selection = Selection{Kind: UCBRegularized, C: 2.0, UntilPulls: 30}
	phased := DefaultConfig()
	phased.Selection = Selection{Kind: PhasedSelection, Bootstrap: 5, MinPullsForExploit: 5}
	cases := []struct {
		name    string
		config  Config
		sampler Sampler
		want    string
	}{
		{"exact thompson", DefaultConfig(), ExactSampler{}, "exact-thompson-v1"},
		{"ucb exact", ucb, ExactSampler{}, "ucb-regularized-v1"},
		{"phased exact", phased, ExactSampler{}, "phased-v1"},
		{"thompson approx", DefaultConfig(), MeanPlusGaussianSampler{}, "thompson-mean+gaussian-v1"},
		{"thompson deterministic", DefaultConfig(), DeterministicSampler{}, "thompson-deterministic-v1"},
		{"ucb approx", ucb, MeanPlusUniformSampler{}, "ucb-regularized-v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := New(tc.config, tc.sampler)
			p.AddArm("a")
			if got := p.LoggingPolicyID(); got != tc.want {
				t.Fatalf("LoggingPolicyID=%q want %q", got, tc.want)
			}
		})
	}
}
