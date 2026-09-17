package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"
)

var (
	reSessionID            = regexp.MustCompile(`ses_\d+`)
	reEvidenceID           = regexp.MustCompile(`evd_\d+`)
	reTimestamp            = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)
	reSecondsSinceActivity = regexp.MustCompile(`"seconds_since_last_activity":\s*[\d.eE+-]+`)
)

func normalizeV2(t *testing.T, data []byte) string {
	t.Helper()
	s := string(data)
	s = reSessionID.ReplaceAllString(s, "SESSION_ID")
	s = reTimestamp.ReplaceAllString(s, "TIMESTAMP")
	s = reEvidenceID.ReplaceAllString(s, "EVIDENCE_ID")
	s = reSecondsSinceActivity.ReplaceAllString(s, `"seconds_since_last_activity":0.0`)
	return s
}

func createV2Session(t *testing.T, server *httptest.Server, query string) string {
	t.Helper()
	body := `{"query":"` + query + `","seeds":["https://example.com"]}`
	resp := postJSONBody(t, server, "/api/v2/agent/sessions", body)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: want 201, got %d: %s", resp.StatusCode, data)
	}
	cr := mustJSON[v2CreateResp](t, bytes.NewReader(data))
	if cr.SessionID == "" {
		t.Fatal("empty session_id in create response")
	}
	return cr.SessionID
}

func getV2Body(t *testing.T, server *httptest.Server, path string) string {
	t.Helper()
	resp, err := http.Get(server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: want 200, got %d: %s", path, resp.StatusCode, data)
	}
	return normalizeV2(t, data)
}

func getV2Events(t *testing.T, server *httptest.Server, path string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: want 200, got %d", path, resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	return normalizeV2(t, data)
}

func postV2Replan(t *testing.T, server *httptest.Server, sid, body string) string {
	t.Helper()
	resp := postJSONBody(t, server, "/api/v2/agent/sessions/"+sid+"/replan", body)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replan: want 200, got %d: %s", resp.StatusCode, data)
	}
	return normalizeV2(t, data)
}

func runV2BasicSequence(t *testing.T, server *httptest.Server, query string) string {
	t.Helper()
	sid := createV2Session(t, server, query)
	state := getV2Body(t, server, "/api/v2/agent/sessions/"+sid+"/state")
	events := getV2Events(t, server, "/api/v2/agent/sessions/"+sid+"/events")
	replan := postV2Replan(t, server, sid, `{"reason_code":"COVERAGE_GAP","target_scope":"discovery"}`)
	result := getV2Body(t, server, "/api/v2/agent/sessions/"+sid+"/result")
	return state + "\n" + events + "\n" + replan + "\n" + result
}

func assertDeterminism(t *testing.T, n int, fn func() string) {
	t.Helper()
	results := make([]string, n)
	for i := 0; i < n; i++ {
		results[i] = fn()
	}
	for i := 1; i < n; i++ {
		if results[i] != results[0] {
			t.Errorf("iteration %d differs from iteration 0\n--- first ---\n%s\n--- iteration %d ---\n%s",
				i, results[0], i, results[i])
		}
	}
}

func TestV2Agent_SeqBasic_Determinism(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()

	assertDeterminism(t, 5, func() string {
		return runV2BasicSequence(t, server, "climate change")
	})
}

func TestV2Agent_SeqReplanRejected_Determinism(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()

	sid := createV2Session(t, server, "climate change")
	assertDeterminism(t, 5, func() string {
		return postV2Replan(t, server, sid, `{"reason_code":"INVALID","target_scope":"discovery"}`)
	})
}

func TestV2Agent_SeqReplanOnce_Determinism(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()

	sid := createV2Session(t, server, "climate change")
	assertDeterminism(t, 5, func() string {
		return postV2Replan(t, server, sid, `{"reason_code":"COVERAGE_GAP","target_scope":"discovery"}`)
	})
}

func TestV2Agent_SeqInactivity_Determinism(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()

	sid := createV2Session(t, server, "climate change")
	assertDeterminism(t, 5, func() string {
		r1 := getV2Body(t, server, "/api/v2/agent/sessions/"+sid+"/state")
		r2 := getV2Body(t, server, "/api/v2/agent/sessions/"+sid+"/state")
		return r1 + "\n" + r2
	})
}

func TestV3AgentReplayIndependentOfSessionID(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()

	first := runV2BasicSequence(t, server, "climate change")
	second := runV2BasicSequence(t, server, "climate change")
	if first != second {
		t.Errorf("responses differ between sessions despite normalization\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}
