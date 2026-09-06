package master

import (
	"time"

	"draw/internal/model"
	"draw/internal/storage"
)

// TaskPersistence is the master-level persistence seam for tasks.
// It adapts the richer storage.TaskStore interface down to the two
// operations the master needs: Save (full task) and UpdateState.
type TaskPersistence interface {
	Save(model.Task) error
	UpdateState(id model.TaskID, state model.TaskState) error
}

// taskStoreAdapter adapts storage.TaskStore to TaskPersistence by
// dropping the startedAt/completedAt timestamps (PLAN §10).
type taskStoreAdapter struct {
	ts storage.TaskStore
}

func (a *taskStoreAdapter) Save(t model.Task) error {
	return a.ts.Save(t)
}

func (a *taskStoreAdapter) UpdateState(id model.TaskID, state model.TaskState) error {
	return a.ts.UpdateState(id, state, time.Time{}, time.Time{})
}

// WithTaskStore installs a storage.TaskStore as the master's task
// persistence seam. The store is wrapped in a taskStoreAdapter.
// When omitted (or nil), task persistence is a no-op (nil-safe).
func WithTaskStore(ts storage.TaskStore) MasterOption {
	return func(m *Master) {
		if ts == nil {
			return
		}
		m.tasksPersist = &taskStoreAdapter{ts: ts}
	}
}

// persistTask saves the full task to the persistence seam.
// Nil-safe: no-op when no TaskPersistence is configured.
// Must be called from within methods (not necessarily holding m.mu).
func (m *Master) persistTask(t model.Task) {
	if m.tasksPersist == nil {
		return
	}
	_ = m.tasksPersist.Save(t)
}

// persistTaskState updates the task's state in the persistence seam.
// Nil-safe: no-op when no TaskPersistence is configured.
func (m *Master) persistTaskState(id model.TaskID, state model.TaskState) {
	if m.tasksPersist == nil {
		return
	}
	_ = m.tasksPersist.UpdateState(id, state)
}
