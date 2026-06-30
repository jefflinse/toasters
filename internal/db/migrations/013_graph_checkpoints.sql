-- 013_graph_checkpoints.sql: Per-thread graph execution checkpoints.
--
-- rhizome persists graph state after every node via a CheckpointStore so a
-- run can resume from the last completed node after a crash or restart. This
-- table is the SQLite-backed store: one row per thread (thread_id = task id),
-- overwritten after each node so Load returns the most recent checkpoint.
--
-- Rows are deleted whenever a task reaches a terminal status (completed,
-- failed, cancelled), so a surviving checkpoint means the process died mid-run
-- without writing a terminal status — exactly the crash case recovery resumes.

CREATE TABLE IF NOT EXISTS graph_checkpoints (
    thread_id  TEXT PRIMARY KEY,
    node_name  TEXT NOT NULL,
    data       BLOB NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
