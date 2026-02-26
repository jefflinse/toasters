-- 003_composition.sql: Three-layer composition model (Skills → Agents → Teams).
-- Definition tables (agents, teams, team_members) are blown away and rebuilt from
-- files on startup (Decision #35). Operational tables are preserved.

-- Drop old definition tables. Order matters for foreign keys.
DROP TABLE IF EXISTS team_members;
DROP TABLE IF EXISTS teams;
DROP TABLE IF EXISTS agents;

-- Recreate agents with superset columns.
CREATE TABLE agents (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    mode             TEXT NOT NULL DEFAULT 'worker',
    provider         TEXT NOT NULL DEFAULT '',
    model            TEXT NOT NULL DEFAULT '',
    temperature      REAL,
    system_prompt    TEXT NOT NULL DEFAULT '',
    tools            TEXT,            -- JSON array
    disallowed_tools TEXT,            -- JSON array
    skills           TEXT,            -- JSON array of skill name references
    permission_mode  TEXT NOT NULL DEFAULT '',
    permissions      TEXT,            -- JSON blob
    mcp_servers      TEXT,            -- JSON blob
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

-- Recreate teams with composition columns.
CREATE TABLE teams (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    lead_agent  TEXT NOT NULL DEFAULT '',
    skills      TEXT,            -- JSON array of team-wide skill names
    provider    TEXT NOT NULL DEFAULT '',
    model       TEXT NOT NULL DEFAULT '',
    culture     TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT '',
    source_path TEXT NOT NULL DEFAULT '',
    is_auto     INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- Skills table.
CREATE TABLE skills (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    tools       TEXT,            -- JSON array
    prompt      TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT '',
    source_path TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- Team-agent junction table (replaces team_members).
CREATE TABLE team_agents (
    team_id  TEXT NOT NULL REFERENCES teams(id),
    agent_id TEXT NOT NULL REFERENCES agents(id),
    role     TEXT NOT NULL DEFAULT 'worker',
    PRIMARY KEY (team_id, agent_id)
);

CREATE INDEX idx_team_agents_agent_id ON team_agents(agent_id);

-- Activity feed.
CREATE TABLE feed_entries (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id     TEXT NOT NULL DEFAULT '',
    entry_type TEXT NOT NULL,
    content    TEXT NOT NULL,
    metadata   TEXT,            -- JSON blob
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_feed_entries_job_id ON feed_entries(job_id);
CREATE INDEX idx_feed_entries_created_at ON feed_entries(created_at);

-- Add columns to tasks (preserve existing data).
ALTER TABLE tasks ADD COLUMN result_summary TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN recommendations TEXT NOT NULL DEFAULT '';
