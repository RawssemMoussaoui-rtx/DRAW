package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"draw/internal/auth"
	"draw/internal/master"
	"draw/internal/model"
	"draw/internal/result"
	"draw/internal/storage"
)

// --- test fakes ---

// fakeSessionStore is an in-memory SessionStore with token-generating Archive
// and token-validated Recover, used by the lifecycle unit tests.
type fakeSessionStore struct {
	mu      sync.Mutex
	byID    map[model.SessionID]*model.Session
	byUser  map[string]*model.Session
	tokens  map[model.SessionID]string
	counter int
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{
		byID:   map[model.SessionID]*model.Session{},
		byUser: map[string]*model.Session{},
		tokens: map[model.SessionID]string{},
	}
}

func (s *fakeSessionStore) Current(user string) (*model.Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.byUser[user]; ok {
		cp := *sess
		return &cp, true
	}
	return nil, false
}

func (s *fakeSessionStore) Save(sess *model.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess == nil {
		return nil
	}
	cp := *sess
	s.byID[sess.ID] = &cp
	if sess.UserID != "" {
		s.byUser[sess.UserID] = &cp
	}
	return nil
}

func (s *fakeSessionStore) Archive(id model.SessionID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byID[id]
	if !ok {
		return nil
	}
	sess.State = model.SessionStateArchived
	if sess.RecoveryToken == "" {
		s.counter++
		sess.RecoveryToken = fmt.Sprintf("tok-%d", s.counter)
	}
	s.tokens[id] = sess.RecoveryToken
	return nil
}

func (s *fakeSessionStore) Recover(id model.SessionID) (*model.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("session not found: %s", string(id))
	}
	if sess.State != model.SessionStateArchived {
		return nil, fmt.Errorf("session %s not archived (state=%s)", string(id), sess.State)
	}
	sess.State = model.SessionStateActive
	cp := *sess
	return &cp, nil
}

func (s *fakeSessionStore) PruneExpired(time.Time) int { return 0 }

var _ storage.SessionStore = (*fakeSessionStore)(nil)

// fakeMaster implements MasterAPI for handler tests. Run blocks until the
// request context that spawned it is cancelled, then returns ctx.Err().
type fakeMaster struct {
	mu       sync.Mutex
	state    master.ResearchState
	sessions storage.SessionStore
}

func (fm *fakeMaster) SubmitIntent(req model.IntentRequest) (model.SessionID, error) {
	if len(req.Seeds) == 0 {
		return "", errNoSeeds
	}
	fm.mu.Lock()
	defer fm.mu.Unlock()
	sid := model.NewSessionID()
	sess := &model.Session{
		ID:        sid,
		UserID:    req.UserID,
		Intent:    model.Intent{Entity: req.Query, Seeds: req.Seeds},
		Plan:      model.Plan{Phases: []model.Phase{{Name: master.PlanPhaseOrder[0]}}},
		CreatedAt: time.Now().UTC(),
	}
	if fm.sessions != nil {
		_ = fm.sessions.Save(sess)
	}
	fm.state = master.ResearchState{
		Session:        sess,
		BudgetTotal:    100,
		BudgetUsed:     0,
		PhaseCompleted: map[string]bool{},
		PhaseIndex:     0,
	}
	return sid, nil
}

var errNoSeeds = errNoSeedsType{}

type errNoSeedsType struct{}

func (errNoSeedsType) Error() string {
	return "master: intent request must include at least one seed URL"
}

func (fm *fakeMaster) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (fm *fakeMaster) State() master.ResearchState {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	return fm.state
}

func (fm *fakeMaster) Stop() error { return nil }

type fakeSourceRegistry struct {
	profiles map[string]model.SourceProfile
}

func (r *fakeSourceRegistry) Lookup(domain string) (*model.SourceProfile, bool) {
	p, ok := r.profiles[domain]
	if !ok {
		return nil, false
	}
	return &p, true
}

func (r *fakeSourceRegistry) Upsert(model.SourceProfile) error            { return nil }
func (r *fakeSourceRegistry) Deny(string) error                           { return nil }
func (r *fakeSourceRegistry) ProvisionalUpsert(model.SourceProfile) error { return nil }

