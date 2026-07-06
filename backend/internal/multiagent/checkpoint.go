// Package multiagent — checkpoint.go implements an Eino
// compose.CheckPointStore backed by SQLite. This is the
// second key differentiator vs gpt-researcher, whose
// LangGraph MemorySaver is imported-but-never-wired
// (orchestrator.py:115 calls `chain.compile()` with no
// checkpointer).
//
// Our implementation:
//
//  1. Stores each checkpoint as a single BLOB row keyed by
//     task_id. Eino passes []byte via Set() and expects
//     []byte from Get().
//  2. Uses the same SQLite database as the report store
//     (data/z-research.db), adding one table
//     (multiagent_checkpoints). This keeps the deployment
//     story single-DB, single-binary.
//  3. The table schema uses "upsert" semantics: Set()
//     replaces the existing row for a given task_id, so
//     each run only ever has one live checkpoint per task.
//     Old checkpoints can be garbage-collected by the
//     caller (e.g. a TTL sweep) — not implemented here.
//
// To enable checkpointing in a graph, pass
// compose.WithCheckPointStore(store) to g.Compile(...). To
// resume, pass compose.WithCheckPointID(taskID) and
// compose.WithCheckPointStore(store) as Invoke options.
//
// Caveat: the Eino checkpoint BLOB format is internal and
// versioned. We do not attempt to interpret it; we just
// persist and return it verbatim.
package multiagent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/compose"

	"z-research/backend/internal/store"
)

// SQLiteCheckPointStore is a thread-safe compose.CheckPointStore
// backed by the project's SQLite database. Construct with
// NewSQLiteCheckPointStore; share one instance across all
// multi-agent runs (it is safe for concurrent use).
type SQLiteCheckPointStore struct {
	st store.Store
	mu sync.Mutex // serializes SQL operations; modernc.org/sqlite is goroutine-safe but we serialize for clarity
	db *sql.DB    // optional: when st does not expose its *sql.DB, this is nil and we fall back to a per-table approach
}

// NewSQLiteCheckPointStore wires the checkpoint store to
// the project SQLite DB. It creates the
// multiagent_checkpoints table on first call.
func NewSQLiteCheckPointStore(ctx context.Context, st store.Store) (*SQLiteCheckPointStore, error) {
	if st == nil {
		return nil, errors.New("nil store")
	}
	cs := &SQLiteCheckPointStore{st: st}
	if err := cs.ensureTable(ctx); err != nil {
		return nil, err
	}
	return cs, nil
}

// Get returns the checkpoint blob for checkPointID, or
// (nil, false, nil) if not found. Errors are returned only
// for I/O failures.
func (c *SQLiteCheckPointStore) Get(ctx context.Context, checkPointID string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	db, err := c.openDB(ctx)
	if err != nil {
		return nil, false, err
	}
	var blob []byte
	row := db.QueryRowContext(ctx,
		"SELECT blob FROM multiagent_checkpoints WHERE task_id = ?",
		checkPointID,
	)
	if err := row.Scan(&blob); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("checkpoint get: %w", err)
	}
	return blob, true, nil
}

// Set upserts the checkpoint blob for checkPointID. The
// previous value (if any) is replaced.
func (c *SQLiteCheckPointStore) Set(ctx context.Context, checkPointID string, blob []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	db, err := c.openDB(ctx)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO multiagent_checkpoints (task_id, blob, updated_at)
		VALUES (?, ?, strftime('%s','now'))
		ON CONFLICT(task_id) DO UPDATE SET
			blob = excluded.blob,
			updated_at = excluded.updated_at
	`, checkPointID, blob)
	if err != nil {
		return fmt.Errorf("checkpoint set: %w", err)
	}
	return nil
}

// ensureTable creates the multiagent_checkpoints table if
// it does not exist. Safe to call repeatedly.
func (c *SQLiteCheckPointStore) ensureTable(ctx context.Context) error {
	db, err := c.openDB(ctx)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS multiagent_checkpoints (
			task_id    TEXT PRIMARY KEY,
			blob       BLOB NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("create multiagent_checkpoints: %w", err)
	}
	return nil
}

// openDB returns a *sql.DB. If the underlying store
// implements an OpenDB method (only the real SQLite store
// does), use it; otherwise return an error.
//
// The store.Store interface does not expose *sql.DB
// directly (intentional encapsulation), so we keep this
// minimal and only support the SQLiteStore concrete type.
// If you need to plug in Postgres, extend this method.
func (c *SQLiteCheckPointStore) openDB(ctx context.Context) (*sql.DB, error) {
	type sqlDBProvider interface {
		DB() *sql.DB
	}
	if p, ok := c.st.(sqlDBProvider); ok {
		return p.DB(), nil
	}
	return nil, fmt.Errorf("checkpoint store: underlying store does not expose *sql.DB (only SQLiteStore is supported; got %T)", c.st)
}

// Compile-time assertion: SQLiteCheckPointStore implements
// the Eino compose.CheckPointStore interface.
var _ compose.CheckPointStore = (*SQLiteCheckPointStore)(nil)
