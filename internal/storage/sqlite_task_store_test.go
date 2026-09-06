package storage

import (
	"context"
	"net/url"
	"testing"
	"time"

	"draw/internal/model"
)

func TestSQLiteTaskStore_SatisfiesInterface(t *testing.T) {
	var _ TaskStore = (*SQLiteTaskStore)(nil)
}

func newTestTaskStore(t *testing.T) *SQLiteTaskStore {
	t.Helper()
	db := newTestDB(t)
	if err := Apply(context.Background(), db); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	store, err := NewSQLiteTaskStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteTaskStore: %v", err)
	}
	return store
}

func mkTestTask(session model.SessionID, key string) model.Task {
	return model.Task{
		ID:        model.NewTaskID(),
		SessionID: session,
		Type:      model.TaskTypeDiscover,
		State:     model.TaskStateReady,
		Priority:  1,
		TaskKey:   key,
		CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestSQLiteTaskStore_SaveAndLoadBySession(t *testing.T) {
	store := newTestTaskStore(t)
	session := model.NewSessionID()
	t1 := mkTestTask(session, "k1")
	t2 := mkTestTask(session, "k2")
	t2.Type = model.TaskTypeFetchHTTP
	if err := store.Save(t1); err != nil {
		t.Fatalf("Save t1: %v", err)
	}
	if err := store.Save(t2); err != nil {
		t.Fatalf("Save t2: %v", err)
	}
	got := store.LoadBySession(session)
	if len(got) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(got))
	}
	if got[0].ID != t2.ID && got[0].ID != t1.ID {
		t.Errorf("unexpected task ID %s", got[0].ID)
	}
	// Results ordered by priority DESC; both have priority 1, so check set membership
	ids := map[model.TaskID]bool{got[0].ID: true, got[1].ID: true}
	if !ids[t1.ID] || !ids[t2.ID] {
		t.Errorf("expected both tasks, got %s and %s", got[0].ID, got[1].ID)
	}
}

func TestSQLiteTaskStore_LoadBySessionEmpty(t *testing.T) {
	store := newTestTaskStore(t)
	got := store.LoadBySession(model.NewSessionID())
	if len(got) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(got))
	}
}

func TestSQLiteTaskStore_LoadPendingFiltersTerminals(t *testing.T) {
	store := newTestTaskStore(t)
	session := model.NewSessionID()
	ready := mkTestTask(session, "k1")
	ready.State = model.TaskStateReady
	running := mkTestTask(session, "k2")
	running.State = model.TaskStateRunning
	retryWait := mkTestTask(session, "k3")
	retryWait.State = model.TaskStateRetryWait
	completed := mkTestTask(session, "k4")
	completed.State = model.TaskStateCompleted
	failed := mkTestTask(session, "k5")
	failed.State = model.TaskStateFailed
	for _, tk := range []model.Task{ready, running, retryWait, completed, failed} {
		if err := store.Save(tk); err != nil {
			t.Fatal(err)
		}
	}
	got := store.LoadPending(session)
	if len(got) != 3 {
		t.Fatalf("expected 3 pending tasks, got %d", len(got))
	}
}

func TestSQLiteTaskStore_UpdateState(t *testing.T) {
	store := newTestTaskStore(t)
	session := model.NewSessionID()
	tk := mkTestTask(session, "k1")
	if err := store.Save(tk); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)
	completedAt := time.Date(2024, 6, 1, 11, 0, 0, 0, time.UTC)
	if err := store.UpdateState(tk.ID, model.TaskStateCompleted, startedAt, completedAt); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	got := store.LoadBySession(session)
	if len(got) != 1 {
		t.Fatalf("expected 1 task, got %d", len(got))
	}
	if got[0].State != model.TaskStateCompleted {
		t.Errorf("State = %s, want %s", got[0].State, model.TaskStateCompleted)
	}
	if !got[0].StartedAt.Equal(startedAt) {
		t.Errorf("StartedAt = %v, want %v", got[0].StartedAt, startedAt)
	}
	if !got[0].CompletedAt.Equal(completedAt) {
		t.Errorf("CompletedAt = %v, want %v", got[0].CompletedAt, completedAt)
	}
}

func TestSQLiteTaskStore_SaveDedupByTaskKey(t *testing.T) {
	store := newTestTaskStore(t)
	session := model.NewSessionID()
	t1 := mkTestTask(session, "dedup-key")
	t1.ID = model.NewTaskID()
	t2 := mkTestTask(session, "dedup-key")
	t2.ID = model.NewTaskID()
	if err := store.Save(t1); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t2); err != nil {
		t.Fatalf("Save dedup should not error: %v", err)
	}
	got := store.LoadBySession(session)
	if len(got) != 1 {
		t.Fatalf("expected 1 task after dedup, got %d", len(got))
	}
	if got[0].ID != t2.ID {
		t.Errorf("expected dedup to keep latest (id=%s), got %s", t2.ID, got[0].ID)
	}
}

func TestSQLiteTaskStore_SaveWithURLAndParent(t *testing.T) {
	store := newTestTaskStore(t)
	session := model.NewSessionID()
	parentID := model.NewTaskID()
	u, err := url.Parse("https://example.com/page")
	if err != nil {
		t.Fatal(err)
	}
	tk := mkTestTask(session, "k1")
	tk.URL = u
	tk.ParentTaskID = &parentID
	tk.ErrorWeight = model.ErrorWeightModerate
	tk.TaskKey = "url-parent-key"
	if err := store.Save(tk); err != nil {
		t.Fatal(err)
	}
	got := store.LoadBySession(session)
	if len(got) != 1 {
		t.Fatalf("expected 1 task, got %d", len(got))
	}
	if got[0].URL == nil || got[0].URL.String() != "https://example.com/page" {
		t.Errorf("URL = %v, want https://example.com/page", got[0].URL)
	}
	if got[0].ParentTaskID == nil || *got[0].ParentTaskID != parentID {
		t.Errorf("ParentTaskID = %v, want %s", got[0].ParentTaskID, parentID)
	}
	if got[0].ErrorWeight != model.ErrorWeightModerate {
		t.Errorf("ErrorWeight = %s, want %s", got[0].ErrorWeight, model.ErrorWeightModerate)
	}
}

func TestSQLiteTaskStore_UserIdRoundTrip(t *testing.T) {
	store := newTestTaskStore(t)
	session := model.NewSessionID()
	tk := mkTestTask(session, "user-key")
	tk.UserId = "alice"
	if err := store.Save(tk); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := store.LoadBySession(session)
	if len(got) != 1 {
		t.Fatalf("expected 1 task, got %d", len(got))
	}
	if got[0].UserId != "alice" {
		t.Errorf("UserId = %q, want alice", got[0].UserId)
	}
}

func TestSQLiteTaskStore_UserIdDefaultsToEmpty(t *testing.T) {
	store := newTestTaskStore(t)
	session := model.NewSessionID()
	tk := mkTestTask(session, "no-user")
	tk.UserId = ""
	if err := store.Save(tk); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := store.LoadBySession(session)
	if len(got) != 1 {
		t.Fatalf("expected 1 task, got %d", len(got))
	}
	if got[0].UserId != "" {
		t.Errorf("UserId = %q, want empty", got[0].UserId)
	}
}
