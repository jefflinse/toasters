-- 006_rename_agent_to_worker.sql: Rename agent → worker throughout the schema.

-- Operational tables: rename in-place.
ALTER TABLE agent_sessions RENAME TO worker_sessions;
ALTER TABLE worker_sessions RENAME COLUMN agent_id TO worker_id;

ALTER TABLE tasks RENAME COLUMN agent_id TO worker_id;
ALTER TABLE progress_reports RENAME COLUMN agent_id TO worker_id;

-- teams: rename lead_agent column
ALTER TABLE teams RENAME COLUMN lead_agent TO lead_worker;

-- Definition tables are rebuilt every startup, so drop and recreate.
DROP TABLE IF EXISTS team_agents;
DROP TABLE IF EXISTS agents;

CREATE TABLE workers (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    mode             TEXT NOT NULL DEFAULT 'worker',
    provider         TEXT NOT NULL DEFAULT '',
    model            TEXT NOT NULL DEFAULT '',
    temperature      REAL,
    system_prompt    TEXT NOT NULL DEFAULT '',
    tools            TEXT,
    disallowed_tools TEXT,
    skills           TEXT,
    permission_mode  TEXT NOT NULL DEFAULT '',
    permissions      TEXT,
    mcp_servers      TEXT,
    max_turns        INTEGER,
    color            TEXT NOT NULL DEFAULT '',
    hidden           INTEGER NOT NULL DEFAULT 0,
    disabled         INTEGER NOT NULL DEFAULT 0,
    source           TEXT NOT NULL DEFAULT '',
    source_path      TEXT NOT NULL DEFAULT '',
    team_id          TEXT NOT NULL DEFAULT '',
    created_at       DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at       DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE team_workers (
    team_id   TEXT NOT NULL REFERENCES teams(id),
    worker_id TEXT NOT NULL REFERENCES workers(id),
    role      TEXT NOT NULL DEFAULT 'worker',
    PRIMARY KEY (team_id, worker_id)
);

CREATE INDEX idx_team_workers_worker_id ON team_workers(worker_id);
