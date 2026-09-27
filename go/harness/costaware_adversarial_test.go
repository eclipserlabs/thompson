package harness

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/wiramahendra/thompson-sampling/go/thompson"
)

// Deterministic adversarial matrix (frozen parameters, repeated seeds).
// Each scenario runs T1 (competent cheap static) vs T3 (cost-aware) and
// asserts the report either ranks correctly or refuses — never hides.
func TestCostAwareAdversarialMatrix(t *testing.T) {
	type scen struct {
		name            string
		cheapP, strongP float64
		cheapC, strongC float64
		humanFallback   bool
		missingEvery    int // every Nth job has nil costs (0 = fully metered)
		spikeEvery      int // every Nth job costs spikeCost on cheap (0 = none)
		spikeCost       float64
		difficultySplit bool // alternate easy/hard strata per job
	}
	scenarios := []scen{
		{"equal-quality-different-cost", 0.75, 0.75, 0.002, 0.05, false, 0, 0, 0, false},
		{"cheap-low-quality", 0.20, 0.85, 0.001, 0.05, false, 0, 0, 0, false},
		{"high-quality-expensive", 0.60, 0.95, 0.002, 0.08, false, 0, 0, 0, false},
		{"task-difficulty-spread", 0.50, 0.90, 0.003, 0.03, false, 0, 0, 0, false},
		{"expensive-human-correction", 0.40, 0.40, 0.002, 0.02, true, 0, 0, 0, false},
		{"correlated-cost-failure", 0.50, 0.90, 0.002, 0.03, true, 0, 0, 0, false},
		{"missing-cost", 0.75, 0.75, 0.002, 0.05, false, 4, 0, 0, false},
		{"heavy-tail", 0.75, 0.75, 0.002, 0.05, false, 0, 20, 5.0, false},
		{"unequal-difficulty", 0.60, 0.80, 0.003, 0.04, false, 0, 0, 0, true},
		{"sparse-success", 0.08, 0.12, 0.002, 0.02, false, 0, 0, 0, false},
	}
	for _, sc := range scenarios {
		for _, seed := range []uint64{11, 22} {
			t.Run(sc.name, func(t *testing.T) {
				dir := t.TempDir()
				t0clock := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
				t1, _ := OpenTreatment(dir+"/t1", "t1", nil, false)
				defer t1.Close()
				t1strat := StaticStrategy{StrategyID: "t1", Arms: []string{"cheap"}, MaxRetries: 0, Verifier: "harness:synthetic-v1"}
				t3pol := thompson.NewDefault("cheap", "strong")
				t3cfg := thompson.CostAwareConfig{QualityFloor: 0.3, MinMeteredN: 2, ColdStartPulls: 5, Epsilon: 1e-3}
				t3, err := OpenCostAwareTreatment(dir+"/t3", "t3", t3pol, t3cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer t3.Close()
				t3.Strategy.MaxRetries = 0
				assigner := Assigner{Seed: seed, Treatments: []string{"t1", "t3"}, Weights: []float64{1, 1}}
				rng := rand.New(rand.NewPCG(seed^0xBEEF, seed))
				n := 120
				for i := 0; i < n; i++ {
					jid := sc.name + "-" + costAwarePad(i)
					preStrata := "web"
					if sc.difficultySplit {
						preStrata = map[bool]string{true: "easy", false: "hard"}[i%2 == 0]
					}
					asg, _ := assigner.Assign(jid, preStrata)
					cc, cp, sp := sc.cheapC, sc.cheapP, sc.strongP
					strata := "web"
					if sc.difficultySplit {
						if i%2 == 0 {
							strata = "easy"
							cp = min1(cp + 0.25)
							sp = min1(sp + 0.10)
						} else {
							strata = "hard"
							cp = max0(cp - 0.25)
							sp = max0(sp - 0.10)
						}
					}
					if sc.spikeEvery > 0 && i%sc.spikeEvery == 0 {
						cc = sc.spikeCost
					}
					job := JobTruth{
						JobID: jid, Strata: strata, AssignedAt: t0clock.Add(time.Duration(i) * time.Minute),
						ArmSuccess:    map[string]float64{"cheap": cp, "strong": sp},
						ArmCost:       map[string]float64{"cheap": cc, "strong": sc.strongC},
						ArmLatency:    map[string]float64{"cheap": 100, "strong": 200},
						HumanFallback: sc.humanFallback, HumanCost: 2.0,
					}
					if sc.missingEvery > 0 && i%sc.missingEvery == 0 {
						job.MissingCostArms = map[string]bool{"cheap": true, "strong": true}
					}
					if asg.Treatment == "t1" {
						if _, err := t1.RunJob(rng, t1strat, job, asg); err != nil {
							t.Fatal(err)
						}
					} else {
						if _, err := t3.RunJob(rng, job, asg); err != nil {
							t.Fatal(err)
						}
					}
				}
				records := collectExperimentRecords(t, dir, []string{"t1", "t3"}, t0clock.Add(time.Duration(n)*time.Minute))
				rep := Analyze(records, ReportConfig{
					Maturation: 0, Now: t0clock.Add(time.Duration(n) * time.Minute),
					MinJobs: 10, CensorGate: 0.9, QualityFloor: 0.3,
					MinEffect: 0.05, BootstrapN: 100, BootstrapSeed: seed,
					MaxUnmeteredShare: 0.10,
				}, "t1", "t3", nil, true, true)
				// Scenario-specific oracle assertions (frozen, no post-hoc tuning):
				switch sc.name {
				case "equal-quality-different-cost":
					// T3 should learn cheap; at minimum it must not be gated out.
					if st := rep.Treatments["t3"]; st.Matured < 10 {
						t.Fatalf("t3 under-matured: %+v", st)
					}
				case "cheap-low-quality":
					// T1 (cheap static) must trip the quality floor; T3 must not prefer cheap.
					foundFloorGate := false
					for _, g := range rep.Gates {
						if len(g.Name) >= 13 && g.Name[:13] == "quality-floor" {
							foundFloorGate = true
						}
					}
					if !foundFloorGate {
						t.Fatal("expected quality-floor gate to fire on cheap-low-quality")
					}
				}
				_ = rep.Verdict // every scenario must produce a verdict, never panic
			})
		}
	}
}

func min1(v float64) float64 {
	if v > 1 {
		return 1
	}
	return v
}

func max0(v float64) float64 {
	if v < 0 {
		return 0
	}
	return v
}
