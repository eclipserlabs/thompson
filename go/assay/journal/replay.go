package journal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
)

// Projections are the entire derived state, rebuilt deterministically from
// the single commit sequence. Nothing here is authoritative: delete it all
// and replay reproduces it exactly.
type Projections struct {
	// Quality maps arm -> {alpha, beta, pulls} over learnable settled
	// outcomes attributed to the deciding arm (binary mapping).
	Quality map[string]ArmPosterior `json:"quality"`
	// CostMean maps arm -> mean fully-loaded cost over fully-metered
	// learnable outcomes; Unmetered counts the rest (never zero-filled).
	CostMean  map[string]float64 `json:"cost_mean"`
	Unmetered map[string]uint64  `json:"unmetered"`
	// Explored counts committed exploration picks per arm (folded from
	// decision rows; must equal the exploration table — verified).
	Explored map[string]uint64 `json:"explored"`
	// Safety maps arm -> state; Emergency is the global stop flag.
	Safety    map[string]string `json:"safety"`
	Emergency bool              `json:"emergency"`
	// Applied maps job -> latest settled version (learner cursor).
	Applied map[string]uint64 `json:"applied"`
	// AtSeq is the ledger length this projection covers.
	AtSeq uint64 `json:"at_seq"`
}

// ArmPosterior is one arm's Beta quality state.
type ArmPosterior struct {
	Alpha float64 `json:"alpha"`
	Beta  float64 `json:"beta"`
	Pulls uint64  `json:"pulls"`
}

// Replay folds every committed event in sequence order into projections.
func (j *Journal) Replay() (*Projections, error) {
	evs, err := j.EventsSince(0)
	if err != nil {
		return nil, err
	}
	p := &Projections{
		Quality: map[string]ArmPosterior{}, CostMean: map[string]float64{},
		Unmetered: map[string]uint64{}, Explored: map[string]uint64{},
		Safety: map[string]string{}, Applied: map[string]uint64{},
	}
	sums := map[string]float64{}
	counts := map[string]uint64{}
	latestOutcome := map[string]StoredEvent{}
	// Latest version per job first (corrections supersede; stale ignored).
	for _, e := range evs {
		if e.Kind != "outcome" {
			continue
		}
		if cur, ok := latestOutcome[e.JobID]; !ok || e.Version > cur.Version {
			latestOutcome[e.JobID] = e
		}
	}
	seenOutcome := map[string]bool{}
	for _, e := range evs {
		switch e.Kind {
		case "decision":
			var d Decision
			if err := json.Unmarshal([]byte(e.Payload), &d); err != nil {
				return nil, fmt.Errorf("journal: replay decision %d: %w", e.Seq, err)
			}
			if d.Explore {
				p.Explored[d.SelectedArm]++
			}
		case "outcome":
			if latestOutcome[e.JobID].Version != e.Version || latestOutcome[e.JobID].Seq != e.Seq {
				continue
			}
			if seenOutcome[e.JobID] {
				continue
			}
			seenOutcome[e.JobID] = true
			var o SettledOutcome
			if err := json.Unmarshal([]byte(e.Payload), &o); err != nil {
				return nil, fmt.Errorf("journal: replay outcome %d: %w", e.Seq, err)
			}
			p.Applied[o.JobID] = o.Version
			if o.Status != StatusAccepted && o.Status != StatusRejected {
				continue // censored: no estimator moves
			}
			arm := ""
			for _, a := range o.Attempts {
				if a.AttemptID == o.DecidingAttempt {
					arm = a.ArmID
					break
				}
			}
			if arm == "" {
				continue // armless (human) decision: strategy accounting only
			}
			q, ok := p.Quality[arm]
			if !ok {
				q = ArmPosterior{Alpha: 1, Beta: 1} // uniform prior
			}
			if o.Status == StatusAccepted {
				q.Alpha++
			} else {
				q.Beta++
			}
			q.Pulls++
			p.Quality[arm] = q
			metered, unmetered := outcomeCost(o)
			if unmetered > 0 {
				p.Unmetered[arm]++
				continue
			}
			sums[arm] += metered
			counts[arm]++
		case "safety":
			var s SafetyTransition
			if err := json.Unmarshal([]byte(e.Payload), &s); err != nil {
				return nil, fmt.Errorf("journal: replay safety %d: %w", e.Seq, err)
			}
			switch s.Type {
			case "ARM_SUSPENDED":
				p.Safety[s.Arm] = "SUSPENDED"
			case "ARM_RESUMED":
				if p.Safety[s.Arm] == "SUSPENDED" {
					p.Safety[s.Arm] = "PREQUALIFIED"
				}
			case "ARM_DISABLED":
				p.Safety[s.Arm] = "DISABLED"
			case "EMERGENCY_STOP":
				p.Emergency = true
			case "EMERGENCY_RELEASED":
				p.Emergency = false
			}
		}
		if e.Seq > p.AtSeq {
			p.AtSeq = e.Seq
		}
	}
	for arm, sum := range sums {
		p.CostMean[arm] = sum / float64(counts[arm])
	}
	return p, nil
}

