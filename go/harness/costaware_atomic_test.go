package harness

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// F2 regression: a malformed cost must refuse the WHOLE settlement atomically.
// Pre-fix, the quality learner moved and the event was stored before the cost
// book failed, leaving estimators permanently diverged on that job.
func TestCostAwareMalformedCostAtomicRefusal(t *testing.T) {
	dir := t.TempDir()
	pol := thompson.NewDefault("cheap", "strong")
	cfg := thompson.DefaultCostAwareConfig()
	tr, err := OpenCostAwareTreatment(dir+"/t3", "t3", pol, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	tr.Strategy.MaxRetries = 0
	pullsBefore := pol.TotalPulls()
	evsBefore := len(tr.Store.Events())

	// Inject a NaN cost directly through a crafted job: bypass Execute by
	// calling RunJob with truth that yields NaN. JobTruth uses float64, so
	// NaN flows into the attempt tape.
	rng := rand.New(rand.NewPCG(1, 1))
	job := costAwareTestJob("jnan", 1.0, 0.0, math.NaN(), 0.04)
	_, err = tr.RunJob(rng, job, Assignment{JobID: "jnan", Strata: "web", Treatment: "t3", Probability: 1})
	if err == nil {
		t.Fatal("expected malformed-cost refusal")
	}
	if pol.TotalPulls() != pullsBefore {
		t.Fatal("F2: quality learner moved despite cost refusal")
	}
	if len(tr.Store.Events()) != evsBefore {
		t.Fatal("F2: event stored despite cost refusal")
	}
}
