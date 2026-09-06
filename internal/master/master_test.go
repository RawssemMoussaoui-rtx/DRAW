package master

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"draw/internal/config"
	"draw/internal/manager"
	"draw/internal/model"
	"draw/internal/storage"
)

type fakeScheduler struct {
	mu       sync.Mutex
	queue    []model.Task
	mgr      manager.Manager
	running  int
	capGlob  int
	stats    SchedulerStats
	admitted []model.TaskID
	released int
}

func newFakeScheduler(mgr manager.Manager, capGlob int) *fakeScheduler {
	return &fakeScheduler{mgr: mgr, capGlob: capGlob, stats: SchedulerStats{GlobalCap: capGlob, BrowserCap: 4, BrowserActive: 0}}
}

func (f *fakeScheduler) Submit(t model.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, t)
	return nil
}

func (f *fakeScheduler) RegisterManager(m manager.Manager, caps []manager.Capability) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mgr = m
	return nil
}

func (f *fakeScheduler) Admit(now time.Time) (model.Task, manager.Manager, manager.WorkerSlot, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running >= f.capGlob || len(f.queue) == 0 {
		return model.Task{}, nil, manager.WorkerSlot{}, false
	}
	t := f.queue[0]
	f.queue = f.queue[1:]
	f.running++
	f.admitted = append(f.admitted, t.ID)
	return t, f.mgr, manager.WorkerSlot{ID: fmt.Sprintf("slot-%d", len(f.admitted))}, true
}

func (f *fakeScheduler) Release(slot manager.WorkerSlot, id model.TaskID, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running > 0 {
		f.running--
	}
	f.released++
}

func (f *fakeScheduler) Stats() SchedulerStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.stats
	s.Queued = len(f.queue)
	s.Active = f.running
	return s
}

type fakeManager struct {
	mu      sync.Mutex
	calls   int
	respond func(model.Task, int) model.TaskResult
}

func (f *fakeManager) Capabilities() []manager.Capability { return nil }
func (f *fakeManager) Execute(t model.Task) (model.TaskResult, error) {
	f.mu.Lock()
	f.calls++
	c := f.calls
	f.mu.Unlock()
	return f.respond(t, c), nil
}

type countingEvidence struct {
	mu sync.Mutex
	ev EvidenceCounts
}

func (c *countingEvidence) Counts(model.SessionID) EvidenceCounts {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ev
}

func setCounts(c *countingEvidence, ev EvidenceCounts) {
	c.mu.Lock()
	c.ev = ev
	c.mu.Unlock()
}

func seedURL() string { return "https://official.example.com" }

func newMasterWithFakes(t *testing.T, mgr *fakeManager, sched *fakeScheduler, ev *countingEvidence) *Master {
	t.Helper()
	cfg := config.Defaults()
	b := NewMaster(cfg, sched)
	b.evidence = ev
	// intent parser already wired; keep default seeds-free parser, seeds come via request
	_ = mgr
	return b
}

func newMasterWithEvidence(t *testing.T) (*Master, *fakeScheduler, *fakeManager, *countingEvidence) {
	t.Helper()
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("storage open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := storage.Apply(context.Background(), db); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	es, err := storage.NewSQLiteEvidenceStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteEvidenceStore: %v", err)
	}
	reg, err := storage.NewSQLiteSourceRegistry(db, 0.3)
	if err != nil {
		t.Fatalf("NewSQLiteSourceRegistry: %v", err)
	}
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{Status: model.RetrievalStatusSuccess}
	}}
	sched := newFakeScheduler(mgr, 4)
	m := NewMaster(config.Defaults(), sched,
		WithEvidenceStore(es),
		WithSourceRegistry(reg),
	)
	return m, sched, mgr, nil
}

func TestSubmitIntentRequiresSeeds(t *testing.T) {
	m := NewMaster(config.Defaults(), newFakeScheduler(&fakeManager{}, 4))
	if _, err := m.SubmitIntent(model.IntentRequest{Query: "research Company X"}); err == nil {
		t.Fatal("expected error when no seeds provided")
	}
}

func TestMasterRunSuccessRoundTrip(t *testing.T) {
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{Status: model.RetrievalStatusSuccess, Evidence: []model.EvidenceID{"e1"}}
	}}
	sched := newFakeScheduler(mgr, 4)
	ev := &countingEvidence{}
	m := newMasterWithFakes(t, mgr, sched, ev)

	_, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1",
		Query:  "research Company X",
		Seeds:  []string{seedURL()},
	})
	if err != nil {
		t.Fatal(err)
	}

	err = m.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	st := m.State()
	if !st.Terminal {
		t.Fatal("expected terminal state")
	}
	if st.TerminalReason != "research_complete" {
		t.Errorf("terminal reason: got %q want %q", st.TerminalReason, "research_complete")
	}
	if st.BudgetUsed != 1 {
		t.Errorf("budget used: got %d want 1 (DISCOVER cost)", st.BudgetUsed)
	}
	if st.EvidenceCount != 1 {
		t.Errorf("evidence count: got %d want 1", st.EvidenceCount)
	}
	if mgr.calls != 1 {
		t.Errorf("manager calls: got %d want 1", mgr.calls)
	}
}

