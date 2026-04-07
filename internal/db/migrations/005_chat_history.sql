-- Chat history persistence: store the operator/user conversation so a server
-- restart doesn't lose context. The TUI hydrates from this table on connect
-- via Operator().History().

CREATE TABLE chat_entries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts TEXT NOT NULL,                  -- RFC3339 timestamp
    role TEXT NOT NULL,                -- "user" / "assistant" / "tool" / "system"
    content TEXT NOT NULL,
    reasoning TEXT NOT NULL DEFAULT '', -- chain-of-thought (assistant only)
    meta TEXT NOT NULL DEFAULT '',      -- byline / model name string
    turn_id TEXT NOT NULL DEFAULT ''    -- correlates with operator turn IDs
);

CREATE INDEX idx_chat_entries_ts ON chat_entries(ts);
