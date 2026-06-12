-- 012_task_description.sql: Give tasks a dedicated description column.
--
-- The per-task description produced by coarse-decompose was stored in
-- `summary`, but summary doubles as the latest status/result text: status
-- updates overwrite it and RetryTask clears it. Worse, dispatch never
-- forwarded it at all — graph nodes only ever saw the task title and had to
-- ask the user for details the system already had. The description is the
-- task's contract and must be immutable for the task's lifetime.
--
-- Backfill from summary for pending tasks only: those haven't run yet, so
-- their summary still holds the original description rather than a status
-- or failure message.

ALTER TABLE tasks ADD COLUMN description TEXT NOT NULL DEFAULT '';

UPDATE tasks SET description = summary WHERE status = 'pending' AND summary != '';
