-- Blocker history: every HITL prompt round (operator ask_user or graph-node
-- interrupt) is recorded when raised and updated when resolved, so past
-- blockers stay browsable in the Blockers modal after they're answered.

CREATE TABLE blockers (
    request_id TEXT PRIMARY KEY,
    source TEXT NOT NULL DEFAULT '',       -- '' = operator; "graph:<node>" = node interrupt
    job_id TEXT NOT NULL DEFAULT '',
    task_id TEXT NOT NULL DEFAULT '',
    questions TEXT NOT NULL DEFAULT '[]',  -- JSON array of {question, options}
    created_at TEXT NOT NULL,              -- RFC3339
    resolved_at TEXT NOT NULL DEFAULT '',  -- RFC3339; '' while pending
    disposition TEXT NOT NULL DEFAULT '',  -- '' pending | answered | dismissed | cancelled
    answer TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_blockers_created_at ON blockers(created_at);