func TestMasterRunUpgradeJSRequiredToBrowser(t *testing.T) {
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		switch tk.Type {
		case model.TaskTypeDiscover:
			return model.TaskResult{Status: model.RetrievalStatusSuccess, Evidence: []model.EvidenceID{"e1"}}
		case model.TaskTypeFetchHTTP:
			return model.TaskResult{Status: model.RetrievalStatusJavascriptRequired}
		case model.TaskTypeFetchBrowser:
			return model.TaskResult{Status: model.RetrievalStatusSuccess, Evidence: []model.EvidenceID{"e2"}}
		}
		return model.TaskResult{Status: model.RetrievalStatusEmpty}
	}}
	sched := newFakeScheduler(mgr, 4)
	ev := &countingEvidence{}
	m := newMasterWithFakes(t, mgr, sched, ev)

	if _, err := m.SubmitIntent(model.IntentRequest{UserID: "u1", Query: "research Company X", Seeds: []string{seedURL()}}); err != nil {
		t.Fatal(err)
	}
	// simulate Frontier injecting a fetch task downstream
	m.mu.Lock()
	extra := model.Task{ID: model.NewTaskID(), SessionID: m.state.Session.ID, Type: model.TaskTypeFetchHTTP,
		SourceClass: model.SourceClassUnknown, URL: mustURL(seedURL()), EstimatedCost: 1,
		TaskKey: "frontier:fetch:1", CreatedAt: time.Now().UTC(), Priority: 500}
	m.pending[extra.ID] = true
	m.tasks[extra.ID] = extra
	m.mu.Unlock()
	_ = sched.Submit(extra)

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	st := m.State()
	if !st.Terminal || st.TerminalReason != "research_complete" {
		t.Fatalf("terminal: %+v", st)
	}
	if st.BudgetUsed != 12 {
		t.Errorf("budget used: got %d want 12 (1+1+10)", st.BudgetUsed)
	}
	browserExecuted := 0
	mgr.mu.Lock()
	// 3 executions: discover, fetch-http, fetch-browser
	calls := mgr.calls
	mgr.mu.Unlock()
	if calls != 3 {
		t.Errorf("manager calls: got %d want 3", calls)
	}
	_ = browserExecuted
}

func TestMasterRunTimeoutRetryThenSuccess(t *testing.T) {
	attempt := 0
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		if tk.Type == model.TaskTypeFetchHTTP {
			attempt++
			if attempt == 1 {
				return model.TaskResult{Status: model.RetrievalStatusTimeout}
			}
			return model.TaskResult{Status: model.RetrievalStatusSuccess, Evidence: []model.EvidenceID{"e1"}}
		}
		return model.TaskResult{Status: model.RetrievalStatusSuccess, Evidence: []model.EvidenceID{"e1"}}
	}}
	sched := newFakeScheduler(mgr, 4)
	ev := &countingEvidence{}
	m := newMasterWithFakes(t, mgr, sched, ev)

	if _, err := m.SubmitIntent(model.IntentRequest{UserID: "u1", Query: "research Company X", Seeds: []string{seedURL()}}); err != nil {
		t.Fatal(err)
	}
	extra := model.Task{ID: model.NewTaskID(), SessionID: m.state.Session.ID, Type: model.TaskTypeFetchHTTP,
		SourceClass: model.SourceClassUnknown, URL: mustURL(seedURL()), EstimatedCost: 1,
		TaskKey: "frontier:fetch:1", CreatedAt: time.Now().UTC(), Priority: 500}
	m.mu.Lock()
	m.pending[extra.ID] = true
	m.tasks[extra.ID] = extra
	m.mu.Unlock()
	_ = sched.Submit(extra)

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	st := m.State()
	if !st.Terminal || st.TerminalReason != "research_complete" {
		t.Fatalf("expected research_complete, got %+v", st)
	}
	mgr.mu.Lock()
	calls := mgr.calls
	mgr.mu.Unlock()
	if calls != 3 {
		t.Errorf("expected 3 manager calls (discover + timeout retry + success), got %d", calls)
	}
	if st.EvidenceCount != 2 {
		t.Errorf("evidence count: got %d want 2 (seed discovery + retried fetch success)", st.EvidenceCount)
	}
}

