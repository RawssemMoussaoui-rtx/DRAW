package v2agent

import (
	"encoding/json"
	"strings"
	"testing"

	"draw/internal/master"
	"draw/internal/model"
)

func TestBuildMetalPrompt_Deterministic(t *testing.T) {
	intent := model.IntentRequest{
		Query:  "research climate change impacts on biodiversity, ocean acidification, polar ice melt",
		Seeds:  []string{"example.com", "test.org"},
		UserID: "user-1",
	}

	state := master.ResearchState{
		Session: &model.Session{
			ID:     model.NewSessionID(),
			UserID: intent.UserID,
			Intent: model.Intent{
				ID:           model.NewIntentID(),
				Type:         model.IntentTypeResearch,
				Entity:       "climate change",
				Topics:       []string{"biodiversity", "ocean acidification", "polar ice melt"},
				Seeds:        intent.Seeds,
				CrawlDepth:   2,
				TaskDepth:    3,
				EffortBudget: 500,
				MaxReplans:   3,
			},
			Plan: model.Plan{
				Phases: []model.Phase{
					{Name: master.PhaseDiscovery},
				},
			},
		},
		MaxReplans:        3,
		AgentMaxReplans:   1,
		InactivityTimeout: 120 * 1000000000,
	}

	sessionID := "sess-test-001"

	first := BuildMetalPrompt(sessionID, intent, state)
	second := BuildMetalPrompt(sessionID, intent, state)

	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first: %v", err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second: %v", err)
	}

	if string(firstJSON) != string(secondJSON) {
		t.Errorf("metal prompt is not deterministic\nfirst:  %s\nsecond: %s", firstJSON, secondJSON)
	}

	if first.SchemaVersion != "v3.0-metal" {
		t.Errorf("schema_version = %q, want %q", first.SchemaVersion, "v3.0-metal")
	}
	if first.SessionID != sessionID {
		t.Errorf("session_id = %q, want %q", first.SessionID, sessionID)
	}
	if first.Query.Raw != intent.Query {
		t.Errorf("query.raw = %q, want %q", first.Query.Raw, intent.Query)
	}
	if len(first.Query.Topics) != 3 {
		t.Errorf("query.topics len = %d, want 3", len(first.Query.Topics))
	}
	if first.Query.Topics[0] != "biodiversity" {
		t.Errorf("query.topics[0] = %q, want %q", first.Query.Topics[0], "biodiversity")
	}
	if len(first.Query.Entities) != 2 {
		t.Errorf("query.entities len = %d, want 2", len(first.Query.Entities))
	}
	if first.Query.Entities[0] != "example.com" {
		t.Errorf("query.entities[0] = %q, want %q", first.Query.Entities[0], "example.com")
	}

	if len(first.EvidenceSchema.Fields) != 9 {
		t.Errorf("evidence_schema.fields len = %d, want 9", len(first.EvidenceSchema.Fields))
	}
	if len(first.EvidenceSchema.VerificationStates) != 5 {
		t.Errorf("evidence_schema.verification_states len = %d, want 5", len(first.EvidenceSchema.VerificationStates))
	}
	if len(first.EvidenceSchema.ExclusionReasons) != 2 {
		t.Errorf("evidence_schema.exclusion_reasons len = %d, want 2", len(first.EvidenceSchema.ExclusionReasons))
	}
	if first.EvidenceSchema.ExclusionReasons[0] == nil || *first.EvidenceSchema.ExclusionReasons[0] != "NON_CONSENSUS_VALUE" {
		t.Errorf("exclusion_reasons[0] = %v, want NON_CONSENSUS_VALUE", first.EvidenceSchema.ExclusionReasons[0])
	}
	if first.EvidenceSchema.ExclusionReasons[1] != nil {
		t.Errorf("exclusion_reasons[1] = %v, want nil", first.EvidenceSchema.ExclusionReasons[1])
	}

	if first.Budget.InternalMax != 3 {
		t.Errorf("budget.internal_max = %d, want 3", first.Budget.InternalMax)
	}
	if first.Budget.AgentMax != 1 {
		t.Errorf("budget.agent_max = %d, want 1", first.Budget.AgentMax)
	}
	if first.Budget.InactivityTimeoutSeconds != 120 {
		t.Errorf("budget.inactivity_timeout_seconds = %d, want 120", first.Budget.InactivityTimeoutSeconds)
	}

	if first.ReplanReasonCodes.CoverageGap == "" {
		t.Error("replan_reason_codes.COVERAGE_GAP is empty")
	}
	if strings.Contains(strings.ToLower(first.ReplanReasonCodes.CoverageGap), "do ") ||
		strings.Contains(strings.ToLower(first.ReplanReasonCodes.CoverageGap), "should ") {
		t.Error("COVERAGE_GAP description should not be instructive")
	}
	if first.ReplanReasonCodes.ManualRecovery == "" {
		t.Error("replan_reason_codes.MANUAL_RECOVERY is empty")
	}
	if strings.Contains(strings.ToLower(first.ReplanReasonCodes.ManualRecovery), "do ") ||
		strings.Contains(strings.ToLower(first.ReplanReasonCodes.ManualRecovery), "should ") {
		t.Error("MANUAL_RECOVERY description should not be instructive")
	}

	if first.Endpoints.State != "/api/v2/agent/sessions/{id}/state" {
		t.Errorf("endpoints.state = %q", first.Endpoints.State)
	}
	if first.Endpoints.Events != "/api/v2/agent/sessions/{id}/events" {
		t.Errorf("endpoints.events = %q", first.Endpoints.Events)
	}
	if first.Endpoints.Replan != "/api/v2/agent/sessions/{id}/replan" {
		t.Errorf("endpoints.replan = %q", first.Endpoints.Replan)
	}
	if first.Endpoints.Result != "/api/v2/agent/sessions/{id}/result" {
		t.Errorf("endpoints.result = %q", first.Endpoints.Result)
	}
}

func TestBuildMetalPrompt_NilSession(t *testing.T) {
	intent := model.IntentRequest{
		Query: "test query",
		Seeds: []string{"seed.example.com"},
	}

	state := master.ResearchState{
		MaxReplans:        3,
		AgentMaxReplans:   1,
		InactivityTimeout: 120 * 1000000000,
	}

	mp := BuildMetalPrompt("sess-nil", intent, state)

	if len(mp.Query.Topics) != 0 {
		t.Errorf("expected empty topics for nil session, got %v", mp.Query.Topics)
	}
	if len(mp.Query.Entities) != 1 {
		t.Errorf("expected 1 entity, got %d", len(mp.Query.Entities))
	}
	if mp.Query.Entities[0] != "seed.example.com" {
		t.Errorf("entity = %q, want %q", mp.Query.Entities[0], "seed.example.com")
	}
}

func TestBuildMetalPrompt_Defaults(t *testing.T) {
	intent := model.IntentRequest{
		Query: "test",
		Seeds: []string{"default.example.com"},
	}

	state := master.ResearchState{
		InactivityTimeout: 120 * 1000000000,
	}

	mp := BuildMetalPrompt("sess-default", intent, state)

	if mp.Budget.InternalMax != 3 {
		t.Errorf("internal_max with zero state = %d, want 3", mp.Budget.InternalMax)
	}
	if mp.Budget.AgentMax != 1 {
		t.Errorf("agent_max with zero state = %d, want 1", mp.Budget.AgentMax)
	}
	if mp.Budget.InactivityTimeoutSeconds != 120 {
		t.Errorf("inactivity_timeout with zero state = %d, want 120", mp.Budget.InactivityTimeoutSeconds)
	}
}
