package storage

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"draw/internal/model"
)

type SQLiteEventStore struct {
	mu sync.Mutex
	db *sql.DB
}

func NewSQLiteEventStore(db *sql.DB) (*SQLiteEventStore, error) {
	return &SQLiteEventStore{db: db}, nil
}

func (s *SQLiteEventStore) Init(ctx context.Context) error {
	return nil
}

func (s *SQLiteEventStore) Append(e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	var taskID interface{}
	if e.TaskID != nil {
		taskID = string(*e.TaskID)
	}
	const q = `INSERT INTO events (session_id, task_id, kind, level, message, data, ts) VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, q,
		string(e.SessionID),
		taskID,
		e.Kind,
		e.Level,
		e.Message,
		nullString(e.Data),
		e.TS.UTC(),
	)
	if err != nil {
		return fmt.Errorf("event append: %w", err)
	}
	return nil
}

func (s *SQLiteEventStore) Load(sessionID model.SessionID, afterID int64, limit int) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	const q = `SELECT id, session_id, task_id, kind, level, message, data, ts
FROM events
WHERE session_id = ? AND id > ?
ORDER BY id ASC
LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, string(sessionID), afterID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows.Scan)
		if err != nil {
			return nil
		}
		out = append(out, e)
	}
	return out
}

func (s *SQLiteEventStore) LoadSince(sessionID model.SessionID, since time.Time) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	const q = `SELECT id, session_id, task_id, kind, level, message, data, ts
FROM events
WHERE session_id = ? AND ts > ?
ORDER BY ts ASC`
	rows, err := s.db.QueryContext(ctx, q, string(sessionID), since.UTC())
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows.Scan)
		if err != nil {
			return nil
		}
		out = append(out, e)
	}
	return out
}

func scanEvent(scan func(...interface{}) error) (Event, error) {
	var (
		id        int64
		sessionID string
		taskID    sql.NullString
		kind      string
		level     string
		message   string
		data      sql.NullString
		ts        time.Time
	)
	if err := scan(&id, &sessionID, &taskID, &kind, &level, &message, &data, &ts); err != nil {
		return Event{}, err
	}
	var e Event
	e.ID = id
	e.SessionID = model.SessionID(sessionID)
	if taskID.Valid {
		tid := model.TaskID(taskID.String)
		e.TaskID = &tid
	}
	e.Kind = kind
	e.Level = level
	e.Message = message
	if data.Valid {
		e.Data = data.String
	}
	e.TS = ts
	return e, nil
}
