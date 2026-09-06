package storage

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"draw/internal/model"
)

type SQLiteTaskStore struct {
	mu sync.Mutex
	db *sql.DB
}

func NewSQLiteTaskStore(db *sql.DB) (*SQLiteTaskStore, error) {
	return &SQLiteTaskStore{db: db}, nil
}

func (s *SQLiteTaskStore) Init(ctx context.Context) error {
	return nil
}

func (s *SQLiteTaskStore) Save(t model.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	var urlVal interface{}
	if t.URL != nil {
		urlVal = t.URL.String()
	}
	var parentID, createdByID, discoveredFromURLID interface{}
	if t.ParentTaskID != nil {
		parentID = string(*t.ParentTaskID)
	}
	if t.CreatedByTaskID != nil {
		createdByID = string(*t.CreatedByTaskID)
	}
	if t.DiscoveredFromURL != nil {
		discoveredFromURLID = string(*t.DiscoveredFromURL)
	}
	var backoffUntil interface{}
	if !t.BackoffUntil.IsZero() {
		backoffUntil = t.BackoffUntil.UTC()
	}
	var startedAt interface{}
	if !t.StartedAt.IsZero() {
		startedAt = t.StartedAt.UTC()
	}
	var completedAt interface{}
	if !t.CompletedAt.IsZero() {
		completedAt = t.CompletedAt.UTC()
	}
	const q = `INSERT OR REPLACE INTO tasks
(id, session_id, type, state, priority, source_class, source_target, url, parent_task_id, created_by_task_id, discovered_from_url_id,
 crawl_depth, task_depth, estimated_cost, error_weight, retry_count, backoff_until, error_info, created_at, started_at, completed_at, task_key, user_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, q,
		string(t.ID),
		string(t.SessionID),
		string(t.Type),
		string(t.State),
		t.Priority,
		nullString(string(t.SourceClass)),
		t.SourceTarget,
		urlVal,
		parentID,
		createdByID,
		discoveredFromURLID,
		t.CrawlDepth,
		t.TaskDepth,
		t.EstimatedCost,
		nullString(string(t.ErrorWeight)),
		t.RetryCount,
		backoffUntil,
		t.ErrorInfo,
		t.CreatedAt.UTC(),
		startedAt,
		completedAt,
		t.TaskKey,
		t.UserId,
	)
	if err != nil {
		return fmt.Errorf("task save: %w", err)
	}
	return nil
}

func (s *SQLiteTaskStore) UpdateState(id model.TaskID, state model.TaskState, startedAt, completedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	var startedVal, completedVal interface{}
	if !startedAt.IsZero() {
		startedVal = startedAt.UTC()
	}
	if !completedAt.IsZero() {
		completedVal = completedAt.UTC()
	}
	const q = `UPDATE tasks SET state = ?, started_at = ?, completed_at = ? WHERE id = ?`
	if _, err := s.db.ExecContext(ctx, q, string(state), startedVal, completedVal, string(id)); err != nil {
		return fmt.Errorf("task update state: %w", err)
	}
	return nil
}

func (s *SQLiteTaskStore) LoadBySession(sid model.SessionID) []model.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadBySession(string(sid), nil)
}

func (s *SQLiteTaskStore) LoadPending(sid model.SessionID) []model.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	states := []model.TaskState{
		model.TaskStateReady,
		model.TaskStateRunning,
		model.TaskStateRetryWait,
	}
	return s.loadBySession(string(sid), states)
}

func (s *SQLiteTaskStore) loadBySession(sid string, pendingStates []model.TaskState) []model.Task {
	ctx := context.Background()
	const cols = `SELECT id, session_id, type, state, priority, source_class, source_target, url, parent_task_id, created_by_task_id, discovered_from_url_id,
 crawl_depth, task_depth, estimated_cost, error_weight, retry_count, backoff_until, error_info, created_at, started_at, completed_at, task_key, user_id
FROM tasks WHERE session_id = ?`
	args := []interface{}{sid}
	query := cols
	if pendingStates != nil {
		placeholders := make([]string, len(pendingStates))
		for i, st := range pendingStates {
			placeholders[i] = "?"
			args = append(args, string(st))
		}
		query += " AND state IN (" + strings.Join(placeholders, ",") + ")"
	}
	query += " ORDER BY priority DESC, created_at ASC"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []model.Task
	for rows.Next() {
		t, err := scanTask(rows.Scan)
		if err != nil {
			return nil
		}
		out = append(out, t)
	}
	return out
}

func scanTask(scan func(...interface{}) error) (model.Task, error) {
	var (
		id, sessionID, typeStr, stateStr string
		priority                         int
		sourceClass                      sql.NullString
		sourceTarget                     sql.NullString
		urlStr                           sql.NullString
		parentTaskID                     sql.NullString
		createdByTaskID                  sql.NullString
		discoveredFromURLID              sql.NullString
		crawlDepth                       int
		taskDepth                        int
		estimatedCost                    int
		errorWeight                      sql.NullString
		retryCount                       int
		backoffUntil                     sql.NullTime
		errorInfo                        sql.NullString
		createdAt                        time.Time
		startedAt                        sql.NullTime
		completedAt                      sql.NullTime
		taskKey                          string
		userID                           string
	)
	if err := scan(
		&id, &sessionID, &typeStr, &stateStr, &priority,
		&sourceClass, &sourceTarget, &urlStr, &parentTaskID,
		&createdByTaskID, &discoveredFromURLID,
		&crawlDepth, &taskDepth, &estimatedCost, &errorWeight,
		&retryCount, &backoffUntil, &errorInfo,
		&createdAt, &startedAt, &completedAt, &taskKey, &userID,
	); err != nil {
		return model.Task{}, err
	}
	var t model.Task
	t.ID = model.TaskID(id)
	t.SessionID = model.SessionID(sessionID)
	t.Type = model.TaskType(typeStr)
	t.State = model.TaskState(stateStr)
	t.Priority = priority
	if sourceClass.Valid {
		t.SourceClass = model.SourceClass(sourceClass.String)
	}
	if sourceTarget.Valid {
		t.SourceTarget = sourceTarget.String
	}
	if urlStr.Valid {
		parsed, err := url.Parse(urlStr.String)
		if err == nil {
			t.URL = parsed
		}
	}
	if parentTaskID.Valid {
		pt := model.TaskID(parentTaskID.String)
		t.ParentTaskID = &pt
	}
	if createdByTaskID.Valid {
		ct := model.TaskID(createdByTaskID.String)
		t.CreatedByTaskID = &ct
	}
	if discoveredFromURLID.Valid {
		du := model.URLID(discoveredFromURLID.String)
		t.DiscoveredFromURL = &du
	}
	t.CrawlDepth = crawlDepth
	t.TaskDepth = taskDepth
	t.EstimatedCost = estimatedCost
	if errorWeight.Valid {
		t.ErrorWeight = model.ErrorWeight(errorWeight.String)
	}
	t.RetryCount = retryCount
	if backoffUntil.Valid {
		t.BackoffUntil = backoffUntil.Time
	}
	if errorInfo.Valid {
		t.ErrorInfo = errorInfo.String
	}
	t.CreatedAt = createdAt
	if startedAt.Valid {
		t.StartedAt = startedAt.Time
	}
	if completedAt.Valid {
		t.CompletedAt = completedAt.Time
	}
	t.TaskKey = taskKey
	t.UserId = userID
	return t, nil
}
