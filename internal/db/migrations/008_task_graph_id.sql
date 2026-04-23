-- 008_task_graph_id.sql: Add graph_id to tasks for the declarative-graph
-- dispatch path. Empty string means "no explicit graph" — callers fall back
-- to team-based dispatch. Both paths coexist while the team vocabulary is
-- being retired.

ALTER TABLE tasks ADD COLUMN graph_id TEXT NOT NULL DEFAULT '';