func TestMasterRunBudgetExhaustedTerminates(t *testing.T) {
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{Status: model.RetrievalStatusSuccess, Evidence: []model.EvidenceID{"e1"}}
	}}
	sched := newFakeScheduler(mgr, 4)
	m := NewMaster(config.Defaults(), sched)
	m.evidence = &countingEvidence{}
	m.intent = NewIntentParser(IntentConfig{
		DefaultCrawlDepth: 2, DefaultTaskDepth: 3,
		DefaultEffortBudget: 2, DefaultMaxReplans: 3,
	})

	if _, err := m.SubmitIntent(model.IntentRequest{UserID: "u1", Query: "research Company X", Seeds: []string{seedURL()}}); err != nil {
		t.Fatal(err)
	}
	extra := model.Task{ID: model.NewTaskID(), SessionID: m.state.Session.ID, Type: model.TaskTypeFetchBrowser,
		SourceClass: model.SourceClassUnknown, URL: mustURL(seedURL()), EstimatedCost: 10,
		TaskKey: "frontier:browser:1", CreatedAt: time.Now().UTC(), Priority: 550}
	m.mu.Lock()
	m.pending[extra.ID] = true
	m.tasks[extra.ID] = extra
	m.mu.Unlock()
	_ = sched.Submit(extra)

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	st := m.State()
	if st.BudgetUsed != 1 {
		t.Errorf("budget used: got %d want 1 (only the affordable DISCOVER)", st.BudgetUsed)
	}
	if !st.Terminal || st.TerminalReason != "budget_exhausted" {
		t.Errorf("expected terminal budget_exhausted, got terminal=%v reason=%q", st.Terminal, st.TerminalReason)
	}
	if mgr.calls != 1 {
		t.Errorf("manager calls: got %d want 1 (browser task unaffordable, not executed)", mgr.calls)
	}
}

func TestMasterRunReplanOnContradictionsBoundedByMaxReplans(t *testing.T) {
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{Status: model.RetrievalStatusSuccess, Evidence: []model.EvidenceID{"e1"}}
	}}
	sched := newFakeScheduler(mgr, 4)
	ev := &countingEvidence{ev: EvidenceCounts{Contradictions: 2}}
	m := newMasterWithFakes(t, mgr, sched, ev)
	m.intent = NewIntentParser(IntentConfig{
		DefaultCrawlDepth: 2, DefaultTaskDepth: 3,
		DefaultEffortBudget: 500, DefaultMaxReplans: 3,
	})

	if _, err := m.SubmitIntent(model.IntentRequest{UserID: "u1", Query: "research Company X", Seeds: []string{seedURL()}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	st := m.State()
	if !st.Terminal {
		t.Fatal("expected terminal")
	}
	if st.ReplanCount != st.MaxReplans {
		t.Errorf("replan count: got %d want %d (capped)", st.ReplanCount, st.MaxReplans)
	}
	if len(st.Plan.Phases[0].Tasks) < 1 {
		t.Error("expected plan to retain seed task")
	}
}

func TestMasterStopHaltsRun(t *testing.T) {
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{Status: model.RetrievalStatusSuccess, Evidence: []model.EvidenceID{"e1"}}
	}}
	sched := newFakeScheduler(mgr, 4)
	m := NewMaster(config.Defaults(), sched)
	m.evidence = &countingEvidence{}

	if _, err := m.SubmitIntent(model.IntentRequest{UserID: "u1", Query: "research Company X", Seeds: []string{seedURL()}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- m.Run(context.Background()) }()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("run after stop: got %v want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not observe Stop within 1s")
	}
}

