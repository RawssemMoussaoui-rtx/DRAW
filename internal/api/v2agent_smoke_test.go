package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestV2AgentEndpoints(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()

	// POST /api/v2/agent/sessions
	body := `{"query":"climate change","seeds":["https://example.com"]}`
	resp, err := http.Post(server.URL+"/api/v2/agent/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create: want 201, got %d: %s", resp.StatusCode, b)
	}
	var cr v2CreateResp
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if cr.SessionID == "" {
		t.Fatal("empty session_id")
	}
	if cr.BriefingTemplate.SchemaVersion != "v3.0-metal" {
		t.Fatalf("schema_version=%q", cr.BriefingTemplate.SchemaVersion)
	}
	if cr.CreatedAt == "" {
		t.Fatal("created_at is empty")
	}

	// GET .../state
	resp, err = http.Get(server.URL + "/api/v2/agent/sessions/" + cr.SessionID + "/state")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("state: want 200, got %d: %s", resp.StatusCode, b)
	}
	var es struct {
		SessionID string `json:"session_id"`
		Status    string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&es); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if es.SessionID != cr.SessionID {
		t.Fatalf("state session_id=%q want %q", es.SessionID, cr.SessionID)
	}

	// GET .../result (no ReportBuilder.Build used — uses AssembleState)
	resp, err = http.Get(server.URL + "/api/v2/agent/sessions/" + cr.SessionID + "/result")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("result: want 200, got %d: %s", resp.StatusCode, b)
	}
	var ar struct {
		SessionID string         `json:"session_id"`
		Status    string         `json:"status"`
		Budget    map[string]int `json:"budget"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ar.SessionID != cr.SessionID {
		t.Fatalf("result session_id=%q want %q", ar.SessionID, cr.SessionID)
	}
	if ar.Budget["total"] != 100 {
		t.Fatalf("budget total=%v want 100", ar.Budget["total"])
	}

	// POST .../replan
	rebody := `{"reason_code":"COVERAGE_GAP","target_scope":"discovery"}`
	resp, err = http.Post(server.URL+"/api/v2/agent/sessions/"+cr.SessionID+"/replan", "application/json", strings.NewReader(rebody))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("replan: want 200, got %d: %s", resp.StatusCode, b)
	}
	var rr struct {
		Accepted        bool   `json:"accepted"`
		RejectionReason string `json:"rejection_reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if rr.Accepted {
		t.Fatal("fakeMaster always rejects")
	}
	if rr.RejectionReason != "not_implemented" {
		t.Fatalf("rejection_reason=%q want not_implemented", rr.RejectionReason)
	}

	// GET .../events — expect event stream headers
	resp, err = http.Get(server.URL + "/api/v2/agent/sessions/" + cr.SessionID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events: want 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("events content-type=%q want text/event-stream", ct)
	}
	resp.Body.Close()
}

type v2CreateResp struct {
	SessionID        string `json:"session_id"`
	BriefingTemplate struct {
		SchemaVersion string `json:"schema_version"`
	} `json:"briefing_template"`
	CreatedAt string `json:"created_at"`
}
