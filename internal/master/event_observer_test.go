package master

import (
	"context"
	"sync"
	"testing"

	"draw/internal/config"
	"draw/internal/model"
)

type fakeEventObserver struct {
	mu     sync.Mutex
	events []Event
}

func (f *fakeEventObserver) Emit(ev Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
}

func (f *fakeEventObserver) nEvents() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func (f *fakeEventObserver) hasTerminalEvent() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range f.events {
		if ev.Kind == "terminal" {
			return true
		}
	}
	return false
}

func (f *fakeEventObserver) hasTaskCompletedEvent() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ev := range f.events {
		if ev.Kind == "task_completed" {
			return true
		}
	}
	return false
}

func TestMaster_EventsEmittedOnTerminal(t *testing.T) {
	obs := &fakeEventObserver{}
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{Status: model.RetrievalStatusSuccess, Evidence: []model.EvidenceID{"e1"}}
	}}
	sched := newFakeScheduler(mgr, 4)
	m := NewMaster(config.Defaults(), sched, WithEventObserver(obs))
	m.evidence = &countingEvidence{}

	if _, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: "research Company X", Seeds: []string{seedURL()},
	}); err != nil {
		t.Fatal(err)
	}

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if !m.State().Terminal {
		t.Fatal("expected terminal state")
	}
	if !obs.hasTaskCompletedEvent() {
		t.Error("expected at least one task_completed event to be emitted")
	}
}

func TestMaster_EventObserverNilSafe(t *testing.T) {
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{Status: model.RetrievalStatusSuccess, Evidence: []model.EvidenceID{"e1"}}
	}}
	sched := newFakeScheduler(mgr, 4)
	m := NewMaster(config.Defaults(), sched)
	m.evidence = &countingEvidence{}
	// m.observer is nil — emitEvent must be a no-op, not a panic

	if _, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: "research Company X", Seeds: []string{seedURL()},
	}); err != nil {
		t.Fatal(err)
	}

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if !m.State().Terminal {
		t.Error("expected terminal state")
	}
}