func TestMasterStateIsSnapshot(t *testing.T) {
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{Status: model.RetrievalStatusSuccess, Evidence: []model.EvidenceID{"e1"}}
	}}
	sched := newFakeScheduler(mgr, 4)
	m := newMasterWithFakes(t, mgr, sched, &countingEvidence{})
	if _, err := m.SubmitIntent(model.IntentRequest{UserID: "u1", Query: "research Company X", Seeds: []string{seedURL()}}); err != nil {
		t.Fatal(err)
	}
	snap := m.State()
	if snap.Session == nil {
		t.Error("snapshot should carry session")
	}
	if snap.Session.ID != m.state.Session.ID {
		t.Error("snapshot session id mismatch")
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

type ingestSpy struct {
	mu    sync.Mutex
	calls int
	lastT model.Task
	lastR model.TaskResult
	err   error
}

func (s *ingestSpy) Process(ctx context.Context, t model.Task, r model.TaskResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.lastT = t
	s.lastR = r
	return s.err
}

func TestMasterDefaultNoopIngestion(t *testing.T) {
	m := NewMaster(config.Defaults(), newFakeScheduler(&fakeManager{}, 4))
	if m.ingest == nil {
		t.Fatal("expected non-nil Ingestion on default Master")
	}
	if _, ok := m.ingest.(noopIngestion); !ok {
		t.Fatalf("expected default ingestion to be noopIngestion, got %T", m.ingest)
	}
}

func TestIngestionInvokedOnSuccess(t *testing.T) {
	spy := &ingestSpy{}
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{
			Status:   model.RetrievalStatusSuccess,
			Evidence: []model.EvidenceID{"e1"},
			Data:     []byte(`<!DOCTYPE html><html><body><a href="https://other.example/x">x</a></body></html>`),
			Headers:  http.Header{"Content-Type": []string{"text/html"}},
		}
	}}
	sched := newFakeScheduler(mgr, 4)
	ev := &countingEvidence{}
	m := NewMaster(config.Defaults(), sched, WithIngestion(spy))
	m.evidence = ev

	if _, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: "research Company X", Seeds: []string{seedURL()},
	}); err != nil {
		t.Fatal(err)
	}

	// Capture the seed DISCOVER task id inserted by SubmitIntent.
	m.mu.Lock()
	seedTasks := m.state.Plan.Phases[0].Tasks
	m.mu.Unlock()
	if len(seedTasks) != 1 {
		t.Fatalf("expected 1 seed task in plan, got %d", len(seedTasks))
	}
	seedID := seedTasks[0]

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()
	if spy.calls < 1 {
		t.Fatalf("expected at least 1 ingestion call, got 0")
	}
	if spy.lastT.ID != seedID {
		t.Errorf("last processed task id: got %v want %v (seed DISCOVER)", spy.lastT.ID, seedID)
	}
}

func TestIngestionErrorIsNonFatal(t *testing.T) {
	spy := &ingestSpy{err: errors.New("boom")}
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{Status: model.RetrievalStatusSuccess, Evidence: []model.EvidenceID{"e1"}}
	}}
	sched := newFakeScheduler(mgr, 4)
	ev := &countingEvidence{}
	m := NewMaster(config.Defaults(), sched, WithIngestion(spy))
	m.evidence = ev

	if _, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: "research Company X", Seeds: []string{seedURL()},
	}); err != nil {
		t.Fatal(err)
	}

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	st := m.State()
	if !st.Terminal {
		t.Fatal("expected terminal state after ingestion error")
	}
	if st.TerminalReason != "research_complete" {
		t.Errorf("terminal reason: got %q want research_complete", st.TerminalReason)
	}

	spy.mu.Lock()
	calls := spy.calls
	spy.mu.Unlock()
	if calls < 1 {
		t.Errorf("expected at least 1 ingestion call, got %d", calls)
	}
}

func TestMasterStopIdempotentBeforeRun(t *testing.T) {
	m := NewMaster(config.Defaults(), newFakeScheduler(&fakeManager{}, 4))
	for i := 0; i < 3; i++ {
		if err := m.Stop(); err != nil {
			t.Errorf("stop call %d: unexpected error %v", i, err)
		}
	}
}

func TestMasterStopBeforeRunDoesNotPanic(t *testing.T) {
	m := NewMaster(config.Defaults(), newFakeScheduler(&fakeManager{}, 4))
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Stop before Run panicked: %v", r)
		}
	}()
	err := m.Run(context.Background())
	if err == nil {
		t.Fatal("expected Run to return an error after Stop, got nil")
	}
}

func TestMasterWithSQLiteSessions(t *testing.T) {
	db, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("storage open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := storage.Apply(context.Background(), db); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ss, err := storage.NewSQLiteSessionStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteSessionStore: %v", err)
	}
	m := NewMaster(config.Defaults(), newFakeScheduler(&fakeManager{
		respond: func(tk model.Task, c int) model.TaskResult {
			return model.TaskResult{Status: model.RetrievalStatusSuccess}
		},
	}, 4), WithSQLiteSessions(ss))

	if _, ok := m.sessions.(*storage.SQLiteSessionStore); !ok {
		t.Fatalf("expected SQLiteSessionStore, got %T", m.sessions)
	}

	if _, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: "research Company X", Seeds: []string{seedURL()},
	}); err != nil {
		t.Fatal(err)
	}

	sess, ok := m.sessions.Current("u1")
	if !ok || sess == nil {
		t.Fatal("expected session saved in SQLite store via Current")
	}
}

