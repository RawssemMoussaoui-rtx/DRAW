package master_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"draw/internal/config"
	"draw/internal/frontier"
	"draw/internal/manager"
	"draw/internal/master"
	"draw/internal/model"
	"draw/internal/orchestrator"
	"draw/internal/storage"
)

// sve3Manager is a deterministic fake manager that returns JSON evidence
// payloads keyed by URL hostname, enabling byte-identical output across runs.
type sve3Manager struct {
	mu       sync.Mutex
	calls    int
	failNext bool
}

func (m *sve3Manager) Capabilities() []manager.Capability {
	return []manager.Capability{manager.CapHTTP}
}

func (m *sve3Manager) Execute(t model.Task) (model.TaskResult, error) {
	m.mu.Lock()
	m.calls++
	shouldFail := m.failNext
	if shouldFail {
		m.failNext = false
	}
	m.mu.Unlock()

	if shouldFail {
		return model.TaskResult{}, fmt.Errorf("simulated execution failure")
	}

	host := "unknown"
	if t.SourceTarget != "" {
		host = t.SourceTarget
	} else if t.URL != nil {
		host = t.URL.Hostname()
	}

	payload := fmt.Sprintf(`[{"name":"Acme","host":"%s","revenue":%d}]`, host, 100+len(host))

	return model.TaskResult{
		Status:  model.RetrievalStatusSuccess,
		Data:    []byte(payload),
		Headers: http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

// sve3Setup creates the real Scheduler + MemoryFrontier + Master for testing.
func sve3Setup(t *testing.T) (*master.Master, *orchestrator.Scheduler, *frontier.MemoryFrontier, *storage.SQLiteEvidenceStore, *sve3Manager) {
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

	cfg := config.Defaults()
	cfg.MaxGlobalConcurrency = 4
	// Disable replan triggers and shorten retry backoff so sessions
	// complete in milliseconds rather than waiting through drain timeouts.
	cfg.Replan.KContradictions = 0
	cfg.Replan.MMissingPrimary = 0
	cfg.Replan.SStaleSources = 0
	cfg.Retry.BackoffBase = time.Millisecond
	cfg.Retry.BackoffMax = 10 * time.Millisecond
	src := orchestrator.NoopSourceRegistry{}
	fr := frontier.NewMemoryFrontier(cfg, src)
	rc := orchestrator.NewResourceController(cfg, orchestrator.NoopResourceSampler{})
	sched := orchestrator.NewScheduler(cfg, fr, rc, src, orchestrator.DefaultTaskCapabilities())
	mgr := &sve3Manager{}
	if err := sched.RegisterManager(mgr, mgr.Capabilities()); err != nil {
		t.Fatalf("register manager: %v", err)
	}
	m := master.NewMaster(cfg, sched,
		master.WithEvidenceStore(es),
		master.WithSourceRegistry(reg),
	)
	return m, sched, fr, es, mgr
}

// sve3PushFrontier pushes URL candidates for a session into the frontier.
func sve3PushFrontier(t *testing.T, fr *frontier.MemoryFrontier, sid model.SessionID, hosts []string) {
	t.Helper()
	cands := make([]frontier.URLCandidate, 0, len(hosts))
	for _, h := range hosts {
		u, err := url.Parse("https://" + h + "/page")
		if err != nil {
			t.Fatalf("url.Parse: %v", err)
		}
		cands = append(cands, frontier.URLCandidate{
			SessionID:    sid,
			URL:          u,
			Domain:       h,
			SourceClass:  model.SourceClassUnknown,
			PriorityHint: 500,
		})
	}
	if err := fr.Push(cands); err != nil {
		t.Fatalf("fr.Push: %v", err)
	}
}

// sve3RunSession submits and runs a single session with the given seeds and
// frontier hosts. Frontier candidates are pushed after SubmitIntent (which
// calls ResetSession) but before Run (which calls Admit -> drainFrontier).
func sve3RunSession(t *testing.T, m *master.Master, fr *frontier.MemoryFrontier, seeds, frontierHosts []string) model.SessionID {
	t.Helper()
	sid, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1",
		Query:  "research Company X",
		Seeds:  seeds,
	})
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}
	if len(frontierHosts) > 0 {
		sve3PushFrontier(t, fr, sid, frontierHosts)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := m.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return sid
}

