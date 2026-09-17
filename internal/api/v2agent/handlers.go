package v2agent

import (
	"encoding/json"
	"net/http"
	"time"

	"draw/internal/master"
	"draw/internal/model"
	"draw/internal/result"
	"draw/internal/storage"
)

// MasterAPI is the narrow Master surface the v2 agent handlers depend on.
// *master.Master satisfies it; the api.MasterAPI interface is a strict
// superset (it additionally exposes SubmitIntent, Run and Stop).
type MasterAPI interface {
	State() master.ResearchState
	Touch() error
	TriggerExternalReplan(reasonCode, targetScope string) (bool, string, map[string]int)
}

// SessionManager is the active-session tracker shared with the v1 API.
// (*api.sessionsManager) satisfies it.
type SessionManager interface {
	Submit(req model.IntentRequest) (model.SessionID, error)
	IsActive(id model.SessionID) bool
}

// V2AgentHandler wires the five external-agent endpoints to the shared
// research engine. It deliberately does NOT import package api — doing so
// would create an import cycle (api -> v2agent -> api). Instead the api layer
// constructs and wires the handler in NewServer, passing each dependency
// explicitly.
type V2AgentHandler struct {
	Master         MasterAPI
	Sessions       SessionManager
	EvidenceStore  storage.EvidenceStore
	SourceRegistry storage.SourceRegistry
	MakeSSEHandler func(model.SessionID) http.HandlerFunc
}

// requireActive validates that the routed session id corresponds to the
// currently active session. Writes 404 on failure, mirroring the v1
// requireActive behaviour.
func (h *V2AgentHandler) requireActive(w http.ResponseWriter, r *http.Request) (model.SessionID, bool) {
	id := model.SessionID(r.PathValue("id"))
	if !h.Sessions.IsActive(id) {
		http.Error(w, "not an active session", http.StatusNotFound)
		return "", false
	}
	return id, true
}

// HandleCreateSession accepts a new intent, submits it through the shared
// session manager (which calls Master.SubmitIntent and launches the Run loop
// in a goroutine), then derives the metal-prompt briefing template from the
// resulting research state.
func (h *V2AgentHandler) HandleCreateSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req CreateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.Seeds) == 0 {
		http.Error(w, "request must include at least one seed URL", http.StatusBadRequest)
		return
	}
	intent := model.IntentRequest{Query: req.Query, Seeds: req.Seeds}
	sid, err := h.Sessions.Submit(intent)
	if err != nil {
		http.Error(w, "submit failed", http.StatusInternalServerError)
		return
	}
	rs := h.Master.State()
	briefing := BuildMetalPrompt(string(sid), intent, rs)
	createdAt := time.Now().UTC()
	if rs.Session != nil {
		createdAt = rs.Session.CreatedAt
	}
	resp := CreateSessionResponse{
		SessionID:        string(sid),
		BriefingTemplate: briefing,
		CreatedAt:        createdAt,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleGetState refreshes the active session's inactivity clock (Touch),
// validates the routed id, and returns the assembled evidence state as JSON.
func (h *V2AgentHandler) HandleGetState(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireActive(w, r); !ok {
		return
	}
	if err := h.Master.Touch(); err != nil {
		http.Error(w, "not an active session", http.StatusNotFound)
		return
	}
	rs := h.Master.State()
	state := result.AssembleState(rs, h.EvidenceStore, h.SourceRegistry)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(state)
}

// HandleGetEvents streams Server-Sent Events for the active session. The SSE
// framing (historical replay + live polling) is provided by the SSEHandler
// built in package api and injected via MakeSSEHandler, keeping this package
// free of an import on package api.
func (h *V2AgentHandler) HandleGetEvents(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireActive(w, r); !ok {
		return
	}
	id := model.SessionID(r.PathValue("id"))
	h.MakeSSEHandler(id).ServeHTTP(w, r)
}

// HandleReplan forwards an external replan request to the master's
// 8-layer protection gate and returns the verdict with the post-replan
// agent budget map.
func (h *V2AgentHandler) HandleReplan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := h.requireActive(w, r); !ok {
		return
	}
	var req ReplanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ReasonCode == "" {
		http.Error(w, "reason_code is required", http.StatusBadRequest)
		return
	}
	accepted, reason, budgetAfter := h.Master.TriggerExternalReplan(req.ReasonCode, req.TargetScope)
	resp := ReplanResponse{
		Accepted:        accepted,
		RejectionReason: reason,
		BudgetAfter:     budgetAfter,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleGetResult returns a self-contained AgentResult. It must NEVER use
// ReportBuilder.Build; instead it reads fresh state, applies exclusion
// reasons (via result.AssembleState -> WithExclusionReasons), and assembles
// the result fields directly from evidence and sources.
func (h *V2AgentHandler) HandleGetResult(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireActive(w, r); !ok {
		return
	}
	if err := h.Master.Touch(); err != nil {
		http.Error(w, "not an active session", http.StatusNotFound)
		return
	}
	rs := h.Master.State()
	assembled := result.AssembleState(rs, h.EvidenceStore, h.SourceRegistry)
	resp := AgentResult{
		SessionID: string(assembled.SessionID),
		Status:    assembled.Status,
		Evidence:  assembled.Evidence,
		Sources:   assembled.Sources,
		Relations: assembled.Relations,
		Budget: map[string]int{
			"used":  rs.BudgetUsed,
			"total": rs.BudgetTotal,
		},
	}
	if rs.Terminal {
		resp.CompletedAt = rs.UpdatedAt.Format(time.RFC3339)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
