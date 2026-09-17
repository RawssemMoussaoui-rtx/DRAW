package v2agent

import (
	"time"

	"draw/internal/model"
	"draw/internal/result"
)

// CreateSessionRequest is the payload for POST /api/v2/agent/sessions.
type CreateSessionRequest struct {
	Query string   `json:"query"`
	Seeds []string `json:"seeds"`
}

// CreateSessionResponse returns the new session id together with the metal
// prompt briefing template that external agents use to drive research.
type CreateSessionResponse struct {
	SessionID        string      `json:"session_id"`
	BriefingTemplate MetalPrompt `json:"briefing_template"`
	CreatedAt        time.Time   `json:"created_at"`
}

// ReplanRequest is the payload for POST /api/v2/agent/sessions/{id}/replan.
type ReplanRequest struct {
	ReasonCode  string `json:"reason_code"`
	TargetScope string `json:"target_scope"`
}

// ReplanResponse reports whether the external replan was accepted by the
// master's 8-layer protection gate, along with the post-replan agent budget.
type ReplanResponse struct {
	Accepted        bool           `json:"accepted"`
	RejectionReason string         `json:"rejection_reason"`
	BudgetAfter     map[string]int `json:"budget_after"`
}

// AgentResult is the self-contained research result returned to external
// agents. It is built directly from master state and evidence exclusion
// reasons (result.WithExclusionReasons, applied inside AssembleState); it
// deliberately never invokes ReportBuilder.Build.
type AgentResult struct {
	SessionID   string                   `json:"session_id"`
	Status      string                   `json:"status"`
	Evidence    []model.Evidence         `json:"evidence"`
	Sources     []model.ReportSource     `json:"sources"`
	Relations   []result.RelationSummary `json:"relations"`
	Budget      map[string]int           `json:"budget"`
	CompletedAt string                   `json:"completed_at"`
}
