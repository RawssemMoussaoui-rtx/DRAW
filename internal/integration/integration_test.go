package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"draw/internal/browser"
	"draw/internal/config"
	"draw/internal/frontier"
	"draw/internal/manager"
	"draw/internal/managers"
	"draw/internal/master"
	"draw/internal/model"
	"draw/internal/orchestrator"
	"draw/internal/workers"
)

// Compile-time interface assertions.
var (
	_ manager.Manager = (*managers.RouterManager)(nil)
	_ manager.Worker  = (*workers.HTTPWorker)(nil)
)

// fakeWorker implements manager.Worker, returning a configurable result per
// task type and recording the ordered list of task types it executed.
type fakeWorker struct {
	mu      sync.Mutex
	calls   []model.TaskType
	results map[model.TaskType]model.RetrievalStatus
}

func newFakeWorker() *fakeWorker {
	return &fakeWorker{
		results: map[model.TaskType]model.RetrievalStatus{
			model.TaskTypeDiscover:     model.RetrievalStatusSuccess,
			model.TaskTypeFetchHTTP:    model.RetrievalStatusJavascriptRequired,
			model.TaskTypeFetchBrowser: model.RetrievalStatusSuccess,
			model.TaskTypeVerify:       model.RetrievalStatusSuccess,
			model.TaskTypeReconcile:    model.RetrievalStatusSuccess,
		},
	}
}

func (w *fakeWorker) Run(t model.Task) (model.TaskResult, error) {
	w.mu.Lock()
	w.calls = append(w.calls, t.Type)
	st := w.results[t.Type]
	ev := model.EvidenceID("e1")
	w.mu.Unlock()
	if st == model.RetrievalStatusSuccess {
		return model.TaskResult{Status: st, Data: []byte("ok"), Evidence: []model.EvidenceID{ev}}, nil
	}
	return model.TaskResult{Status: st}, nil
}

func (w *fakeWorker) Close() error { return nil }

// fakeBrowserContext implements browser.BrowserContext. Done returns an
// already-closed channel; Capture returns non-empty bytes on demand.
type fakeBrowserContext struct {
	done chan struct{}
}

func newFakeBrowserContext() *fakeBrowserContext {
	ch := make(chan struct{})
	close(ch)
	return &fakeBrowserContext{done: ch}
}

func (b *fakeBrowserContext) Done() <-chan struct{} { return b.done }
func (b *fakeBrowserContext) Capture(u string) ([]byte, error) {
	return []byte("rendered"), nil
}

// fakeBrowserManager implements browser.BrowserManager without launching a
// real Chromium process.
type fakeBrowserManager struct {
	mu       sync.Mutex
	acquires int
	releases int
}

func (m *fakeBrowserManager) Acquire(_ context.Context, _ *browser.AuthorizedSession) (browser.BrowserContext, error) {
	m.mu.Lock()
	m.acquires++
	m.mu.Unlock()
	return newFakeBrowserContext(), nil
}

func (m *fakeBrowserManager) Release(_ browser.BrowserContext) error {
	m.mu.Lock()
	m.releases++
	m.mu.Unlock()
	return nil
}

func (m *fakeBrowserManager) Stats() browser.BrowserStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return browser.BrowserStats{Active: 0, Cap: 4}
}

func (m *fakeBrowserManager) CleanupExpired(time.Time, time.Duration) int { return 0 }
func (m *fakeBrowserManager) Close() error                                { return nil }

// newTestGraph builds the real execution graph: frontier + resource controller
// + scheduler, with the supplied worker and browser manager wired into the
// router facade. It returns the master and scheduler so callers can submit
// extra tasks directly when needed.
func newTestGraph(t *testing.T, w manager.Worker, bmgr browser.BrowserManager) (*master.Master, *orchestrator.Scheduler) {
	t.Helper()
	cfg := config.Defaults()
	src := orchestrator.NoopSourceRegistry{}
	fr := frontier.NewMemoryFrontier(cfg, src)
	rc := orchestrator.NewResourceController(cfg, orchestrator.NoopResourceSampler{})
	sched := orchestrator.NewScheduler(cfg, fr, rc, src, orchestrator.DefaultTaskCapabilities())
	web := managers.NewWebManager(w)
	news := managers.NewNewsManager(w)
	social := managers.NewSocialManager()
	specialized := managers.NewSpecializedManager()
	brow := managers.NewBrowserAdapter(bmgr, 2*time.Minute)
	router := managers.NewRouterManager(web, news, brow, social, specialized)
	if err := sched.RegisterManager(router, router.Capabilities()); err != nil {
		t.Fatalf("register manager: %v", err)
	}
	m := master.NewMaster(cfg, sched, master.WithMemorySessions())
	return m, sched
}

