-- Per-node execution metrics: one row per logical graph-node execution.
-- Rhizome retries of the same node collapse into the single row the outer
-- (Event/Persistence) middleware call produces, so this table stays a
-- ground truth for "how many times did this node actually run" — not a
-- per-attempt log. Foundation for future auto-tuning; aggregated by
-- internal/db NodeExecutionStats.
CREATE TABLE node_executions (
    id         TEXT PRIMARY KEY,
    job_id     TEXT NOT NULL,
    task_id    TEXT NOT NULL,
    graph_id   TEXT NOT NULL DEFAULT '',
    node       TEXT NOT NULL,
    status     TEXT NOT NULL,
    elapsed_ms INTEGER NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX idx_node_executions_node ON node_executions(node);
CREATE INDEX idx_node_executions_job_id ON node_executions(job_id);
