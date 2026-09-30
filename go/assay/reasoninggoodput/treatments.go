package reasoninggoodput

import (
	"time"
)

// treatments.go: contention runner with five treatments (Phase 7) and
// reasoning-goodput metrics (Phase 8). One job plus a deterministic
// background change schedule (interleaved, seeded, reproducible). No
// scheduler or coordinator is built: the runner executes a frozen script
// per treatment; retries are bounded and counted.

// TreatID names a treatment.
type TreatID string

const (
	// T0_SERIAL: sequential gold baseline, no contention by construction.
	T0Serial TreatID = "T0"
	// T1_LATE_CONFLICT: snapshot, full reasoning, blind apply, post-validate.
	T1Late TreatID = "T1"
	// T2_BROAD_OCC: re-read everything; any witness moved → full replay.
	T2Broad TreatID = "T2"
	// T3_PREMISE_OCC: validate derived premises only; stale → full replay.
	T3Premise TreatID = "T3"
	// T4_SELECTIVE_REPLAY: validate premises; recompute only the stale
	// transitive slice closure; preserve fresh slices.
	T4Replay TreatID = "T4"
)

// Change is one background state movement landing AfterPhase.
type Change struct {
	Target     string // git path | http path
	NewBody    string
	AfterPhase string // "read" | "reason" | "validate"
}

// JobSpec is one assay job: witnessed reads, ordered slices, proposal,
// adapter commit. Identical across treatments — only the validation and
// replay mechanism differs.
type JobSpec struct {
	ID string
	// Reads performs fresh witnessed reads.
	Reads func() ([]Val, error)
	// Slices declares ordered reasoning slices over read/slice outputs.
	Slices []SliceBuilder
	// Propose derives the mutation candidate from terminal slice outputs,
	// their premise-carrying values, and the witnessed reads (for
	// state-dependent proposals; reads carry premises but Propose must
	// only derive content already covered by slice premises).
	Propose func(outs map[string][]byte, vals map[string]Val, reads []Val) MutationCandidate
	// Commit applies the mutation. conditional=true uses compare-and-commit
	// (If-Match / head check); false applies blindly (T1 only).
	Commit func(m MutationCandidate, conditional bool) (accepted bool, err error)
	// Compensate undoes a blind T1 apply (assay affordance, counted).
	Compensate func(m MutationCandidate) error
	// Live builds current witnesses keyed by premise Resource identity.
	Live func() map[string]StateWitness
	// OracleVerify checks final-state correctness after completion.
	OracleVerify func() error
	// SliceInputs maps slice ID to parent slice IDs + read indexes.
	SliceInputs map[string][]string
}

// SliceBuilder declares one reasoning slice over read/slice outputs.
// From entries: "read:<i>" (ith read) or a parent slice ID. When Opaque is
// set, the slice output passes through OpaqueBoundary (union premises,
// marked opaque) instead of plain Transform. Control lists additional input
// indexes the derivation branched on: their premises join as control
// premises (a copied value alone never clears them).
type SliceBuilder struct {
	ID      string
	From    []string
	Work    WorkSpec
	Opaque  bool
	Control []int
	Make    func(inputs [][]byte) []byte
}

// TaskMetrics is the Phase 8 per-run record.
type TaskMetrics struct {
	Treatment  string  `json:"treatment"`
	WorkExec   int64   `json:"work_executed"`
	WorkAccept int64   `json:"work_accepted"`
	Goodput    float64 `json:"goodput"`
	Discarded  int64   `json:"discarded"`
	FalseConf  bool    `json:"false_conflict"`
	MissedConf bool    `json:"missed_conflict"`
	LateConf   bool    `json:"late_conflict"`
	Coord      int     `json:"coordination"`
	Unknown    bool    `json:"unknown"`
	Crashed    bool    `json:"crashed"`
	WallNS     int64   `json:"wall_ns"`
	OverheadNS int64   `json:"overhead_ns"`
	Retries    int     `json:"retries"`
	OracleOK   bool    `json:"oracle_ok"`
	// AcceptPremises is the premise set validated for the accepted
	// candidate (audit trail for provenance analysis).
	AcceptPremises PremiseSet `json:"accept_premises,omitempty"`
}