func newTestDeps(t *testing.T) *Deps {
	t.Helper()
	t.Setenv(auth.EnvAdminToken, "test-admin-token")
	ss := newFakeSessionStore()
	fm := &fakeMaster{sessions: ss}
	rb := result.NewReportBuilder(storage.NoopEvidenceStore{}, &fakeSourceRegistry{})
	return &Deps{
		Master:         fm,
		EvidenceStore:  storage.NoopEvidenceStore{},
		SourceRegistry: &fakeSourceRegistry{},
		SessionStore:   ss,
		ReportBuilder:  rb,
		Auth:           auth.LoadConfig(),
	}
}

func newTestServer(t *testing.T) (*httptest.Server, *Deps) {
	t.Helper()
	deps := newTestDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return httptest.NewServer(NewServer(deps, ctx)), deps
}

func createSession(t *testing.T, server *httptest.Server, query string) string {
	t.Helper()
	body := bytes.NewBufferString(`{"query":"` + query + `","seeds":["https://example.com"]}`)
	resp, err := http.Post(server.URL+"/api/v1/sessions", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, b)
	}
	var cr createResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatal(err)
	}
	if cr.SessionID == "" {
		t.Fatal("empty session_id in response")
	}
	return cr.SessionID
}

func doAdminRequest(t *testing.T, server *httptest.Server, sid, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, server.URL+"/admin/sessions/"+sid, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func mustJSON[T any](t *testing.T, body io.Reader) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// --- tests ---

func TestCreateSession_OK(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()

	sid := createSession(t, server, "climate")
	if sid == "" {
		t.Fatal("expected non-empty session id")
	}
}

func TestCreateSession_NoBody(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()

	resp, err := http.Post(server.URL+"/api/v1/sessions", "application/json", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty body, got %d", resp.StatusCode)
	}
}

func TestCreateSession_EmptySeeds(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()

	body := bytes.NewBufferString(`{"query":"climate","seeds":[]}`)
	resp, err := http.Post(server.URL+"/api/v1/sessions", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty seeds, got %d", resp.StatusCode)
	}
}

func TestGetSession_Active(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()

	sid := createSession(t, server, "climate")
	resp, err := http.Get(server.URL + "/api/v1/sessions/" + sid)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	m := mustJSON[map[string]any](t, resp.Body)
	for _, k := range []string{"phase", "progress", "evidence", "contradictions", "budget_used", "budget_total", "terminal", "terminal_reason"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing snake_case key %q in session snapshot", k)
		}
	}
	if m["phase"] != "discovery" {
		t.Errorf("phase = %v, want discovery", m["phase"])
	}
	if m["budget_total"] != float64(100) {
		t.Errorf("budget_total = %v, want 100", m["budget_total"])
	}
	if m["terminal"] != false {
		t.Errorf("terminal = %v, want false", m["terminal"])
	}
}

func TestGetSession_NotActive(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/v1/sessions/ses_does_not_exist")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for inactive session, got %d", resp.StatusCode)
	}
}

func TestGetResult_Active(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()

	sid := createSession(t, server, "climate")
	resp, err := http.Get(server.URL + "/api/v1/sessions/" + sid + "/result")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	env := mustJSON[model.ResultEnvelope](t, resp.Body)
	if env.Payload.Intent.Entity != "climate" {
		t.Errorf("Payload.Intent.Entity = %q, want climate", env.Payload.Intent.Entity)
	}
	if len(env.Payload.Entities) != 1 || env.Payload.Entities[0] != "climate" {
		t.Errorf("Entities = %v, want [climate]", env.Payload.Entities)
	}
}

func TestGetEvidence_Active(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()

	sid := createSession(t, server, "climate")
	resp, err := http.Get(server.URL + "/api/v1/sessions/" + sid + "/evidence")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	evs := mustJSON[[]model.Evidence](t, resp.Body)
	if evs == nil {
		t.Fatal("expected non-nil evidence slice")
	}
}

