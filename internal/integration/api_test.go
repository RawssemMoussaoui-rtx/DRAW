package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"draw/internal/api"
	"draw/internal/auth"
	"draw/internal/browser"
	"draw/internal/config"
	"draw/internal/frontier"
	"draw/internal/manager"
	"draw/internal/managers"
	"draw/internal/master"
	"draw/internal/model"
	"draw/internal/orchestrator"
	"draw/internal/result"
	"draw/internal/storage"
)

// newAPITestGraph builds the real execution graph: frontier + resource controller
// + scheduler, with the supplied worker and browser manager wired into the
// router facade. It additionally wires the SQLite SessionStore and EventStore
// onto the Master (so SubmitIntent persists sessions and emitted master events
// are durably stored for SSE replay). The in-memory SQLite db (MaxOpenConns(1))
// is shared by es/reg/sessions/events, exactly like newEvidenceTestGraph.
func newAPITestGraph(t *testing.T, w manager.Worker, bmgr browser.BrowserManager) (*master.Master, *orchestrator.Scheduler, storage.SourceRegistry, *storage.SQLiteEvidenceStore, *storage.SQLiteSessionStore, *storage.SQLiteEventStore) {
	t.Helper()
	cfg := config.Defaults()

	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("storage open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := storage.Apply(context.Background(), db); err != nil {
		t.Fatalf("storage Apply: %v", err)
	}

	es, err := storage.NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteEvidenceStore: %v", err)
	}
	reg, err := storage.NewSQLiteSourceRegistry(db, 0.3)
	if err != nil {
		t.Fatalf("NewSQLiteSourceRegistry: %v", err)
	}
	ss, err := storage.NewSQLiteSessionStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteSessionStore: %v", err)
	}
	evs, err := storage.NewSQLiteEventStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteEventStore: %v", err)
	}

	fr := frontier.NewMemoryFrontier(cfg, reg)
	rc := orchestrator.NewResourceController(cfg, orchestrator.NoopResourceSampler{})
	sched := orchestrator.NewScheduler(cfg, fr, rc, reg, orchestrator.DefaultTaskCapabilities())

	web := managers.NewWebManager(w)
	news := managers.NewNewsManager(w)
	social := managers.NewSocialManager()
	specialized := managers.NewSpecializedManager()
	brow := managers.NewBrowserAdapter(bmgr, 2*time.Minute)
	router := managers.NewRouterManager(web, news, brow, social, specialized)
	if err := sched.RegisterManager(router, router.Capabilities()); err != nil {
		t.Fatalf("register manager: %v", err)
	}

	m := master.NewMaster(cfg, sched,
		master.WithSQLiteSessions(ss),
		master.WithEvidenceStore(es),
		master.WithSourceRegistry(reg),
		master.WithEventObserver(&eventObserverAdapter{es: evs}),
	)
	return m, sched, reg, es, ss, evs
}

// eventObserverAdapter bridges master.EventObserver (which emits master.Event)
// to storage.EventStore (which persists storage.Event). The master.Event struct
// intentionally omits the storage surrogate key (ID); the adapter leaves it as
// zero and SQLite auto-assigns it on append. This mirrors the production wiring
// in cmd/rd-engine/main.go.
type eventObserverAdapter struct {
	es storage.EventStore
}

var _ master.EventObserver = (*eventObserverAdapter)(nil)

func (a *eventObserverAdapter) Emit(ev master.Event) {
	_ = a.es.Append(storage.Event{
		SessionID: ev.SessionID,
		TaskID:    ev.TaskID,
		Kind:      ev.Kind,
		Level:     ev.Level,
		Message:   ev.Message,
		Data:      ev.Data,
		TS:        ev.TS,
	})
}

// denylistWorker wraps a manager.Worker, returning RetrievalStatusBlocked for
// tasks whose URL domain is denied in the wrapped source registry. All other
// task types are delegated to the inner worker unchanged.
type denylistWorker struct {
	mu    sync.Mutex
	inner manager.Worker
	reg   storage.SourceRegistry
}

var _ manager.Worker = (*denylistWorker)(nil)

func (w *denylistWorker) Run(t model.Task) (model.TaskResult, error) {
	if t.URL != nil {
		w.mu.Lock()
		reg := w.reg
		w.mu.Unlock()
		if reg != nil {
			p, ok := reg.Lookup(strings.ToLower(t.URL.Hostname()))
			if ok && p != nil && p.Denied {
				return model.TaskResult{Status: model.RetrievalStatusBlocked}, nil
			}
		}
	}
	return w.inner.Run(t)
}

