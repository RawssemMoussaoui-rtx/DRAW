package api

import "net/http"

func (s *apiServer) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/sessions", s.createSession())
	mux.HandleFunc("GET /api/v1/sessions/{id}", s.getSession())
	mux.HandleFunc("POST /api/v1/sessions/{id}/stop", s.stopSession())
	mux.HandleFunc("POST /api/v1/sessions/{id}/archive", s.archiveSessionHandler())
	mux.HandleFunc("POST /api/v1/sessions/{id}/recover", s.recoverSessionHandler())
	mux.HandleFunc("GET /api/v1/sessions/{id}/result", s.getResult())
	mux.HandleFunc("GET /api/v1/sessions/{id}/evidence", s.getEvidence())
	mux.HandleFunc("GET /api/v1/sessions/{id}/sources", s.getSources())
	mux.HandleFunc("GET /api/v1/sessions/{id}/events", s.getEvents())
	mux.HandleFunc("POST /api/v2/agent/sessions", s.v2.HandleCreateSession)
	mux.HandleFunc("GET /api/v2/agent/sessions/{id}/state", s.v2.HandleGetState)
	mux.HandleFunc("GET /api/v2/agent/sessions/{id}/events", s.v2.HandleGetEvents)
	mux.HandleFunc("POST /api/v2/agent/sessions/{id}/replan", s.v2.HandleReplan)
	mux.HandleFunc("GET /api/v2/agent/sessions/{id}/result", s.v2.HandleGetResult)
	// admin-telemetry + admin SSE (reqing §0.11 §12.2; Q3=A): auth-gated; payload = full state (scheduler-stats deferred to Phase H)
	authed := s.deps.Auth
	mux.Handle("GET /admin/sessions/{id}", http.HandlerFunc(authed.RequireAdmin(s.adminSession)))
	mux.Handle("GET /admin/sessions/{id}/events", http.HandlerFunc(authed.RequireAdmin(s.adminEvents)))
	mux.Handle("POST /admin/sessions/{id}/recover", http.HandlerFunc(authed.RequireAdmin(s.adminRecover)))
	// Chatbox SPA fallback (served at "/")
	mux.Handle("/", NewChatbox())
}
