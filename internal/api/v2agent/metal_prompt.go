package v2agent

import (
	"draw/internal/master"
	"draw/internal/model"
)

const MetalPromptSchemaVersion = "v3.0-metal"

var EvidenceSchemaFields = []string{
	"id",
	"claim",
	"value",
	"topic",
	"source_id",
	"origin_url",
	"verification_state",
	"extraction_seq",
	"exclusion_reason",
}

var EvidenceVerificationStates = []string{
	"UNVERIFIED",
	"PARTIALLY_VERIFIED",
	"VERIFIED",
	"DISPUTED",
	"UNRESOLVED",
}

var EndpointPaths = Endpoints{
	State:  "/api/v2/agent/sessions/{id}/state",
	Events: "/api/v2/agent/sessions/{id}/events",
	Replan: "/api/v2/agent/sessions/{id}/replan",
	Result: "/api/v2/agent/sessions/{id}/result",
}

var ReasonCodes = ReplanReasonCodes{
	CoverageGap:    "evidence coverage missing for an existing topic or entity in the session",
	ManualRecovery: "manual recovery request for session progress deadlock",
}

type MetalPrompt struct {
	SchemaVersion     string            `json:"schema_version"`
	SessionID         string            `json:"session_id"`
	Query             MetalQuery        `json:"query"`
	EvidenceSchema    EvidenceSchema    `json:"evidence_schema"`
	Budget            Budget            `json:"budget"`
	ReplanReasonCodes ReplanReasonCodes `json:"replan_reason_codes"`
	Endpoints         Endpoints         `json:"endpoints"`
}

type MetalQuery struct {
	Raw      string   `json:"raw"`
	Topics   []string `json:"topics"`
	Entities []string `json:"entities"`
}

type EvidenceSchema struct {
	Fields             []string  `json:"fields"`
	VerificationStates []string  `json:"verification_states"`
	ExclusionReasons   []*string `json:"exclusion_reasons"`
}

type Budget struct {
	InternalMax              int `json:"internal_max"`
	AgentMax                 int `json:"agent_max"`
	InactivityTimeoutSeconds int `json:"inactivity_timeout_seconds"`
}

type ReplanReasonCodes struct {
	CoverageGap    string `json:"COVERAGE_GAP"`
	ManualRecovery string `json:"MANUAL_RECOVERY"`
}

type Endpoints struct {
	State  string `json:"state"`
	Events string `json:"events"`
	Replan string `json:"replan"`
	Result string `json:"result"`
}

func BuildMetalPrompt(sessionID string, intent model.IntentRequest, state master.ResearchState) MetalPrompt {
	var topics []string
	if state.Session != nil {
		topics = append([]string(nil), state.Session.Intent.Topics...)
	}

	entities := append([]string(nil), intent.Seeds...)

	internalMax := state.MaxReplans
	if internalMax <= 0 {
		internalMax = 3
	}
	agentMax := state.AgentMaxReplans
	if agentMax <= 0 {
		agentMax = 1
	}
	inactivitySeconds := int(state.InactivityTimeout.Seconds())
	if inactivitySeconds <= 0 {
		inactivitySeconds = 120
	}

	nonConsensus := string(model.ExclusionReasonNonConsensusValue)

	return MetalPrompt{
		SchemaVersion: MetalPromptSchemaVersion,
		SessionID:     sessionID,
		Query: MetalQuery{
			Raw:      intent.Query,
			Topics:   topics,
			Entities: entities,
		},
		EvidenceSchema: EvidenceSchema{
			Fields:             EvidenceSchemaFields,
			VerificationStates: EvidenceVerificationStates,
			ExclusionReasons:   []*string{&nonConsensus, nil},
		},
		Budget: Budget{
			InternalMax:              internalMax,
			AgentMax:                 agentMax,
			InactivityTimeoutSeconds: inactivitySeconds,
		},
		ReplanReasonCodes: ReasonCodes,
		Endpoints:         EndpointPaths,
	}
}
