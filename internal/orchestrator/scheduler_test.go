package orchestrator

import (
	"net/url"
	"testing"
	"time"

	"draw/internal/config"
	"draw/internal/frontier"
	"draw/internal/manager"
	"draw/internal/model"
)

type fakeManager struct {
	caps []manager.Capability
	name string
}

func (f *fakeManager) Capabilities() []manager.Capability { return f.caps }
func (f *fakeManager) Execute(model.Task) (model.TaskResult, error) {
	return model.TaskResult{Status: model.RetrievalStatusSuccess}, nil
}

func newTestScheduler(t *testing.T, src storageSourceRegistry) *Scheduler {
	t.Helper()
	cfg := config.Defaults()
	rc := NewResourceController(cfg, NoopResourceSampler{})
	fr := frontier.NewMemoryFrontier(cfg, src)
	return NewScheduler(cfg, fr, rc, src, DefaultTaskCapabilities())
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return u
}

func testTask(id model.TaskID, session model.SessionID, tt model.TaskType, priority, cost int, domain string) model.Task {
	return model.Task{
		ID:            id,
		SessionID:     session,
		Type:          tt,
		State:         model.TaskStateReady,
		Priority:      priority,
		SourceTarget:  domain,
		EstimatedCost: cost,
		TaskKey:       string(id),
		CreatedAt:     time.Unix(1000, 0),
	}
}

