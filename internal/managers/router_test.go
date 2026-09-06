package managers

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"draw/internal/browser"
	"draw/internal/manager"
	"draw/internal/model"
)

// Compile-time interface assertions.
var (
	_ manager.Manager = (*WebManager)(nil)
	_ manager.Manager = (*NewsManager)(nil)
	_ manager.Manager = (*BrowserAdapter)(nil)
	_ manager.Manager = (*SocialManager)(nil)
	_ manager.Manager = (*SpecializedManager)(nil)
	_ manager.Manager = (*RouterManager)(nil)
)

// fakeWorker implements manager.Worker with a configurable result and a call counter.
type fakeWorker struct {
	result model.TaskResult
	err    error
	calls  int
}

func (f *fakeWorker) Run(model.Task) (model.TaskResult, error) {
	f.calls++
	return f.result, f.err
}

func (f *fakeWorker) Close() error { return nil }

// fakeBrowserContext implements browser.BrowserContext.
type fakeBrowserContext struct {
	data    []byte
	capErr  error
	capture string
}

func (f *fakeBrowserContext) Done() <-chan struct{} {
	return make(chan struct{})
}

func (f *fakeBrowserContext) Capture(u string) ([]byte, error) {
	f.capture = u
	if f.capErr != nil {
		return nil, f.capErr
	}
	return f.data, nil
}

// fakeBrowserManager implements browser.BrowserManager.
type fakeBrowserManager struct {
	acquireErr   error
	acquireCalls int
	released     int
	releaseErr   error
	returned     *fakeBrowserContext
}

func (f *fakeBrowserManager) Acquire(context.Context, *browser.AuthorizedSession) (browser.BrowserContext, error) {
	f.acquireCalls++
	if f.acquireErr != nil {
		return nil, f.acquireErr
	}
	if f.returned == nil {
		f.returned = &fakeBrowserContext{}
	}
	return f.returned, nil
}

func (f *fakeBrowserManager) Release(browser.BrowserContext) error {
	f.released++
	return f.releaseErr
}

func (f *fakeBrowserManager) Stats() browser.BrowserStats {
	var z browser.BrowserStats
	return z
}

func (f *fakeBrowserManager) CleanupExpired(time.Time, time.Duration) int { return 0 }

func (f *fakeBrowserManager) Close() error { return nil }

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("bad url %q: %v", s, err)
	}
	return u
}

func httpTask(t *testing.T, tt model.TaskType, sc model.SourceClass) model.Task {
	t.Helper()
	return model.Task{Type: tt, SourceClass: sc, URL: mustURL(t, "https://example.com")}
}

// newSpyRouter builds a RouterManager wired with counting spies and returns the
// router together with its sub-spies so tests can assert dispatch.
func newSpyRouter(t *testing.T, browseData []byte, browseCapErr error, acquireErr error) (*RouterManager, *fakeWorker, *fakeWorker, *fakeBrowserManager) {
	t.Helper()
	webW := &fakeWorker{result: model.TaskResult{Status: model.RetrievalStatusSuccess}}
	newsW := &fakeWorker{result: model.TaskResult{Status: model.RetrievalStatusSuccess}}
	fbm := &fakeBrowserManager{
		returned:   &fakeBrowserContext{data: browseData},
		acquireErr: acquireErr,
	}
	if browseCapErr != nil {
		fbm.returned.capErr = browseCapErr
	}
	rm := NewRouterManager(
		NewWebManager(webW),
		NewNewsManager(newsW),
		NewBrowserAdapter(fbm, time.Second),
		NewSocialManager(),
		NewSpecializedManager(),
	)
	return rm, webW, newsW, fbm
}

func TestRouterCapabilitiesUnionDeduplicated(t *testing.T) {
	rm, _, _, _ := newSpyRouter(t, []byte("x"), nil, nil)
	caps := rm.Capabilities()
	seen := map[manager.Capability]bool{}
	for _, c := range caps {
		if seen[c] {
			t.Fatalf("duplicate capability %q in union", c)
		}
		seen[c] = true
	}
	want := map[manager.Capability]bool{
		manager.CapHTTP:        true,
		manager.CapBrowserAnon: true,
		manager.CapBrowserAuth: true,
		manager.CapRSS:         true,
	}
	if len(seen) != len(want) {
		t.Fatalf("capabilities mismatch: got %v want %v", caps, want)
	}
	for c := range want {
		if !seen[c] {
			t.Fatalf("missing capability %q", c)
		}
	}
}

