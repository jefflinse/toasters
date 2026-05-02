-- 011_drop_workers.sql: Retire the workers definition table. Workers were a
-- vestigial holdover from the team era (see 009_drop_teams.sql). Nothing
-- writes to this table since teams were removed; dispatch now flows through
-- declarative graphs and roles. The operational tables (worker_sessions,
-- tasks.worker_id, progress_reports.worker_id) are unaffected — those
-- continue to track per-execution state.

DROP TABLE IF EXISTS workers;
