package realreplay

import (
	"context"
	"fmt"
	"path/filepath"
)

// SmokeReport summarizes the two unscored setup invocations.
type SmokeReport struct {
	S0Calls      int               `json:"s0_calls"`
	S0Tools      map[string]int    `json:"s0_tools"`
	Premises     []Premise         `json:"s0_premises"`
	FirstStale   int               `json:"first_stale_call_under_H1"`
	Continuation ContinuationStats `json:"continuation"`
	ResumeCalls  int               `json:"resume_calls"`
	ResumeTokens []Tokens          `json:"resume_call_tokens"`
	S0Tokens     []Tokens          `json:"s0_call_tokens"`
	Notes        []string          `json:"notes"`
}

// Smoke runs the unscored setup check: one HTTP S0 execution (smoke-1) and
// one real resume of it under H1 (smoke-2). It validates capture, ETag
// binding, continuation import and resume mechanics before the freeze.
func (r *Runner) Smoke(ctx context.Context) (*SmokeReport, error) {
	rep := &SmokeReport{S0Tools: map[string]int{}}
	base := filepath.Join(r.Work, "smoke")
	if err := ClearDir(base); err != nil {
		return nil, err
	}
	ws := filepath.Join(r.Work, "ws-smoke")
	aw, aw0, err := NewAuthority(filepath.Join(base, "authw"), HTTPWorkspace())
	if err != nil {
		return nil, err
	}
	srv, err := NewHTTPAuthority(HTTPAddr, HTTPS0())
	if err != nil {
		return nil, err
	}
	defer srv.Close()
	if err := aw.Materialize(ws, aw0); err != nil {
		return nil, err
	}
	start := httpEtags(srv)
	s0, err := r.invoke(ctx, "smoke-1", ws, TaskHTTP, "")
	if err != nil {
		return rep, err
	}
	if err := checkAlignment(s0, 0); err != nil {
		rep.Notes = append(rep.Notes, err.Error())
	}
	if err := checkSnapshot0(s0, HTTPWorkspace()); err != nil {
		rep.Notes = append(rep.Notes, err.Error())
	}
	rep.S0Calls = len(s0.Stream.Calls)
	for _, c := range s0.Stream.Calls {
		rep.S0Tokens = append(rep.S0Tokens, c.Tokens)
		for _, t := range c.Tools {
			rep.S0Tools[t.Tool]++
		}
	}
	w := &World{Workspace: ws, Git: aw, Base: aw0, HTTP: srv, HTTPLog: srv.Log(), HTTPStart: start}
	ps, err := w.Premises(s0.Stream.Calls, 0)
	if err != nil {
		return rep, err
	}
	rep.Premises = ps
	for p, b := range HTTPMutations["H1"] {
		srv.Set(p, b)
	}
	v, err := Validate(ps, aw, srv)
	if err != nil {
		return rep, err
	}
	rep.FirstStale = v.FirstStale
	if v.FirstStale <= 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("first stale call %d: no preserved prefix to resume", v.FirstStale))
		return rep, nil
	}
	snap, err := SnapshotFiles(s0.Stream.Calls[v.FirstStale].Snapshot)
	if err != nil {
		return rep, err
	}
	if err := setWorkspace(aw, ws, aw0, snap); err != nil {
		return rep, err
	}
	c, st, err := r.resume(ctx, "smoke-2", ws, s0.Export, v.FirstStale, "Sm01")
	rep.Continuation = st
	if c != nil {
		rep.ResumeCalls = len(c.Stream.Calls)
		for _, k := range c.Stream.Calls {
			rep.ResumeTokens = append(rep.ResumeTokens, k.Tokens)
		}
	}
	if err != nil {
		rep.Notes = append(rep.Notes, err.Error())
	}
	return rep, nil
}