func TestRouterDispatch(t *testing.T) {
	tests := []struct {
		name     string
		taskType model.TaskType
		source   model.SourceClass
		expect   string // "web" | "news" | "browser"
	}{
		{"fetch_browser", model.TaskTypeFetchBrowser, model.SourceClassUnknown, "browser"},
		{"fetch_http_news", model.TaskTypeFetchHTTP, model.SourceClassNews, "news"},
		{"fetch_http_official", model.TaskTypeFetchHTTP, model.SourceClassOfficial, "web"},
		{"fetch_http_specialized", model.TaskTypeFetchHTTP, model.SourceClassSpecialized, "web"},
		{"fetch_http_unknown", model.TaskTypeFetchHTTP, model.SourceClassUnknown, "web"},
		{"fetch_http_empty_source", model.TaskTypeFetchHTTP, "", "web"},
		{"fetch_http_social_falls_to_web", model.TaskTypeFetchHTTP, model.SourceClassSocial, "web"},
		{"discover", model.TaskTypeDiscover, model.SourceClassOfficial, "web"},
		{"verify", model.TaskTypeVerify, model.SourceClassOfficial, "web"},
		{"reconcile", model.TaskTypeReconcile, model.SourceClassOfficial, "web"},
		{"default_unrecognized_type", model.TaskType("BOGUS"), model.SourceClassOfficial, "web"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rm, webW, newsW, fbm := newSpyRouter(t, []byte("b"), nil, nil)
			_, _ = rm.Execute(httpTask(t, tc.taskType, tc.source))

			if tc.expect == "web" && webW.calls != 1 {
				t.Fatalf("expected web worker invoked once, got web=%d news=%d browser=%d", webW.calls, newsW.calls, fbm.acquireCalls)
			}
			if tc.expect == "web" && (newsW.calls != 0 || fbm.acquireCalls != 0) {
				t.Fatalf("expected only web invoked, got web=%d news=%d browser=%d", webW.calls, newsW.calls, fbm.acquireCalls)
			}

			if tc.expect == "news" && newsW.calls != 1 {
				t.Fatalf("expected news worker invoked once, got web=%d news=%d browser=%d", webW.calls, newsW.calls, fbm.acquireCalls)
			}
			if tc.expect == "news" && (webW.calls != 0 || fbm.acquireCalls != 0) {
				t.Fatalf("expected only news invoked, got web=%d news=%d browser=%d", webW.calls, newsW.calls, fbm.acquireCalls)
			}

			if tc.expect == "browser" && fbm.acquireCalls != 1 {
				t.Fatalf("expected browser acquire once, got web=%d news=%d browser=%d", webW.calls, newsW.calls, fbm.acquireCalls)
			}
			if tc.expect == "browser" && (webW.calls != 0 || newsW.calls != 0) {
				t.Fatalf("expected only browser invoked, got web=%d news=%d browser=%d", webW.calls, newsW.calls, fbm.acquireCalls)
			}
		})
	}
}

func TestRouterDispatchDeterministic(t *testing.T) {
	rm, webW, _, _ := newSpyRouter(t, []byte("b"), nil, nil)
	task := httpTask(t, model.TaskTypeFetchHTTP, model.SourceClassOfficial)
	if _, err := rm.Execute(task); err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if webW.calls != 1 {
		t.Fatalf("expected 1 web call, got %d", webW.calls)
	}
	if _, err := rm.Execute(task); err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if webW.calls != 2 {
		t.Fatal("expected second identical dispatch to hit same sub-manager")
	}
	if _, err := rm.Execute(task); err != nil {
		t.Fatalf("Execute err: %v", err)
	}
}

func TestWebManagerExecutePassthrough(t *testing.T) {
	ok := model.TaskResult{Status: model.RetrievalStatusSuccess, Data: []byte("hello")}
	w := NewWebManager(&fakeWorker{result: ok})
	res, err := w.Execute(model.Task{Type: model.TaskTypeFetchHTTP})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != model.RetrievalStatusSuccess {
		t.Fatalf("expected SUCCESS, got %q", res.Status)
	}
	if string(res.Data) != "hello" {
		t.Fatalf("expected passthrough data, got %q", res.Data)
	}
}