// neItem is a normalized evidence item with non-deterministic fields stripped.
type neItem struct {
	SourceID      string  `json:"source_id"`
	Topic         string  `json:"topic"`
	Claim         string  `json:"claim"`
	Value         string  `json:"value"`
	Confidence    float64 `json:"confidence"`
	Verification  string  `json:"verification"`
	OriginURL     string  `json:"origin_url,omitempty"`
	ExtractionSeq int     `json:"extraction_seq,omitempty"`
}

// normalizeEvidence strips non-deterministic fields (ID, SessionID, TaskID,
// CollectedAt) and sorts the result so that N independent runs produce
// byte-identical output for byte-identical evidence/aggregation.
func normalizeEvidence(evs []model.Evidence) []byte {
	out := make([]neItem, 0, len(evs))
	for _, ev := range evs {
		item := neItem{
			SourceID:     string(ev.SourceID),
			Topic:        ev.Topic,
			Claim:        ev.Claim,
			Value:        ev.Value,
			Confidence:   ev.Confidence,
			Verification: string(ev.Verification),
		}
		if ev.OriginURL != nil {
			item.OriginURL = *ev.OriginURL
		}
		if ev.ExtractionSeq != nil {
			item.ExtractionSeq = *ev.ExtractionSeq
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		si := strings.Join([]string{out[i].SourceID, out[i].Topic, out[i].Claim, out[i].Value, out[i].OriginURL}, "|")
		sj := strings.Join([]string{out[j].SourceID, out[j].Topic, out[j].Claim, out[j].Value, out[j].OriginURL}, "|")
		if si != sj {
			return si < sj
		}
		return out[i].ExtractionSeq < out[j].ExtractionSeq
	})
	b, _ := json.Marshal(out)
	return b
}

// TestSVE3_SessionScopedDedup runs N=5 independent iterations. Each iteration
// creates a fresh real Scheduler + MemoryFrontier + Master, runs 3 sessions
// with overlapping seed/frontier URL sets, and asserts:
//   - Byte-identical evidence output across all N runs.
//   - After all sessions complete and are reset, both the frontier's byKey
//     and the scheduler's materialized maps return length 0.
func TestSVE3_SessionScopedDedup(t *testing.T) {
	const runs = 5

	sessions := []struct {
		name          string
		seeds         []string
		frontierHosts []string
	}{
		{
			"s1",
			[]string{"https://alpha.example", "https://beta.example", "https://gamma.example"},
			[]string{"alpha.example", "beta.example", "gamma.example"},
		},
		{
			"s2",
			[]string{"https://beta.example", "https://gamma.example", "https://delta.example"},
			[]string{"beta.example", "gamma.example", "delta.example"},
		},
		{
			"s3",
			[]string{"https://gamma.example", "https://delta.example", "https://epsilon.example"},
			[]string{"gamma.example", "delta.example", "epsilon.example"},
		},
	}

	var outputs [][]byte

	for run := 0; run < runs; run++ {
		m, sched, fr, es, _ := sve3Setup(t)

		for _, s := range sessions {
			sid := sve3RunSession(t, m, fr, s.seeds, s.frontierHosts)
			t.Logf("run %d %s: sid=%s", run, s.name, sid)
		}

		// After all 3 sessions complete and are reset, assert clean state.
		// The Master's Run defer calls ResetSession for the final session;
		// SubmitIntent called ResetSession for each preceding session.
		if fr.Len() != 0 {
			t.Errorf("run %d: frontier byKey not empty after reset: %d", run, fr.Len())
		}
		if sched.TotalMaterialized() != 0 {
			t.Errorf("run %d: scheduler materialized not empty after reset: %d", run, sched.TotalMaterialized())
		}
		stats := sched.Stats()
		if stats.Queued != 0 || stats.Active != 0 {
			t.Errorf("run %d: scheduler not drained: queued=%d active=%d", run, stats.Queued, stats.Active)
		}

		// Capture normalized evidence for cross-run comparison.
		evs := es.Query(storage.EvidenceFilter{})
		outputs = append(outputs, normalizeEvidence(evs))
	}

	// Assert byte-identical evidence/aggregation output across all runs.
	for i := 1; i < len(outputs); i++ {
		if !bytes.Equal(outputs[0], outputs[i]) {
			t.Errorf("run %d: evidence output differs from run 0\n  run0: %s\n  run%d: %s",
				i, outputs[0], i, outputs[i])
		}
	}
}

