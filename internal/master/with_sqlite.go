package master

import "draw/internal/storage"

// WithSQLiteSessions swaps the default MemorySessionStore for a
// storage.SessionStore implementation (e.g. SQLiteSessionStore from
// the Storage Module). Pass nil to retain the default memory store.
//
// Q2 (A): This option enables the persistent ACTIVE→ARCHIVED→RECOVERED
// session lifecycle while keeping the single-active-session architecture.
func WithSQLiteSessions(s storage.SessionStore) MasterOption {
	return func(m *Master) {
		if s != nil {
			m.sessions = s
		}
	}
}
