package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jefflinse/rhizome"
)

// CheckpointStore is a SQLite-backed rhizome.CheckpointStore. It persists
// graph execution state per thread (thread_id = task id) so a run can resume
// from its last completed node after a crash or restart. Save/Load satisfy
// rhizome.CheckpointStore; Delete lets the executor drop a checkpoint once a
// task reaches a terminal status.
//
// It is a thin view over the same *sql.DB as the owning SQLiteStore, obtained
// via SQLiteStore.CheckpointStore(). Safe for concurrent use (database/sql
// handles connection pooling; WAL mode allows concurrent readers).
type CheckpointStore struct {
	db *sql.DB
}

// CheckpointStore returns a rhizome-compatible checkpoint store backed by this
// database. Returns a fresh lightweight view each call.
func (s *SQLiteStore) CheckpointStore() *CheckpointStore {
	return &CheckpointStore{db: s.db}
}

// Save records the checkpoint for threadID, overwriting any previous entry —
// rhizome keeps only the latest checkpoint per thread, so Load returns the
// most recently saved node.
func (c *CheckpointStore) Save(ctx context.Context, threadID, nodeName string, data []byte) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO graph_checkpoints (thread_id, node_name, data, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(thread_id) DO UPDATE SET
		     node_name  = excluded.node_name,
		     data       = excluded.data,
		     updated_at = excluded.updated_at`,
		threadID, nodeName, data, now)
	if err != nil {
		return fmt.Errorf("saving checkpoint for %q: %w", threadID, err)
	}
	return nil
}

// Load returns the most recent checkpoint for threadID, or
// rhizome.ErrNoCheckpoint if none exists.
func (c *CheckpointStore) Load(ctx context.Context, threadID string) (string, []byte, error) {
	var (
		nodeName string
		data     []byte
	)
	err := c.db.QueryRowContext(ctx,
		`SELECT node_name, data FROM graph_checkpoints WHERE thread_id = ?`,
		threadID).Scan(&nodeName, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, rhizome.ErrNoCheckpoint
	}
	if err != nil {
		return "", nil, fmt.Errorf("loading checkpoint for %q: %w", threadID, err)
	}
	return nodeName, data, nil
}

// Delete removes the checkpoint for threadID. It is a no-op (no error) when
// no checkpoint exists.
func (c *CheckpointStore) Delete(ctx context.Context, threadID string) error {
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM graph_checkpoints WHERE thread_id = ?`, threadID); err != nil {
		return fmt.Errorf("deleting checkpoint for %q: %w", threadID, err)
	}
	return nil
}
