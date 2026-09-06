package master

// BrowserAuthProvider checks whether a user may use authorized (authenticated)
// browser sessions. The structural shape (Authorized(string) bool) satisfies
// auth.FileSessionAuth from the browser auth module — master does NOT import
// the auth package; the contract is satisfied by duck typing.
//
// Q3 (A): AUTH_REQUIRED stays terminal unless this provider explicitly
// authorizes the session's user. Failing closed when nil preserves the
// prior behavior where authorized browser escalation was never available.
type BrowserAuthProvider interface {
	Authorized(user string) bool
}

// disabledBrowserAuth is the fail-closed default: it never authorizes.
type disabledBrowserAuth struct{}

func (disabledBrowserAuth) Authorized(string) bool { return false }

// WithBrowserAuth wires a BrowserAuthProvider into the Decider so that
// canAuthorizeBrowser can check explicit user authorization before upgrading
// an AUTH_REQUIRED result to a browser fetch. When p is nil or omitted,
// the provider defaults to disabledBrowserAuth (fail-closed).
func WithBrowserAuth(p BrowserAuthProvider) MasterOption {
	return func(m *Master) {
		if p == nil {
			p = disabledBrowserAuth{}
		}
		if d, ok := m.decider.(Decider); ok {
			d.BrowserAuth = p
			m.decider = d
		}
	}
}