// TestSVE3_ConcurrentFrontierSessions verifies that concurrent pushes for
// two different sessions with overlapping URLs do not collide, and that
// ResetSession for one session leaves the other intact.
func TestSVE3_ConcurrentFrontierSessions(t *testing.T) {
	cfg := config.Defaults()
	src := orchestrator.NoopSourceRegistry{}
	fr := frontier.NewMemoryFrontier(cfg, src)
	rc := orchestrator.NewResourceController(cfg, orchestrator.NoopResourceSampler{})
	sched := orchestrator.NewScheduler(cfg, fr, rc, src, orchestrator.DefaultTaskCapabilities())

	s1 := model.SessionID("ses:alpha")
	s2 := model.SessionID("ses:beta")
	shared := "shared.example"

	cands := func(sid model.SessionID, hosts []string) []frontier.URLCandidate {
		out := make([]frontier.URLCandidate, 0, len(hosts))
		for _, h := range hosts {
			u, _ := url.Parse("https://" + h + "/page")
			out = append(out, frontier.URLCandidate{
				SessionID:    sid,
				URL:          u,
				Domain:       h,
				SourceClass:  model.SourceClassUnknown,
				PriorityHint: 500,
			})
		}
		return out
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = fr.Push(cands(s1, []string{shared, "unique1.example"}))
	}()
	go func() {
		defer wg.Done()
		_ = fr.Push(cands(s2, []string{shared, "unique2.example"}))
	}()
	wg.Wait()

	if fr.Len() != 4 {
		t.Errorf("frontier len after concurrent push: got %d want 4 (two sessions, two unique + one shared each)", fr.Len())
	}

	// Reset session 1; session 2 must remain intact.
	if err := fr.ResetSession(s1); err != nil {
		t.Fatalf("ResetSession(s1): %v", err)
	}
	if fr.Len() != 2 {
		t.Errorf("frontier len after reset s1: got %d want 2 (s2 only)", fr.Len())
	}

	// Reset session 2; everything should be clean.
	if err := fr.ResetSession(s2); err != nil {
		t.Fatalf("ResetSession(s2): %v", err)
	}
	if fr.Len() != 0 {
		t.Errorf("frontier len after reset s2: got %d want 0", fr.Len())
	}
	if sched.TotalMaterialized() != 0 {
		t.Errorf("scheduler materialized after resets: got %d want 0", sched.TotalMaterialized())
	}
}