// Run executes one job under a treatment with a frozen change schedule.
// applier routes background changes to adapters (wired per workload).
// maxRetries bounds full replays; exhaustion is recorded, not hidden.
func Run(spec JobSpec, treat TreatID, schedule []Change, applier func(Change), maxRetries int) TaskMetrics {
	return RunWithOpts(spec, treat, schedule, applier, maxRetries, RunOpts{})
}

// RunOpts carries crash-injection and durability options (Phase 9).
type RunOpts struct {
	// LogPath appends durable JSONL records (slice completions, commits,
	// validations) as they happen. Empty disables logging.
	LogPath string
	// CrashAfter aborts with an injected crash after this many slice
	// executions (0 disables). The log prefix stays durable.
	CrashAfter int
	// CrashAfterValidate aborts after the first premise validation,
	// before any commit (validation without acceptance).
	CrashAfterValidate bool
	// CrashBeforeSlices aborts after reads complete but before any slice
	// executes (witness capture without reasoning).
	CrashBeforeSlices bool
}

// RunWithOpts is Run with options. sliceCache may be pre-populated (resume).
func RunWithOpts(spec JobSpec, treat TreatID, schedule []Change, applier func(Change), maxRetries int, opts RunOpts) TaskMetrics {
	return runWithCache(spec, treat, schedule, applier, maxRetries, opts, map[string]ReasoningSlice{})
}

