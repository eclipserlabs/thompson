package thompson

// Observer hooks for metrics, logging, and alerting.
//
// Attach an Observer to a Policy with [Policy.SetObserver]. All callbacks are
// synchronous and run on the hot path — keep them cheap.

// Observer is a hook for policy lifecycle events.
type Observer interface {
	OnSelect(chosen string, scores map[string]float64)
	OnRecord(arm string, reward float64, posterior Posterior)
	OnArmAdded(id string, warmStarted bool)
	OnDiscount(factor float64)
}

// NoopObserver does nothing. The default when none is attached.
type NoopObserver struct{}

func (NoopObserver) OnSelect(string, map[string]float64) {}
func (NoopObserver) OnRecord(string, float64, Posterior) {}
func (NoopObserver) OnArmAdded(string, bool)             {}
func (NoopObserver) OnDiscount(float64)                  {}
