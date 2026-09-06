package api

import (
	"errors"

	"draw/internal/model"
	"draw/internal/storage"
)

// archiveSession persists the session as archived via the SessionStore so it
// can be recovered later. It is a thin wrapper around SessionStore.Archive that
// is nil-safe (no-op when the store is unconfigured).
func archiveSession(ss storage.SessionStore, sid model.SessionID) error {
	if ss == nil {
		return nil
	}
	return ss.Archive(sid)
}

// ErrRecoveryTokenMismatch is returned when the supplied recovery token does
// not match the token stored for the session.
var ErrRecoveryTokenMismatch = errors.New("recovery token mismatch")

// ErrSessionStoreNotConfigured is returned when no SessionStore is wired.
var ErrSessionStoreNotConfigured = errors.New("session store not configured")

// recoverSession validates the recovery token, marks the session active, and
// re-submits the original intent through the sessionsManager to create a fresh
// research run. The new session id is returned.
func recoverSession(sm *sessionsManager, ss storage.SessionStore, sid model.SessionID, token string) (model.SessionID, error) {
	if ss == nil {
		return "", ErrSessionStoreNotConfigured
	}
	sess, err := ss.Recover(sid)
	if err != nil {
		return "", err
	}
	if sess == nil || sess.RecoveryToken == "" {
		return "", errors.New("no recovery token for session")
	}
	if token != "" && sess.RecoveryToken != token {
		return "", ErrRecoveryTokenMismatch
	}
	req := model.IntentRequest{
		UserID: sess.UserID,
		Query:  sess.Intent.Entity,
		Seeds:  append([]string(nil), sess.Intent.Seeds...),
	}
	return sm.Submit(req)
}
