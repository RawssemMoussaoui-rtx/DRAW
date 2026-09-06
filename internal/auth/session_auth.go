package auth

import (
	"os"
	"strings"
)

// EnvironmentAuthUsers is the environment variable holding the comma-separated
// list of authorized browser session users. When unset or empty, all
// authorized-browser requests remain AUTH_REQUIRED terminal (fail-closed).
const EnvironmentAuthUsers = "DRAW_AUTH_USERS"

// SessionAuthProvider checks whether a user is authorized for a session.
// Its method signature must match master.BrowserAuthProvider.Authorized(user string) bool
// for structural (compile-time) compatibility without an import.
type SessionAuthProvider interface {
	Authorized(user string) bool
}

// FileSessionAuth implements SessionAuthProvider by checking membership in a
// set of authorized users. It is constructed from a comma-separated list
// (typically the DRAW_AUTH_USERS environment variable). No tokens, cookies, or
// plaintext credentials are stored — only lowercased username strings.
type FileSessionAuth struct {
	users map[string]bool
}

// NewFileSessionAuth creates a FileSessionAuth from a raw comma-separated
// list of usernames. Whitespace is trimmed and usernames are lowercased.
// An empty raw string yields a fail-closed provider (all Authorized calls
// return false).
func NewFileSessionAuth(raw string) *FileSessionAuth {
	a := &FileSessionAuth{users: make(map[string]bool)}
	for _, part := range strings.Split(raw, ",") {
		u := strings.ToLower(strings.TrimSpace(part))
		if u != "" {
			a.users[u] = true
		}
	}
	return a
}

// Authorized returns true only when user (lowercased) is in the set.
// A nil FileSessionAuth or one with no users is fail-closed (always false).
func (a *FileSessionAuth) Authorized(user string) bool {
	if a == nil || len(a.users) == 0 {
		return false
	}
	return a.users[strings.ToLower(user)]
}

// NewSessionAuthFromEnv reads the DRAW_AUTH_USERS environment variable and
// returns a SessionAuthProvider. If the variable is unset or empty, the
// returned provider is fail-closed (no users authorized).
func NewSessionAuthFromEnv() SessionAuthProvider {
	return NewFileSessionAuth(os.Getenv(EnvironmentAuthUsers))
}