func (w *denylistWorker) Close() error { return w.inner.Close() }

// setRegistry wires the source registry after construction (it is only known
// after newAPITestGraph builds it). Called before Run starts, so no data race.
func (w *denylistWorker) setRegistry(reg storage.SourceRegistry) {
	w.mu.Lock()
	w.reg = reg
	w.mu.Unlock()
}

// injectContradictionFetches submits three FetchHTTP tasks (alpha/beta/gamma)
// directly to the scheduler, mirroring the proven contradiction circuit in
// evidence_test.go (lines 171-194). CreatedAt is deterministic (base +
// 0/1/2 ms) so the injected set is stable across runs.
func injectContradictionFetches(t *testing.T, sched *orchestrator.Scheduler, sid model.SessionID) {
	t.Helper()
	hosts := []string{"alpha.example", "beta.example", "gamma.example"}
	base := time.Now().UTC()
	for i, host := range hosts {
		u, err := url.Parse("https://" + host + "/api")
		if err != nil {
			t.Fatalf("parse url: %v", err)
		}
		task := model.Task{
			ID:            model.NewTaskID(),
			SessionID:     sid,
			Type:          model.TaskTypeFetchHTTP,
			State:         model.TaskStateReady,
			Priority:      500,
			SourceClass:   model.SourceClassUnknown,
			SourceTarget:  host,
			URL:           u,
			EstimatedCost: 1,
			TaskKey:       fmt.Sprintf("evidence:fetch:%d", i),
			CreatedAt:     base.Add(time.Duration(i) * time.Millisecond),
		}
		if err := sched.Submit(task); err != nil {
			t.Fatalf("sched.Submit: %v", err)
		}
	}
}

// createSessionViaAPI POSTs a session creation request through the real API
// server and returns the resulting session id, asserting a 201 response with a
// decoded session_id (POST /api/v1/sessions -> 201 {"session_id":"..."}).
func createSessionViaAPI(t *testing.T, server *httptest.Server) model.SessionID {
	t.Helper()
	body := `{"query":"research Acme Corp, revenue","seeds":["https://alpha.example"],"userId":"u1"}`
	resp, err := http.Post(server.URL+"/api/v1/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201 from POST /api/v1/sessions, got %d: %s", resp.StatusCode, b)
	}
	var cr struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	if cr.SessionID == "" {
		t.Fatal("empty session_id in POST /api/v1/sessions response")
	}
	return model.SessionID(cr.SessionID)
}

// sseEvent is a single parsed Server-Sent Event frame.
type sseEvent struct {
	event string
	data  string
}

// parseSSEEvents splits an SSE byte stream into events. Each event block is
// terminated by a blank line (CRLF normalized to LF for robustness).
func parseSSEEvents(body string) []sseEvent {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	var events []sseEvent
	for _, block := range strings.Split(body, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		var ev, data string
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(line, "event:"):
				ev = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
		if ev != "" || data != "" {
			events = append(events, sseEvent{event: ev, data: data})
		}
	}
	return events
}

// sseReport consumes the SSE stream for a session until the report event is
// emitted (the stream closes right after the report, once the master reaches a
// terminal state). It fatal-errors on a 30s timeout or on a missing report
// event.
func sseReport(t *testing.T, server *httptest.Server, sid model.SessionID) model.ResultEnvelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/api/v1/sessions/"+string(sid)+"/events", nil)
	if err != nil {
		t.Fatalf("build sse request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sse GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("sse: expected 200, got %d: %s", resp.StatusCode, b)
	}
	body, _ := io.ReadAll(resp.Body)

	var env model.ResultEnvelope
	found := false
	for _, e := range parseSSEEvents(string(body)) {
		if e.event != "report" || e.data == "" {
			continue
		}
		if err := json.Unmarshal([]byte(e.data), &env); err != nil {
			t.Fatalf("unmarshal report event: %v", err)
		}
		found = true
	}
	if !found {
		t.Fatalf("no report event in SSE stream (got %d events)", len(parseSSEEvents(string(body))))
	}
	return env
}

// postNoBody sends a POST request with an empty body and returns the response
// (the caller owns the body and must close it).
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