// seedURLs builds n distinct seed URLs (distinct hosts) so seedTasks
// produces n tasks instead of deduping to a single one.
func seedURLs(n int) []string {
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = fmt.Sprintf("https://seed%d.example.com", i)
	}
	return out
}

// TestSchedulerNoDeadlockUnderLoad spins up several concurrent fake tasks
// (cap=3) and asserts the Run loop terminates under a hard timeout rather
// than deadlocking.
func TestSchedulerNoDeadlockUnderLoad(t *testing.T) {
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		time.Sleep(5 * time.Millisecond)
		return model.TaskResult{Status: model.RetrievalStatusSuccess}
	}}
	sched := newFakeScheduler(mgr, 3)
	cfg := config.Defaults()
	cfg.MaxGlobalConcurrency = 3
	m := NewMaster(cfg, sched)

	if _, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: "research Company X", Seeds: seedURLs(10),
	}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- m.Run(context.Background()) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not complete within timeout; likely deadlock")
	}

	mgr.mu.Lock()
	calls := mgr.calls
	mgr.mu.Unlock()
	if calls != 10 {
		t.Errorf("manager calls: got %d want 10", calls)
	}
}

// TestConcurrentRunCapEnforced asserts the Scheduler's global cap is never
// exceeded (Active <= 3) while 12 tasks execute concurrently, and that all
// 12 tasks complete.
func TestConcurrentRunCapEnforced(t *testing.T) {
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		time.Sleep(10 * time.Millisecond)
		return model.TaskResult{Status: model.RetrievalStatusSuccess, Evidence: []model.EvidenceID{"e1"}}
	}}
	sched := newFakeScheduler(mgr, 3)
	cfg := config.Defaults()
	cfg.MaxGlobalConcurrency = 3
	m := NewMaster(cfg, sched)

	if _, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: "research Company X", Seeds: seedURLs(12),
	}); err != nil {
		t.Fatal(err)
	}

	var (
		obsWg     sync.WaitGroup
		stopObs   int32
		mu        sync.Mutex
		maxActive int
	)
	obsWg.Add(1)
	go func() {
		defer obsWg.Done()
		for atomic.LoadInt32(&stopObs) == 0 {
			s := sched.Stats()
			mu.Lock()
			if s.Active > maxActive {
				maxActive = s.Active
			}
			mu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	atomic.StoreInt32(&stopObs, 1)
	obsWg.Wait()

	if maxActive > 3 {
		t.Errorf("active exceeded global cap: got %d want <= 3", maxActive)
	}
	mgr.mu.Lock()
	calls := mgr.calls
	mgr.mu.Unlock()
	if calls != 12 {
		t.Errorf("manager calls: got %d want 12 (all tasks complete)", calls)
	}
}

// TestMasterRunCancelStopsWorkers stops the Master mid-run and asserts no
// dispatch worker goroutines linger after Run returns (drain waits for them).
func TestMasterRunCancelStopsWorkers(t *testing.T) {
	var active int32
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		atomic.AddInt32(&active, 1)
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		return model.TaskResult{Status: model.RetrievalStatusSuccess}
	}}
	sched := newFakeScheduler(mgr, 3)
	cfg := config.Defaults()
	cfg.MaxGlobalConcurrency = 3
	m := NewMaster(cfg, sched)

	if _, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: "research Company X", Seeds: seedURLs(12),
	}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- m.Run(context.Background()) }()

	// Wait until at least one worker is in-flight, then stop mid-flight.
	timeout := time.After(500 * time.Millisecond)
	for atomic.LoadInt32(&active) == 0 {
		select {
		case <-time.After(time.Millisecond):
		case <-timeout:
			t.Fatal("no worker ever started executing")
		}
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected Run to return an error after Stop")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not observe Stop within 2s")
	}

	// After Run has returned (its drain waited for the in-flight workers),
	// no worker goroutine should still be inside Execute.
	settle := time.After(200 * time.Millisecond)
	<-settle
	if a := atomic.LoadInt32(&active); a != 0 {
		t.Errorf("lingering in-flight workers after settle: %d", a)
	}
}

