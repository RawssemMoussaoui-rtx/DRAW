package browser

import (
	"context"
	"runtime"
	"testing"
	"time"
)

// TestBrowserManager_CloseIsIdempotent verifies the Q10 shutdown fix: calling
// Close (which cancels all registry entries and resets active state) must be
// safe to call more than once with no panic and no error.
func TestBrowserManager_CloseIsIdempotent(t *testing.T) {
	m := NewBrowserManager(BrowserOpts{})

	if err := m.Close(); err != nil {
		t.Fatalf("first Close: unexpected error: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("second Close: unexpected error: %v", err)
	}
}

// TestCleanupExpired_ReapsStaleContexts verifies the registry sweep: an entry
// whose createdAt+ttl < before is cancelled and removed, while fresh entries
// are retained.
func TestCleanupExpired_ReapsStaleContexts(t *testing.T) {
	m := NewBrowserManager(BrowserOpts{})
	defer m.Close()

	ttl := 5 * time.Minute
	staleAt := time.Now().Add(-10 * time.Minute)

	var staleCancelled int
	staleCancel := func() { staleCancelled++ }

	m.registry.add(staleCancel, staleAt, ttl)
	m.registry.add(func() {}, time.Now(), ttl)

	before := m.registry.count()
	if before != 2 {
		t.Fatalf("registry count: got %d want 2", before)
	}

	removed := m.CleanupExpired(time.Now(), ttl)
	if removed < 1 {
		t.Fatalf("expected >=1 removed, got %d", removed)
	}

	after := m.registry.count()
	if after >= before {
		t.Fatalf("registry should shrink: before=%d after=%d", before, after)
	}
	if after != 1 {
		t.Errorf("fresh entry should remain: after=%d want 1", after)
	}
	if staleCancelled != 1 {
		t.Errorf("expected stale cancel called once, got %d", staleCancelled)
	}
}

// TestAcquire_Release_NoLeak_NoChromium verifies the no-Chromium fast-fail
// path (mirrors TestAcquireNilAuthIsAnonymous). An anonymous (nil-auth)
// Acquire with an unresolvable ChromiumPath must reject before launching a
// browser, leave Active at 0, and spawn no residual goroutines.
func TestAcquire_Release_NoLeak_NoChromium(t *testing.T) {
	m := NewBrowserManager(BrowserOpts{ChromiumPath: "/definitely/nonexistent/chrome"})
	defer m.Close()

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()

	bc, err := m.Acquire(context.Background(), nil)
	if err == nil {
		// Defensive: a bogus path must never resolve.
		_ = m.Release(bc)
		t.Skip("chromium launched unexpectedly at bogus path")
	}

	if stats := m.Stats(); stats.Active != 0 {
		t.Fatalf("active after failed acquire: got %d want 0", stats.Active)
	}
	if bc != nil {
		t.Fatal("expected nil BrowserContext on acquire error")
	}

	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	after := runtime.NumGoroutine()

	if after > before+1 {
		t.Errorf("possible goroutine leak: before=%d after=%d (delta=%d)", before, after, after-before)
	}
}
