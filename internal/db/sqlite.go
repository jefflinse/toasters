package db

import (
	"context"
	"database/sql"
	"encoding/json"
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
		`INSERT INTO jobs (id, title, type, status, created_at, updated_at, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Title, job.Type, string(job.Status),
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
		`SELECT id, title, type, status, created_at, updated_at, metadata
		 FROM jobs WHERE id = ?`, id)

	j := &Job{}
	var status string
	var createdAt, updatedAt string
	var metadata sql.NullString

	if err := row.Scan(&j.ID, &j.Title, &j.Type, &status,
		&createdAt, &updatedAt, &metadata); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("job %q not found", id)
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
	query := "SELECT id, title, type, status, created_at, updated_at, metadata FROM jobs"
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

		if err := rows.Scan(&j.ID, &j.Title, &j.Type, &status,
			&createdAt, &updatedAt, &metadata); err != nil {
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
		`INSERT INTO tasks (id, job_id, title, status, agent_id, parent_id, sort_order,
		                     created_at, updated_at, summary, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.JobID, task.Title, string(task.Status),
		task.AgentID, task.ParentID, task.SortOrder,
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
		`SELECT id, job_id, title, status, agent_id, parent_id, sort_order,
		        created_at, updated_at, summary, metadata
		 FROM tasks WHERE id = ?`, id)

	return scanTask(row)
}

func (s *SQLiteStore) ListTasksForJob(ctx context.Context, jobID string) ([]*Task, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, job_id, title, status, agent_id, parent_id, sort_order,
		        created_at, updated_at, summary, metadata
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
		`SELECT t.id, t.job_id, t.title, t.status, t.agent_id, t.parent_id, t.sort_order,
		        t.created_at, t.updated_at, t.summary, t.metadata
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

// --- Agents ---

func (s *SQLiteStore) UpsertAgent(ctx context.Context, agent *Agent) error {
	now := time.Now().UTC()
	if agent.CreatedAt.IsZero() {
		agent.CreatedAt = now
	}
	if agent.UpdatedAt.IsZero() {
		agent.UpdatedAt = now
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agents (id, name, description, mode, model, provider, temperature,
		                      system_prompt, tools, created_at, updated_at, source)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		     name = excluded.name,
		     description = excluded.description,
		     mode = excluded.mode,
		     model = excluded.model,
		     provider = excluded.provider,
		     temperature = excluded.temperature,
		     system_prompt = excluded.system_prompt,
		     tools = excluded.tools,
		     updated_at = excluded.updated_at,
		     source = excluded.source`,
		agent.ID, agent.Name, agent.Description, agent.Mode,
		agent.Model, agent.Provider, agent.Temperature,
		agent.SystemPrompt, nullableJSON(agent.Tools),
		agent.CreatedAt.Format(time.RFC3339), agent.UpdatedAt.Format(time.RFC3339),
		agent.Source,
	)
	if err != nil {
		return fmt.Errorf("upserting agent: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetAgent(ctx context.Context, id string) (*Agent, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, mode, model, provider, temperature,
		        system_prompt, tools, created_at, updated_at, source
		 FROM agents WHERE id = ?`, id)

	a := &Agent{}
	var createdAt, updatedAt string
	var temperature sql.NullFloat64
	var tools sql.NullString

	if err := row.Scan(&a.ID, &a.Name, &a.Description, &a.Mode,
		&a.Model, &a.Provider, &temperature,
		&a.SystemPrompt, &tools, &createdAt, &updatedAt, &a.Source); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("agent %q not found", id)
		}
		return nil, fmt.Errorf("scanning agent: %w", err)
	}

	if temperature.Valid {
		a.Temperature = &temperature.Float64
	}
	if tools.Valid {
		a.Tools = json.RawMessage(tools.String)
	}
	a.CreatedAt = parseTime(createdAt)
	a.UpdatedAt = parseTime(updatedAt)
	return a, nil
}

func (s *SQLiteStore) ListAgents(ctx context.Context) ([]*Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, mode, model, provider, temperature,
		        system_prompt, tools, created_at, updated_at, source
		 FROM agents ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var agents []*Agent
	for rows.Next() {
		a := &Agent{}
		var createdAt, updatedAt string
		var temperature sql.NullFloat64
		var tools sql.NullString

		if err := rows.Scan(&a.ID, &a.Name, &a.Description, &a.Mode,
			&a.Model, &a.Provider, &temperature,
			&a.SystemPrompt, &tools, &createdAt, &updatedAt, &a.Source); err != nil {
			return nil, fmt.Errorf("scanning agent row: %w", err)
		}

		if temperature.Valid {
			a.Temperature = &temperature.Float64
		}
		if tools.Valid {
			a.Tools = json.RawMessage(tools.String)
		}
		a.CreatedAt = parseTime(createdAt)
		a.UpdatedAt = parseTime(updatedAt)
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// --- Teams ---

func (s *SQLiteStore) CreateTeam(ctx context.Context, team *Team) error {
	now := time.Now().UTC()
	if team.CreatedAt.IsZero() {
		team.CreatedAt = now
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO teams (id, name, description, coordinator, created_at, metadata)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		team.ID, team.Name, team.Description, team.Coordinator,
		team.CreatedAt.Format(time.RFC3339), nullableJSON(team.Metadata),
	)
	if err != nil {
		return fmt.Errorf("inserting team: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetTeam(ctx context.Context, id string) (*Team, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, coordinator, created_at, metadata
		 FROM teams WHERE id = ?`, id)

	t := &Team{}
	var createdAt string
	var metadata sql.NullString

	if err := row.Scan(&t.ID, &t.Name, &t.Description, &t.Coordinator,
		&createdAt, &metadata); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("team %q not found", id)
		}
		return nil, fmt.Errorf("scanning team: %w", err)
	}

	t.CreatedAt = parseTime(createdAt)
	if metadata.Valid {
		t.Metadata = json.RawMessage(metadata.String)
	}
	return t, nil
}

func (s *SQLiteStore) ListTeams(ctx context.Context) ([]*Team, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, coordinator, created_at, metadata
		 FROM teams ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("listing teams: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var teams []*Team
	for rows.Next() {
		t := &Team{}
		var createdAt string
		var metadata sql.NullString

		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Coordinator,
			&createdAt, &metadata); err != nil {
			return nil, fmt.Errorf("scanning team row: %w", err)
		}

		t.CreatedAt = parseTime(createdAt)
		if metadata.Valid {
			t.Metadata = json.RawMessage(metadata.String)
		}
		teams = append(teams, t)
	}
	return teams, rows.Err()
}

func (s *SQLiteStore) AddTeamMember(ctx context.Context, member *TeamMember) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO team_members (team_id, agent_id, role) VALUES (?, ?, ?)",
		member.TeamID, member.AgentID, member.Role)
	if err != nil {
		return fmt.Errorf("adding team member: %w", err)
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
		&t.AgentID, &t.ParentID, &t.SortOrder,
		&createdAt, &updatedAt, &t.Summary, &metadata); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("task not found")
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
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("session not found")
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
		return fmt.Errorf("%s %q not found", entity, id)
	}
	return nil
}