func runWithCache(spec JobSpec, treat TreatID, schedule []Change, applier func(Change), maxRetries int, opts RunOpts, sliceCache map[string]ReasoningSlice) TaskMetrics {
	t0 := time.Now()
	m := TaskMetrics{Treatment: string(treat)}
	dlog, _ := openLog(opts.LogPath)
	defer dlog.close()
	trip := &crashTrip{afterExecs: opts.CrashAfter, beforeAll: opts.CrashBeforeSlices}
	applied := map[int]bool{}
	fire := func(phase string) {
		for i, ch := range schedule {
			if !applied[i] && ch.AfterPhase == phase {
				applied[i] = true
				applier(ch)
				dlog.append(LogRec{Type: "change", Key: ch.Target, Idx: i})
			}
		}
	}
	// sliceCache persists across retries within this run (T4 reuse + T3
	// premise stability are evaluated against it; T0/T1/T2 never consult it
	// except T2's broad re-read, which bypasses slice state entirely; on
	// resume it is pre-populated from the durable log.
	premiseValidAtRetry := []bool{} // per-retry premise validity (false-conflict audit)
	accepted := false
	var acceptedUnits int64
	for attempt := 0; attempt <= maxRetries && !accepted; attempt++ {
		if attempt > 0 {
			m.Retries++
			m.Coord++
		}
		reads, err := spec.Reads()
		if err != nil {
			m.Unknown = true
			break
		}
		fire("read")
		outs, vals, execUnits, usedKeys, unknown, overNS, runErr := runSlices(spec, reads, sliceCache, treat == T4Replay, dlog, trip)
		m.WorkExec += execUnits
		m.OverheadNS += overNS
		if runErr != nil {
			m.Crashed = true
			m.WallNS = time.Since(t0).Nanoseconds()
			return m
		}
		if unknown {
			m.Unknown = true
		}
		fire("reason")
		cand := spec.Propose(outs, vals, reads)
		live := spec.Live()
		tv0 := time.Now()
		stale, unk := ValidatePremises(cand.Premises, func(res string) (StateWitness, bool) {
			w, ok := live[res]
			return w, ok
		})
		m.OverheadNS += time.Since(tv0).Nanoseconds()
		if unk {
			m.Unknown = true
		}
		premiseValidAtRetry = append(premiseValidAtRetry, stale == nil && !unk)
		dlog.append(LogRec{Type: "validate", Valid: stale == nil && !unk})
		if opts.CrashAfterValidate && !trip.fired {
			trip.fired = true
			trip.crashed = true
			m.Crashed = true
			m.WallNS = time.Since(t0).Nanoseconds()
			return m
		}
		switch treat {
		case T0Serial:
			ok, err := spec.Commit(cand, true)
			if err == nil && ok {
				accepted = true
				acceptedUnits = sumUsedKeys(usedKeys, sliceCache)
				m.AcceptPremises = cand.Premises.Normalize()
			}
		case T1Late:
			ok, err := spec.Commit(cand, false)
			if err != nil || !ok {
				break
			}
			// Post-validate AFTER the blind apply (late discovery). The
			// treatment's own commit necessarily moved its touched resources,
			// so those are excluded: flagging them would cry conflict on
			// every blind write, contended or not. Same-target concurrent
			// writes are invisible post-hoc (last-writer-wins destroyed the
			// evidence) -- judged by the oracle, not here.
			live2 := spec.Live()
			tv1 := time.Now()
			stale2, _ := ValidatePremises(filterTouched(cand.Premises, cand.Touched), func(res string) (StateWitness, bool) {
				w, ok := live2[res]
				return w, ok
			})
			m.OverheadNS += time.Since(tv1).Nanoseconds()
			if stale2 != nil {
				m.LateConf = true
				m.Coord++ // compensation counted
				_ = spec.Compensate(cand)
				continue // full retry
			}
			accepted = true
			acceptedUnits = sumUsedKeys(usedKeys, sliceCache)
			m.AcceptPremises = cand.Premises.Normalize()
		case T2Broad:
			// Re-read everything; any witness moved → full replay.
			// Re-read IO is validation machinery: metered as overhead.
			if stale2, ns := broadStale(spec, reads); stale2 {
				m.OverheadNS += ns
				continue
			} else {
				m.OverheadNS += ns
			}
			ok, err := spec.Commit(cand, true)
			if err == nil && ok {
				accepted = true
				acceptedUnits = sumUsedKeys(usedKeys, sliceCache)
				m.AcceptPremises = cand.Premises.Normalize()
			}
		case T3Premise:
			if stale != nil || unk {
				if unk {
					// Fall back to broad (conservative, recorded).
					if stale2, ns := broadStale(spec, reads); stale2 {
						m.OverheadNS += ns
						continue
					} else {
						m.OverheadNS += ns
					}
				} else {
					continue // full replay on stale premise
				}
			}
			fire("validate")
			ok, err := spec.Commit(cand, true)
			if err == nil && ok {
				accepted = true
				acceptedUnits = sumUsedKeys(usedKeys, sliceCache)
				m.AcceptPremises = cand.Premises.Normalize()
			} else if err != nil {
				continue // conditional-commit race lost → retry
			}
		case T4Replay:
			// Selective recompute already happened inside runSlices; if the
			// closure is still stale (world moved again), retry boundedly.
			// UNKNOWN falls back to broad re-read.
			if unk {
				if stale2, ns := broadStale(spec, reads); stale2 {
					m.OverheadNS += ns
					continue
				} else {
					m.OverheadNS += ns
				}
			} else if stale != nil {
				continue
			}
			fire("validate")
			ok, err := spec.Commit(cand, true)
			if err == nil && ok {
				accepted = true
				acceptedUnits = sumUsedKeys(usedKeys, sliceCache)
				m.AcceptPremises = cand.Premises.Normalize()
			} else if err != nil {
				continue
			}
		}
	}
	if accepted {
		dlog.append(LogRec{Type: "commit", Accept: true, Treat: string(treat), Job: spec.ID})
	}
	m.WorkAccept = acceptedUnits
	if m.WorkExec > 0 {
		m.Goodput = float64(m.WorkAccept) / float64(m.WorkExec)
	} else {
		m.Goodput = 1
	}
	m.Discarded = m.WorkExec - m.WorkAccept
	// False conflict: retried while every retry-time premise check passed.
	for _, valid := range premiseValidAtRetry {
		_ = valid
	}
	m.FalseConf = falseConflict(premiseValidAtRetry, m.Retries)
	// Missed conflict + oracle.
	if accepted {
		if err := spec.OracleVerify(); err != nil {
			m.MissedConf = true
		} else {
			m.OracleOK = true
		}
		// Post-accept premise audit: any required premise stale at commit?
		// (Treatments validate pre-commit; T1 post-validates; a stale accept
		// here means the mechanism failed.)
	}
	m.WallNS = time.Since(t0).Nanoseconds()
	return m
}