// TestSVE3_SessionIDReuse verifies that after ResetSession, the same session
// ID can be re-used without stale dedup entries contaminating the new run.
func TestSVE3_SessionIDReuse(t *testing.T) {
	cfg := config.Defaults()
	src := orchestrator.NoopSourceRegistry{}
	fr := frontier.NewMemoryFrontier(cfg, src)

	sid := model.SessionID("reuse:ses")

	// First push + reset.
	cands := func() []frontier.URLCandidate {
		u, _ := url.Parse("https://example.com/page")
		return []frontier.URLCandidate{{
			SessionID: sid, URL: u, Domain: "example.com",
			SourceClass: model.SourceClassUnknown, PriorityHint: 500,
		}}
	}
	_ = fr.Push(cands())
	if fr.Len() != 1 {
		t.Fatalf("frontier len after first push: got %d want 1", fr.Len())
	}
	_ = fr.ResetSession(sid)
	if fr.Len() != 0 {
		t.Fatalf("frontier len after first reset: got %d want 0", fr.Len())
	}

	// Second push with the SAME session ID — must not be deduped against nothing.
	_ = fr.Push(cands())
	if fr.Len() != 1 {
		t.Errorf("frontier len after reuse push: got %d want 1 (should re-accept)", fr.Len())
	}
	_ = fr.ResetSession(sid)
	if fr.Len() != 0 {
		t.Errorf("frontier len after reuse reset: got %d want 0", fr.Len())
	}
}

// TestSVE3_CancelMidFlight verifies that Cancel invokes ResetSession,
// cleaning the scheduler and frontier even when tasks are still in-flight.
func TestSVE3_CancelMidFlight(t *testing.T) {
	m, sched, fr, _, _ := sve3Setup(t)

	seeds := []string{
		"https://s1.example", "https://s2.example", "https://s3.example",
		"https://s4.example", "https://s5.example",
	}
	sid, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: "research Company X", Seeds: seeds,
	})
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}
	sve3PushFrontier(t, fr, sid, []string{"s1.example", "s2.example", "s3.example"})

	done := make(chan error, 1)
	go func() { done <- m.Run(context.Background()) }()

	// Wait until at least one worker is in-flight.
	deadline := time.After(2 * time.Second)
	for {
		if sched.Stats().Active > 0 {
			break
		}
		select {
		case <-time.After(time.Millisecond):
		case <-deadline:
			t.Fatal("no worker ever started executing")
		}
	}

	m.Cancel()

	select {
	case err := <-done:
		_ = err // Run may return context.Canceled or nil
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Cancel within 5s")
	}

	// ResetSession (called inside Cancel) must have cleaned the scheduler.
	if sched.TotalMaterialized() != 0 {
		t.Errorf("scheduler materialized after Cancel: got %d want 0", sched.TotalMaterialized())
	}
	if fr.Len() != 0 {
		t.Errorf("frontier byKey after Cancel: got %d want 0", fr.Len())
	}
	stats := sched.Stats()
	if stats.Queued != 0 {
		t.Errorf("scheduler queued after Cancel: got %d want 0", stats.Queued)
	}
}

// TestSVE3_FailureLeak verifies that task execution failures (error returns)
// do not prevent Run's defer from calling ResetSession — the scheduler and
// frontier must be clean after Run returns.
func TestSVE3_FailureLeak(t *testing.T) {
	m, sched, fr, _, mgr := sve3Setup(t)

	seeds := []string{"https://fail.example"}
	sid, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: "research Company X", Seeds: seeds,
	})
	if err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}
	sve3PushFrontier(t, fr, sid, []string{"fail.example"})

	// First Execute call fails; subsequent calls succeed.
	mgr.mu.Lock()
	mgr.failNext = true
	mgr.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := m.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	st := m.State()
	if !st.Terminal {
		t.Fatal("expected terminal state after failure + retry")
	}

	if sched.TotalMaterialized() != 0 {
		t.Errorf("scheduler materialized after Run: got %d want 0", sched.TotalMaterialized())
	}
	if fr.Len() != 0 {
		t.Errorf("frontier byKey after Run: got %d want 0", fr.Len())
	}
	stats := sched.Stats()
	if stats.Queued != 0 || stats.Active != 0 {
		t.Errorf("scheduler not drained: queued=%d active=%d", stats.Queued, stats.Active)
	}
}