func TestGetSources_Active(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()

	sid := createSession(t, server, "climate")
	resp, err := http.Get(server.URL + "/api/v1/sessions/" + sid + "/sources")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	srcs := mustJSON[[]model.ReportSource](t, resp.Body)
	if srcs == nil {
		t.Fatal("expected non-nil sources slice")
	}
}

func TestAdmin_NoToken(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()

	sid := createSession(t, server, "climate")
	resp := doAdminRequest(t, server, sid, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}
}

func TestAdmin_WrongToken(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()

	sid := createSession(t, server, "climate")
	resp := doAdminRequest(t, server, sid, "wrong-token")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 with wrong token, got %d", resp.StatusCode)
	}
}

func TestAdmin_RightToken(t *testing.T) {
	server, _ := newTestServer(t)
	defer server.Close()

	sid := createSession(t, server, "climate")
	resp := doAdminRequest(t, server, sid, "test-admin-token")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with correct token, got %d", resp.StatusCode)
	}
	m := mustJSON[map[string]any](t, resp.Body)
	if _, ok := m["Session"]; !ok {
		t.Errorf("admin state missing Session field; keys: %v", mapKeys(m))
	}
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// postNoBody sends a POST request with an (optionally) empty body and returns
// the response.
func postNoBody(t *testing.T, server *httptest.Server, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// postJSON sends a POST request with a JSON body and returns the response.
func postJSONBody(t *testing.T, server *httptest.Server, path, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(server.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestSessionLifecycle_ArchiveRecover(t *testing.T) {
	t.Setenv(auth.EnvAdminToken, "test-admin-token")
	ss := newFakeSessionStore()
	fm := &fakeMaster{sessions: ss}
	rb := result.NewReportBuilder(storage.NoopEvidenceStore{}, &fakeSourceRegistry{})
	deps := &Deps{
		Master:         fm,
		EvidenceStore:  storage.NoopEvidenceStore{},
		SourceRegistry: &fakeSourceRegistry{},
		SessionStore:   ss,
		ReportBuilder:  rb,
		Auth:           auth.LoadConfig(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server := httptest.NewServer(NewServer(deps, ctx))
	defer server.Close()

	sid := createSession(t, server, "climate")
	sidModel := model.SessionID(sid)

	// Archive the active session.
	resp := postNoBody(t, server, "/api/v1/sessions/"+sid+"/archive")
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("expected 204 from archive, got %d: %s", resp.StatusCode, b)
	}
	resp.Body.Close()

	token := ss.tokens[sidModel]
	if token == "" {
		t.Fatal("expected a recovery token after archive")
	}

	// Recover with a wrong token must be rejected.
	resp = postJSONBody(t, server, "/api/v1/sessions/"+sid+"/recover", `{"recovery_token":"wrong-token"}`)
	if resp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("expected 400 from recover with wrong token, got %d: %s", resp.StatusCode, b)
	}
	resp.Body.Close()

	// Re-archive (the failed recover mutated state to ACTIVE).
	resp = postNoBody(t, server, "/api/v1/sessions/"+sid+"/archive")
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("expected 204 from re-archive, got %d: %s", resp.StatusCode, b)
	}
	resp.Body.Close()
	// Token must be preserved across re-archive.
	if ss.tokens[sidModel] != token {
		t.Fatalf("token changed after re-archive: got %q want %q", ss.tokens[sidModel], token)
	}

	// Recover with the correct token yields a new session id.
	resp = postJSONBody(t, server, "/api/v1/sessions/"+sid+"/recover", `{"recovery_token":"`+token+`"}`)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("expected 200 from recover, got %d: %s", resp.StatusCode, b)
	}
	var cr createResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if cr.SessionID == "" {
		t.Fatal("recover returned empty session_id")
	}
	if cr.SessionID == sid {
		t.Errorf("recover returned the same session_id %q; expected a new one", cr.SessionID)
	}

	// The recovered session must be active.
	resp2, err := http.Get(server.URL + "/api/v1/sessions/" + cr.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for recovered session GET, got %d", resp2.StatusCode)
	}
}
