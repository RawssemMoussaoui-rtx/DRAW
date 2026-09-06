package storage

import (
	"context"
	"testing"
	"time"

	"draw/internal/model"
)

func TestSQLiteEventStore_SatisfiesInterface(t *testing.T) {
	var _ EventStore = (*SQLiteEventStore)(nil)
}

func newTestEventStore(t *testing.T) *SQLiteEventStore {
	t.Helper()
	db := newTestDB(t)
	if err := Apply(context.Background(), db); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	store, err := NewSQLiteEventStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteEventStore: %v", err)
	}
	return store
}

func TestSQLiteEventStore_AppendAndLoad(t *testing.T) {
	store := newTestEventStore(t)
	session := model.NewSessionID()
	tsk := model.NewTaskID()
	events := []Event{
		{SessionID: session, TaskID: &tsk, Kind: "DISCOVERY", Level: "INFO", Message: "task created", TS: time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)},
		{SessionID: session, TaskID: &tsk, Kind: "FETCH", Level: "WARN", Message: "retry needed", TS: time.Date(2024, 1, 1, 10, 1, 0, 0, time.UTC)},
		{SessionID: session, Kind: "COMPLETE", Level: "INFO", Message: "done", TS: time.Date(2024, 1, 1, 10, 2, 0, 0, time.UTC)},
	}
	for _, e := range events {
		if err := store.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got := store.Load(session, 0, 100)
	if len(got) != 3 {
		t.Fatalf("expected 3 events, got %d", len(got))
	}
	if got[0].Kind != "DISCOVERY" {
		t.Errorf("first event Kind = %q, want DISCOVERY", got[0].Kind)
	}
	if got[2].Kind != "COMPLETE" {
		t.Errorf("last event Kind = %q, want COMPLETE", got[2].Kind)
	}
}

func TestSQLiteEventStore_LoadWithAfterID(t *testing.T) {
	store := newTestEventStore(t)
	session := model.NewSessionID()
	for i := 0; i < 5; i++ {
		e := Event{
			SessionID: session,
			Kind:      "tick",
			Level:     "INFO",
			Message:   string(rune('a' + i)),
			TS:        time.Date(2024, 1, 1, 10, i, 0, 0, time.UTC),
		}
		if err := store.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	// After the first event (id=1), should get 4 events
	got := store.Load(session, 1, 100)
	if len(got) != 4 {
		t.Fatalf("expected 4 events after id=1, got %d", len(got))
	}
	if got[0].Message != "b" {
		t.Errorf("first event Message = %q, want b", got[0].Message)
	}
}

func TestSQLiteEventStore_LoadWithLimit(t *testing.T) {
	store := newTestEventStore(t)
	session := model.NewSessionID()
	for i := 0; i < 5; i++ {
		e := Event{
			SessionID: session,
			Kind:      "tick",
			Level:     "INFO",
			Message:   string(rune('a' + i)),
			TS:        time.Date(2024, 1, 1, 10, i, 0, 0, time.UTC),
		}
		if err := store.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got := store.Load(session, 0, 3)
	if len(got) != 3 {
		t.Fatalf("expected 3 events (limited), got %d", len(got))
	}
}

func TestSQLiteEventStore_LoadSince(t *testing.T) {
	store := newTestEventStore(t)
	session := model.NewSessionID()
	e1 := Event{SessionID: session, Kind: "early", Level: "INFO", Message: "m1", TS: time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)}
	e2 := Event{SessionID: session, Kind: "late", Level: "INFO", Message: "m2", TS: time.Date(2024, 1, 1, 11, 0, 0, 0, time.UTC)}
	e3 := Event{SessionID: session, Kind: "later", Level: "INFO", Message: "m3", TS: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)}
	for _, e := range []Event{e1, e2, e3} {
		if err := store.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	since := time.Date(2024, 1, 1, 11, 30, 0, 0, time.UTC)
	got := store.LoadSince(session, since)
	if len(got) != 1 {
		t.Fatalf("expected 1 event since cutoff, got %d", len(got))
	}
	if got[0].Kind != "later" {
		t.Errorf("event Kind = %q, want later", got[0].Kind)
	}
}

func TestSQLiteEventStore_LoadSeparatesBySession(t *testing.T) {
	store := newTestEventStore(t)
	s1 := model.NewSessionID()
	s2 := model.NewSessionID()
	for _, sess := range []model.SessionID{s1, s2} {
		e := Event{
			SessionID: sess,
			Kind:      "tick",
			Level:     "INFO",
			Message:   string(sess[0]) + " msg",
			TS:        time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
		}
		if err := store.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	got1 := store.Load(s1, 0, 100)
	if len(got1) != 1 {
		t.Fatalf("expected 1 event for session 1, got %d", len(got1))
	}
	got2 := store.Load(s2, 0, 100)
	if len(got2) != 1 {
		t.Fatalf("expected 1 event for session 2, got %d", len(got2))
	}
}

func TestSQLiteEventStore_AppendWithData(t *testing.T) {
	store := newTestEventStore(t)
	session := model.NewSessionID()
	e := Event{
		SessionID: session,
		Kind:      "ERROR",
		Level:     "ERROR",
		Message:   "something failed",
		Data:      `{"code": 500, "reason": "timeout"}`,
		TS:        time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
	}
	if err := store.Append(e); err != nil {
		t.Fatal(err)
	}
	got := store.Load(session, 0, 1)
	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	if got[0].Data != `{"code": 500, "reason": "timeout"}` {
		t.Errorf("Data = %q, want JSON blob", got[0].Data)
	}
}