// TestConcurrentRunDrainsBufferedResults is a regression test for the
// concurrent Run fill+drain loop. When a fast worker releases its slot
// (Active→0) while a result is still buffered in resultsCh, the
// drained() completion check must flush+process the buffered result
// (which may trigger a contradiction replan) instead of discarding it.
//
// Three FetchHTTP tasks from distinct hosts return contradictory revenue
// (100/200/300). After the 2nd, the circuit fires a replan that admits
// Verify+Reconcile tasks. Under MaxGlobalConcurrency=4 with a fast
// (no-sleep) fake worker, the race is exercised; across 3 runs the
// circuit must always complete (matching TestPhaseH_DeterminismAcrossRuns
// which was flaky run2/run3 before the fix).
func TestConcurrentRunDrainsBufferedResults(t *testing.T) {
	for run := 1; run <= 3; run++ {
		db, err := storage.Open(":memory:")
		if err != nil {
			t.Fatalf("run %d: storage open: %v", run, err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { db.Close() })
		if err := storage.Apply(context.Background(), db); err != nil {
			t.Fatalf("run %d: Apply: %v", run, err)
		}
		es, err := storage.NewSQLiteEvidenceStore(db)
		if err != nil {
			t.Fatalf("run %d: NewSQLiteEvidenceStore: %v", run, err)
		}
		reg, err := storage.NewSQLiteSourceRegistry(db, 0.3)
		if err != nil {
			t.Fatalf("run %d: NewSQLiteSourceRegistry: %v", run, err)
		}

		revenueByHost := map[string]int{
			"alpha.example": 100,
			"beta.example":  200,
			"gamma.example": 300,
		}
		mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
			if tk.Type == model.TaskTypeFetchHTTP {
				host := tk.SourceTarget
				if host == "" && tk.URL != nil {
					host = tk.URL.Hostname()
				}
				rev := revenueByHost[host]
				if rev == 0 {
					rev = 100
				}
				return model.TaskResult{
					Status:  model.RetrievalStatusSuccess,
					Data:    []byte(fmt.Sprintf(`[{"revenue":%d,"name":"Acme"}]`, rev)),
					Headers: http.Header{"Content-Type": []string{"application/json"}},
				}
			}
			return model.TaskResult{Status: model.RetrievalStatusSuccess}
		}}

		sched := newFakeScheduler(mgr, 4)
		cfg := config.Defaults()
		cfg.MaxGlobalConcurrency = 4
		m := NewMaster(cfg, sched,
			WithEvidenceStore(es),
			WithSourceRegistry(reg),
		)

		sid, err := m.SubmitIntent(model.IntentRequest{
			UserID: "u1",
			Query:  "research Acme Corp, revenue",
			Seeds:  []string{"https://alpha.example"},
		})
		if err != nil {
			t.Fatalf("run %d: SubmitIntent: %v", run, err)
		}

		// Inject three contradictory FetchHTTP tasks directly into the
		// scheduler queue (bypassing m.pending) to reproduce the race:
		// these tasks' results are buffered in resultsCh while the
		// DISCOVER seed task (tracked in m.pending) is the only pending
		// entry.
		hosts := []string{"alpha.example", "beta.example", "gamma.example"}
		for i, host := range hosts {
			u, err := url.Parse("https://" + host + "/api")
			if err != nil {
				t.Fatalf("run %d: url.Parse: %v", run, err)
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
				CreatedAt:     time.Now().UTC(),
			}
			if err := sched.Submit(task); err != nil {
				t.Fatalf("run %d: sched.Submit: %v", run, err)
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := m.Run(ctx); err != nil {
			cancel()
			t.Fatalf("run %d: Run: %v", run, err)
		}
		cancel()

		st := m.State()
		if !st.Terminal {
			t.Fatalf("run %d: expected terminal state, got Terminal=%v reason=%q", run, st.Terminal, st.TerminalReason)
		}
		// Terminal reason may be "research_complete" (all work drained) or a
		// DecisionStop reason when replan budget is exhausted but contradictions
		// persist (replanTriggerIfAny returns non-nil and CanReplan is false).
		if st.TerminalReason != "research_complete" && !strings.Contains(st.TerminalReason, "deterministic stop") {
			t.Errorf("run %d: terminal reason: got %q want research_complete or deterministic stop", run, st.TerminalReason)
		}
		if st.ReplanCount < 1 {
			t.Errorf("run %d: ReplanCount = %d, want >= 1 (contradiction replan must fire)", run, st.ReplanCount)
		}
		if st.EvidenceCount < 3 {
			t.Errorf("run %d: EvidenceCount = %d, want >= 3 (evidence items extracted from fetches)", run, st.EvidenceCount)
		}
		if st.Evidence.Contradictions < 2 {
			t.Errorf("run %d: Evidence.Contradictions = %d, want >= 2 (DISPUTED count)", run, st.Evidence.Contradictions)
		}

		disputed := es.Query(storage.EvidenceFilter{
			SessionID:    sid,
			Verification: model.VerificationDisputed,
		})
		if len(disputed) < 2 {
			t.Errorf("run %d: SQLite DISPUTED evidence rows = %d, want >= 2", run, len(disputed))
		}

		mgr.mu.Lock()
		calls := mgr.calls
		mgr.mu.Unlock()
		if calls < 6 {
			t.Errorf("run %d: manager calls = %d, want >= 6 (seed + 3 fetch + >= 2 replan tasks)", run, calls)
		}

		t.Logf("run %d: calls=%d replan=%d contradictions=%d evidence=%d terminal=%s",
			run, calls, st.ReplanCount, st.Evidence.Contradictions, st.EvidenceCount, st.TerminalReason)
	}
}

func TestDecisionStopIdempotent(t *testing.T) {
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{Status: model.RetrievalStatusSuccess}
	}}
	sched := newFakeScheduler(mgr, 4)
	m := NewMaster(config.Defaults(), sched)
	m.evidence = &countingEvidence{}

	if _, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: "research Company X", Seeds: []string{seedURL()},
	}); err != nil {
		t.Fatal(err)
	}

	t1 := model.Task{
		ID:        model.NewTaskID(),
		SessionID: m.state.Session.ID,
		Type:      model.TaskTypeFetchHTTP,
		State:     model.TaskStateRunning,
	}
	d := Decision{Action: DecisionStop, Reason: "test: deterministic stop"}

	// First DecisionStop — should fire and set StopPending + Terminal
	// First DecisionStop — should fire and set StopPending + Terminal,
	// returning true so the Run loop enters the handleStop drain path (5s + Cancel).
	stopped := m.applyDecision(context.Background(), t1, model.TaskResult{}, d)
	if !stopped {
		t.Fatal("first DecisionStop should return true (handleStop path)")
	}
	if !m.StopPending {
		t.Error("first DecisionStop should set StopPending=true")
	}
	if !m.state.Terminal {
		t.Error("first DecisionStop should set Terminal=true")
	}
	reasonBefore := m.state.TerminalReason

	// Second DecisionStop on a different task — must be idempotent: no double side-effect
	t2 := model.Task{
		ID:        model.NewTaskID(),
		SessionID: m.state.Session.ID,
		Type:      model.TaskTypeFetchHTTP,
		State:     model.TaskStateRunning,
	}
	stopped2 := m.applyDecision(context.Background(), t2, model.TaskResult{}, d)
	if !stopped2 {
		t.Fatal("second DecisionStop should return true (idempotent)")
	}
	if m.state.TerminalReason != reasonBefore {
		t.Errorf("second DecisionStop changed terminal reason: was %q now %q (idempotency violated)",
			reasonBefore, m.state.TerminalReason)
	}
	if !m.StopPending {
		t.Error("StopPending should remain true after idempotent call")
	}
	if !m.state.Terminal {
		t.Error("Terminal should remain true after idempotent call")
	}
}

