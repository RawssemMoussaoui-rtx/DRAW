package model

import (
	"net/http"
	"net/url"
	"time"
)

type Task struct {
	ID                TaskID
	SessionID         SessionID
	UserId            string
	Type              TaskType
	State             TaskState
	Priority          int
	SourceClass       SourceClass
	SourceTarget      string
	URL               *url.URL
	ParentTaskID      *TaskID
	CreatedByTaskID   *TaskID
	DiscoveredFromURL *URLID
	CrawlDepth        int
	TaskDepth         int
	EstimatedCost     int
	ErrorWeight       ErrorWeight
	RetryCount        int
	BackoffUntil      time.Time
	ErrorInfo         string
	TaskKey           string
	CreatedAt         time.Time
	StartedAt         time.Time
	CompletedAt       time.Time
}

type TaskResult struct {
	Status   RetrievalStatus
	Data     []byte
	Evidence []EvidenceID
	Headers  http.Header
	Error    error
}

func (t Task) IsTerminal() bool {
	switch t.State {
	case TaskStateCompleted, TaskStateCancelled, TaskStateExpired:
		return true
	}
	return false
}
