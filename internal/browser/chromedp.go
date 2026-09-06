package browser

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

const captureTimeout = 30 * time.Second

// ErrChromiumNotFound is returned when no Chromium binary can be resolved.
var ErrChromiumNotFound = fmt.Errorf("browser: chromium not found")

// BrowserOpts configures a chromedp-backed BrowserManager.
type BrowserOpts struct {
	// MaxConcurrency is the maximum number of simultaneous browser
	// contexts. Defaults to 4 when zero or negative.
	MaxConcurrency int

	// DefaultTTL is the time-to-live assigned to each acquired context.
	// Contexts older than TTL are eligible for CleanupExpired. Defaults
	// to 2 minutes when zero or negative.
	DefaultTTL time.Duration

	// ChromiumPath overrides the Chromium binary discovery chain.
	ChromiumPath string
}

// chromedpManager implements BrowserManager using chromedp.
type chromedpManager struct {
	mu       sync.Mutex
	opts     BrowserOpts
	registry *contextRegistry
	active   int
}

// NewBrowserManager returns a lazily-initialised BrowserManager. No
// Chromium process is launched at construction time; the binary is
// resolved and launched on the first Acquire call.
func NewBrowserManager(opts BrowserOpts) *chromedpManager {
	if opts.MaxConcurrency <= 0 {
		opts.MaxConcurrency = 4
	}
	if opts.DefaultTTL <= 0 {
		opts.DefaultTTL = 2 * time.Minute
	}
	return &chromedpManager{
		opts:     opts,
		registry: newContextRegistry(),
	}
}

// validateAuth enforces fail-closed authorization for browser sessions.
// A nil auth argument represents an anonymous session and is allowed. A
// non-nil session that is not Authorized() is rejected — this prevents a
// session whose user is not in the auth provider's set from consuming a
// concurrency slot or launching Chromium.
//
// In Phase D, authorized sessions are accepted by signature only: no
// plaintext credentials or cookies are injected into the browser context.
func validateAuth(auth *AuthorizedSession) error {
	if auth != nil && !auth.Authorized() {
		return fmt.Errorf("browser: authorized session rejected: user %q not authorized", auth.User())
	}
	return nil
}

// Acquire resolves Chromium, launches a headless browser, validates it
// with a fail-fast navigation, and returns a BrowserContext.
//
// If auth is non-nil the session is treated as authorized; however, in
// Phase D no real credentials or cookies are wired — AUTH_REQUIRED remains
// terminal and the authorized branch is accepted by signature only.
//
// PHASE-J REUSE DESIGN (plan §9.2, NOT IMPLEMENTED IN PHASE I):
// A future PooledBrowserManager would intercept Acquire to reuse an idle
// BrowserContext from a per-session pool keyed by *AuthorizedSession
// identity (or an "anonymous" bucket when auth is nil). On a cache miss
// it launches a new exec-allocator exactly as today (chromedp.go:108),
// but instead of returning a one-shot chromedpBrowserContext it returns
// a pooled wrapper whose Release returns the context to the pool rather
// than cancelling the allocator. Idle entries are evicted by CleanupExpired
// (chromedp.go:159) once createdAt+TTL < before, cancelling both the
// browser context and the underlying allocator. This eliminates the
// per-task Chromium spin-up cost measured in §0.13 #5.
// The current acquire-launch-cancel path is the Phase-I fallback; Phase J
// swaps the impl behind the BrowserManager interface with zero behavior
// change to callers. Gated on §0.13 #5 baseline-green.
func (m *chromedpManager) Acquire(ctx context.Context, auth *AuthorizedSession) (BrowserContext, error) {
	// Fail-closed auth validation: a non-nil session whose user is not
	// authorized is rejected before consuming a concurrency slot.
	if err := validateAuth(auth); err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.active >= m.opts.MaxConcurrency {
		m.mu.Unlock()
		return nil, fmt.Errorf("browser: concurrency limit reached (%d/%d)", m.active, m.opts.MaxConcurrency)
	}
	m.active++
	m.mu.Unlock()

	// Authorized sessions are accepted by signature only. No plaintext
	// credentials or cookies are injected. (Phase D: framework-only.)

	path, err := resolveChromiumPath(m.opts)
	if err != nil {
		m.decrementActive()
		return nil, err
	}

	allocOpts := buildAllocatorOpts(path)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocOpts...)
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)

	// Fail-fast: validate the browser is usable before handing it out.
	if err := chromedp.Run(browserCtx, chromedp.Navigate("about:blank")); err != nil {
		cancelBrowser()
		cancelAlloc()
		m.decrementActive()
		return nil, fmt.Errorf("browser: chromium unusable: %w", err)
	}

	combinedCancel := func() {
		cancelBrowser()
		cancelAlloc()
		m.decrementActive()
	}

	createdAt := time.Now()
	id := m.registry.add(combinedCancel, createdAt, m.opts.DefaultTTL)

	return &chromedpBrowserContext{
		ctx: browserCtx,
		id:  id,
	}, nil
}

