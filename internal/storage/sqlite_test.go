package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func tableExists(ctx context.Context, db *sql.DB, name string) bool {
	var found bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type IN ('table') AND name = ?)`, name).Scan(&found)
	if err != nil {
		return false
	}
	return found
}

func TestApplyCreatesCoreTables(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, name := range coreTables {
		if !tableExists(ctx, db, name) {
			t.Errorf("missing table %q after migration", name)
		}
	}
}

func TestApplyIdempotent(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	for i := 0; i < 3; i++ {
		if err := Apply(ctx, db); err != nil {
			t.Fatalf("Apply pass %d: %v", i, err)
		}
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 applied migration rows, got %d", n)
	}
}

func TestTaskDedupUniqueConstraint(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	const base = `INSERT INTO tasks (id, session_id, type, state, task_key, created_at) VALUES (?, ?, ?, ?, ?, ?)`
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, base, "t1", "s1", "DISCOVER", "READY", "k1", now); err != nil {
		t.Fatalf("insert first: %v", err)
	}
	if _, err := db.ExecContext(ctx, base, "t2", "s1", "DISCOVER", "READY", "k1", now); err == nil {
		t.Fatal("expected UNIQUE violation for duplicate (session_id, task_key)")
	}
	if _, err := db.ExecContext(ctx, base, "t3", "s1", "DISCOVER", "READY", "k2", now); err != nil {
		t.Fatalf("distinct task_key should succeed: %v", err)
	}
}

func TestTaskEnumCheckConstraints(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	const base = `INSERT INTO tasks (id, session_id, type, state, task_key, created_at) VALUES (?, ?, ?, ?, ?, ?)`
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, base, "x1", "s1", "DISCOVER", "BOGUS_STATE", "k1", now); err == nil {
		t.Fatal("expected CHECK failure for invalid TaskState")
	}
	if _, err := db.ExecContext(ctx, base, "x2", "s1", "BOGUS_TYPE", "READY", "k2", now); err == nil {
		t.Fatal("expected CHECK failure for invalid TaskType")
	}
	if _, err := db.ExecContext(ctx, base, "x3", "s1", "DISCOVER", "READY", "k3", now); err != nil {
		t.Fatalf("valid task insert should succeed: %v", err)
	}
}

func TestEvidenceConstraints(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	const base = `INSERT INTO evidence (id, session_id, task_id, source_id, confidence, verification_state, collected_at) VALUES (?, ?, ?, ?, ?, ?, ?)`
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, base, "e1", "s1", "t1", "src1", 1.5, "UNVERIFIED", now); err == nil {
		t.Fatal("expected CHECK failure for confidence > 1")
	}
	if _, err := db.ExecContext(ctx, base, "e2", "s1", "t1", "src1", -0.1, "UNVERIFIED", now); err == nil {
		t.Fatal("expected CHECK failure for confidence < 0")
	}
	if _, err := db.ExecContext(ctx, base, "e3", "s1", "t1", "src1", 0.5, "BOGUS_VS", now); err == nil {
		t.Fatal("expected CHECK failure for invalid verification_state")
	}
	if _, err := db.ExecContext(ctx, base, "e4", "s1", "t1", "src1", 0.5, "UNVERIFIED", now); err != nil {
		t.Fatalf("valid evidence insert should succeed: %v", err)
	}
}

func TestSourceProfileConstraints(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	const base = `INSERT INTO source_profiles (domain, class, quality_score, auth_type) VALUES (?, ?, ?, ?)`
	if _, err := db.ExecContext(ctx, base, "bad", "NOT_A_CLASS", 0.3, "NONE"); err == nil {
		t.Fatal("expected CHECK failure for invalid SourceClass")
	}
	if _, err := db.ExecContext(ctx, base, "bad2", "OFFICIAL", 5.0, "NONE"); err == nil {
		t.Fatal("expected CHECK failure for quality_score > 1")
	}
	if _, err := db.ExecContext(ctx, base, "ok.example", "OFFICIAL", 0.3, "USER_AUTHORIZED"); err != nil {
		t.Fatalf("valid source_profile insert should succeed: %v", err)
	}
}

func TestRelationsPrimaryKeyAndKind(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	const base = `INSERT INTO relations (from_id, to_id, kind, strength) VALUES (?, ?, ?, ?)`
	if _, err := db.ExecContext(ctx, base, "e1", "e2", "CONTRADICTS", 0.8); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, base, "e1", "e2", "CONTRADICTS", 0.9); err == nil {
		t.Fatal("expected PRIMARY KEY violation for duplicate relation")
	}
	if _, err := db.ExecContext(ctx, base, "e1", "e2", "SUPPORTS", 0.9); err != nil {
		t.Fatalf("distinct kind should succeed: %v", err)
	}
	if _, err := db.ExecContext(ctx, base, "e1", "e2", "BOGUS_KIND", 0.9); err == nil {
		t.Fatal("expected CHECK failure for invalid EvidenceRelation kind")
	}
}

func TestMigration_V2_userIdColumn(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var col string
	err := db.QueryRowContext(ctx,
		`SELECT name FROM pragma_table_info('tasks') WHERE name = 'user_id'`,
	).Scan(&col)
	if err != nil {
		t.Fatalf("expected user_id column in tasks: %v", err)
	}
	if col != "user_id" {
		t.Errorf("column = %q, want user_id", col)
	}

	// Existing rows should get the default empty value.
	now := time.Now().UTC()
	const base = `INSERT INTO tasks (id, session_id, type, state, task_key, created_at) VALUES (?, ?, ?, ?, ?, ?)`
	if _, err := db.ExecContext(ctx, base, "t1", "s1", "DISCOVER", "READY", "k1", now); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	var userID string
	if err := db.QueryRowContext(ctx, `SELECT user_id FROM tasks WHERE id = 't1'`).Scan(&userID); err != nil {
		t.Fatalf("select user_id: %v", err)
	}
	if userID != "" {
		t.Errorf("user_id = %q, want empty default", userID)
	}
}

func TestMigration_V3_indexesCreated(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, idx := range []string{
		"idx_evidence_verification_state",
		"idx_events_session_id",
	} {
		var found string
		err := db.QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, idx,
		).Scan(&found)
		if err != nil {
			t.Errorf("index %q not found after migration: %v", idx, err)
		}
	}
}

func TestMigration_AppliesSequentially(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	if err := Apply(ctx, db); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 migration rows (V1+V2+V3), got %d", n)
	}
}

func TestOpenAppliesWALPragmasOnFileDSN(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "draw.db") + "?mode=rwc"
	db, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if max := db.Stats().MaxOpenConnections; max != 8 {
		t.Errorf("MaxOpenConnections = %d, want 8", max)
	}

	db.SetMaxOpenConns(1)

	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}

	var bt int
	if err := db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&bt); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if bt != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", bt)
	}
}

func TestOpenSkipsWALOnMemoryDSN(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open :memory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	db.SetMaxOpenConns(1)
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if mode == "wal" {
		t.Errorf(":memory: journal_mode should not be wal, got %q", mode)
	}
}
