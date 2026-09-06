package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"draw/internal/auth"
)

// chromiumAvailable skips the test if no Chromium binary can be resolved.
func chromiumAvailable(t *testing.T) {
	t.Helper()
	_, err := resolveChromiumPath(BrowserOpts{})
	if err != nil {
		t.Skipf("chromium not available: %v", err)
	}
}

// ---------------------------------------------------------------------------
// AuthorizedSession tests (no Chromium)
// ---------------------------------------------------------------------------

func TestAuthorizedSession(t *testing.T) {
	if NewAuthorizedSession("u", nil) != nil {
		t.Error("nil SessionAuth should produce nil session")
	}

	s := NewAuthorizedSession("alice", auth.DisabledSessionAuth())
	if s.User() != "alice" {
		t.Errorf("user: got %q want %q", s.User(), "alice")
	}
	if s.Authorized() {
		t.Error("DisabledSessionAuth must not authorize")
	}
}

// ---------------------------------------------------------------------------
// contextRegistry tests (no Chromium)
// ---------------------------------------------------------------------------

func TestContextRegistryAddCancelCount(t *testing.T) {
	r := newContextRegistry()

	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	ctx3, cancel3 := context.WithCancel(context.Background())

	id1 := r.add(cancel1, time.Now(), time.Minute)
	id2 := r.add(cancel2, time.Now(), time.Minute)
	id3 := r.add(cancel3, time.Now(), time.Minute)

	if r.count() != 3 {
		t.Fatalf("count after add: got %d want 3", r.count())
	}

	if !r.cancel(id2) {
		t.Error("cancel(existing) should return true")
	}
	if r.count() != 2 {
		t.Errorf("count after cancel: got %d want 2", r.count())
	}
	if ctx2.Err() == nil {
		t.Error("ctx2 should be cancelled after registry.cancel")
	}

	// Double cancel returns false
	if r.cancel(id2) {
		t.Error("double cancel should return false")
	}

	// Cancel non-existent ID returns false
	if r.cancel(9999) {
		t.Error("cancel(non-existent) should return false")
	}

	r.close()
	if r.count() != 0 {
		t.Errorf("count after close: got %d want 0", r.count())
	}
	if ctx1.Err() == nil {
		t.Error("ctx1 should be cancelled after close")
	}
	if ctx3.Err() == nil {
		t.Error("ctx3 should be cancelled after close")
	}
	_ = id1
	_ = id3
}

func TestContextRegistryCleanupExpired(t *testing.T) {
	r := newContextRegistry()

	var cancelled int
	mkCancel := func() context.CancelFunc {
		return func() { cancelled++ }
	}

	now := time.Now()
	r.add(mkCancel(), now.Add(-10*time.Minute), 0)
	r.add(mkCancel(), now, 0)

	// before=now, ttl=5min: entries where createdAt+5min < now are removed
	// old: (now-10min)+5min = (now-5min) < now → expired
	// fresh: now+5min > now → not expired
	removed := r.cleanupExpired(now, 5*time.Minute)
	if removed != 1 {
		t.Errorf("removed: got %d want 1", removed)
	}
	if r.count() != 1 {
		t.Errorf("count after cleanup: got %d want 1", r.count())
	}
	if cancelled != 1 {
		t.Errorf("cancel calls: got %d want 1", cancelled)
	}
}

func TestContextRegistryCleanupExpiredAllFresh(t *testing.T) {
	r := newContextRegistry()

	var cancelled int
	mkCancel := func() context.CancelFunc {
		return func() { cancelled++ }
	}

	now := time.Now()
	r.add(mkCancel(), now, 0)
	r.add(mkCancel(), now.Add(1*time.Minute), 0)

	removed := r.cleanupExpired(now, 5*time.Minute)
	if removed != 0 {
		t.Errorf("removed: got %d want 0", removed)
	}
	if cancelled != 0 {
		t.Errorf("cancel calls: got %d want 0", cancelled)
	}
}

// ---------------------------------------------------------------------------
// BrowserContext Done() test (no Chromium — uses a real context)
// ---------------------------------------------------------------------------

func TestBrowserContextDoneChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	bc := &chromedpBrowserContext{ctx: ctx}

	select {
	case <-bc.Done():
		t.Fatal("Done should not be closed initially")
	default:
	}

	cancel()

	select {
	case <-bc.Done():
		// expected
	case <-time.After(time.Second):
		t.Error("Done should be closed after cancel")
	}
}

// ---------------------------------------------------------------------------
// Manager config tests (no Chromium)
// ---------------------------------------------------------------------------

func TestBrowserManagerDefaults(t *testing.T) {
	m := NewBrowserManager(BrowserOpts{})
	stats := m.Stats()
	if stats.Cap != 4 {
		t.Errorf("default cap: got %d want 4", stats.Cap)
	}
	if stats.Active != 0 {
		t.Errorf("default active: got %d want 0", stats.Active)
	}
	if m.opts.DefaultTTL != 2*time.Minute {
		t.Errorf("default TTL: got %v want 2m", m.opts.DefaultTTL)
	}
}

func TestBrowserManagerCustom(t *testing.T) {
	m := NewBrowserManager(BrowserOpts{MaxConcurrency: 8, DefaultTTL: 5 * time.Minute})
	stats := m.Stats()
	if stats.Cap != 8 {
		t.Errorf("custom cap: got %d want 8", stats.Cap)
	}
	if m.opts.DefaultTTL != 5*time.Minute {
		t.Errorf("custom TTL: got %v want 5m", m.opts.DefaultTTL)
	}
}

// ---------------------------------------------------------------------------
// Chromium resolution tests (no browser launch)
// ---------------------------------------------------------------------------

func TestResolveChromiumPathExplicitMissing(t *testing.T) {
	_, err := resolveChromiumPath(BrowserOpts{ChromiumPath: "/definitely/nonexistent/chrome"})
	if err == nil {
		t.Skip("chromium found at nonexistent path unexpectedly")
	}
	if err != ErrChromiumNotFound && !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected explicit not-found error, got: %v", err)
	}
}

func TestResolveChromiumPathEnvMissing(t *testing.T) {
	old := os.Getenv(EnvChromiumPath)
	os.Setenv(EnvChromiumPath, "/nonexistent/env/chrome")
	defer os.Setenv(EnvChromiumPath, old)

	_, err := resolveChromiumPath(BrowserOpts{})
	if err == nil {
		t.Skip("chromium found via env unexpectedly")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected explicit not-found error, got: %v", err)
	}
}

func TestAcquireChromiumNotFound(t *testing.T) {
	m := NewBrowserManager(BrowserOpts{ChromiumPath: "/definitely/nonexistent/chrome"})
	defer m.Close()

	_, err := m.Acquire(context.Background(), nil)
	if err == nil {
		t.Skip("chromium launched unexpectedly")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected explicit not-found error, got: %v", err)
	}
	// active should be decremented after failed Acquire
	if m.Stats().Active != 0 {
		t.Errorf("active after failed acquire: got %d want 0", m.Stats().Active)
	}
}

// ---------------------------------------------------------------------------
// Chromium integration tests (skip if Chromium unavailable)
// ---------------------------------------------------------------------------

