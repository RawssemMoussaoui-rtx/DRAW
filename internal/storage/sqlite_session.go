package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"draw/internal/model"
)

const recoveryExpiry = 24 * time.Hour

type SQLiteSessionStore struct {
	mu sync.Mutex
	db *sql.DB
}

func NewSQLiteSessionStore(db *sql.DB) (*SQLiteSessionStore, error) {
	return &SQLiteSessionStore{db: db}, nil
}

func (s *SQLiteSessionStore) Init(ctx context.Context) error {
	return nil
}

func (s *SQLiteSessionStore) Current(user string) (*model.Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current(user)
}

func (s *SQLiteSessionStore) current(user string) (*model.Session, bool) {
	ctx := context.Background()
	const q = `SELECT id, user_id, state, intent, plan, created_at, recovery_token
FROM sessions
WHERE user_id = ? AND state = 'ACTIVE'
ORDER BY created_at DESC
LIMIT 1`
	row := s.db.QueryRowContext(ctx, q, user)
	var id, uid, state, intentJSON, planJSON string
	var recoveryToken sql.NullString
	var createdAt time.Time
	if err := row.Scan(&id, &uid, &state, &intentJSON, &planJSON, &createdAt, &recoveryToken); err != nil {
		if err == sql.ErrNoRows {
			return nil, false
		}
		return nil, false
	}
	var sess model.Session
	sess.ID = model.SessionID(id)
	sess.UserID = uid
	sess.State = model.SessionState(state)
	sess.CreatedAt = createdAt
	if recoveryToken.Valid {
		sess.RecoveryToken = recoveryToken.String
	}
	if intentJSON != "" {
		if err := json.Unmarshal([]byte(intentJSON), &sess.Intent); err != nil {
			return nil, false
		}
	}
	if planJSON != "" {
		if err := json.Unmarshal([]byte(planJSON), &sess.Plan); err != nil {
			return nil, false
		}
	}
	return &sess, true
}

func (s *SQLiteSessionStore) Save(sess *model.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	intentJSON, err := json.Marshal(sess.Intent)
	if err != nil {
		return fmt.Errorf("session save: marshal intent: %w", err)
	}
	planJSON, err := json.Marshal(sess.Plan)
	if err != nil {
		return fmt.Errorf("session save: marshal plan: %w", err)
	}
	const q = `INSERT OR REPLACE INTO sessions
(id, user_id, state, intent, plan, created_at, recovery_token)
VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err = s.db.ExecContext(ctx, q,
		string(sess.ID),
		sess.UserID,
		string(model.SessionStateActive),
		string(intentJSON),
		string(planJSON),
		sess.CreatedAt.UTC(),
		nullString(sess.RecoveryToken),
	)
	if err != nil {
		return fmt.Errorf("session save: %w", err)
	}
	return nil
}

func (s *SQLiteSessionStore) Archive(id model.SessionID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	const sel = `SELECT recovery_token FROM sessions WHERE id = ?`
	var existingToken sql.NullString
	err := s.db.QueryRowContext(ctx, sel, string(id)).Scan(&existingToken)
	if err != nil {
		return fmt.Errorf("session archive: lookup: %w", err)
	}
	var token string
	if existingToken.Valid {
		token = existingToken.String
	}
	if token == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return fmt.Errorf("session archive: generate token: %w", err)
		}
		token = hex.EncodeToString(b)
	}
	const q = `UPDATE sessions SET state = 'ARCHIVED', recovery_token = ? WHERE id = ?`
	_, err = s.db.ExecContext(ctx, q, token, string(id))
	if err != nil {
		return fmt.Errorf("session archive: %w", err)
	}
	now := time.Now().UTC()
	const rq = `INSERT OR REPLACE INTO sessions_recovery (session_id, snapshot, expires_at, created_at) VALUES (?, ?, ?, ?)`
	if _, err := s.db.ExecContext(ctx, rq, string(id), token, now.Add(recoveryExpiry), now); err != nil {
		return fmt.Errorf("session archive: recovery record: %w", err)
	}
	return nil
}

func (s *SQLiteSessionStore) Recover(id model.SessionID) (*model.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	now := time.Now().UTC()
	const recoveryCheck = `SELECT session_id FROM sessions_recovery WHERE session_id = ? AND expires_at > ?`
	var recoverySID string
	if err := s.db.QueryRowContext(ctx, recoveryCheck, string(id), now).Scan(&recoverySID); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("session recover: no valid recovery entry for %s", string(id))
		}
		return nil, fmt.Errorf("session recover: %w", err)
	}
	const q = `UPDATE sessions SET state = 'ACTIVE' WHERE id = ? AND recovery_token IS NOT NULL`
	res, err := s.db.ExecContext(ctx, q, string(id))
	if err != nil {
		return nil, fmt.Errorf("session recover: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return nil, fmt.Errorf("session recover: session %s has no recovery token or does not exist", string(id))
	}
	return s.loadByID(ctx, string(id))
}

// SessionUserID returns the user_id associated with the given session ID.
// It is a non-interface convenience method used by the manager layer to
// resolve the session's user for the browser auth boundary.
func (s *SQLiteSessionStore) SessionUserID(sessionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	const q = `SELECT user_id FROM sessions WHERE id = ?`
	var userID string
	if err := s.db.QueryRowContext(ctx, q, sessionID).Scan(&userID); err != nil {
		return ""
	}
	return userID
}

// PruneExpired removes sessions marked DELETED or EXPIRED that were created
// before the given cutoff time.
// Phase-H: best-effort. Full archival retention pruning (based on
// SchedulerConfig.SessionRetention for ARCHIVED sessions) is deferred to Phase I.
func (s *SQLiteSessionStore) PruneExpired(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	const q = `DELETE FROM sessions WHERE state IN ('DELETED', 'EXPIRED') AND created_at < ?`
	res, err := s.db.ExecContext(ctx, q, now)
	if err != nil {
		return 0
	}
	affected, _ := res.RowsAffected()
	return int(affected)
}

func (s *SQLiteSessionStore) loadByID(ctx context.Context, id string) (*model.Session, error) {
	const q = `SELECT id, user_id, state, intent, plan, created_at, recovery_token FROM sessions WHERE id = ?`
	var sess model.Session
	var sid, uid, state, intentJSON, planJSON string
	var recoveryToken sql.NullString
	var createdAt time.Time
	if err := s.db.QueryRowContext(ctx, q, id).Scan(
		&sid, &uid, &state, &intentJSON, &planJSON, &createdAt, &recoveryToken,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("session not found: %s", id)
		}
		return nil, fmt.Errorf("session load: %w", err)
	}
	sess.ID = model.SessionID(sid)
	sess.UserID = uid
	sess.State = model.SessionState(state)
	sess.CreatedAt = createdAt
	if recoveryToken.Valid {
		sess.RecoveryToken = recoveryToken.String
	}
	if intentJSON != "" {
		if err := json.Unmarshal([]byte(intentJSON), &sess.Intent); err != nil {
			return nil, fmt.Errorf("session load: unmarshal intent: %w", err)
		}
	}
	if planJSON != "" {
		if err := json.Unmarshal([]byte(planJSON), &sess.Plan); err != nil {
			return nil, fmt.Errorf("session load: unmarshal plan: %w", err)
		}
	}
	return &sess, nil
}
