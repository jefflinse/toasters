-- 002_phase3_jobs.sql: Add description and workspace_dir to jobs, team_id to tasks.

ALTER TABLE jobs ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN workspace_dir TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN team_id TEXT NOT NULL DEFAULT '';