func itoaOS(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

type storageSourceRegistry = interface {
	Lookup(string) (*model.SourceProfile, bool)
	Upsert(model.SourceProfile) error
	Deny(string) error
	ProvisionalUpsert(model.SourceProfile) error
}

type testRegistry struct {
	profiles map[string]model.SourceProfile
}

func (r testRegistry) Lookup(domain string) (*model.SourceProfile, bool) {
	p, ok := r.profiles[domain]
	if !ok {
		return nil, false
	}
	return &p, true
}
func (r testRegistry) Upsert(model.SourceProfile) error            { return nil }
func (r testRegistry) Deny(string) error                           { return nil }
func (r testRegistry) ProvisionalUpsert(model.SourceProfile) error { return nil }

func TestSchedulerSubmitDedupAgainstPending(t *testing.T) {
	s := newTestScheduler(t, NoopSourceRegistry{})
	t1 := testTask("t1", "ses1", model.TaskTypeFetchHTTP, 100, 1, "a.example")
	if err := s.Submit(t1); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(t1); err != nil {
		t.Fatal(err)
	}
	if st := s.Stats(); st.Queued != 1 {
		t.Errorf("duplicate submit should not queue twice: got Queued=%d want 1", st.Queued)
	}
}

func TestSchedulerSubmitRetryResubmitAllowed(t *testing.T) {
	s := newTestScheduler(t, NoopSourceRegistry{})
	s.RegisterManager(&fakeManager{caps: []manager.Capability{manager.CapHTTP}}, []manager.Capability{manager.CapHTTP})
	now := time.Unix(1000, 0)
	t1 := testTask("t1", "ses1", model.TaskTypeFetchHTTP, 100, 1, "a.example")
	if err := s.Submit(t1); err != nil {
		t.Fatal(err)
	}
	_, _, slot, ok := s.Admit(now)
	if !ok {
		t.Fatal("expected admit")
	}
	s.Release(slot, t1.ID, false)
	t1.State = model.TaskStateRetryWait
	t1.BackoffUntil = now
	if err := s.Submit(t1); err != nil {
		t.Fatal(err)
	}
	if st := s.Stats(); st.Queued != 1 {
		t.Errorf("retry re-submit should requeue: got Queued=%d want 1", st.Queued)
	}
}

func TestSchedulerGlobalQueueOrdering(t *testing.T) {
	s := newTestScheduler(t, NoopSourceRegistry{})
	s.RegisterManager(&fakeManager{caps: []manager.Capability{manager.CapHTTP}}, []manager.Capability{manager.CapHTTP})
	now := time.Unix(1000, 0)
	low := testTask("t-low", "ses1", model.TaskTypeFetchHTTP, 10, 1, "a.example")
	hi := testTask("t-hi", "ses1", model.TaskTypeFetchHTTP, 1000, 1, "a.example")
	if err := s.Submit(low); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(hi); err != nil {
		t.Fatal(err)
	}
	task, _, _, ok := s.Admit(now)
	if !ok {
		t.Fatal("expected admit")
	}
	if string(task.ID) != "t-hi" {
		t.Errorf("higher priority should admit first: got %s want t-hi", task.ID)
	}
}

func TestSchedulerCostAscTieBreak(t *testing.T) {
	s := newTestScheduler(t, NoopSourceRegistry{})
	s.RegisterManager(&fakeManager{caps: []manager.Capability{manager.CapHTTP}}, []manager.Capability{manager.CapHTTP})
	now := time.Unix(1000, 0)
	h := testTask("t-h", "ses1", model.TaskTypeFetchHTTP, 500, 3, "a.example")
	l := testTask("t-l", "ses1", model.TaskTypeFetchHTTP, 500, 1, "a.example")
	if err := s.Submit(h); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(l); err != nil {
		t.Fatal(err)
	}
	task, _, _, ok := s.Admit(now)
	if !ok || string(task.ID) != "t-l" {
		t.Errorf("lower cost should win tie at equal priority: got ok=%v id=%s", ok, task.ID)
	}
}

func TestSchedulerGlobalCapEnforced(t *testing.T) {
	cfg := config.Defaults()
	cfg.MaxGlobalConcurrency = 3
	rc := NewResourceController(cfg, NoopResourceSampler{})
	fr := frontier.NewMemoryFrontier(cfg, NoopSourceRegistry{})
	s := NewScheduler(cfg, fr, rc, NoopSourceRegistry{}, DefaultTaskCapabilities())
	s.RegisterManager(&fakeManager{caps: []manager.Capability{manager.CapHTTP}}, []manager.Capability{manager.CapHTTP})
	now := time.Unix(1000, 0)
	for i := 0; i < 5; i++ {
		if err := s.Submit(testTask(model.TaskID("t"+itoaOS(i)), "ses1", model.TaskTypeFetchHTTP, 500, 1, "a.example")); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, _, _, ok := s.Admit(now); !ok {
			t.Fatalf("expected admit %d within global cap", i)
		}
	}
	if _, _, _, ok := s.Admit(now); ok {
		t.Error("4th admit should exceed global cap and fail")
	}
}

func TestSchedulerPerDomainLimit(t *testing.T) {
	cfg := config.Defaults()
	cfg.DomainLimits = map[string]int{"a.example": 1}
	rc := NewResourceController(cfg, NoopResourceSampler{})
	fr := frontier.NewMemoryFrontier(cfg, NoopSourceRegistry{})
	s := NewScheduler(cfg, fr, rc, NoopSourceRegistry{}, DefaultTaskCapabilities())
	s.RegisterManager(&fakeManager{caps: []manager.Capability{manager.CapHTTP}}, []manager.Capability{manager.CapHTTP})
	now := time.Unix(1000, 0)
	if err := s.Submit(testTask("t1", "ses1", model.TaskTypeFetchHTTP, 500, 1, "a.example")); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(testTask("t2", "ses1", model.TaskTypeFetchHTTP, 500, 1, "a.example")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := s.Admit(now); !ok {
		t.Fatal("expected first a.example task to admit")
	}
	if _, _, _, ok := s.Admit(now); ok {
		t.Error("second a.example task should be blocked by per-domain limit of 1")
	}
}

func TestSchedulerAdmitSetsRunningState(t *testing.T) {
	s := newTestScheduler(t, NoopSourceRegistry{})
	s.RegisterManager(&fakeManager{caps: []manager.Capability{manager.CapHTTP}}, []manager.Capability{manager.CapHTTP})
	now := time.Unix(1000, 0)
	if err := s.Submit(testTask("t1", "ses1", model.TaskTypeFetchHTTP, 500, 1, "a.example")); err != nil {
		t.Fatal(err)
	}
	task, _, _, ok := s.Admit(now)
	if !ok {
		t.Fatal("expected admit")
	}
	if task.State != model.TaskStateRunning {
		t.Errorf("admitted task state: got %s want %s", task.State, model.TaskStateRunning)
	}
	if !task.StartedAt.Equal(now) {
		t.Errorf("started at should be admit-now: got %v want %v", task.StartedAt, now)
	}
}

func TestSchedulerBackoffGating(t *testing.T) {
	s := newTestScheduler(t, NoopSourceRegistry{})
	s.RegisterManager(&fakeManager{caps: []manager.Capability{manager.CapHTTP}}, []manager.Capability{manager.CapHTTP})
	now := time.Unix(1000, 0)
	t1 := testTask("t1", "ses1", model.TaskTypeFetchHTTP, 500, 1, "a.example")
	t1.State = model.TaskStateRetryWait
	t1.BackoffUntil = now.Add(5 * time.Second)
	if err := s.Submit(t1); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := s.Admit(now); ok {
		t.Error("task with future backoff should not admit")
	}
	if _, _, _, ok := s.Admit(now.Add(6 * time.Second)); !ok {
		t.Error("task should admit after backoff elapsed")
	}
}

func TestSchedulerFrontierDrainDedup(t *testing.T) {
	cfg := config.Defaults()
	cfg.MaxGlobalConcurrency = 3
	rc := NewResourceController(cfg, NoopResourceSampler{})
	fr := frontier.NewMemoryFrontier(cfg, NoopSourceRegistry{})
	s := NewScheduler(cfg, fr, rc, NoopSourceRegistry{}, DefaultTaskCapabilities())
	s.RegisterManager(&fakeManager{caps: []manager.Capability{manager.CapHTTP}}, []manager.Capability{manager.CapHTTP})
	now := time.Unix(1000, 0)
	uA := mustURL(t, "https://a.example/x")
	candA := frontier.URLCandidate{SessionID: "ses1", URL: uA, Domain: "a.example", PriorityHint: 5}
	if err := fr.Push([]frontier.URLCandidate{candA}); err != nil {
		t.Fatal(err)
	}
	task, _, _, ok := s.Admit(now)
	if !ok {
		t.Fatal("expected first frontier candidate to admit")
	}
	if task.SourceTarget != "a.example" {
		t.Errorf("drained task domain: got %q want a.example", task.SourceTarget)
	}
	// duplicate url: deduped at frontier level (byKey already holds canonical key)
	if err := fr.Push([]frontier.URLCandidate{candA}); err != nil {
		t.Fatal(err)
	}
	if fr.Len() != 1 {
		t.Errorf("frontier len after dup push: got %d want 1", fr.Len())
	}
	// non-destructive peek + scheduler keyIndex must NOT re-materialize the drained candidate
	if _, _, _, ok := s.Admit(now); ok {
		t.Error("admit must not re-materialize an already-drained frontier candidate")
	}
	// a distinct candidate still flows through within the same drain batch
	uB := mustURL(t, "https://b.example/y")
	if err := fr.Push([]frontier.URLCandidate{{SessionID: "ses1", URL: uB, Domain: "b.example", PriorityHint: 1}}); err != nil {
		t.Fatal(err)
	}
	_, _, _, ok = s.Admit(now)
	if !ok {
		t.Error("distinct frontier candidate should admit")
	}
}

func TestSchedulerMinProfileLimit(t *testing.T) {
	cfg := config.Defaults()
	cfg.MaxGlobalConcurrency = 5
	cfg.DomainLimits = map[string]int{"a.example": 5}
	src := testRegistry{profiles: map[string]model.SourceProfile{"a.example": {PerDomainLimit: 1}}}
	rc := NewResourceController(cfg, NoopResourceSampler{})
	fr := frontier.NewMemoryFrontier(cfg, src)
	s := NewScheduler(cfg, fr, rc, src, DefaultTaskCapabilities())
	s.RegisterManager(&fakeManager{caps: []manager.Capability{manager.CapHTTP}}, []manager.Capability{manager.CapHTTP})
	now := time.Unix(1000, 0)
	if err := s.Submit(testTask("t1", "ses1", model.TaskTypeFetchHTTP, 500, 1, "a.example")); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(testTask("t2", "ses1", model.TaskTypeFetchHTTP, 500, 1, "a.example")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := s.Admit(now); !ok {
		t.Fatal("expected first a.example task to admit (profile limit governs)")
	}
	if _, _, _, ok := s.Admit(now); ok {
		t.Error("second a.example task should be blocked: min(profile=1, config=5) = 1")
	}
}

func TestSchedulerBrowserCapConcurrency(t *testing.T) {
	cfg := config.Defaults()
	cfg.MaxGlobalConcurrency = 20
	cfg.Upgrade.BrowserMaxConcurrency = 2
	rc := NewResourceController(cfg, NoopResourceSampler{})
	fr := frontier.NewMemoryFrontier(cfg, NoopSourceRegistry{})
	s := NewScheduler(cfg, fr, rc, NoopSourceRegistry{}, DefaultTaskCapabilities())
	s.RegisterManager(&fakeManager{caps: []manager.Capability{manager.CapBrowserAnon}}, []manager.Capability{manager.CapBrowserAnon})
	now := time.Unix(1000, 0)
	for i := 0; i < 4; i++ {
		if err := s.Submit(testTask(model.TaskID("b"+itoaOS(i)), "ses1", model.TaskTypeFetchBrowser, 1, 1, "b"+itoaOS(i)+".example")); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, ok := s.Admit(now); !ok {
		t.Fatal("expected first browser task to admit")
	}
	if _, _, _, ok := s.Admit(now); !ok {
		t.Fatal("expected second browser task to admit (browser cap = 2)")
	}
	if _, _, _, ok := s.Admit(now); ok {
		t.Error("third browser task should be blocked by browser concurrency cap")
	}
}

func TestSchedulerCountersFreeOnRelease(t *testing.T) {
	cfg := config.Defaults()
	cfg.MaxGlobalConcurrency = 1
	rc := NewResourceController(cfg, NoopResourceSampler{})
	fr := frontier.NewMemoryFrontier(cfg, NoopSourceRegistry{})
	s := NewScheduler(cfg, fr, rc, NoopSourceRegistry{}, DefaultTaskCapabilities())
	s.RegisterManager(&fakeManager{caps: []manager.Capability{manager.CapHTTP}}, []manager.Capability{manager.CapHTTP})
	now := time.Unix(1000, 0)
	if err := s.Submit(testTask("t1", "ses1", model.TaskTypeFetchHTTP, 500, 1, "a.example")); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(testTask("t2", "ses1", model.TaskTypeFetchHTTP, 500, 1, "a.example")); err != nil {
		t.Fatal(err)
	}
	task, _, slot, ok := s.Admit(now)
	if !ok {
		t.Fatal("expected t1 to admit")
	}
	if _, _, _, ok := s.Admit(now); ok {
		t.Error("second task should be blocked while t1 is active (global cap = 1)")
	}
	s.Release(slot, task.ID, true)
	if _, _, _, ok := s.Admit(now); !ok {
		t.Error("after release, t2 should be admissible again")
	}
}

func TestSchedulerStatsIntegration(t *testing.T) {
	cfg := config.Defaults()
	cfg.MaxGlobalConcurrency = 2
	rc := NewResourceController(cfg, NoopResourceSampler{})
	fr := frontier.NewMemoryFrontier(cfg, NoopSourceRegistry{})
	s := NewScheduler(cfg, fr, rc, NoopSourceRegistry{}, DefaultTaskCapabilities())
	s.RegisterManager(&fakeManager{caps: []manager.Capability{manager.CapHTTP}}, []manager.Capability{manager.CapHTTP})
	now := time.Unix(1000, 0)
	if err := s.Submit(testTask("t1", "ses1", model.TaskTypeFetchHTTP, 500, 1, "a.example")); err != nil {
		t.Fatal(err)
	}
	task, _, _, ok := s.Admit(now)
	if !ok {
		t.Fatal("expected t1 to admit")
	}
	st := s.Stats()
	if st.Active != 1 {
		t.Errorf("Active after admit: got %d want 1", st.Active)
	}
	if st.Queued != 0 {
		t.Errorf("Queued after admit: got %d want 0", st.Queued)
	}
	if st.GlobalCap != 2 {
		t.Errorf("GlobalCap: got %d want 2", st.GlobalCap)
	}
	if st.DomainActive["a.example"] != 1 {
		t.Errorf("DomainActive[a.example]: got %d want 1", st.DomainActive["a.example"])
	}
	for i := 0; i < 2; i++ {
		if err := s.Submit(testTask(model.TaskID("q"+itoaOS(i)), "ses1", model.TaskTypeFetchHTTP, 500, 1, "a.example")); err != nil {
			t.Fatal(err)
		}
	}
	st = s.Stats()
	if st.Active != 1 {
		t.Errorf("Active after queueing: got %d want 1", st.Active)
	}
	if st.Queued != 2 {
		t.Errorf("Queued after queueing: got %d want 2", st.Queued)
	}
	s.Release(s.active[task.ID].slot, task.ID, true)
	st = s.Stats()
	if st.Active != 0 {
		t.Errorf("Active after release: got %d want 0", st.Active)
	}
	if st.DomainActive["a.example"] != 0 {
		t.Errorf("DomainActive after release: got %d want 0", st.DomainActive["a.example"])
	}
}
