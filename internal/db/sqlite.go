package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // Pure-Go SQLite driver.
)

// SQLiteStore implements Store backed by a SQLite database.
type SQLiteStore struct {
	db *sql.DB
}

// Compile-time check that SQLiteStore implements Store.
var _ Store = (*SQLiteStore)(nil)

// Open creates or opens a SQLite database at the given path.
// It creates parent directories if needed, enables WAL mode and foreign keys,
// and runs any pending migrations.
func Open(path string) (*SQLiteStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating database directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// SQLite is single-writer; pin to one connection so PRAGMAs apply consistently.
	// Without this, database/sql's connection pool may open new connections that
	// don't inherit per-connection PRAGMAs like foreign_keys=ON.
	db.SetMaxOpenConns(1)

	// Set PRAGMAs for performance and correctness.
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close() //nolint:errcheck
			return nil, fmt.Errorf("setting pragma %q: %w", p, err)
		}
	}

	if err := migrate(db); err != nil {
		db.Close() //nolint:errcheck
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// --- Jobs ---

func (s *SQLiteStore) CreateJob(ctx context.Context, job *Job) error {
	now := time.Now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	if job.UpdatedAt.IsZero() {
		job.UpdatedAt = now
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO jobs (id, title, description, type, status, workspace_dir, created_at, updated_at, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Title, job.Description, job.Type, string(job.Status),
		job.WorkspaceDir,
		job.CreatedAt.Format(time.RFC3339), job.UpdatedAt.Format(time.RFC3339),
		nullableJSON(job.Metadata),
	)
	if err != nil {
		return fmt.Errorf("inserting job: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetJob(ctx context.Context, id string) (*Job, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, title, description, type, status, workspace_dir, created_at, updated_at, metadata
		 FROM jobs WHERE id = ?`, id)

	j := &Job{}
	var status string
	var createdAt, updatedAt string
	var metadata sql.NullString

	if err := row.Scan(&j.ID, &j.Title, &j.Description, &j.Type, &status,
		&j.WorkspaceDir, &createdAt, &updatedAt, &metadata); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("job %q: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("scanning job: %w", err)
	}

	j.Status = JobStatus(status)
	j.CreatedAt = parseTime(createdAt)
	j.UpdatedAt = parseTime(updatedAt)
	if metadata.Valid {
		j.Metadata = json.RawMessage(metadata.String)
	}
	return j, nil
}

func (s *SQLiteStore) ListJobs(ctx context.Context, filter JobFilter) ([]*Job, error) {
	query := "SELECT id, title, description, type, status, workspace_dir, created_at, updated_at, metadata FROM jobs"
	var args []any
	var conditions []string

	if filter.Status != nil {
		conditions = append(conditions, "status = ?")
		args = append(args, string(*filter.Status))
	}
	if filter.Type != nil {
		conditions = append(conditions, "type = ?")
		args = append(args, *filter.Type)
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	query += " ORDER BY created_at DESC"

	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	if filter.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, filter.Offset)
	}

	return s.queryJobs(ctx, query, args...)
}

func (s *SQLiteStore) ListAllJobs(ctx context.Context) ([]*Job, error) {
	return s.queryJobs(ctx,
		"SELECT id, title, description, type, status, workspace_dir, created_at, updated_at, metadata FROM jobs ORDER BY created_at DESC")
}

// queryJobs executes a query and scans the results into Job structs.
func (s *SQLiteStore) queryJobs(ctx context.Context, query string, args ...any) ([]*Job, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing jobs: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var jobs []*Job
	for rows.Next() {
		j := &Job{}
		var status string
		var createdAt, updatedAt string
		var metadata sql.NullString

		if err := rows.Scan(&j.ID, &j.Title, &j.Description, &j.Type, &status,
			&j.WorkspaceDir, &createdAt, &updatedAt, &metadata); err != nil {
			return nil, fmt.Errorf("scanning job row: %w", err)
		}

		j.Status = JobStatus(status)
		j.CreatedAt = parseTime(createdAt)
		j.UpdatedAt = parseTime(updatedAt)
		if metadata.Valid {
			j.Metadata = json.RawMessage(metadata.String)
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (s *SQLiteStore) UpdateJob(ctx context.Context, id string, update JobUpdate) error {
	var sets []string
	var args []any

	if update.Title != nil {
		sets = append(sets, "title = ?")
		args = append(args, *update.Title)
	}
	if update.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, *update.Description)
	}
	if update.Status != nil {
		sets = append(sets, "status = ?")
		args = append(args, string(*update.Status))
	}
	if update.WorkspaceDir != nil {
		sets = append(sets, "workspace_dir = ?")
		args = append(args, *update.WorkspaceDir)
	}

	if len(sets) == 0 {
		return nil // nothing to update
	}

	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().UTC().Format(time.RFC3339))

	args = append(args, id)
	query := "UPDATE jobs SET " + strings.Join(sets, ", ") + " WHERE id = ?"

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating job: %w", err)
	}
	return checkRowsAffected(result, "job", id)
}

func (s *SQLiteStore) UpdateJobStatus(ctx context.Context, id string, status JobStatus) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx,
		"UPDATE jobs SET status = ?, updated_at = ? WHERE id = ?",
		string(status), now, id)
	if err != nil {
		return fmt.Errorf("updating job status: %w", err)
	}
	return checkRowsAffected(result, "job", id)
}

// --- Tasks ---

func (s *SQLiteStore) CreateTask(ctx context.Context, task *Task) error {
	now := time.Now().UTC()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = now
	}
	if task.UpdatedAt.IsZero() {
		task.UpdatedAt = now
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tasks (id, job_id, title, status, agent_id, team_id, parent_id, sort_order,
		                     created_at, updated_at, summary, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.JobID, task.Title, string(task.Status),
		task.AgentID, task.TeamID, task.ParentID, task.SortOrder,
		task.CreatedAt.Format(time.RFC3339), task.UpdatedAt.Format(time.RFC3339),
		task.Summary, nullableJSON(task.Metadata),
	)
	if err != nil {
		return fmt.Errorf("inserting task: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetTask(ctx context.Context, id string) (*Task, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, job_id, title, status, agent_id, team_id, parent_id, sort_order,
		        created_at, updated_at, summary, metadata, result_summary, recommendations
		 FROM tasks WHERE id = ?`, id)

	return scanTask(row)
}

func (s *SQLiteStore) ListTasksForJob(ctx context.Context, jobID string) ([]*Task, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, job_id, title, status, agent_id, team_id, parent_id, sort_order,
		        created_at, updated_at, summary, metadata, result_summary, recommendations
		 FROM tasks WHERE job_id = ? ORDER BY sort_order, created_at`, jobID)
	if err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var tasks []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s *SQLiteStore) UpdateTaskStatus(ctx context.Context, id string, status TaskStatus, summary string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx,
		"UPDATE tasks SET status = ?, summary = ?, updated_at = ? WHERE id = ?",
		string(status), summary, now, id)
	if err != nil {
		return fmt.Errorf("updating task status: %w", err)
	}
	return checkRowsAffected(result, "task", id)
}

func (s *SQLiteStore) UpdateTaskResult(ctx context.Context, id string, resultSummary, recommendations string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx,
		"UPDATE tasks SET result_summary = ?, recommendations = ?, updated_at = ? WHERE id = ?",
		resultSummary, recommendations, now, id)
	if err != nil {
		return fmt.Errorf("updating task result: %w", err)
	}
	return checkRowsAffected(result, "task", id)
}

func (s *SQLiteStore) AssignTask(ctx context.Context, id string, teamID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx,
		"UPDATE tasks SET team_id = ?, status = ?, updated_at = ? WHERE id = ? AND status = ?",
		teamID, string(TaskStatusInProgress), now, id, string(TaskStatusPending))
	if err != nil {
		return fmt.Errorf("assigning task: %w", err)
	}
	return checkRowsAffected(result, "task", id)
}

func (s *SQLiteStore) AddTaskDependency(ctx context.Context, taskID, dependsOn string) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO task_dependencies (task_id, depends_on) VALUES (?, ?)",
		taskID, dependsOn)
	if err != nil {
		return fmt.Errorf("adding task dependency: %w", err)
	}
	return nil
}

// GetReadyTasks returns tasks that are pending and have all dependencies completed.
func (s *SQLiteStore) GetReadyTasks(ctx context.Context, jobID string) ([]*Task, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.id, t.job_id, t.title, t.status, t.agent_id, t.team_id, t.parent_id, t.sort_order,
		        t.created_at, t.updated_at, t.summary, t.metadata, t.result_summary, t.recommendations
		 FROM tasks t
		 WHERE t.job_id = ?
		   AND t.status = 'pending'
		   AND NOT EXISTS (
		       SELECT 1 FROM task_dependencies td
		       JOIN tasks dep ON dep.id = td.depends_on
		       WHERE td.task_id = t.id AND dep.status != 'completed'
		   )
		 ORDER BY t.sort_order, t.created_at`, jobID)
	if err != nil {
		return nil, fmt.Errorf("getting ready tasks: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var tasks []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// --- Progress ---

func (s *SQLiteStore) ReportProgress(ctx context.Context, report *ProgressReport) error {
	now := time.Now().UTC()
	if report.CreatedAt.IsZero() {
		report.CreatedAt = now
	}

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO progress_reports (job_id, task_id, agent_id, status, message, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		report.JobID, report.TaskID, report.AgentID,
		report.Status, report.Message,
		report.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("inserting progress report: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("getting last insert id: %w", err)
	}
	report.ID = id
	return nil
}

func (s *SQLiteStore) GetRecentProgress(ctx context.Context, jobID string, limit int) ([]*ProgressReport, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, job_id, task_id, agent_id, status, message, created_at
		 FROM progress_reports
		 WHERE job_id = ?
		 ORDER BY created_at DESC
		 LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing progress reports: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var reports []*ProgressReport
	for rows.Next() {
		r := &ProgressReport{}
		var createdAt string
		if err := rows.Scan(&r.ID, &r.JobID, &r.TaskID, &r.AgentID,
			&r.Status, &r.Message, &createdAt); err != nil {
			return nil, fmt.Errorf("scanning progress report: %w", err)
		}
		r.CreatedAt = parseTime(createdAt)
		reports = append(reports, r)
	}
	return reports, rows.Err()
}

// --- Skills ---

func (s *SQLiteStore) UpsertSkill(ctx context.Context, skill *Skill) error {
	now := time.Now().UTC()
	if skill.CreatedAt.IsZero() {
		skill.CreatedAt = now
	}
	if skill.UpdatedAt.IsZero() {
		skill.UpdatedAt = now
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO skills (id, name, description, tools, prompt, source, source_path,
		                      created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		     name = excluded.name,
		     description = excluded.description,
		     tools = excluded.tools,
		     prompt = excluded.prompt,
		     source = excluded.source,
		     source_path = excluded.source_path,
		     updated_at = excluded.updated_at`,
		skill.ID, skill.Name, skill.Description, nullableJSON(skill.Tools),
		skill.Prompt, skill.Source, skill.SourcePath,
		skill.CreatedAt.Format(time.RFC3339), skill.UpdatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("upserting skill: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetSkill(ctx context.Context, id string) (*Skill, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, tools, prompt, source, source_path,
		        created_at, updated_at
		 FROM skills WHERE id = ?`, id)

	return scanSkill(row)
}

func (s *SQLiteStore) ListSkills(ctx context.Context) ([]*Skill, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, tools, prompt, source, source_path,
		        created_at, updated_at
		 FROM skills ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing skills: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var skills []*Skill
	for rows.Next() {
		sk, err := scanSkill(rows)
		if err != nil {
			return nil, err
		}
		skills = append(skills, sk)
	}
	return skills, rows.Err()
}

func (s *SQLiteStore) DeleteAllSkills(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM skills")
	if err != nil {
		return fmt.Errorf("deleting all skills: %w", err)
	}
	return nil
}

// --- Agents ---

func (s *SQLiteStore) UpsertAgent(ctx context.Context, agent *Agent) error {
	now := time.Now().UTC()
	if agent.CreatedAt.IsZero() {
		agent.CreatedAt = now
	}
	if agent.UpdatedAt.IsZero() {
		agent.UpdatedAt = now
	}

	var hidden, disabled int
	if agent.Hidden {
		hidden = 1
	}
	if agent.Disabled {
		disabled = 1
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agents (id, name, description, mode, model, provider, temperature,
		                      system_prompt, tools, disallowed_tools, skills,
		                      permission_mode, permissions, mcp_servers, max_turns,
		                      color, hidden, disabled, source, source_path, team_id,
		                      created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		     name = excluded.name,
		     description = excluded.description,
		     mode = excluded.mode,
		     model = excluded.model,
		     provider = excluded.provider,
		     temperature = excluded.temperature,
		     system_prompt = excluded.system_prompt,
		     tools = excluded.tools,
		     disallowed_tools = excluded.disallowed_tools,
		     skills = excluded.skills,
		     permission_mode = excluded.permission_mode,
		     permissions = excluded.permissions,
		     mcp_servers = excluded.mcp_servers,
		     max_turns = excluded.max_turns,
		     color = excluded.color,
		     hidden = excluded.hidden,
		     disabled = excluded.disabled,
		     source = excluded.source,
		     source_path = excluded.source_path,
		     team_id = excluded.team_id,
		     updated_at = excluded.updated_at`,
		agent.ID, agent.Name, agent.Description, agent.Mode,
		agent.Model, agent.Provider, agent.Temperature,
		agent.SystemPrompt, nullableJSON(agent.Tools),
		nullableJSON(agent.DisallowedTools), nullableJSON(agent.Skills),
		agent.PermissionMode, nullableJSON(agent.Permissions),
		nullableJSON(agent.MCPServers), agent.MaxTurns,
		agent.Color, hidden, disabled,
		agent.Source, agent.SourcePath, agent.TeamID,
		agent.CreatedAt.Format(time.RFC3339), agent.UpdatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("upserting agent: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetAgent(ctx context.Context, id string) (*Agent, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, mode, model, provider, temperature,
		        system_prompt, tools, disallowed_tools, skills,
		        permission_mode, permissions, mcp_servers, max_turns,
		        color, hidden, disabled, source, source_path, team_id,
		        created_at, updated_at
		 FROM agents WHERE id = ?`, id)

	a, err := scanAgent(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("agent %q: %w", id, ErrNotFound)
		}
		return nil, err
	}
	return a, nil
}

func (s *SQLiteStore) ListAgents(ctx context.Context) ([]*Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, mode, model, provider, temperature,
		        system_prompt, tools, disallowed_tools, skills,
		        permission_mode, permissions, mcp_servers, max_turns,
		        color, hidden, disabled, source, source_path, team_id,
		        created_at, updated_at
		 FROM agents ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var agents []*Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

func (s *SQLiteStore) DeleteAllAgents(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM agents")
	if err != nil {
		return fmt.Errorf("deleting all agents: %w", err)
	}
	return nil
}

// --- Teams ---

func (s *SQLiteStore) UpsertTeam(ctx context.Context, team *Team) error {
	now := time.Now().UTC()
	if team.CreatedAt.IsZero() {
		team.CreatedAt = now
	}
	if team.UpdatedAt.IsZero() {
		team.UpdatedAt = now
	}

	var isAuto int
	if team.IsAuto {
		isAuto = 1
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO teams (id, name, description, lead_agent, skills, provider, model,
		                     culture, source, source_path, is_auto, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		     name = excluded.name,
		     description = excluded.description,
		     lead_agent = excluded.lead_agent,
		     skills = excluded.skills,
		     provider = excluded.provider,
		     model = excluded.model,
		     culture = excluded.culture,
		     source = excluded.source,
		     source_path = excluded.source_path,
		     is_auto = excluded.is_auto,
		     updated_at = excluded.updated_at`,
		team.ID, team.Name, team.Description, team.LeadAgent,
		nullableJSON(team.Skills), team.Provider, team.Model,
		team.Culture, team.Source, team.SourcePath, isAuto,
		team.CreatedAt.Format(time.RFC3339), team.UpdatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("upserting team: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetTeam(ctx context.Context, id string) (*Team, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, lead_agent, skills, provider, model,
		        culture, source, source_path, is_auto, created_at, updated_at
		 FROM teams WHERE id = ?`, id)

	t, err := scanTeam(row)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("team %q: %w", id, ErrNotFound)
		}
		return nil, err
	}
	return t, nil
}

func (s *SQLiteStore) ListTeams(ctx context.Context) ([]*Team, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, lead_agent, skills, provider, model,
		        culture, source, source_path, is_auto, created_at, updated_at
		 FROM teams ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing teams: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var teams []*Team
	for rows.Next() {
		t, err := scanTeam(rows)
		if err != nil {
			return nil, err
		}
		teams = append(teams, t)
	}
	return teams, rows.Err()
}

func (s *SQLiteStore) DeleteAllTeams(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM teams")
	if err != nil {
		return fmt.Errorf("deleting all teams: %w", err)
	}
	return nil
}

// --- Team Agents ---

func (s *SQLiteStore) AddTeamAgent(ctx context.Context, ta *TeamAgent) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO team_agents (team_id, agent_id, role) VALUES (?, ?, ?)",
		ta.TeamID, ta.AgentID, ta.Role)
	if err != nil {
		return fmt.Errorf("adding team agent: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ListTeamAgents(ctx context.Context, teamID string) ([]*TeamAgent, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT team_id, agent_id, role FROM team_agents WHERE team_id = ? ORDER BY role, agent_id",
		teamID)
	if err != nil {
		return nil, fmt.Errorf("listing team agents: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var teamAgents []*TeamAgent
	for rows.Next() {
		ta := &TeamAgent{}
		if err := rows.Scan(&ta.TeamID, &ta.AgentID, &ta.Role); err != nil {
			return nil, fmt.Errorf("scanning team agent: %w", err)
		}
		teamAgents = append(teamAgents, ta)
	}
	return teamAgents, rows.Err()
}

func (s *SQLiteStore) DeleteAllTeamAgents(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM team_agents")
	if err != nil {
		return fmt.Errorf("deleting all team agents: %w", err)
	}
	return nil
}

// --- Feed ---

func (s *SQLiteStore) CreateFeedEntry(ctx context.Context, entry *FeedEntry) error {
	now := time.Now().UTC()
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO feed_entries (job_id, entry_type, content, metadata, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		entry.JobID, string(entry.EntryType), entry.Content,
		nullableJSON(entry.Metadata),
		entry.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("inserting feed entry: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("getting last insert id: %w", err)
	}
	entry.ID = id
	return nil
}

func (s *SQLiteStore) ListFeedEntries(ctx context.Context, jobID string, limit int) ([]*FeedEntry, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, job_id, entry_type, content, metadata, created_at
		 FROM feed_entries
		 WHERE job_id = ?
		 ORDER BY created_at DESC
		 LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing feed entries: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	return scanFeedEntries(rows)
}

func (s *SQLiteStore) ListRecentFeedEntries(ctx context.Context, limit int) ([]*FeedEntry, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, job_id, entry_type, content, metadata, created_at
		 FROM feed_entries
		 ORDER BY created_at DESC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing recent feed entries: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	return scanFeedEntries(rows)
}

// --- Rebuild ---

func (s *SQLiteStore) RebuildDefinitions(ctx context.Context, skills []*Skill, agents []*Agent, teams []*Team, teamAgents []*TeamAgent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning rebuild transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Delete in dependency order: team_agents first (references teams and agents).
	for _, table := range []string{"team_agents", "agents", "teams", "skills"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return fmt.Errorf("clearing %s: %w", table, err)
		}
	}

	// Insert skills.
	for _, sk := range skills {
		now := time.Now().UTC()
		if sk.CreatedAt.IsZero() {
			sk.CreatedAt = now
		}
		if sk.UpdatedAt.IsZero() {
			sk.UpdatedAt = now
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO skills (id, name, description, tools, prompt, source, source_path,
			                      created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sk.ID, sk.Name, sk.Description, nullableJSON(sk.Tools),
			sk.Prompt, sk.Source, sk.SourcePath,
			sk.CreatedAt.Format(time.RFC3339), sk.UpdatedAt.Format(time.RFC3339),
		); err != nil {
			return fmt.Errorf("inserting skill %q: %w", sk.ID, err)
		}
	}

	// Insert agents.
	for _, a := range agents {
		now := time.Now().UTC()
		if a.CreatedAt.IsZero() {
			a.CreatedAt = now
		}
		if a.UpdatedAt.IsZero() {
			a.UpdatedAt = now
		}
		var hidden, disabled int
		if a.Hidden {
			hidden = 1
		}
		if a.Disabled {
			disabled = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agents (id, name, description, mode, model, provider, temperature,
			                      system_prompt, tools, disallowed_tools, skills,
			                      permission_mode, permissions, mcp_servers, max_turns,
			                      color, hidden, disabled, source, source_path, team_id,
			                      created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, a.Name, a.Description, a.Mode,
			a.Model, a.Provider, a.Temperature,
			a.SystemPrompt, nullableJSON(a.Tools),
			nullableJSON(a.DisallowedTools), nullableJSON(a.Skills),
			a.PermissionMode, nullableJSON(a.Permissions),
			nullableJSON(a.MCPServers), a.MaxTurns,
			a.Color, hidden, disabled,
			a.Source, a.SourcePath, a.TeamID,
			a.CreatedAt.Format(time.RFC3339), a.UpdatedAt.Format(time.RFC3339),
		); err != nil {
			return fmt.Errorf("inserting agent %q: %w", a.ID, err)
		}
	}

	// Insert teams.
	for _, t := range teams {
		now := time.Now().UTC()
		if t.CreatedAt.IsZero() {
			t.CreatedAt = now
		}
		if t.UpdatedAt.IsZero() {
			t.UpdatedAt = now
		}
		var isAuto int
		if t.IsAuto {
			isAuto = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO teams (id, name, description, lead_agent, skills, provider, model,
			                     culture, source, source_path, is_auto, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.Name, t.Description, t.LeadAgent,
			nullableJSON(t.Skills), t.Provider, t.Model,
			t.Culture, t.Source, t.SourcePath, isAuto,
			t.CreatedAt.Format(time.RFC3339), t.UpdatedAt.Format(time.RFC3339),
		); err != nil {
			return fmt.Errorf("inserting team %q: %w", t.ID, err)
		}
	}

	// Insert team agents.
	for _, ta := range teamAgents {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO team_agents (team_id, agent_id, role) VALUES (?, ?, ?)",
			ta.TeamID, ta.AgentID, ta.Role,
		); err != nil {
			return fmt.Errorf("inserting team agent %s/%s: %w", ta.TeamID, ta.AgentID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing rebuild transaction: %w", err)
	}
	return nil
}

// --- Sessions ---

func (s *SQLiteStore) CreateSession(ctx context.Context, session *AgentSession) error {
	now := time.Now().UTC()
	if session.StartedAt.IsZero() {
		session.StartedAt = now
	}

	var endedAt *string
	if session.EndedAt != nil {
		v := session.EndedAt.UTC().Format(time.RFC3339)
		endedAt = &v
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_sessions (id, agent_id, job_id, task_id, status, model, provider,
		                              tokens_in, tokens_out, started_at, ended_at, cost_usd)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.AgentID, session.JobID, session.TaskID,
		string(session.Status), session.Model, session.Provider,
		session.TokensIn, session.TokensOut,
		session.StartedAt.Format(time.RFC3339), endedAt, session.CostUSD,
	)
	if err != nil {
		return fmt.Errorf("inserting session: %w", err)
	}
	return nil
}

func (s *SQLiteStore) UpdateSession(ctx context.Context, id string, update SessionUpdate) error {
	var sets []string
	var args []any

	if update.Status != nil {
		sets = append(sets, "status = ?")
		args = append(args, string(*update.Status))
	}
	if update.TokensIn != nil {
		sets = append(sets, "tokens_in = ?")
		args = append(args, *update.TokensIn)
	}
	if update.TokensOut != nil {
		sets = append(sets, "tokens_out = ?")
		args = append(args, *update.TokensOut)
	}
	if update.EndedAt != nil {
		sets = append(sets, "ended_at = ?")
		args = append(args, update.EndedAt.UTC().Format(time.RFC3339))
	}
	if update.CostUSD != nil {
		sets = append(sets, "cost_usd = ?")
		args = append(args, *update.CostUSD)
	}

	if len(sets) == 0 {
		return nil // nothing to update
	}

	args = append(args, id)
	query := "UPDATE agent_sessions SET " + strings.Join(sets, ", ") + " WHERE id = ?"

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("updating session: %w", err)
	}
	return checkRowsAffected(result, "session", id)
}

func (s *SQLiteStore) GetActiveSessions(ctx context.Context) ([]*AgentSession, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, job_id, task_id, status, model, provider,
		        tokens_in, tokens_out, started_at, ended_at, cost_usd
		 FROM agent_sessions
		 WHERE status = 'active'
		 ORDER BY started_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("listing active sessions: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var sessions []*AgentSession
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

// --- Artifacts ---

func (s *SQLiteStore) LogArtifact(ctx context.Context, artifact *Artifact) error {
	now := time.Now().UTC()
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = now
	}

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO artifacts (job_id, task_id, type, path, summary, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		artifact.JobID, artifact.TaskID, artifact.Type,
		artifact.Path, artifact.Summary,
		artifact.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("inserting artifact: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("getting last insert id: %w", err)
	}
	artifact.ID = id
	return nil
}

func (s *SQLiteStore) ListArtifactsForJob(ctx context.Context, jobID string) ([]*Artifact, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, job_id, task_id, type, path, summary, created_at
		 FROM artifacts
		 WHERE job_id = ?
		 ORDER BY created_at`, jobID)
	if err != nil {
		return nil, fmt.Errorf("listing artifacts: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var artifacts []*Artifact
	for rows.Next() {
		a := &Artifact{}
		var createdAt string
		if err := rows.Scan(&a.ID, &a.JobID, &a.TaskID, &a.Type,
			&a.Path, &a.Summary, &createdAt); err != nil {
			return nil, fmt.Errorf("scanning artifact: %w", err)
		}
		a.CreatedAt = parseTime(createdAt)
		artifacts = append(artifacts, a)
	}
	return artifacts, rows.Err()
}

// --- Helpers ---

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanTask(s scanner) (*Task, error) {
	t := &Task{}
	var status string
	var createdAt, updatedAt string
	var metadata sql.NullString

	if err := s.Scan(&t.ID, &t.JobID, &t.Title, &status,
		&t.AgentID, &t.TeamID, &t.ParentID, &t.SortOrder,
		&createdAt, &updatedAt, &t.Summary, &metadata,
		&t.ResultSummary, &t.Recommendations); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scanning task: %w", err)
	}

	t.Status = TaskStatus(status)
	t.CreatedAt = parseTime(createdAt)
	t.UpdatedAt = parseTime(updatedAt)
	if metadata.Valid {
		t.Metadata = json.RawMessage(metadata.String)
	}
	return t, nil
}

func scanSkill(s scanner) (*Skill, error) {
	sk := &Skill{}
	var createdAt, updatedAt string
	var tools sql.NullString

	if err := s.Scan(&sk.ID, &sk.Name, &sk.Description, &tools,
		&sk.Prompt, &sk.Source, &sk.SourcePath,
		&createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scanning skill: %w", err)
	}

	if tools.Valid {
		sk.Tools = json.RawMessage(tools.String)
	}
	sk.CreatedAt = parseTime(createdAt)
	sk.UpdatedAt = parseTime(updatedAt)
	return sk, nil
}

func scanAgent(s scanner) (*Agent, error) {
	a := &Agent{}
	var createdAt, updatedAt string
	var temperature sql.NullFloat64
	var maxTurns sql.NullInt64
	var tools, disallowedTools, skills, permissions, mcpServers sql.NullString
	var hidden, disabled int

	if err := s.Scan(&a.ID, &a.Name, &a.Description, &a.Mode,
		&a.Model, &a.Provider, &temperature,
		&a.SystemPrompt, &tools, &disallowedTools, &skills,
		&a.PermissionMode, &permissions, &mcpServers, &maxTurns,
		&a.Color, &hidden, &disabled, &a.Source, &a.SourcePath, &a.TeamID,
		&createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scanning agent: %w", err)
	}

	if temperature.Valid {
		a.Temperature = &temperature.Float64
	}
	if maxTurns.Valid {
		v := int(maxTurns.Int64)
		a.MaxTurns = &v
	}
	if tools.Valid {
		a.Tools = json.RawMessage(tools.String)
	}
	if disallowedTools.Valid {
		a.DisallowedTools = json.RawMessage(disallowedTools.String)
	}
	if skills.Valid {
		a.Skills = json.RawMessage(skills.String)
	}
	if permissions.Valid {
		a.Permissions = json.RawMessage(permissions.String)
	}
	if mcpServers.Valid {
		a.MCPServers = json.RawMessage(mcpServers.String)
	}
	a.Hidden = hidden != 0
	a.Disabled = disabled != 0
	a.CreatedAt = parseTime(createdAt)
	a.UpdatedAt = parseTime(updatedAt)
	return a, nil
}

func scanTeam(s scanner) (*Team, error) {
	t := &Team{}
	var createdAt, updatedAt string
	var skills sql.NullString
	var isAuto int

	if err := s.Scan(&t.ID, &t.Name, &t.Description, &t.LeadAgent,
		&skills, &t.Provider, &t.Model,
		&t.Culture, &t.Source, &t.SourcePath, &isAuto,
		&createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scanning team: %w", err)
	}

	if skills.Valid {
		t.Skills = json.RawMessage(skills.String)
	}
	t.IsAuto = isAuto != 0
	t.CreatedAt = parseTime(createdAt)
	t.UpdatedAt = parseTime(updatedAt)
	return t, nil
}

func scanFeedEntries(rows *sql.Rows) ([]*FeedEntry, error) {
	var entries []*FeedEntry
	for rows.Next() {
		e := &FeedEntry{}
		var createdAt string
		var metadata sql.NullString
		if err := rows.Scan(&e.ID, &e.JobID, &e.EntryType, &e.Content,
			&metadata, &createdAt); err != nil {
			return nil, fmt.Errorf("scanning feed entry: %w", err)
		}
		if metadata.Valid {
			e.Metadata = json.RawMessage(metadata.String)
		}
		e.CreatedAt = parseTime(createdAt)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func scanSession(s scanner) (*AgentSession, error) {
	sess := &AgentSession{}
	var status string
	var startedAt string
	var endedAt sql.NullString
	var costUSD sql.NullFloat64

	if err := s.Scan(&sess.ID, &sess.AgentID, &sess.JobID, &sess.TaskID,
		&status, &sess.Model, &sess.Provider,
		&sess.TokensIn, &sess.TokensOut,
		&startedAt, &endedAt, &costUSD); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scanning session: %w", err)
	}

	sess.Status = SessionStatus(status)
	sess.StartedAt = parseTime(startedAt)
	if endedAt.Valid {
		t := parseTime(endedAt.String)
		sess.EndedAt = &t
	}
	if costUSD.Valid {
		sess.CostUSD = &costUSD.Float64
	}
	return sess, nil
}

// parseTime parses a time string, trying RFC3339 first, then SQLite's default format.
func parseTime(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t
	}
	return time.Time{}
}

// nullableJSON returns nil if the JSON is nil or empty, otherwise the string form.
func nullableJSON(data json.RawMessage) any {
	if len(data) == 0 {
		return nil
	}
	return string(data)
}

// checkRowsAffected returns an error if no rows were affected by an update.
func checkRowsAffected(result sql.Result, entity, id string) error {
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%s %q: %w", entity, id, ErrNotFound)
	}
	return nil
}
