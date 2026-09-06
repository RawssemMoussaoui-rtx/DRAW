package storage

import (
	"time"

	"draw/internal/model"
)

type SessionStore interface {
	Current(user string) (*model.Session, bool)
	Save(*model.Session) error
	Archive(model.SessionID) error
	Recover(model.SessionID) (*model.Session, error)
	PruneExpired(time.Time) int
}

type SourceRegistry interface {
	Lookup(domain string) (*model.SourceProfile, bool)
	Upsert(model.SourceProfile) error
	Deny(domain string) error
	ProvisionalUpsert(model.SourceProfile) error
}

type TaskFilter struct {
	SessionID model.SessionID
	State     model.TaskState
}

type TaskStore interface {
	Save(t model.Task) error
	UpdateState(id model.TaskID, state model.TaskState, startedAt, completedAt time.Time) error
	LoadBySession(sid model.SessionID) []model.Task
	LoadPending(sid model.SessionID) []model.Task
}

type Event struct {
	ID        int64
	SessionID model.SessionID
	TaskID    *model.TaskID
	Kind      string
	Level     string
	Message   string
	Data      string
	TS        time.Time
}

type EventStore interface {
	Append(Event) error
	Load(sessionID model.SessionID, afterID int64, limit int) []Event
	LoadSince(sessionID model.SessionID, since time.Time) []Event
}