func TestAcquireCaptureReleaseWithChromium(t *testing.T) {
	chromiumAvailable(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Test</title></head><body><h1>Hello Browser</h1></body></html>`)
	}))
	defer srv.Close()

	m := NewBrowserManager(BrowserOpts{})
	defer m.Close()

	bc, err := m.Acquire(context.Background(), nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	if stats := m.Stats(); stats.Active != 1 {
		t.Errorf("active after acquire: got %d want 1", stats.Active)
	}

	data, err := bc.Capture(srv.URL)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty capture")
	}
	if !strings.Contains(string(data), "Hello Browser") {
		t.Errorf("capture missing expected content; got: %s", string(data))
	}

	if err := m.Release(bc); err != nil {
		t.Fatalf("Release: %v", err)
	}

	select {
	case <-bc.Done():
		// expected
	case <-time.After(3 * time.Second):
		t.Error("Done should close after Release")
	}

	if stats := m.Stats(); stats.Active != 0 {
		t.Errorf("active after release: got %d want 0", stats.Active)
	}
}

func TestAcquireConcurrencyLimitWithChromium(t *testing.T) {
	chromiumAvailable(t)

	m := NewBrowserManager(BrowserOpts{MaxConcurrency: 1})
	defer m.Close()

	bc, err := m.Acquire(context.Background(), nil)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer m.Release(bc)

	// Second Acquire must fail at capacity check (before resolving Chromium).
	_, err = m.Acquire(context.Background(), nil)
	if err == nil {
		t.Fatal("expected concurrency limit error on second Acquire")
	}
}

func TestGoroutineLeakWithChromium(t *testing.T) {
	chromiumAvailable(t)

	m := NewBrowserManager(BrowserOpts{})
	defer m.Close()

	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	before := runtime.NumGoroutine()
	var msBefore runtime.MemStats
	runtime.ReadMemStats(&msBefore)

	for i := 0; i < 3; i++ {
		bc, err := m.Acquire(context.Background(), nil)
		if err != nil {
			t.Fatalf("Acquire #%d: %v", i, err)
		}
		if err := m.Release(bc); err != nil {
			t.Fatalf("Release #%d: %v", i, err)
		}
	}

	runtime.GC()
	time.Sleep(500 * time.Millisecond)
	after := runtime.NumGoroutine()
	var msAfter runtime.MemStats
	runtime.ReadMemStats(&msAfter)

	if after > before+10 {
		t.Errorf("possible goroutine leak: before=%d after=%d", before, after)
	}
	_ = msBefore
	_ = msAfter
}

func TestAcquireNilAuthIsAnonymous(t *testing.T) {
	chromiumAvailable(t)

	m := NewBrowserManager(BrowserOpts{})
	defer m.Close()

	bc, err := m.Acquire(context.Background(), nil)
	if err != nil {
		t.Fatalf("Acquire with nil auth: %v", err)
	}
	if err := m.Release(bc); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// ---------------------------------------------------------------------------
// AuthorizedSession Acquire tests
// ---------------------------------------------------------------------------

// TestAcquireAuthorizedSession verifies that a valid (authorized)
// AuthorizedSession passes validateAuth and proceeds to browser launch.
// When Chromium is unavailable, we assert the failure is NOT an auth-rejection
// error (i.e. auth validation succeeded).
func TestAcquireAuthorizedSession(t *testing.T) {
	a := auth.NewFileSessionAuth("alice,bob")
	s := NewAuthorizedSession("alice", a)
	if !s.Authorized() {
		t.Fatal("expected alice to be authorized before Acquire")
	}

	m := NewBrowserManager(BrowserOpts{ChromiumPath: "/definitely/nonexistent/chrome"})
	defer m.Close()

	bc, err := m.Acquire(context.Background(), s)
	if err == nil {
		// Chromium is actually available at the bogus path — clean up.
		_ = m.Release(bc)
		t.Skip("chromium launched unexpectedly at bogus path")
	}
	// The error must NOT be an auth-rejection error.
	if strings.Contains(err.Error(), "authorized session rejected") {
		t.Errorf("authorized session should not be rejected: %v", err)
	}
	if m.Stats().Active != 0 {
		t.Errorf("active after Acquire with authorized session (chromium unavailable): got %d want 0", m.Stats().Active)
	}
}

// TestAcquireAuthorizedSession_unverifiedFails verifies that an unverified
// (non-authorized) AuthorizedSession is rejected by validateAuth before
// Chromium is resolved or a concurrency slot is consumed.
func TestAcquireAuthorizedSession_unverifiedFails(t *testing.T) {
	a := auth.NewFileSessionAuth("alice,bob")
	s := NewAuthorizedSession("eve", a)
	if s.Authorized() {
		t.Fatal("expected eve to NOT be authorized before Acquire")
	}

	m := NewBrowserManager(BrowserOpts{})
	defer m.Close()

	_, err := m.Acquire(context.Background(), s)
	if err == nil {
		t.Fatal("expected error for unverified authorized session")
	}
	if !strings.Contains(err.Error(), "authorized session rejected") {
		t.Errorf("expected auth-rejection error, got: %v", err)
	}
	// No concurrency slot should be consumed on auth rejection.
	if m.Stats().Active != 0 {
		t.Errorf("active after rejected session: got %d want 0", m.Stats().Active)
	}
}