func outcomeCost(o SettledOutcome) (float64, int) {
	metered := 0.0
	unmetered := 0
	human := false
	for _, a := range o.Attempts {
		if a.CostUSD == nil {
			unmetered++
			continue
		}
		metered += *a.CostUSD
	}
	for _, a := range o.Attempts {
		if a.ArmID == "" {
			human = true
		}
	}
	if o.HumanReviewCost == nil {
		if human {
			unmetered++
		}
	} else {
		metered += *o.HumanReviewCost
	}
	return metered, unmetered
}

// VerifyExploration cross-checks the transactional exploration table
// against replay: they must agree exactly, or the database is corrupt
// (fail closed rather than serve either number).
func (j *Journal) VerifyExploration() error {
	p, err := j.Replay()
	if err != nil {
		return err
	}
	rows, err := j.db.Query(`SELECT arm, used FROM exploration`)
	if err != nil {
		return err
	}
	defer rows.Close()
	table := map[string]uint64{}
	for rows.Next() {
		var arm string
		var used uint64
		if err := rows.Scan(&arm, &used); err != nil {
			return err
		}
		table[arm] = used
	}
	if err := rows.Err(); err != nil {
		return err
	}
	arms := map[string]bool{}
	for a := range table {
		arms[a] = true
	}
	for a := range p.Explored {
		arms[a] = true
	}
	names := make([]string, 0, len(arms))
	for a := range arms {
		names = append(names, a)
	}
	sort.Strings(names)
	for _, a := range names {
		if table[a] != p.Explored[a] {
			return fmt.Errorf("journal: exploration table %d != replay %d for %q (corrupt?)", table[a], p.Explored[a], a)
		}
	}
	return nil
}

// SaveCheckpoint persists a named projection snapshot (audit/cached fast
// path only; recovery works without it by full replay).
func (j *Journal) SaveCheckpoint(name string, p *Projections) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = j.db.Exec(`INSERT INTO checkpoints(name, state, ledger_seq) VALUES(?,?,?) ON CONFLICT(name) DO UPDATE SET state=excluded.state, ledger_seq=excluded.ledger_seq`,
		name, string(raw), p.AtSeq)
	return err
}

// LoadCheckpoint reads a named snapshot; corrupt or missing snapshots
// return an error the caller must answer with full replay (never with
// guessed state).
func (j *Journal) LoadCheckpoint(name string) (*Projections, error) {
	var raw string
	var seq uint64
	err := j.db.QueryRow(`SELECT state, ledger_seq FROM checkpoints WHERE name=?`, name).Scan(&raw, &seq)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("journal: no checkpoint %q", name)
	}
	if err != nil {
		return nil, err
	}
	var p Projections
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("journal: corrupt checkpoint %q: %w", name, err)
	}
	return &p, nil
}
