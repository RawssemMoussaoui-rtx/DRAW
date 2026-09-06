package evalharness

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"sync/atomic"
	"time"

	"modernc.org/sqlite"
)

// PerfMetrics captures per-scenario performance / resource measurements taken
// around the execution of a scenario (see RunV1OnScenario). It is produced
// entirely within this package: the database query counter is obtained by
// opening the in-memory SQLite database through a counting driver wrapper
// (see openCountingDB) instead of going through storage.Open, so NO production
// code outside internal/evalharness is inspected, modified, or instrumented.
type PerfMetrics struct {
	// Elapsed is wall-clock time spent inside RunV1OnScenario.
	Elapsed time.Duration
	// AllocDelta is the delta of runtime.MemStats.TotalAlloc (bytes allocated
	// during the scenario) measured before/after the run.
	AllocDelta uint64
	// LiveHeap is runtime.MemStats.Alloc (bytes) immediately after the run.
	LiveHeap uint64
	// Sys is runtime.MemStats.Sys (bytes) immediately after the run.
	Sys uint64
	// DBQueries is the number of Exec/Query statements dispatched against the
	// SQLite store during the scenario (migrations/setup excluded).
	DBQueries int64
}

// countingConnector wraps a driver.Connector so that every *driver.Conn it
// produces is a countingConn that increments a shared atomic counter for each
// statement executed via the ExecerContext / QueryerContext fast paths.
//
// This is the standard "interpose on physical connections" pattern documented
// by the modernc.org/sqlite package (NewConnector's doc comment). It lets us
// count DB queries for a scenario while reusing the unmodified
// internal/storage and internal/model packages: the concrete SQLite stores
// still receive a plain *sql.DB and call ExecContext/QueryContext/BeginTx as
// before; those calls simply flow through our counting connection first.
type countingConnector struct {
	driver.Connector
	counter *int64
}

func (c *countingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: conn, counter: c.counter}, nil
}

// countingConn delegates every driver method to the underlying connection and
// transparently counts statements that go through the ExecerContext /
// QueryerContext paths (the path modernc.org/sqlite and database/sql use for
// every ExecContext/QueryContext/BeginTx-then-Exec call). Optional interfaces
// are delegated faithfully so that connection-pool behaviour (Ping,
// ResetSession, IsValid, BeginTx, PrepareContext) is preserved bit-for-bit.
type countingConn struct {
	driver.Conn
	counter *int64
}

// ExecContext implements driver.ExecerContext.
//
// If the underlying driver supports direct execution it is delegated to and
// the statement is counted. Otherwise driver.ErrSkip is returned so that
// database/sql falls back to the Prepare path (and the statement is counted
// there instead). Because database/sql uses either the direct-exec path OR the
// prepare path for any given statement (never both), there is no double
// counting.
func (c *countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if ec, ok := c.Conn.(driver.ExecerContext); ok {
		atomic.AddInt64(c.counter, 1)
		return ec.ExecContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if qc, ok := c.Conn.(driver.QueryerContext); ok {
		atomic.AddInt64(c.counter, 1)
		return qc.QueryContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}

func (c *countingConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *countingConn) ResetSession(ctx context.Context) error {
	if r, ok := c.Conn.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *countingConn) IsValid() bool {
	if v, ok := c.Conn.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if bt, ok := c.Conn.(driver.ConnBeginTx); ok {
		return bt.BeginTx(ctx, opts)
	}
	return c.Conn.Begin()
}

func (c *countingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, query)
	}
	return c.Conn.Prepare(query)
}

// openCountingDB opens a SQLite database through the counting driver wrapper.
// It mirrors storage.Open's DSN handling (notably the busy_timeout pragma) so
// that behaviour — and therefore the deterministic M1-M7 semantics — is
// identical to the production open path, with the only addition being that the
// returned counter records Exec/Query statements. MaxOpenConns is left to the
// caller (the harness pins it to 1 for in-memory sharing, exactly as before).
func openCountingDB(dsn string) (*sql.DB, *int64, error) {
	var counter int64
	dsn = countingBusyDSN(dsn)
	base, err := sqlite.NewConnector(dsn)
	if err != nil {
		return nil, nil, err
	}
	db := sql.OpenDB(&countingConnector{Connector: base, counter: &counter})
	return db, &counter, nil
}

// countingBusyDSN replicates storage.withBusyTimeoutPragma so the counting DB
// receives the exact DSN SQLite would have from storage.Open(:memory:).
func countingBusyDSN(dsn string) string {
	if strings.Contains(dsn, "busy_timeout") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "_pragma=busy_timeout%3d5000"
}
