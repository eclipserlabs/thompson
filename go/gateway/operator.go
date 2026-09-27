package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// operatorRequest is the wire body for suspend/resume/release. Reason is
// mandatory: anonymous or unexplained safety actions are rejected.
type operatorRequest struct {
	Arm    string `json:"arm,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type operatorResponse struct {
	OK     bool   `json:"ok"`
	Action string `json:"action"`
	Arm    string `json:"arm,omitempty"`
}

// operatorAuth resolves the operator identity or rejects. A nil hook means
// no operator actions are possible (fail closed).
func (rt *Router) operatorAuth(r *http.Request) (string, bool) {
	if rt.operatorAuthHook == nil {
		return "", false
	}
	return rt.operatorAuthHook(r)
}

// SuspendHandler implements POST /v1/operator/suspend (internal listener
// only): persistently suspends one arm (or, with empty arm, emergency-stops
// all adaptive selection to the fallback).
func (rt *Router) SuspendHandler(w http.ResponseWriter, r *http.Request) {
	rt.operatorAction(w, r, "suspend", func(op string, req operatorRequest) error {
		if rt.safety == nil {
			return fmt.Errorf("safety controller not configured")
		}
		if req.Arm == "" {
			return rt.safety.EmergencyStop("operator:"+op, req.Reason)
		}
		return rt.safety.Suspend("operator:"+op, req.Arm, req.Reason)
	})
}

// ResumeHandler implements POST /v1/operator/resume: re-admits a suspended
// arm, or releases an emergency stop with arm="". Disabled arms are refused
// (re-approval required); attempts without reason are refused; everything is
// logged either way.
func (rt *Router) ResumeHandler(w http.ResponseWriter, r *http.Request) {
	rt.operatorAction(w, r, "resume", func(op string, req operatorRequest) error {
		if rt.safety == nil {
			return fmt.Errorf("safety controller not configured")
		}
		if req.Arm == "" {
			return rt.safety.EmergencyRelease("operator:"+op, req.Reason)
		}
		return rt.safety.Resume("operator:"+op, req.Arm, req.Reason)
	})
}

func (rt *Router) operatorAction(w http.ResponseWriter, r *http.Request, action string, fn func(op string, req operatorRequest) error) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !rt.checkInstance(w, r) {
		return
	}
	op, ok := rt.operatorAuth(r)
	if !ok || op == "" {
		http.Error(w, "unauthorized operator", http.StatusUnauthorized)
		return
	}
	var req operatorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("malformed operator request: %v", err), http.StatusBadRequest)
		return
	}
	if req.Reason == "" {
		http.Error(w, "operator reason is required", http.StatusBadRequest)
		return
	}
	if err := fn(op, req); err != nil {
		http.Error(w, fmt.Sprintf("operator action refused: %v", err), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(operatorResponse{OK: true, Action: action, Arm: req.Arm})
}
