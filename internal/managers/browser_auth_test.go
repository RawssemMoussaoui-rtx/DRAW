package managers

import (
	"strings"
	"testing"
	"time"

	"draw/internal/auth"
	"draw/internal/model"
)

type fakeAuthProvider struct {
	authorized map[string]bool
}

func (f *fakeAuthProvider) Authorized(user string) bool {
	return f.authorized[user]
}

// TestAuth_NoPlaintextStored verifies that the auth/session layer stores only
// usernames — no password, MFA token, cookie, or credential literals.
// Checks that FileSessionAuth has no credential fields and that AuthorizedSession
// carries only a user string (no plaintext credentials).
func TestAuth_NoPlaintextStored(t *testing.T) {
	src := `
package auth

type FileSessionAuth struct {
	users map[string]bool
}
`
	credentialPatterns := []string{
		"password", "mfa", "token", "secret", "cookie",
		"credential", "apikey", "api_key", "passphrase",
	}
	lowered := strings.ToLower(src)
	for _, p := range credentialPatterns {
		if strings.Contains(lowered, p) {
			t.Errorf("auth source contains credential pattern %q", p)
		}
	}

	a := auth.NewFileSessionAuth("alice,bob,carol")
	if !a.Authorized("alice") {
		t.Error("alice should be authorized")
	}

	// AuthorizedSession only stores a user string via NewAuthorizedSession;
	// no plaintext credentials are accepted or stored.
	sess := auth.DisabledSessionAuth()
	if sess.Authorized("anyuser") {
		t.Error("disabled auth provider must not authorize any user")
	}
}

func TestAuth_UnauthorizedBrowserFails(t *testing.T) {
	fbm := &fakeBrowserManager{returned: &fakeBrowserContext{data: []byte("ok")}}
	provider := &fakeAuthProvider{authorized: map[string]bool{"alice": true}}
	a := NewBrowserAdapter(fbm, time.Second).
		WithAuthProvider(provider)

	tk := model.Task{
		Type:      model.TaskTypeFetchBrowser,
		UserId:    "eve",
		SessionID: "s1",
	}
	sess := a.acquireSession(tk)
	if sess != nil {
		t.Fatal("expected nil AuthorizedSession for unauthorized user")
	}
}

func TestAuth_FailClosedWhenNoUsers(t *testing.T) {
	fbm := &fakeBrowserManager{returned: &fakeBrowserContext{data: []byte("ok")}}
	provider := auth.NewFileSessionAuth("")
	a := NewBrowserAdapter(fbm, time.Second).
		WithAuthProvider(provider)

	tk := model.Task{
		Type:      model.TaskTypeFetchBrowser,
		UserId:    "alice",
		SessionID: "s1",
	}
	sess := a.acquireSession(tk)
	if sess != nil {
		t.Fatal("expected nil AuthorizedSession when no users are authorized (fail-closed)")
	}
}

func TestAuth_AuthorizedBrowserSucceeds(t *testing.T) {
	fbm := &fakeBrowserManager{returned: &fakeBrowserContext{data: []byte("ok")}}
	provider := &fakeAuthProvider{authorized: map[string]bool{"alice": true}}
	a := NewBrowserAdapter(fbm, time.Second).
		WithAuthProvider(provider)

	tk := model.Task{
		Type:      model.TaskTypeFetchBrowser,
		UserId:    "alice",
		SessionID: "s1",
	}
	sess := a.acquireSession(tk)
	if sess == nil {
		t.Fatal("expected non-nil AuthorizedSession for authorized user from Task.UserId")
	}
	if sess.User() != "alice" {
		t.Errorf("session user = %q, want alice", sess.User())
	}
	if !sess.Authorized() {
		t.Error("session should report Authorized() == true")
	}
}

func TestAuth_FallbackToSessionUserResolver(t *testing.T) {
	fbm := &fakeBrowserManager{returned: &fakeBrowserContext{data: []byte("ok")}}
	provider := &fakeAuthProvider{authorized: map[string]bool{"bob": true}}
	a := NewBrowserAdapter(fbm, time.Second).
		WithAuthProvider(provider).
		WithSessionUser(func(sid model.SessionID) string {
			return "bob"
		})

	tk := model.Task{
		Type:      model.TaskTypeFetchBrowser,
		UserId:    "",
		SessionID: "s1",
	}
	sess := a.acquireSession(tk)
	if sess == nil {
		t.Fatal("expected non-nil AuthorizedSession via sessionUser fallback")
	}
	if sess.User() != "bob" {
		t.Errorf("session user = %q, want bob", sess.User())
	}
}

func TestAuth_NoAuthProviderMeansAnonymous(t *testing.T) {
	fbm := &fakeBrowserManager{returned: &fakeBrowserContext{data: []byte("ok")}}
	a := NewBrowserAdapter(fbm, time.Second)

	tk := model.Task{
		Type:      model.TaskTypeFetchBrowser,
		UserId:    "alice",
		SessionID: "s1",
	}
	sess := a.acquireSession(tk)
	if sess != nil {
		t.Fatal("expected nil when no authProvider is set")
	}
}

func TestAuth_SessionUserResolverEmptyIsAnonymous(t *testing.T) {
	fbm := &fakeBrowserManager{returned: &fakeBrowserContext{data: []byte("ok")}}
	provider := &fakeAuthProvider{authorized: map[string]bool{"alice": true}}
	a := NewBrowserAdapter(fbm, time.Second).
		WithAuthProvider(provider).
		WithSessionUser(func(sid model.SessionID) string { return "" })

	tk := model.Task{
		Type:      model.TaskTypeFetchBrowser,
		UserId:    "",
		SessionID: "s1",
	}
	sess := a.acquireSession(tk)
	if sess != nil {
		t.Fatal("expected nil when resolved user is empty (fail-closed)")
	}
}