func (m *chromedpManager) decrementActive() {
	m.mu.Lock()
	m.active--
	m.mu.Unlock()
}

// Release cancels the browser session and deregisters it.
func (m *chromedpManager) Release(bc BrowserContext) error {
	ctx, ok := bc.(*chromedpBrowserContext)
	if !ok {
		return fmt.Errorf("browser: unknown BrowserContext type")
	}
	m.registry.cancel(ctx.id)
	return nil
}

// Stats returns the active and capacity counts.
func (m *chromedpManager) Stats() BrowserStats {
	m.mu.Lock()
	active := m.active
	m.mu.Unlock()
	return BrowserStats{Active: active, Cap: m.opts.MaxConcurrency}
}

// CleanupExpired cancels and removes contexts whose createdAt+ttl < before.
func (m *chromedpManager) CleanupExpired(before time.Time, ttl time.Duration) int {
	return m.registry.cleanupExpired(before, ttl)
}

// Close cancels all active contexts and resets state.
func (m *chromedpManager) Close() error {
	m.registry.close()
	m.mu.Lock()
	m.active = 0
	m.mu.Unlock()
	return nil
}

// chromedpBrowserContext implements BrowserContext.
type chromedpBrowserContext struct {
	ctx context.Context
	id  int
}

func (b *chromedpBrowserContext) Done() <-chan struct{} {
	return b.ctx.Done()
}

// Capture navigates to url, waits for the document body, and returns
// the rendered outer HTML of the <html> element.
func (b *chromedpBrowserContext) Capture(url string) ([]byte, error) {
	captureCtx, cancel := context.WithTimeout(b.ctx, captureTimeout)
	defer cancel()

	var res string
	err := chromedp.Run(captureCtx,
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.OuterHTML("html", &res, chromedp.ByQuery),
	)
	if err != nil {
		return nil, fmt.Errorf("browser: capture failed for %s: %w", url, err)
	}
	return []byte(res), nil
}

// contextRegistry tracks active browser contexts for lifecycle management.
// It is decoupled from chromedp so that CleanupExpired, Stats, and Release
// can be unit-tested without launching a browser.
//
// PHASE-J REUSE DESIGN (plan §9.2, NOT IMPLEMENTED IN PHASE I):
// The entries map[int]*registryEntry would be augmented by a per-session
// idle pool (map[string][]*pooledEntry) where each pooledEntry wraps a
// still-live chromedp browser context alongside its allocator cancel func.
// On Release, instead of cancel(id) immediately (chromedp.go:146), the
// entry is returned to the pool if Active < Cap and age < TTL, keeping the
// process alive for reuse by the next Acquire with the same
// *AuthorizedSession identity. CleanupExpired (chromedp.go:255) evicts
// idle entries whose createdAt+TTL < before, cancelling both the browser
// context and allocator (double-cancel-safe via sync.Once per entry).
// The current cancel-on-release semantics below are Phase-I behavior;
// Phase J branches on a pool-enabled flag to divert to the pooled path.
// Gated on §0.13 #5 baseline-green.
type contextRegistry struct {
	mu      sync.Mutex
	nextID  int
	entries map[int]*registryEntry
}

type registryEntry struct {
	cancel    context.CancelFunc
	createdAt time.Time
	ttl       time.Duration
}

