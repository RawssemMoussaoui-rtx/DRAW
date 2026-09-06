package managers

import (
	"context"
	"time"

	"draw/internal/auth"
	"draw/internal/browser"
	"draw/internal/manager"
	"draw/internal/model"
)

const defaultBrowserTimeout = 15 * time.Second

// BrowserAdapter adapts the browser.BrowserManager into a manager.Manager so it
// can be registered with the scheduler and participate in RouterManager routing.
type BrowserAdapter struct {
	bm           browser.BrowserManager
	ttl          time.Duration
	authProvider auth.SessionAuthProvider
	sessionUser  func(model.SessionID) string
}

func NewBrowserAdapter(bm browser.BrowserManager, ttl time.Duration) *BrowserAdapter {
	return &BrowserAdapter{bm: bm, ttl: ttl}
}

// WithAuthProvider injects an optional SessionAuthProvider (structurally
// compatible with master.BrowserAuthProvider). When set by the Master (S1),
// BrowserAdapter forwards an AuthorizedSession to BrowserManager.Acquire when
// the session user is authorized. When nil (default), all browser sessions
// are anonymous — preserving current behavior.
func (a *BrowserAdapter) WithAuthProvider(p auth.SessionAuthProvider) *BrowserAdapter {
	a.authProvider = p
	return a
}

// WithSessionUser injects a resolver that maps a SessionID to a user string.
// This is the frontier-task fallback: when a Task has no UserId set, the
// BrowserAdapter calls this resolver to look up the user from the session.
// When nil or returning "", the session is treated as anonymous (fail-closed).
func (a *BrowserAdapter) WithSessionUser(fn func(model.SessionID) string) *BrowserAdapter {
	a.sessionUser = fn
	return a
}

func (a *BrowserAdapter) Capabilities() []manager.Capability {
	return []manager.Capability{manager.CapBrowserAnon, manager.CapBrowserAuth}
}

func (a *BrowserAdapter) Execute(t model.Task) (model.TaskResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultBrowserTimeout)
	defer cancel()

	session := a.acquireSession(t)
	bc, err := a.bm.Acquire(ctx, session)
	if err != nil {
		return model.TaskResult{Status: model.RetrievalStatusInvalidContent, Error: err}, nil
	}
	data, capErr := bc.Capture(t.URL.String())
	_ = a.bm.Release(bc)
	if capErr != nil {
		return model.TaskResult{Status: model.RetrievalStatusInvalidContent, Error: capErr}, nil
	}
	return model.TaskResult{Status: model.RetrievalStatusSuccess, Data: data}, nil
}

// acquireSession builds an AuthorizedSession for the task's session user when
// an authProvider is configured and the user is authorized.
//
// Resolution order:
//  1. t.UserId — set by Master on seeds/upgrades.
//  2. a.sessionUser(t.SessionID) — frontier-task fallback resolver.
//
// If no user can be resolved, the session is anonymous (nil) and
// AUTH_REQUIRED stays terminal upstream (decision.go:102-114).
// The authProvider (master.BrowserAuthProvider, auth_seam.go:24) stays
// fail-closed: on any auth error Authorized returns false → anonymous →
// validateAuth (chromedp.go:70) still gates.
func (a *BrowserAdapter) acquireSession(t model.Task) *browser.AuthorizedSession {
	if a.authProvider == nil {
		return nil
	}
	user := t.UserId
	if user == "" {
		if a.sessionUser != nil {
			user = a.sessionUser(t.SessionID)
		}
	}
	if user == "" {
		return nil
	}
	if a.authProvider.Authorized(user) {
		return browser.NewAuthorizedSession(user, a.authProvider)
	}
	return nil
}
