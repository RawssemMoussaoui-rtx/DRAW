package api

import (
	"encoding/json"
	"net/http"

	"draw/internal/model"
)

func (s *apiServer) adminSession(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireActive(w, r); !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.deps.Master.State())
}

func (s *apiServer) adminEvents(w http.ResponseWriter, r *http.Request) {
	id := model.SessionID(r.PathValue("id"))
	NewEventSDEHandler(s.deps.Master, s.deps.EventStore, s.buildResult, id).Handler().ServeHTTP(w, r)
}

// adminRecover mirrors recoverSessionHandler for admin callers: recovers an
// archived session and re-submits its intent.
func (s *apiServer) adminRecover(w http.ResponseWriter, r *http.Request) {
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