// TestExec_HTTPRoundTripRealScheduler exercises the real Master + real Scheduler
// + real RouterManager + real HTTPWorker/fetchClient against an httptest server
// that returns 200. The DISCOVER seed task is admitted, fetched, and the run
// terminates with research_complete.
func TestExec_HTTPRoundTripRealScheduler(t *testing.T) {
	cfg := config.Defaults()
	fc := workers.NewFetchClient(workers.FetchClientOpts{
		UserAgentPool: []string{"draw/1.0 (+https://draw.local)"},
		MaxAttempts:   cfg.Retry.MaxAttempts,
		BaseTimeout:   10 * time.Second,
		MaxRedirects:  5,
	})
	httpWorker := workers.NewHTTPWorker(fc, workers.WorkerOpts{
		UserAgentPool: []string{"draw/1.0 (+https://draw.local)"},
		MaxAttempts:   cfg.Retry.MaxAttempts,
		Timeout:       30 * time.Second,
		MaxRedirects:  5,
	})
	bmgr := &fakeBrowserManager{}
	m, _ := newTestGraph(t, httpWorker, bmgr)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	if _, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1",
		Query:  "research X",
		Seeds:  []string{server.URL},
	}); err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := m.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	st := m.State()
	if !st.Terminal {
		t.Fatalf("expected terminal state, got %+v", st)
	}
	if st.TerminalReason != "research_complete" {
		t.Errorf("terminal reason: got %q want research_complete", st.TerminalReason)
	}
	if st.BudgetUsed < 1 {
		t.Errorf("budget used: got %d want >= 1", st.BudgetUsed)
	}
}

// TestExec_JSRequiredEscalationNoChromium exercises the escalation path through
// the real scheduler and router: DISCOVER succeeds, FETCH_HTTP returns
// JAVASCRIPT_REQUIRED, the decider upgrades to FETCH_BROWSER, and the browser
// adapter (backed by a fake browser manager) completes it. No Chromium is
// required.
func TestExec_JSRequiredEscalationNoChromium(t *testing.T) {
	w := newFakeWorker()
	bmgr := &fakeBrowserManager{}
	m, sched := newTestGraph(t, w, bmgr)

	seed := "https://official.example.com"
	sid, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1",
		Query:  "research X",
		Seeds:  []string{seed},
	})
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}

	u, err := url.Parse(seed)
	if err != nil {
		t.Fatalf("parse seed: %v", err)
	}
	fetchTask := model.Task{
		ID:            model.NewTaskID(),
		SessionID:     sid,
		Type:          model.TaskTypeFetchHTTP,
		State:         model.TaskStateReady,
		Priority:      500,
		SourceClass:   model.SourceClassUnknown,
		URL:           u,
		EstimatedCost: 1,
		TaskKey:       "frontier:fetch:1",
		CreatedAt:     time.Now().UTC(),
	}
	if err := sched.Submit(fetchTask); err != nil {
		t.Fatalf("sched.Submit: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := m.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	st := m.State()
	if !st.Terminal {
		t.Fatalf("expected terminal state, got %+v", st)
	}
	if st.TerminalReason != "research_complete" {
		t.Errorf("terminal reason: got %q want research_complete", st.TerminalReason)
	}
	if st.BudgetUsed != 12 {
		t.Errorf("budget used: got %d want 12 (DISCOVER=1 + FETCH_HTTP=1 + FETCH_BROWSER=10)", st.BudgetUsed)
	}

	w.mu.Lock()
	workerCalls := append([]model.TaskType(nil), w.calls...)
	w.mu.Unlock()
	if len(workerCalls) != 2 {
		t.Errorf("fakeWorker task count: got %d want 2 (DISCOVER + FETCH_HTTP)", len(workerCalls))
	}
	for _, tt := range workerCalls {
		if tt == model.TaskTypeFetchBrowser {
			t.Error("fakeWorker should not handle FETCH_BROWSER (browser adapter should)")
		}
	}

	bmgr.mu.Lock()
	acquires := bmgr.acquires
	releases := bmgr.releases
	bmgr.mu.Unlock()
	if acquires != 1 {
		t.Errorf("browser acquires: got %d want 1 (browser escalation path)", acquires)
	}
	if releases != 1 {
		t.Errorf("browser releases: got %d want 1", releases)
	}
}