func TestDecisionStopContradictionExhaustionPreservesResearchComplete(t *testing.T) {
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{Status: model.RetrievalStatusSuccess}
	}}
	sched := newFakeScheduler(mgr, 4)
	m := NewMaster(config.Defaults(), sched)
	m.evidence = &countingEvidence{}

	if _, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: "research Company X", Seeds: []string{seedURL()},
	}); err != nil {
		t.Fatal(err)
	}

	// Simulate contradiction-exhaustion: evidence has contradictions >=
	// KContradictions (default 2) and replan budget is exhausted.
	m.mu.Lock()
	m.state.Evidence.Contradictions = 2
	m.state.ReplanCount = 2
	m.state.MaxReplans = 2
	m.mu.Unlock()

	tk := model.Task{
		ID:        model.NewTaskID(),
		SessionID: m.state.Session.ID,
		Type:      model.TaskTypeFetchHTTP,
		State:     model.TaskStateRunning,
	}
	d := Decision{
		Action: DecisionStop,
		Reason: "replan trigger (CONTRADICTION) required but replan budget exhausted (2/2); deterministic stop",
	}

	// P10 idempotency guard (edge-3): the first DecisionStop returns true —
	// it sets StopPending + Terminal, returning true so the Run loop enters
	// the handleStop drain path (5s + Cancel); idempotent re-entry also returns true. Mirrors
	// TestDecisionStopIdempotent. The Phase F terminal reason is verified below.
	stopped := m.applyDecision(context.Background(), tk, model.TaskResult{}, d)
	if !stopped {
		t.Fatal("first DecisionStop should return true (handleStop path)")
	}
	if !m.StopPending {
		t.Error("first DecisionStop should set StopPending=true")
	}
	if !m.state.Terminal {
		t.Error("first DecisionStop should set Terminal=true")
	}
	// Phase F: contradiction-exhaustion must preserve "research_complete"
	if m.state.TerminalReason != "research_complete" {
		t.Errorf("contradiction-exhaustion terminal reason: got %q want %q (Phase F)",
			m.state.TerminalReason, "research_complete")
	}
}