// postJSONBody sends a POST request with the given JSON body and returns the
// response (the caller owns the body and must close it).
func postJSONBody(t *testing.T, server *httptest.Server, path, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(server.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// circuitResult holds the artefacts produced by runContradictionCircuit so that
// Phase-H tests can reuse the server, stores, and session id.
type circuitResult struct {
	Env       model.ResultEnvelope
	Evidence  *storage.SQLiteEvidenceStore
	Sessions  *storage.SQLiteSessionStore
	Events    *storage.SQLiteEventStore
	Server    *httptest.Server
	SessionID model.SessionID
}

// runContradictionCircuit wires the real master through the real API server,
// creates a session, injects the three contradictory fetch tasks, and reads the
// SSE report.
//
// Q5 harness-race repair: the contradiction tasks are injected into the scheduler
// BEFORE the master's Run loop starts draining. Previously, createSessionViaAPI
// launched `go master.Run` via sessionsManager.Submit, and injectContradictionFetches
// raced against that goroutine — Run could drain the single seed Discover task to
// terminal (drained()) before the three FetchHTTP tasks entered the queue. The
// fix: call SubmitIntent directly (which queues the seed task but does NOT start
// Run), inject the three tasks, then start Run. The SSE stream is still served
// through the real API (GET /sessions/{id}/events), so end-to-end coverage of
// the SSE/report path is preserved. createSessionViaAPI/sseReport helper
// signatures are unchanged and are reused by the Phase-H tests.
func runContradictionCircuit(t *testing.T) circuitResult {
	t.Helper()
	w := newContradictionWorker()
	bmgr := &fakeBrowserManager{}
	m, sched, reg, es, ss, evs := newAPITestGraph(t, w, bmgr)
	deps := &api.Deps{
		Master:         m,
		EvidenceStore:  es,
		SourceRegistry: reg,
		EventStore:     evs,
		SessionStore:   ss,
		ReportBuilder:  result.NewReportBuilder(es, reg),
		Auth:           auth.Config{},
	}
	serverCtx, serverCancel := context.WithCancel(context.Background())
	t.Cleanup(func() { serverCancel() })
	server := httptest.NewServer(api.NewServer(deps, serverCtx))
	t.Cleanup(func() { server.Close() })

	// Q5: SubmitIntent queues the seed Discover task but does NOT start Run,
	// so we can inject the contradiction tasks before the async drain begins.
	req := model.IntentRequest{
		Query:  "research Acme Corp, revenue",
		Seeds:  []string{"https://alpha.example"},
		UserID: "u1",
	}
	sid, err := m.SubmitIntent(req)
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}
	injectContradictionFetches(t, sched, sid)

	go func() {
		_ = m.Run(serverCtx)
		_ = ss.Archive(sid)
	}()

	env := sseReport(t, server, sid)
	return circuitResult{
		Env:       env,
		Evidence:  es,
		Sessions:  ss,
		Events:    evs,
		Server:    server,
		SessionID: sid,
	}
}

// TestPhaseG_APIE2E_ContradictionCircuit proves the Phase-G API E2E circuit:
//
//	POST /api/v1/sessions  -> 201 {session_id}  (seed Discover queued)
//	3x FetchHTTP (alpha/beta/gamma -> revenue 100/200/300) via injectContradictionFetches
//	master.Run drains them -> evidence.Extract -> ComputeRelations(CONTRADICTS)
//	  -> ComputeVerification(DISPUTED) -> DecisionEngine replan (Verify+Reconcile)
//	master reaches terminal -> SSE emits report event (stream closes)
//
// The report envelope must carry the contradiction output, and the in-memory
// SQLite evidence store must persist DISPUTED evidence that the report is
// derived from.
func TestPhaseG_APIE2E_ContradictionCircuit(t *testing.T) {
	r := runContradictionCircuit(t)

	if r.Env.Status == "" || !strings.Contains(strings.ToLower(r.Env.Status), "compl") {
		t.Errorf("expected a terminal 'complete'-derived status, got %q", r.Env.Status)
	}
	if len(r.Env.Payload.Sources) < 1 {
		t.Errorf("env.Payload.Sources = %d, want >= 1", len(r.Env.Payload.Sources))
	}
	if len(r.Env.Payload.Findings) < 1 {
		t.Errorf("env.Payload.Findings = %d, want >= 1", len(r.Env.Payload.Findings))
	}
	if len(r.Env.Payload.Contradictions) < 1 {
		t.Errorf("env.Payload.Contradictions = %d, want >= 1", len(r.Env.Payload.Contradictions))
	}
	if r.Env.Payload.Completeness <= 0 || r.Env.Payload.Completeness > 1 {
		t.Errorf("env.Payload.Completeness = %v, want in (0, 1]", r.Env.Payload.Completeness)
	}

	// SQLite proof: the circuit persisted DISPUTED evidence for this session,
	// which the report is derived from.
	disputed := r.Evidence.Query(storage.EvidenceFilter{
		SessionID:    r.SessionID,
		Verification: model.VerificationDisputed,
	})
	if len(disputed) < 1 {
		t.Errorf("DISPUTED evidence items in SQLite = %d, want >= 1", len(disputed))
	}
}

// TestPhaseG_APIE2E_ReportDeterministic runs the full contradiction circuit
// twice and asserts the ID/time-independent structural invariants: equal
// counts of sources/findings/contradictions, equal completeness/confidence, and
// identical source ordering by (Name, Quality). It deliberately does NOT assert
// byte-identical JSON since evidence IDs and collected/completed timestamps
// differ across runs (per §0.12/§0.13 #4).
func TestPhaseG_APIE2E_ReportDeterministic(t *testing.T) {
	run := func() model.ResultEnvelope {
		return runContradictionCircuit(t).Env
	}
	e1, e2 := run(), run()

	if len(e1.Payload.Sources) != len(e2.Payload.Sources) {
		t.Errorf("Sources count: %d vs %d", len(e1.Payload.Sources), len(e2.Payload.Sources))
	}
	if len(e1.Payload.Findings) != len(e2.Payload.Findings) {
		t.Errorf("Findings count: %d vs %d", len(e1.Payload.Findings), len(e2.Payload.Findings))
	}
	if len(e1.Payload.Contradictions) != len(e2.Payload.Contradictions) {
		t.Errorf("Contradictions count: %d vs %d", len(e1.Payload.Contradictions), len(e2.Payload.Contradictions))
	}
	if e1.Payload.Completeness != e2.Payload.Completeness {
		t.Errorf("Completeness: %v vs %v", e1.Payload.Completeness, e2.Payload.Completeness)
	}
	if e1.Payload.Confidence != e2.Payload.Confidence {
		t.Errorf("Confidence: %v vs %v", e1.Payload.Confidence, e2.Payload.Confidence)
	}

	s1, s2 := e1.Payload.Sources, e2.Payload.Sources
	for i := 0; i < len(s1) && i < len(s2); i++ {
		if s1[i].Name != s2[i].Name || s1[i].Quality != s2[i].Quality {
			t.Errorf("Sources[%d] differ: (%q,%v) vs (%q,%v)",
				i, s1[i].Name, s1[i].Quality, s2[i].Name, s2[i].Quality)
		}
	}
}

// TestPhaseH_APIE2E_ResumeAfterRestart proves the Phase-H archive/recover
// lifecycle through the real API:
//
//  1. Run the contradiction circuit to terminal (via runContradictionCircuit).
//  2. POST /sessions/{id}/archive -> 204 (idempotent, generates recovery token).
//  3. Read the recovery token from the SQLite session store.
//  4. POST /sessions/{id}/recover {recovery_token} -> 200 {new_session_id}.
//  5. GET /sessions/{new_session_id} -> 200 (recovered session is active).
//  6. sseReport on the new session -> terminal report (recovered session runs).
//
// It also verifies that a wrong recovery token is rejected (400) without
// corrupting the session so a subsequent correct recover still succeeds.
func TestPhaseH_APIE2E_ResumeAfterRestart(t *testing.T) {
	r := runContradictionCircuit(t)
	sid := string(r.SessionID)

	// 1. Explicit archive (the Run goroutine also archives on terminal; this
	//    ensures a deterministic token before we read it).
	archiveResp := postNoBody(t, r.Server, "/api/v1/sessions/"+sid+"/archive")
	if archiveResp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(archiveResp.Body)
		archiveResp.Body.Close()
		t.Fatalf("expected 204 from archive, got %d: %s", archiveResp.StatusCode, b)
	}
	archiveResp.Body.Close()

	// 2. Read the recovery token from the store. Recover sets state back to
	//    ACTIVE (idempotent for a subsequent recover call).
	sess, err := r.Sessions.Recover(r.SessionID)
	if err != nil {
		t.Fatalf("Recover read: %v", err)
	}
	if sess == nil || sess.RecoveryToken == "" {
		t.Fatal("expected a non-empty recovery token after archive")
	}
	token := sess.RecoveryToken

	// 3. Wrong token must be rejected. This leaves the session ACTIVE.
	badResp := postJSONBody(t, r.Server, "/api/v1/sessions/"+sid+"/recover", `{"recovery_token":"wrong-token"}`)
	if badResp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(badResp.Body)
		badResp.Body.Close()
		t.Fatalf("expected 400 from recover with wrong token, got %d: %s", badResp.StatusCode, b)
	}
	badResp.Body.Close()

	// 4. Re-archive (failed recover mutated state to ACTIVE) — idempotent.
	reArchive := postNoBody(t, r.Server, "/api/v1/sessions/"+sid+"/archive")
	if reArchive.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 from re-archive, got %d", reArchive.StatusCode)
	}
	reArchive.Body.Close()

	// 5. Correct token -> new session id.
	recResp := postJSONBody(t, r.Server, "/api/v1/sessions/"+sid+"/recover",
		`{"recovery_token":"`+token+`"}`)
	if recResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(recResp.Body)
		recResp.Body.Close()
		t.Fatalf("expected 200 from recover, got %d: %s", recResp.StatusCode, b)
	}
	var cr struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(recResp.Body).Decode(&cr); err != nil {
		t.Fatal(err)
	}
	recResp.Body.Close()
	if cr.SessionID == "" {
		t.Fatal("recover returned empty session_id")
	}
	if cr.SessionID == sid {
		t.Errorf("recover returned the same session_id %q; expected a new one", cr.SessionID)
	}
	newSid := model.SessionID(cr.SessionID)

	// 6. The recovered session is active.
	checkResp, err := http.Get(r.Server.URL + "/api/v1/sessions/" + string(newSid))
	if err != nil {
		t.Fatal(err)
	}
	if checkResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(checkResp.Body)
		checkResp.Body.Close()
		t.Fatalf("expected 200 for recovered session GET, got %d: %s", checkResp.StatusCode, b)
	}
	checkResp.Body.Close()

	// 7. The recovered session runs to terminal on its own (SSE report). The
	//    recovered seed (alpha.example) is NOT denied, so DISCOVER succeeds and
	//    the master terminates with research_complete.
	recEnv := sseReport(t, r.Server, newSid)
	if recEnv.Status == "" || !strings.Contains(strings.ToLower(recEnv.Status), "compl") {
		t.Errorf("recovered session report status = %q, want a 'complete'-derived terminal", recEnv.Status)
	}
}

