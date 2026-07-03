-- Historical context-window occupancy per worker session: context_tokens is
-- the prompt size of the session's final model round-trip; context_window is
-- the resolved context window for its provider/model at completion time.
-- Both default to 0, meaning "unavailable" (usage not reported, or the
-- window could not be resolved) rather than a real empty context — see the
-- tokens==0 handling in the metrics aggregation (SessionStats).
ALTER TABLE worker_sessions ADD COLUMN context_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE worker_sessions ADD COLUMN context_window INTEGER NOT NULL DEFAULT 0;
