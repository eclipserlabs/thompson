package harness

import (
	"testing"
	"time"
)

// collectExperimentRecords loads per-treatment ledgers and folds them into
// matured job records with identity jobmap (harness-local experiment).
func collectExperimentRecords(t *testing.T, root string, txs []string, now time.Time) []JobRecord {
	t.Helper()
	var all []JobRecord
	for _, tx := range txs {
		asg, evs, err := LoadTreatmentDir(root + "/" + tx)
		if err != nil {
			t.Fatal(err)
		}
		recs := MatureJobs(asg, evs, now, 0)
		all = append(all, recs...)
	}
	return all
}
