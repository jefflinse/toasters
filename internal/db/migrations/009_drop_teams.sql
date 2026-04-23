-- 009_drop_teams.sql: Retire the teams concept. Task dispatch now routes
-- through declarative graphs exclusively (Phase 4). Workers stay (vestigial
-- for now — only populated from team definitions, which are gone) but lose
-- their team_id column.

DROP TABLE IF EXISTS team_workers;
DROP TABLE IF EXISTS teams;
ALTER TABLE tasks DROP COLUMN team_id;
ALTER TABLE workers DROP COLUMN team_id;
