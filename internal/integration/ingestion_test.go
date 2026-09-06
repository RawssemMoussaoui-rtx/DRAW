package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"draw/internal/config"
	"draw/internal/frontier"
	"draw/internal/ingestion"
	"draw/internal/managers"
	"draw/internal/master"
	"draw/internal/model"
	"draw/internal/orchestrator"
	"draw/internal/workers"
)

// hitCounter records the number of HTTP hits received per request path. It is
// safe for concurrent use by httptest server goroutines.
type hitCounter struct {
	mu   sync.Mutex
	hits map[string]int
	base string
}

func newHitCounter() *hitCounter {
	return &hitCounter{hits: map[string]int{}}
}

func (hc *hitCounter) record(path string) {
	hc.mu.Lock()
	hc.hits[path]++
	hc.mu.Unlock()
}

func (hc *hitCounter) snapshot() map[string]int {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	cp := make(map[string]int, len(hc.hits))
	for k, v := range hc.hits {
		cp[k] = v
	}
	return cp
}

// newIngestionGraph builds the real execution graph WITH the Phase-E ingestion
// seam wired: a real frontier backed by a real ingestion.Processor that feeds
// discovered URLs into the scheduler's frontier. It also stands up an httptest
// server instrumented with a hitCounter. The server is closed via t.Cleanup.
//
// Unlike newTestGraph, this helper wires ingestion so the scheduler materializes
// discovered URLs (pushed by the Processor into the frontier) into FETCH_HTTP
// tasks. It returns the Master, the concrete frontier (for Len/Has assertions),
// and the hitCounter (whose base field is the localhost-rewritten seed URL).
func newIngestionGraph(t *testing.T) (*master.Master, *frontier.MemoryFrontier, *hitCounter) {
	t.Helper()

	cfg := config.Defaults()
	src := orchestrator.NoopSourceRegistry{}
	fr := frontier.NewMemoryFrontier(cfg, src)
	rc := orchestrator.NewResourceController(cfg, orchestrator.NoopResourceSampler{})
	sched := orchestrator.NewScheduler(cfg, fr, rc, src, orchestrator.DefaultTaskCapabilities())

	fc := workers.NewFetchClient(workers.FetchClientOpts{
		UserAgentPool: []string{"draw/1.0 (+https://draw.local)"},
		MaxAttempts:   cfg.Retry.MaxAttempts,
		BaseTimeout:   10 * time.Second,
		MaxRedirects:  5,
	})
	w := workers.NewHTTPWorker(fc, workers.WorkerOpts{
		UserAgentPool: []string{"draw/1.0 (+https://draw.local)"},
		MaxAttempts:   cfg.Retry.MaxAttempts,
		Timeout:       30 * time.Second,
		MaxRedirects:  5,
	})

	bmgr := &fakeBrowserManager{}
	web := managers.NewWebManager(w)
	news := managers.NewNewsManager(w)
	social := managers.NewSocialManager()
	specialized := managers.NewSpecializedManager()
	brow := managers.NewBrowserAdapter(bmgr, 2*time.Minute)
	router := managers.NewRouterManager(web, news, brow, social, specialized)
	if err := sched.RegisterManager(router, router.Capabilities()); err != nil {
		t.Fatalf("register manager: %v", err)
	}

	proc := ingestion.NewProcessor(fr, ingestion.WithMaxURLsPerPage(2000))
	m := master.NewMaster(cfg, sched,
		master.WithMemorySessions(),
		master.WithIngestion(proc),
	)

	hc := newHitCounter()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		hc.record(r.URL.Path)
		if r.URL.Path == "/" {
			rw.Header().Set("Content-Type", "text/html")
			_, _ = rw.Write([]byte(`<html><body><base href="/"><a href="/a">a</a><a href="/a">adup</a><a href="/b">b</a><a href="http://10.0.0.1/private">p</a><a href="ftp://example.com/y">ftp</a></body></html>`))
			return
		}
		rw.Header().Set("Content-Type", "text/plain")
		_, _ = rw.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	hc.base = strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	return m, fr, hc
}

// TestIngestionFeedsFrontier is an end-to-end integration test of Phase E: the
// ingestion Processor (wired into the Master) consumes the DISCOVER seed's
// retrieved HTML, extracts & resolves links, and pushes URLCandidates into the
// MemoryFrontier. The scheduler's frontier-drain then materializes the surviving
// (deduped, hard-filtered) URLs into FETCH_HTTP tasks that execute against the
// httptest server. This proves the full path: ingestion -> frontier -> scheduler
// -> worker -> fetch.
func TestIngestionFeedsFrontier(t *testing.T) {
	m, fr, hc := newIngestionGraph(t)
	base := hc.base

	if _, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1",
		Query:  "research X",
		Seeds:  []string{base},
	}); err != nil {
		t.Fatalf("SubmitIntent: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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

	if got := fr.Len(); got != 2 {
		t.Errorf("frontier Len: got %d want 2 (only /a and /b; dup /a deduped; 10.0.0.1 private & ftp hard-filtered)", got)
	}
	if !fr.Has("localhost", base+"/a") {
		t.Errorf("frontier missing /a")
	}
	if !fr.Has("localhost", base+"/b") {
		t.Errorf("frontier missing /b")
	}

	hits := hc.snapshot()
	if hits["/"] < 1 {
		t.Errorf(`hitCounter "/" count: got %d want >= 1 (seed DISCOVER fetch)`, hits["/"])
	}
	if hits["/a"] < 1 {
		t.Errorf(`hitCounter "/a" count: got %d want >= 1 (materialized FETCH_HTTP task)`, hits["/a"])
	}
	if hits["/b"] < 1 {
		t.Errorf(`hitCounter "/b" count: got %d want >= 1 (materialized FETCH_HTTP task)`, hits["/b"])
	}
	if len(hits) != 3 {
		t.Errorf("distinct hit paths: got %d want 3 (no fetch of private/ftp, no double-fetch)", len(hits))
	}
	for _, path := range []string{"/", "/a", "/b"} {
		if hits[path] != 1 {
			t.Errorf("hitCounter %q count: got %d want 1", path, hits[path])
		}
	}
}
