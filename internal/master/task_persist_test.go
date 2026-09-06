package master

import (
	"context"
	"sync"
	"testing"

	"draw/internal/config"
	"draw/internal/model"
)

type fakeTaskPersist struct {
	mu      sync.Mutex
	saves   []model.Task
	updates []taskStateUpdate
}

type taskStateUpdate struct {
	id    model.TaskID
	state model.TaskState
}

func (f *fakeTaskPersist) Save(t model.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saves = append(f.saves, t)
	return nil
}

func (f *fakeTaskPersist) UpdateState(id model.TaskID, state model.TaskState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, taskStateUpdate{id: id, state: state})
	return nil
}

func (f *fakeTaskPersist) nSaves() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.saves)
}

func (f *fakeTaskPersist) nUpdates() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.updates)
}

func TestMaster_TaskPersistenceHookCalled(t *testing.T) {
	tp := &fakeTaskPersist{}
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{Status: model.RetrievalStatusSuccess, Evidence: []model.EvidenceID{"e1"}}
	}}
	sched := newFakeScheduler(mgr, 4)
	m := NewMaster(config.Defaults(), sched)
	m.evidence = &countingEvidence{}
	m.tasksPersist = tp

	if _, err := m.SubmitIntent(model.IntentRequest{
		UserID: "u1", Query: "research Company X", Seeds: []string{seedURL()},
	}); err != nil {
		t.Fatal(err)
	}

	if tp.nSaves() == 0 {
		t.Error("expected persistTask to be called for seed tasks")
	}

	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if tp.nUpdates() == 0 {
		t.Error("expected persistTaskState to be called for completed tasks")
	}
}

func TestMaster_TaskPersistenceNilSafe(t *testing.T) {
	mgr := &fakeManager{respond: func(tk model.Task, c int) model.TaskResult {
		return model.TaskResult{Status: model.RetrievalStatusSuccess, Evidence: []model.EvidenceID{"e1"}}
	}}
	sched := newFakeScheduler(mgr, 4)
	m := NewMaster(config.Defaults(), sched)
	m.evidence = &countingEvidence{}
	// m.tasksPersist is nil — calls must be no-ops, not panics

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