// TestSVE3_SeparatorCollision verifies that session IDs containing the ':'
// separator do not cause key collisions in the frontier or scheduler.
// The session-scoped keys (sessionID:canonicalURL) must remain distinct.
func TestSVE3_SeparatorCollision(t *testing.T) {
	cfg := config.Defaults()
	src := orchestrator.NoopSourceRegistry{}
	fr := frontier.NewMemoryFrontier(cfg, src)

	// Two session IDs that both contain ':' — the SplitN-based Has()
	// in the frontier may mis-parse, but Push/ResetSession use exact-map
	// lookups and must still isolate correctly.
	s1 := model.SessionID("ses:colon")
	s2 := model.SessionID("ses:col")

	cands := func(sid model.SessionID, host string) []frontier.URLCandidate {
		u, _ := url.Parse("https://" + host + "/page")
		return []frontier.URLCandidate{{
			SessionID: sid, URL: u, Domain: host,
			SourceClass: model.SourceClassUnknown, PriorityHint: 500,
		}}
	}

	_ = fr.Push(cands(s1, "a.example"))
	_ = fr.Push(cands(s2, "a.example"))

	if fr.Len() != 2 {
		t.Fatalf("frontier len after two colon sessions: got %d want 2", fr.Len())
	}

	// Reset s1 only.
	_ = fr.ResetSession(s1)

	if fr.Len() != 1 {
		t.Errorf("frontier len after reset s1: got %d want 1 (s2 only)", fr.Len())
	}

	// Reset s2.
	_ = fr.ResetSession(s2)

	if fr.Len() != 0 {
		t.Errorf("frontier len after reset s2: got %d want 0", fr.Len())
	}
}

// TestSVE3_DedupAcrossSessionsViaScheduler verifies at the scheduler level
// that the same TaskKey from two different sessions is NOT deduped —
// session-scoped keys (sessionID:taskKey) keep them distinct.
func TestSVE3_SchedulerDedupAcrossSessions(t *testing.T) {
	cfg := config.Defaults()
	src := orchestrator.NoopSourceRegistry{}
	fr := frontier.NewMemoryFrontier(cfg, src)
	rc := orchestrator.NewResourceController(cfg, orchestrator.NoopResourceSampler{})
	sched := orchestrator.NewScheduler(cfg, fr, rc, src, orchestrator.DefaultTaskCapabilities())
	mgr := &sve3Manager{}
	_ = sched.RegisterManager(mgr, mgr.Capabilities())

	now := time.Unix(1000, 0)
	s1 := model.SessionID("ses1")
	s2 := model.SessionID("ses2")

	tk := func(sid model.SessionID, id string) model.Task {
		return model.Task{
			ID:            model.TaskID(id),
			SessionID:     sid,
			Type:          model.TaskTypeFetchHTTP,
			State:         model.TaskStateReady,
			Priority:      500,
			SourceClass:   model.SourceClassUnknown,
			SourceTarget:  "example.com",
			EstimatedCost: 1,
			TaskKey:       "shared:task:key",
			CreatedAt:     now,
		}
	}

	// Same TaskKey for two different sessions.
	t1 := tk(s1, "t1")
	t2 := tk(s2, "t2")
	if err := sched.Submit(t1); err != nil {
		t.Fatal(err)
	}
	if err := sched.Submit(t2); err != nil {
		t.Fatal(err)
	}

	if st := sched.Stats(); st.Queued != 2 {
		t.Errorf("queued after two same-key submits from different sessions: got %d want 2", st.Queued)
	}

	// Reset s1; s2's task must survive.
	sched.ResetSession(s1)
	if st := sched.Stats(); st.Queued != 1 {
		t.Errorf("queued after reset s1: got %d want 1 (s2 only)", st.Queued)
	}

	// Reset s2; everything clean.
	sched.ResetSession(s2)
	if st := sched.Stats(); st.Queued != 0 {
		t.Errorf("queued after reset s2: got %d want 0", st.Queued)
	}
	if sched.TotalMaterialized() != 0 {
		t.Errorf("materialized after all resets: got %d want 0", sched.TotalMaterialized())
	}
}