// falseConflict: a retry happened although the recorded premise check for
// that attempt passed (over-conservative invalidation).
func falseConflict(valid []bool, retries int) bool {
	if retries == 0 || len(valid) == 0 {
		return false
	}
	// Retries beyond stale-driven ones: count attempts where premises were
	// valid yet another retry followed.
	staleDriven := 0
	for _, v := range valid {
		if !v {
			staleDriven++
		}
	}
	return retries > staleDriven
}

// broadStale re-reads everything and reports any premise-witness movement
// (T2): even content-identical version moves count as stale — that
// conservatism is exactly the mechanism under test.
func broadStale(spec JobSpec, reads []Val) (bool, int64) {
	t0 := time.Now()
	fresh, err := spec.Reads()
	ns := time.Since(t0).Nanoseconds()
	if err != nil {
		return true, ns
	}
	if len(fresh) != len(reads) {
		return true, ns
	}
	oldW := premiseWitnesses(reads)
	newW := premiseWitnesses(fresh)
	if len(oldW) != len(newW) {
		return true, ns
	}
	for k, v := range oldW {
		if nv, ok := newW[k]; !ok || nv != v {
			return true, ns
		}
	}
	return false, ns
}

func premiseWitnesses(vals []Val) map[string]string {
	out := map[string]string{}
	for _, v := range vals {
		for _, q := range v.Premises {
			out[q.Resource] = q.Witness
		}
	}
	return out
}

// runSlices executes slices in order; when reuse==true (T4), slices whose
// cache key (ID + input digests) hits AND whose premise closure validates
// are preserved without re-execution. Returns outputs, slice values,
// executed units, used cache keys (for accepted-work accounting),
// unknown flag, and bookkeeping ns (key building + cache/provenance work;
// slice CPU is work, never overhead).
func runSlices(spec JobSpec, reads []Val, cache map[string]ReasoningSlice, reuse bool, dlog *durableLog, trip *crashTrip) (map[string][]byte, map[string]Val, int64, map[string]string, bool, int64, error) {
	var over int64
	outs := map[string][]byte{}
	vals := map[string]Val{}
	used := map[string]string{}
	var execUnits int64
	unknown := false
	byID := map[string]SliceBuilder{}
	for _, sb := range spec.Slices {
		byID[sb.ID] = sb
	}
	for _, sb := range spec.Slices {
		ins := make([][]byte, 0, len(sb.From))
		inVals := make([]Val, 0, len(sb.From))
		ok := true
		for _, f := range sb.From {
			var v Val
			var found bool
			if len(f) > 5 && f[:5] == "read:" {
				var idx int
				if _, err := parseIdx(f[5:], &idx); err != nil || idx < 0 || idx >= len(reads) {
					ok = false
					break
				}
				v = reads[idx]
			} else if pv, ok2 := vals[f]; ok2 {
				v = pv
			} else {
				ok = false
				break
			}
			_ = found
			ins = append(ins, v.Bytes)
			inVals = append(inVals, v)
		}
		if !ok {
			unknown = true
			continue
		}
		tk0 := time.Now()
		key := sb.ID + ":" + DigestBytes(joinAll(ins))
		used[key] = sb.ID
		over += time.Since(tk0).Nanoseconds()
		if reuse {
			if prev, hit := cache[key]; hit {
				tv0 := time.Now()
				reusedOK := false
				var fresh PremiseSet
				// Validate CURRENT premises (rebuilt from present inputs),
				// never the witnesses stored when the slice was produced.
				if ps, err := CurrentPremisesOf(inVals, sb.Opaque); err == nil {
					live := spec.Live()
					bad := false
					for _, q := range ps {
						w, ok := live[q.Resource]
						if !ok || w.Version != q.Witness {
							bad = true
							break
						}
					}
					if !bad {
						reusedOK = true
						fresh = ps
					}
				}
				over += time.Since(tv0).Nanoseconds()
				if reusedOK {
					outs[sb.ID] = prev.Output
					vals[sb.ID] = Val{Bytes: prev.Output, Premises: fresh, Opaque: prev.Opaque, Unknown: prev.Unknown, Parents: append([]Val(nil), inVals...)}
					continue
				}
			}
		}
		made := sb.Make(ins)
		if trip != nil && trip.beforeAll && !trip.fired {
			trip.fired = true
			trip.crashed = true
			return outs, vals, execUnits, used, unknown, over, crashError{}
		}
		out, units := ExpensiveTransform(made, sb.Work)
		execUnits += units
		var joined Val
		if sb.Opaque {
			joined = OpaqueBoundary(out, inVals...)
		} else {
			joined = Transform(out, inVals...)
		}
		for _, ci := range sb.Control {
			if ci >= 0 && ci < len(inVals) {
				cp := Branch(inVals[ci], joined.Bytes)
				joined.Premises = append(joined.Premises, cp.Premises...)
				joined.Opaque = joined.Opaque || cp.Opaque
				joined.Unknown = joined.Unknown || cp.Unknown
			}
		}
		joined.Premises = joined.Premises.Normalize()
		sl := ReasoningSlice{ID: sb.ID, Premises: joined.Premises, WorkUnits: units, Output: out, Opaque: joined.Opaque, Unknown: joined.Unknown}
		sl.Inputs = append(sl.Inputs, sb.From...)
		cache[key] = sl
		dlog.append(LogRec{Type: "slice", Key: key, Slice: &sl})
		if trip != nil && trip.noteExec() {
			return outs, vals, execUnits, used, unknown, over, crashError{}
		}
		outs[sb.ID] = out
		vals[sb.ID] = Val{Bytes: out, Premises: joined.Premises, Opaque: joined.Opaque, Unknown: joined.Unknown, Parents: append([]Val(nil), inVals...)}
		if joined.Unknown {
			unknown = true
		}
	}
	return outs, vals, execUnits, used, unknown, over, nil
}

