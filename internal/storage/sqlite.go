package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const DriverName = "sqlite"

const EnvSQLiteDSN = "DRAW_SQLITE_DSN"

func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open(DriverName, withBusyTimeoutPragma(dsn))
	if err != nil {
		return nil, fmt.Errorf("storage open: %w", err)
	}
	db.SetMaxOpenConns(8)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if !isInMemoryDSN(dsn) {
		if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
			slog.Warn("sqlite WAL unavailable", "dsn", dsn, "err", err)
		}
		if _, err := db.ExecContext(ctx, `PRAGMA synchronous=NORMAL`); err != nil {
			slog.Warn("sqlite synchronous=NORMAL unavailable", "dsn", dsn, "err", err)
		}
	}

	return db, nil
}

func isInMemoryDSN(dsn string) bool {
	return strings.Contains(dsn, ":memory:")
}

func withBusyTimeoutPragma(dsn string) string {
	if strings.Contains(dsn, "busy_timeout") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "_pragma=busy_timeout%3d5000"
}

type Migration struct {
	Version int64
	Name    string
	Up      string
}

func Apply(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at DATETIME NOT NULL)`); err != nil {
		return fmt.Errorf("storage: create schema_migrations: %w", err)
	}
	pending, err := pendingMigrations(ctx, db)
	if err != nil {
		return err
	}
	for _, m := range pending {
		if err := applyMigration(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, m Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin migration %d: %w", m.Version, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, m.Up); err != nil {
		return fmt.Errorf("storage: migration %d (%s): %w", m.Version, m.Name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, m.Version, time.Now().UTC()); err != nil {
		return fmt.Errorf("storage: record migration %d: %w", m.Version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit migration %d: %w", m.Version, err)
	}
	return nil
}

func pendingMigrations(ctx context.Context, db *sql.DB) ([]Migration, error) {
	applied := map[int64]bool{}
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("storage: list applied migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("storage: scan applied version: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: rows: %w", err)
	}
	var out []Migration
	for _, m := range migrations {
		if !applied[m.Version] {
			out = append(out, m)
		}
	}
	return out, nil
}