func TestDecisionStopNonContradictionUsesDeterministicReason(t *testing.T) {
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{Status: model.RetrievalStatusSuccess}
	}}
	sched := newFakeScheduler(mgr, 4)
	m := NewMaster(config.Defaults(), sched)
	m.evidence = &countingEvidence{}

	if _, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: "research Company X", Seeds: []string{seedURL()},
	}); err != nil {
		t.Fatal(err)
	}

	// MissingPrimary exhaustion: no contradictions, so terminal reason
	// should remain the "deterministic stop" reason from the Decision.
	m.mu.Lock()
	m.state.Evidence.MissingPrimary = 2
	m.state.ReplanCount = 2
	m.state.MaxReplans = 2
	m.mu.Unlock()

	tk := model.Task{
		ID:        model.NewTaskID(),
		SessionID: m.state.Session.ID,
		Type:      model.TaskTypeFetchHTTP,
		State:     model.TaskStateRunning,
	}
	d := Decision{
		Action: DecisionStop,
		Reason: "replan trigger (MISSING_PRIMARY) required but replan budget exhausted (2/2); deterministic stop",
	}

	_ = m.applyDecision(context.Background(), tk, model.TaskResult{}, d)
	if m.state.TerminalReason != d.Reason {
		t.Errorf("non-contradiction stop reason: got %q want %q",
			m.state.TerminalReason, d.Reason)
	}
}

func TestStopPendingSessionIsolation(t *testing.T) {
	// Single-session model (Phase 0/A1): StopPending is stored on
	// ResearchState (not map[SessionID]bool). When a new session is submitted
	// via SubmitIntent, the drain/timeout logic clears StopPending so the new
	// session starts clean — no leakage from the previous session.
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{Status: model.RetrievalStatusSuccess}
	}}
	sched := newFakeScheduler(mgr, 4)
	m := NewMaster(config.Defaults(), sched)
	m.evidence = &countingEvidence{}

	// First session
	if _, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: "research Company X", Seeds: []string{seedURL()},
	}); err != nil {
		t.Fatal(err)
	}
	firstSessionID := m.state.Session.ID

	// Simulate DecisionStop having fired on the first session
	m.mu.Lock()
	m.StopPending = true
	m.state.Terminal = true
	m.pending = map[model.TaskID]bool{}
	m.tasks = map[model.TaskID]model.Task{}
	m.mu.Unlock()
	// Clear scheduler queue so drained() returns true immediately
	sched.mu.Lock()
	sched.queue = nil
	sched.running = 0
	sched.mu.Unlock()

	// Second session — should drain and clear StopPending
	if _, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: "research Company Y", Seeds: []string{"https://other.example.com"},
	}); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	newStopPending := m.StopPending
	newSessionID := m.state.Session.ID
	pendingCount := len(m.pending)
	m.mu.Unlock()

	if newStopPending {
		t.Error("StopPending should be false for new session (cleared by drain)")
	}
	if newSessionID == firstSessionID {
		t.Error("new session should have a different ID")
	}
	if pendingCount != 1 {
		t.Errorf("pending tasks: got %d want 1 (only new seed task)", pendingCount)
	}
}

func TestRejectTaskInjectionWhileStopPending(t *testing.T) {
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{Status: model.RetrievalStatusSuccess}
	}}
	sched := newFakeScheduler(mgr, 4)
	obs := &fakeEventObserver{}
	m := NewMaster(config.Defaults(), sched, WithEventObserver(obs))
	m.evidence = &countingEvidence{}

	if _, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: "research Company X", Seeds: []string{seedURL()},
	}); err != nil {
		t.Fatal(err)
	}

	// Simulate DecisionStop having fired
	m.mu.Lock()
	m.StopPending = true
	m.state.Terminal = true
	m.mu.Unlock()

	tk := model.Task{
		ID:        model.NewTaskID(),
		SessionID: m.state.Session.ID,
		Type:      model.TaskTypeFetchHTTP,
		State:     model.TaskStateRunning,
	}

	// DecisionExecute with Replan while StopPending → rejected
	d := Decision{
		Action: DecisionExecute,
		Reason: "success",
		Replan: &ReplanTrigger{Kind: ReplanTriggerContradiction},
	}
	stopped := m.applyDecision(context.Background(), tk, model.TaskResult{}, d)
	if stopped {
		t.Error("DecisionExecute while StopPending should not return stop=true")
	}

	// Verify rejection event was emitted
	if !obs.hasEventKind("stop_pending_reject") {
		t.Error("expected stop_pending_reject event to be emitted")
	}
}

func (o *fakeEventObserver) hasEventKind(kind string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, ev := range o.events {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}