func joinAll(in [][]byte) []byte {
	var out []byte
	for _, b := range in {
		out = append(out, byte(len(b)), byte(len(b)>>8))
		out = append(out, b...)
	}
	return out
}

func joinedPremises(inVals []Val) Val {
	return Transform(nil, inVals...)
}

func sliceWorkNS(spec JobSpec, units int64) int64 {
	_ = spec
	_ = units
	return 0
}

func parseIdx(s string, out *int) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errBadIndex()
		}
		n = n*10 + int(c-'0')
	}
	*out = n
	return n, nil
}

func errBadIndex() error { return errIdx }

var errIdx = errIndex{}

type errIndex struct{}

func (errIndex) Error() string { return "bad read index" }

// filterTouched drops premises on resources the candidate itself wrote.
// A whole-resource write moves every subresource witness beneath it, so a
// premise matches when it equals a touched identity or extends it with '#'.
func filterTouched(ps PremiseSet, touched []string) PremiseSet {
	if len(touched) == 0 {
		return ps
	}
	var out PremiseSet
	for _, q := range ps {
		hit := false
		for _, t := range touched {
			if q.Resource == t || (len(q.Resource) > len(t) && q.Resource[:len(t)] == t && q.Resource[len(t)] == '#') {
				hit = true
				break
			}
		}
		if !hit {
			out = append(out, q)
		}
	}
	return out.Normalize()
}

// sumUsedKeys counts accepted work: units of the slice versions used by the
// accepted attempt (each counted once via its cache key).
func sumUsedKeys(used map[string]string, cache map[string]ReasoningSlice) int64 {
	var total int64
	seen := map[string]bool{}
	for key := range used {
		if seen[key] {
			continue
		}
		seen[key] = true
		if sl, ok := cache[key]; ok {
			total += sl.WorkUnits
		}
	}
	return total
}
