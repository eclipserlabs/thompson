// Package bench implements the independent research-benchmark harness:
// versioned synthetic corpus, minimal baseline learners, and the S1–S5 /
// performance / economics experiments. It reuses the production outcome
// contract types for traces but implements its own learners and measurement
// so the comparison is independent. No customer data enters here.
package bench

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sort"
)

// CorpusVersion versions the generator family. Any generator change bumps
// it and invalidates prior fixture digests.
const CorpusVersion = 1

// DevSeeds selects static baselines; EvalSeeds score everyone. The sets are
// disjoint by construction: tuning on eval is forbidden and detectable
// (fixture digests pin both).
var DevSeeds = []uint64{11, 22, 33}

var EvalSeeds = []uint64{101, 102, 103, 104, 105}

// ArmTruth is one strategy's ground truth on one job.
type ArmTruth struct {
	SuccessP float64  `json:"success_p"`
	CostUSD  *float64 `json:"cost_usd"`
}

// JobSpec is one corpus job: ground truth plus the observation schedule.
type JobSpec struct {
	JobID    string              `json:"job_id"`
	Strata   string              `json:"strata"`
	Arms     map[string]ArmTruth `json:"arms"`
	VerifyAt int64               `json:"verify_at"`
	Missing  map[string]bool     `json:"missing_cost_arms,omitempty"`
	// CorrectTo maps attempt index -> corrected verdict ("success"/"failure")
	// applied as a retrospective v2 settlement.
	CorrectTo string `json:"correct_to,omitempty"`
	// Duplicate emits the v1 settlement twice (idempotency probe).
	Duplicate bool `json:"duplicate,omitempty"`
	// Unresolved emits no settlement at all (censoring probe).
	Unresolved bool `json:"unresolved,omitempty"`
	// HumanFix appends a human rescue attempt at the stated cost.
	HumanFix *float64 `json:"human_fix_cost,omitempty"`
}

// Scenario is one named, versioned workload with frozen parameters.
type Scenario struct {
	Name   string    `json:"name"`
	Params string    `json:"params"`
	Jobs   []JobSpec `json:"jobs"`
	Digest string    `json:"digest"`
}

func fptr(v float64) *float64 { return &v }