// TestPhaseH_APIE2E_DenylistBlocksSeed proves that a denied seed source
// short-circuits the research to a BLOCKED terminal state via the real API:
//
//	reg.Deny("alpha.example")
//	POST /sessions {seed: https://alpha.example} -> sm.Submit -> master.Run
//	Discover task -> denylistWorker -> RetrievalStatusBlocked
//	decider -> DecisionTerminate("blocked")
//	SSE report.Status contains "block".
//
// The SSE endpoint (GET /sessions/{id}/events) serves historical + live events
// through NewEventSDEHandler. createSessionViaAPI/sseReport are reused.
func TestPhaseH_APIE2E_DenylistBlocksSeed(t *testing.T) {
	bmgr := &fakeBrowserManager{}
	dw := &denylistWorker{inner: newContradictionWorker()}
	m, _, reg, es, ss, evs := newAPITestGraph(t, dw, bmgr)

	// Wire the registry onto the denylist worker AFTER the graph is built (the
	// registry is created inside newAPITestGraph), but BEFORE Run starts.
	dw.setRegistry(reg)

	// Deny the seed domain so the seed Discover task is blocked.
	if err := reg.Deny("alpha.example"); err != nil {
		t.Fatalf("reg.Deny: %v", err)
	}

	deps := &api.Deps{
		Master:         m,
		EvidenceStore:  es,
		SourceRegistry: reg,
		EventStore:     evs,
		SessionStore:   ss,
		ReportBuilder:  result.NewReportBuilder(es, reg),
		Auth:           auth.Config{},
	}
	serverCtx, serverCancel := context.WithCancel(context.Background())
	t.Cleanup(func() { serverCancel() })
	server := httptest.NewServer(api.NewServer(deps, serverCtx))
	defer server.Close()

	// createSessionViaAPI -> POST /sessions -> 201 {session_id} -> sm.Submit ->
	// go master.Run. The seed Discover task (alpha.example, priority 1000) is
	// queued by SubmitIntent before Run starts, so there is no injection race.
	sid := createSessionViaAPI(t, server)

	// SSE report: the blocked DISCOVER task drives the master to terminal with
	// the "blocked" reason.
	env := sseReport(t, server, sid)

	if env.Status == "" {
		t.Fatal("expected non-empty terminal status")
	}
	if !strings.Contains(strings.ToLower(env.Status), "block") {
		t.Errorf("expected terminal status to contain 'block', got %q", env.Status)
	}
}
