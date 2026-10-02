// Package reasoninggoodput implements the reasoning-goodput assay
// (THOMPSON_REASONING_GOODPUT_ASSAY_V1): expensive speculative computation
// over versioned external state, preserved across concurrent state movement
// via authoritative witnesses and mechanically derived premises. Research
// harness only: stdlib plus the git CLI for the Git adapter; no servers
// beyond local httptest fixtures, no coordinators, no lock services.
package reasoninggoodput

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"time"
)

// Fail-closed errors.
var (
	ErrMissingWitness = errors.New("reasoninggoodput: no authoritative witness")
	ErrUnknown        = errors.New("reasoninggoodput: unclassifiable dependency state")
)

// ResourceRef names an external resource without versioning it.
type ResourceRef struct {
	Authority string `json:"authority"` // "git" | "http"
	ID        string `json:"id"`        // repo path | URL
}

// StateWitness binds a resource to its authoritative version identity.
// ObservedAt is diagnostics-only and never participates in comparison.
type StateWitness struct {
	Ref        ResourceRef `json:"ref"`
	Version    string      `json:"version"` // blob/commit OID | strong ETag
	ObservedAt string      `json:"observed_at"`
}

// SameVersion compares authoritative identities only.
func (w StateWitness) SameVersion(o StateWitness) bool {
	return w.Ref == o.Ref && w.Version != "" && w.Version == o.Version
}

// WitnessedRead is value bytes plus the witness observed with them.
type WitnessedRead struct {
	Value   []byte       `json:"value"`
	Witness StateWitness `json:"witness"`
	Path    string       `json:"path"` // subresource/field path, "" if whole
}

// Premise is one mechanically derived dependency of reasoning.
type Premise struct {
	Resource   string `json:"resource"`   // resource or subresource identity
	Witness    string `json:"witness"`    // authoritative version identity
	Provenance string `json:"provenance"` // binder rule: direct|field|git|opaque|control
}

// PremiseSet is a sorted, deduplicated premise collection.
type PremiseSet []Premise

// Normalize sorts and deduplicates (deterministic).
func (p PremiseSet) Normalize() PremiseSet {
	seen := map[Premise]bool{}
	var out PremiseSet
	for _, q := range p {
		if !seen[q] {
			seen[q] = true
			out = append(out, q)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Resource != out[j].Resource {
			return out[i].Resource < out[j].Resource
		}
		if out[i].Witness != out[j].Witness {
			return out[i].Witness < out[j].Witness
		}
		return out[i].Provenance < out[j].Provenance
	})
	return out
}

// Covers reports whether every premise in need is satisfied by an
// equal-or-newer... no: by an EXACT witness match in have. Witness movement
// never counts as coverage (changed version = stale premise).
func (p PremiseSet) Covers(need PremiseSet) (bool, Premise) {
	have := map[Premise]bool{}
	for _, q := range p {
		have[q] = true
	}
	for _, q := range need {
		if !have[q] {
			return false, q
		}
	}
	return true, Premise{}
}

// ReasoningSlice is one unit of expensive work with its premise closure.
type ReasoningSlice struct {
	ID        string     `json:"id"`
	Premises  PremiseSet `json:"premises"`
	Inputs    []string   `json:"inputs"` // parent slice IDs (closure edges)
	WorkUnits int64      `json:"work_units"`
	Output    []byte     `json:"output"`
	Opaque    bool       `json:"opaque"`
	Unknown   bool       `json:"unknown"`
}

// OutputDigest names the derived output.
func (s ReasoningSlice) OutputDigest() string { return DigestBytes(s.Output) }

// MutationCandidate is a proposed commit with its full premise closure.
type MutationCandidate struct {
	Op       string     `json:"op"`
	Args     []byte     `json:"args"`
	Premises PremiseSet `json:"premises"`
	Slices   []string   `json:"slices"`
	// Touched lists premise Resource identities this mutation writes
	// (e.g. "http:/doc", "git:out/result.txt"). Post-commit validation
	// excludes them: a treatment's own write necessarily moves those
	// witnesses, so treating that movement as a conflict would flag
	// every blind commit, including uncontended ones.
	Touched []string `json:"touched"`
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// DigestBytes hashes raw bytes (content identity helper).
func DigestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// DigestString hashes a string.
func DigestString(s string) string { return DigestBytes([]byte(s)) }