// Generate builds the scenario deterministically from seed. Current tick t
// (job index) drives nonstationarity; all draws after the header use the
// seeded stream in job order.
func Generate(name string, seed uint64, n int) Scenario {
	rng := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
	mk := func(jid, strata string, arms map[string]ArmTruth) JobSpec {
		return JobSpec{JobID: jid, Strata: strata, Arms: arms, VerifyAt: int64(rng.Uint64() % 5)}
	}
	var jobs []JobSpec
	switch name {
	case "equal-cost-gap":
		for i := 0; i < n; i++ {
			jobs = append(jobs, mk(fmt.Sprintf("job-%05d", i), "web", map[string]ArmTruth{
				"cheap":  {SuccessP: 0.75, CostUSD: fptr(0.002)},
				"strong": {SuccessP: 0.75, CostUSD: fptr(0.05)},
			}))
		}
	case "quality-gap":
		for i := 0; i < n; i++ {
			jobs = append(jobs, mk(fmt.Sprintf("job-%05d", i), "web", map[string]ArmTruth{
				"cheap":  {SuccessP: 0.40, CostUSD: fptr(0.002)},
				"strong": {SuccessP: 0.90, CostUSD: fptr(0.05)},
			}))
		}
	case "static-matches":
		// Cheap is both cheapest and best: the competent static IS optimal.
		for i := 0; i < n; i++ {
			jobs = append(jobs, mk(fmt.Sprintf("job-%05d", i), "web", map[string]ArmTruth{
				"cheap":  {SuccessP: 0.90, CostUSD: fptr(0.002)},
				"strong": {SuccessP: 0.85, CostUSD: fptr(0.05)},
			}))
		}
	case "deterioration":
		for i := 0; i < n; i++ {
			cp := 0.90
			if i >= n/2 {
				cp = 0.10
			}
			jobs = append(jobs, mk(fmt.Sprintf("job-%05d", i), "web", map[string]ArmTruth{
				"cheap":  {SuccessP: cp, CostUSD: fptr(0.002)},
				"strong": {SuccessP: 0.85, CostUSD: fptr(0.05)},
			}))
		}
	case "drift":
		for i := 0; i < n; i++ {
			f := float64(i) / float64(n)
			jobs = append(jobs, mk(fmt.Sprintf("job-%05d", i), "web", map[string]ArmTruth{
				"cheap":  {SuccessP: 0.90 - 0.5*f, CostUSD: fptr(0.002 + 0.06*f)},
				"strong": {SuccessP: 0.85, CostUSD: fptr(0.05)},
			}))
		}
	case "delayed-missing":
		for i := 0; i < n; i++ {
			j := mk(fmt.Sprintf("job-%05d", i), "web", map[string]ArmTruth{
				"cheap":  {SuccessP: 0.75, CostUSD: fptr(0.002)},
				"strong": {SuccessP: 0.75, CostUSD: fptr(0.05)},
			})
			j.VerifyAt = int64(i % 30)
			if i%4 == 0 {
				j.Missing = map[string]bool{"cheap": true, "strong": true}
			}
			if i%8 == 7 {
				j.Unresolved = true
			}
			jobs = append(jobs, j)
		}
	case "corrections":
		for i := 0; i < n; i++ {
			j := mk(fmt.Sprintf("job-%05d", i), "web", map[string]ArmTruth{
				"cheap":  {SuccessP: 0.75, CostUSD: fptr(0.002)},
				"strong": {SuccessP: 0.75, CostUSD: fptr(0.05)},
			})
			if i%10 == 9 {
				j.CorrectTo = "failure" // v1 success later revoked
			}
			if i%10 == 4 {
				j.Duplicate = true
			}
			jobs = append(jobs, j)
		}
	case "fallback-chains":
		for i := 0; i < n; i++ {
			j := mk(fmt.Sprintf("job-%05d", i), "web", map[string]ArmTruth{
				"cheap":  {SuccessP: 0.50, CostUSD: fptr(0.002)},
				"strong": {SuccessP: 0.90, CostUSD: fptr(0.03)},
			})
			jobs = append(jobs, j)
		}
	case "human-trap":
		for i := 0; i < n; i++ {
			hc := 2.0
			j := mk(fmt.Sprintf("job-%05d", i), "web", map[string]ArmTruth{
				"cheap":  {SuccessP: 0.0, CostUSD: fptr(0.001)},
				"strong": {SuccessP: 0.90, CostUSD: fptr(0.05)},
			})
			j.HumanFix = &hc
			jobs = append(jobs, j)
		}
	case "missing-costs":
		for i := 0; i < n; i++ {
			j := mk(fmt.Sprintf("job-%05d", i), "web", map[string]ArmTruth{
				"cheap":  {SuccessP: 0.90, CostUSD: fptr(0.002)},
				"strong": {SuccessP: 0.90, CostUSD: fptr(0.05)},
			})
			if (i*7)%10 < 9 {
				j.Missing = map[string]bool{"cheap": true}
			}
			jobs = append(jobs, j)
		}
	case "heavy-tail":
		for i := 0; i < n; i++ {
			cc := 0.002
			if i%20 == 0 {
				cc = 5.0
			}
			jobs = append(jobs, mk(fmt.Sprintf("job-%05d", i), "web", map[string]ArmTruth{
				"cheap":  {SuccessP: 0.75, CostUSD: fptr(cc)},
				"strong": {SuccessP: 0.75, CostUSD: fptr(0.05)},
			}))
		}
	case "difficulty-split":
		for i := 0; i < n; i++ {
			strata, cp, sp := "easy", 0.95, 0.90
			if i%2 == 1 {
				strata, cp, sp = "hard", 0.35, 0.70
			}
			jobs = append(jobs, mk(fmt.Sprintf("job-%05d", i), strata, map[string]ArmTruth{
				"cheap":  {SuccessP: cp, CostUSD: fptr(0.003)},
				"strong": {SuccessP: sp, CostUSD: fptr(0.04)},
			}))
		}
	case "intervention":
		for i := 0; i < n; i++ {
			cp := 0.90
			if i >= n/3 {
				cp = 0.10
			}
			jobs = append(jobs, mk(fmt.Sprintf("job-%05d", i), "web", map[string]ArmTruth{
				"cheap":  {SuccessP: cp, CostUSD: fptr(0.002)},
				"strong": {SuccessP: 0.85, CostUSD: fptr(0.05)},
			}))
		}
	default:
		panic("bench: unknown scenario " + name)
	}
	sc := Scenario{Name: name, Params: fmt.Sprintf("v%d/seed=%d/n=%d", CorpusVersion, seed, n), Jobs: jobs}
	sc.Digest = Digest(sc)
	return sc
}

// Digest assigns the content digest over canonical JSON excluding Digest.
func Digest(sc Scenario) string {
	c := sc
	c.Digest = ""
	b, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("corpus-v%d-%x", CorpusVersion, sum[:8])
}

// SortedNames lists scenarios deterministically for runners.
func SortedNames(ss []Scenario) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}
