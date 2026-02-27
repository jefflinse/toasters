-- 004_job_status_setup.sql: Document new job status values for setup phase.
-- The jobs.status column is TEXT with no CHECK constraint (SQLite ALTER COLUMN
-- is not supported), so new values are valid immediately. Valid status values:
--   pending, setting_up, decomposing, active, paused, completed, failed, cancelled
SELECT 1; -- no-op; migration recorded to advance schema version