func TestWebManagerExecuteErrorInvalidContent(t *testing.T) {
	w := NewWebManager(&fakeWorker{err: errors.New("boom")})
	res, err := w.Execute(model.Task{Type: model.TaskTypeFetchHTTP})
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if res.Status != model.RetrievalStatusInvalidContent {
		t.Fatalf("expected INVALID_CONTENT, got %q", res.Status)
	}
	if res.Error == nil {
		t.Fatal("expected error to be wrapped in result")
	}
}

func TestNewsManagerCapsAndExecute(t *testing.T) {
	w := NewNewsManager(&fakeWorker{result: model.TaskResult{Status: model.RetrievalStatusSuccess}})
	caps := w.Capabilities()
	if len(caps) != 1 || caps[0] != manager.CapHTTP {
		t.Fatalf("expected [CapHTTP], got %v", caps)
	}
	res, err := w.Execute(model.Task{Type: model.TaskTypeFetchHTTP})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != model.RetrievalStatusSuccess {
		t.Fatalf("expected SUCCESS, got %q", res.Status)
	}
}

func TestBrowserAdapterExecuteSuccess(t *testing.T) {
	fbm := &fakeBrowserManager{returned: &fakeBrowserContext{data: []byte("page")}}
	a := NewBrowserAdapter(fbm, time.Second)
	res, err := a.Execute(model.Task{Type: model.TaskTypeFetchBrowser, URL: mustURL(t, "https://example.com")})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != model.RetrievalStatusSuccess {
		t.Fatalf("expected SUCCESS, got %q", res.Status)
	}
	if string(res.Data) != "page" {
		t.Fatalf("expected captured data, got %q", res.Data)
	}
	if fbm.acquireCalls != 1 {
		t.Fatalf("expected one acquire, got %d", fbm.acquireCalls)
	}
	if fbm.released != 1 {
		t.Fatalf("expected one release, got %d", fbm.released)
	}
	if fbm.returned.capture != "https://example.com" {
		t.Fatalf("expected captured url, got %q", fbm.returned.capture)
	}
}

func TestBrowserAdapterExecuteAcquireError(t *testing.T) {
	fbm := &fakeBrowserManager{acquireErr: errors.New("acquire failed")}
	a := NewBrowserAdapter(fbm, time.Second)
	res, err := a.Execute(model.Task{Type: model.TaskTypeFetchBrowser, URL: mustURL(t, "https://example.com")})
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if res.Status != model.RetrievalStatusInvalidContent {
		t.Fatalf("expected INVALID_CONTENT on acquire failure, got %q", res.Status)
	}
	if fbm.acquireCalls != 1 {
		t.Fatalf("expected one acquire attempt, got %d", fbm.acquireCalls)
	}
}

func TestBrowserAdapterExecuteCaptureError(t *testing.T) {
	fbm := &fakeBrowserManager{returned: &fakeBrowserContext{capErr: errors.New("capture failed")}}
	a := NewBrowserAdapter(fbm, time.Second)
	res, err := a.Execute(model.Task{Type: model.TaskTypeFetchBrowser, URL: mustURL(t, "https://example.com")})
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if res.Status != model.RetrievalStatusInvalidContent {
		t.Fatalf("expected INVALID_CONTENT on capture failure, got %q", res.Status)
	}
}

func TestSocialManagerStub(t *testing.T) {
	m := NewSocialManager()
	if len(m.Capabilities()) == 0 {
		t.Fatal("SocialManager must advertise at least one capability")
	}
	res, err := m.Execute(model.Task{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != model.RetrievalStatusEmpty {
		t.Fatalf("expected EMPTY, got %q", res.Status)
	}
}

func TestSpecializedManagerStub(t *testing.T) {
	m := NewSpecializedManager()
	if len(m.Capabilities()) == 0 {
		t.Fatal("SpecializedManager must advertise at least one capability")
	}
	res, err := m.Execute(model.Task{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != model.RetrievalStatusEmpty {
		t.Fatalf("expected EMPTY, got %q", res.Status)
	}
}
