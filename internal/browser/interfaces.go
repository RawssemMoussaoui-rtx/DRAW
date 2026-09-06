package browser

import (
	"context"
	"time"

	"draw/internal/auth"
)

// EnvChromiumPath is the environment variable used to override the Chromium
// binary location for the chromedp-backed BrowserManager.
const EnvChromiumPath = "RD_CHROMIUM_PATH"

// EnvNoSandbox forces Chromium to launch with --no-sandbox.
const EnvNoSandbox = "RD_BROWSER_NO_SANDBOX"

// AuthorizedSession carries framework-side authorization metadata. It is
// opaque to the browser package: no plaintext credentials or cookies are
// stored or injected. In Phase D, the authorized branch is accepted by
// signature only — AUTH_REQUIRED remains terminal and no real credentials
// are wired.
type AuthorizedSession struct {
	user string
	auth auth.SessionAuth
}

// NewAuthorizedSession creates an AuthorizedSession bound to user and backed
// by the provided SessionAuth. If a is nil the result is nil, representing
// an anonymous (un-authorized) session.
func NewAuthorizedSession(user string, a auth.SessionAuth) *AuthorizedSession {
	if a == nil {
		return nil
	}
	return &AuthorizedSession{user: user, auth: a}
}

// User returns the user identifier associated with the session.
func (s *AuthorizedSession) User() string {
	if s == nil {
		return ""
	}
	return s.user
}

// Authorized reports whether the session's user is authorized by the
// injected SessionAuth. A nil session is not authorized.
func (s *AuthorizedSession) Authorized() bool {
	if s == nil || s.auth == nil {
		return false
	}
	return s.auth.Authorized(s.user)
}

// BrowserContext represents a single browser session acquired from a
// BrowserManager. Capture renders the given URL and returns the page
// bytes. Done returns a channel that is closed when the context is no
// longer valid (typically after Release).
type BrowserContext interface {
	Done() <-chan struct{}
	Capture(url string) ([]byte, error)
}

// BrowserStats reports the current concurrency usage of a BrowserManager.
type BrowserStats struct {
	Active int
	Cap    int
}

// BrowserManager manages a pool of browser contexts.
type BrowserManager interface {
	// Acquire returns a BrowserContext. A nil auth argument yields an
	// anonymous (un-authorized) context.
	Acquire(ctx context.Context, auth *AuthorizedSession) (BrowserContext, error)

	// Release returns the context to the manager and cancels the
	// underlying browser session.
	Release(BrowserContext) error

	// Stats returns current active count and configured capacity.
	Stats() BrowserStats

	// CleanupExpired cancels and removes contexts whose
	// createdAt+ttl < before. Returns the number removed.
	CleanupExpired(before time.Time, ttl time.Duration) int

	// Close shuts down the manager, cancelling all active contexts.
	Close() error
}
