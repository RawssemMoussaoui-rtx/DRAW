package storage

import (
	"context"
	"testing"
	"time"

	"draw/internal/model"
)

func TestSQLiteSessionStore_SatisfiesInterface(t *testing.T) {
	var _ SessionStore = (*SQLiteSessionStore)(nil)
}

func newTestSessionStore(t *testing.T) *SQLiteSessionStore {
	t.Helper()
	db := newTestDB(t)
	if err := Apply(context.Background(), db); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	store, err := NewSQLiteSessionStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteSessionStore: %v", err)
	}
	return store
}

func mkTestSession(user string) *model.Session {
	return &model.Session{
		ID:        model.NewSessionID(),
		UserID:    user,
		State:     model.SessionStateActive,
		Intent:    model.Intent{Type: model.IntentTypeResearch, Entity: "physics"},
		Plan:      model.Plan{Phases: []model.Phase{{Name: "phase1"}}},
		CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestSQLiteSessionStore_SaveAndCurrent(t *testing.T) {
	store := newTestSessionStore(t)
	sess := mkTestSession("alice")
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok := store.Current("alice")
	if !ok {
		t.Fatal("Current: expected active session for alice")
	}
	if got.ID != sess.ID {
		t.Errorf("ID = %s, want %s", got.ID, sess.ID)
	}
	if got.UserID != "alice" {
		t.Errorf("UserID = %q, want alice", got.UserID)
	}
	if got.State != model.SessionStateActive {
		t.Errorf("State = %s, want %s", got.State, model.SessionStateActive)
	}
	if got.Intent.Entity != "physics" {
		t.Errorf("Intent.Entity = %q, want physics", got.Intent.Entity)
	}
	if len(got.Plan.Phases) != 1 || got.Plan.Phases[0].Name != "phase1" {
		t.Errorf("Plan.Phases = %v, want one phase named phase1", got.Plan.Phases)
	}
}

func TestSQLiteSessionStore_CurrentReturnsFalseWhenNoActive(t *testing.T) {
	store := newTestSessionStore(t)
	got, ok := store.Current("nobody")
	if ok {
		t.Fatalf("expected no active session, got %+v", got)
	}
	if got != nil {
		t.Errorf("expected nil session, got %+v", got)
	}
}

func TestSQLiteSessionStore_CurrentSingleActivePerUser(t *testing.T) {
	store := newTestSessionStore(t)
	s1 := mkTestSession("bob")
	s1.CreatedAt = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	s2 := mkTestSession("bob")
	s2.ID = model.NewSessionID()
	s2.CreatedAt = time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	if err := store.Save(s1); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(s2); err != nil {
		t.Fatal(err)
	}
	got, ok := store.Current("bob")
	if !ok {
		t.Fatal("expected active session for bob")
	}
	if got.ID != s2.ID {
		t.Errorf("expected most recent active session, got %s, want %s", got.ID, s2.ID)
	}
}

func TestSQLiteSessionStore_ArchiveGeneratesToken(t *testing.T) {
	store := newTestSessionStore(t)
	sess := mkTestSession("carol")
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	if err := store.Archive(sess.ID); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	got, ok := store.Current("carol")
	if ok {
		t.Fatal("expected no active session after archive")
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestSQLiteSessionStore_RecoverRestoresActive(t *testing.T) {
	store := newTestSessionStore(t)
	sess := mkTestSession("dave")
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	if err := store.Archive(sess.ID); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	recovered, err := store.Recover(sess.ID)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if recovered.State != model.SessionStateActive {
		t.Errorf("State = %s, want %s", recovered.State, model.SessionStateActive)
	}
	got, ok := store.Current("dave")
	if !ok {
		t.Fatal("expected active session after recover")
	}
	if got.ID != sess.ID {
		t.Errorf("ID = %s, want %s", got.ID, sess.ID)
	}
}

func TestSQLiteSessionStore_RecoverFailsWithoutRecoveryEntry(t *testing.T) {
	store := newTestSessionStore(t)
	sess := mkTestSession("eve")
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	if err := store.Archive(sess.ID); err != nil {
		t.Fatal(err)
	}
	// Corrupt the recovery entry by expiring it
	ctx := context.Background()
	db := store.db
	const expireQ = `UPDATE sessions_recovery SET expires_at = ? WHERE session_id = ?`
	if _, err := db.ExecContext(ctx, expireQ, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), string(sess.ID)); err != nil {
		t.Fatal(err)
	}
	_, err := store.Recover(sess.ID)
	if err == nil {
		t.Fatal("expected error when recovery entry expired")
	}
}

func TestSQLiteSessionStore_PruneExpired(t *testing.T) {
	store := newTestSessionStore(t)
	old := mkTestSession("frank")
	old.CreatedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	old.State = model.SessionStateDeleted
	if err := store.Save(old); err != nil {
		t.Fatal(err)
	}
	// Simulate a deleted state directly
	ctx := context.Background()
	const q = `UPDATE sessions SET state = 'DELETED' WHERE id = ?`
	if _, err := store.db.ExecContext(ctx, q, string(old.ID)); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	pruned := store.PruneExpired(cutoff)
	if pruned != 1 {
		t.Errorf("PruneExpired = %d, want 1", pruned)
	}
}

func TestSQLiteSessionStore_PruneExpiredKeepsActive(t *testing.T) {
	store := newTestSessionStore(t)
	sess := mkTestSession("grace")
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC()
	pruned := store.PruneExpired(cutoff)
	if pruned != 0 {
		t.Errorf("PruneExpired = %d, want 0 (active sessions not pruned)", pruned)
	}
}

func TestSQLiteSessionStore_SessionUserID(t *testing.T) {
	store := newTestSessionStore(t)
	sess := mkTestSession("alice")
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	got := store.SessionUserID(string(sess.ID))
	if got != "alice" {
		t.Errorf("SessionUserID = %q, want alice", got)
	}
}

func TestSQLiteSessionStore_SessionUserIDNotFound(t *testing.T) {
	store := newTestSessionStore(t)
	got := store.SessionUserID("nonexistent-session")
	if got != "" {
		t.Errorf("SessionUserID = %q, want empty for nonexistent session", got)
	}
}
