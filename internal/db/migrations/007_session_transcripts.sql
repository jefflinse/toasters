-- 007_session_transcripts.sql: Add session-level observability.
-- Captures system prompt, tools, and per-message transcripts for every LLM session.

-- Add system prompt and tools to worker_sessions for quick inspection.
ALTER TABLE worker_sessions ADD COLUMN system_prompt TEXT NOT NULL DEFAULT '';
ALTER TABLE worker_sessions ADD COLUMN tools_json TEXT NOT NULL DEFAULT '';

-- Per-message transcript for full session replay.
CREATE TABLE session_messages (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id   TEXT NOT NULL,
    seq          INTEGER NOT NULL,
    role         TEXT NOT NULL,
    content      TEXT NOT NULL DEFAULT '',
    tool_calls   TEXT NOT NULL DEFAULT '',
    tool_call_id TEXT NOT NULL DEFAULT '',
    created_at   DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_session_messages_session ON session_messages(session_id, seq);
