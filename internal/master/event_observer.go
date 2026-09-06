package master

import (
	"time"

	"draw/internal/model"
)

// Event is the master-level event emitted through the EventObserver seam.
// It intentionally omits the storage-layer surrogate primary key (ID);
// persistence is responsible for assigning it on Append.
type Event struct {
	SessionID model.SessionID
	TaskID    *model.TaskID
	Kind      string
	Level     string
	Message   string
	Data      string
	TS        time.Time
}

// EventObserver receives master events for terminal transitions and
// replan triggers. Implementations are best-effort and must not block
// or alter task/ResearchState semantics.
type EventObserver interface {
	Emit(ev Event)
}

// WithEventObserver installs an EventObserver onto the Master. When
// omitted, event emission is a no-op (nil-safe).
func WithEventObserver(o EventObserver) MasterOption {
	return func(m *Master) { m.observer = o }
}

// emitEvent is nil-safe. Called from observeAndDecide for terminal
// transitions and replan triggers. No Run-loop edits.
func (m *Master) emitEvent(ev Event) {
	if m.observer == nil {
		return
	}
	m.observer.Emit(ev)
}

// emitEventsForDecision emits Events for task completions, terminal
// decisions and replan triggers. Additive call site inside observeAndDecide — does not alter
// the Run loop.
func (m *Master) emitEventsForDecision(t model.Task, d Decision) {
	switch d.Action {
	case DecisionExecute:
		m.emitEvent(Event{
			SessionID: t.SessionID,
			TaskID:    &t.ID,
			Kind:      "task_completed",
			Level:     "INFO",
			Message:   "task executed and completed",
			TS:        time.Now().UTC(),
		})
	case DecisionTerminate, DecisionStop:
		m.emitEvent(Event{
			SessionID: t.SessionID,
			TaskID:    &t.ID,
			Kind:      "terminal",
			Level:     "WARN",
			Message:   reasonFor(d.Action, d.Reason),
			TS:        time.Now().UTC(),
		})
	}
	if d.Replan != nil {
		m.emitEvent(Event{
			SessionID: t.SessionID,
			TaskID:    &t.ID,
			Kind:      "replan_triggered",
			Level:     "INFO",
			Message:   string(d.Replan.Kind),
			TS:        time.Now().UTC(),
		})
	}
}