func newContextRegistry() *contextRegistry {
	return &contextRegistry{
		entries: make(map[int]*registryEntry),
	}
}

func (r *contextRegistry) add(cancel context.CancelFunc, createdAt time.Time, ttl time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	id := r.nextID
	r.entries[id] = &registryEntry{
		cancel:    cancel,
		createdAt: createdAt,
		ttl:       ttl,
	}
	return id
}

func (r *contextRegistry) cancel(id int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[id]
	if !ok {
		return false
	}
	if entry.cancel != nil {
		entry.cancel()
	}
	delete(r.entries, id)
	return true
}

func (r *contextRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// cleanupExpired cancels and removes entries where createdAt.Add(ttl) < before.
func (r *contextRegistry) cleanupExpired(before time.Time, ttl time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := 0
	for id, entry := range r.entries {
		if entry.createdAt.Add(ttl).Before(before) {
			if entry.cancel != nil {
				entry.cancel()
			}
			delete(r.entries, id)
			removed++
		}
	}
	return removed
}

func (r *contextRegistry) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, entry := range r.entries {
		if entry.cancel != nil {
			entry.cancel()
		}
		delete(r.entries, id)
	}
}

// resolveChromiumPath resolves the Chromium binary path following the chain:
// BrowserOpts.ChromiumPath → RD_CHROMIUM_PATH env → common names via
// exec.LookPath → platform install paths.
func resolveChromiumPath(opts BrowserOpts) (string, error) {
	if opts.ChromiumPath != "" {
		if _, err := os.Stat(opts.ChromiumPath); err == nil {
			return opts.ChromiumPath, nil
		}
		if err := validateExecutable(opts.ChromiumPath); err == nil {
			return opts.ChromiumPath, nil
		}
		return "", fmt.Errorf("browser: chromium at configured path not found: %s", opts.ChromiumPath)
	}
	if p := strings.TrimSpace(os.Getenv(EnvChromiumPath)); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		if err := validateExecutable(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("browser: chromium at %s not found: %s", EnvChromiumPath, p)
	}
	for _, name := range chromiumBinaryNames {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	if p, ok := findChromiumInDefaultPaths(); ok {
		return p, nil
	}
	return "", ErrChromiumNotFound
}

// chromiumBinaryNames are executable names searched via LookPath.
var chromiumBinaryNames = []string{
	"chrome",
	"chromium",
	"google-chrome",
	"chromium-browser",
	"chrome.exe",
}

// findChromiumInDefaultPaths checks platform-specific install locations.
func findChromiumInDefaultPaths() (string, bool) {
	candidates := []string{}
	switch runtime.GOOS {
	case "windows":
		candidates = append(candidates,
			filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Google", "Chrome", "Application", "chrome.exe"),
		)
	case "darwin":
		candidates = append(candidates,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		)
	case "linux":
		candidates = append(candidates,
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/usr/sbin/chromium-browser",
		)
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
		if err := validateExecutable(p); err == nil {
			return p, true
		}
	}
	return "", false
}

// validateExecutable checks if path is an executable file.
func validateExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("not a file")
	}
	if runtime.GOOS != "windows" {
		if info.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("not executable")
		}
	}
	return nil
}

// buildAllocatorOpts constructs the chromedp ExecAllocatorOption list for
// a headless Chromium instance.
func buildAllocatorOpts(path string) []chromedp.ExecAllocatorOption {
	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(path),
		chromedp.Headless,
		chromedp.DisableGPU,
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.IgnoreCertErrors,
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.WindowSize(1280, 720),
	}
	if forceNoSandbox() {
		opts = append(opts, chromedp.NoSandbox)
	}
	return opts
}

// forceNoSandbox reports whether --no-sandbox should be forced. This is
// the case when RD_BROWSER_NO_SANDBOX is set, or when running as root on
// Linux (Chrome refuses to run as root otherwise).
func forceNoSandbox() bool {
	v := strings.TrimSpace(os.Getenv(EnvNoSandbox))
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	if runtime.GOOS == "linux" {
		return os.Getuid() == 0
	}
	return false
}
