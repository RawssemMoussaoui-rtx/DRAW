package api

import (
	"encoding/json"
	"net/http"

	"draw/internal/model"
)

type createResponse struct {
	SessionID string `json:"session_id"`
}

// requireActive validates that the routed session id corresponds to the
// currently active session. It writes a 404 on failure.
func (s *apiServer) requireActive(w http.ResponseWriter, r *http.Request) (model.SessionID, bool) {
	id := model.SessionID(r.PathValue("id"))
	if !s.sm.IsActive(id) {
		http.Error(w, "not an active session", http.StatusNotFound)
		return "", false
	}
	return id, true
}

func (s *apiServer) createSession() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req model.IntentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(req.Seeds) == 0 {
			http.Error(w, "request must include at least one seed URL", http.StatusBadRequest)
			return
		}
		sid, err := s.sm.Submit(req)
		if err != nil {
			http.Error(w, "submit failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(createResponse{SessionID: string(sid)})
	}
}

func (s *apiServer) getSession() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.requireActive(w, r); !ok {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(toStateResponse(s.deps.Master.State()))
	}
}

func (s *apiServer) stopSession() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if _, ok := s.requireActive(w, r); !ok {
			return
		}
		if err := s.sm.Stop(); err != nil {
			http.Error(w, "stop failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("stopped"))
	}
}

// archiveSessionHandler archives the session so it can be recovered later.
// Returns 204 on success. The session need not be active in the manager
// (it may already be terminal); archiving is idempotent.
func (s *apiServer) archiveSessionHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := model.SessionID(r.PathValue("id"))
		if err := archiveSession(s.deps.SessionStore, id); err != nil {
			http.Error(w, "archive failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// recoverSessionHandler recovers a previously archived session, re-submits its
// intent, and returns the new session id.
func (s *apiServer) recoverSessionHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := model.SessionID(r.PathValue("id"))
		var body struct {
			RecoveryToken string `json:"recovery_token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		newSid, err := recoverSession(s.sm, s.deps.SessionStore, id, body.RecoveryToken)
		if err != nil {
			http.Error(w, "recover failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(createResponse{SessionID: string(newSid)})
	}
}

// ServeHTTP makes *apiServer usable directly as an http.Handler, which
// httptest.NewServer(NewServer(deps)) requires. Production serving goes
// through Serve, which assembles the mux once; this path rebuilds the small
// route tree per request, which is negligible for the test suite.
func (s *apiServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	s.routes(mux)
	mux.ServeHTTP(w, r)
}
