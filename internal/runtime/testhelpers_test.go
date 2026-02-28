package runtime

import (
	"context"

	"github.com/jefflinse/toasters/internal/db"
)

// toolNames extracts tool names from a slice of ToolDef for readable error messages.
func toolNames(defs []ToolDef) []string {
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	return names
}

// noopStore is a minimal db.Store implementation for tests that need a non-nil
// store. All methods succeed with zero-value results.
type noopStore struct{}

func (s *noopStore) CreateJob(_ context.Context, _ *db.Job) error                  { return nil }
func (s *noopStore) GetJob(_ context.Context, _ string) (*db.Job, error)           { return nil, nil }
func (s *noopStore) ListJobs(_ context.Context, _ db.JobFilter) ([]*db.Job, error) { return nil, nil }
func (s *noopStore) ListAllJobs(_ context.Context) ([]*db.Job, error)              { return nil, nil }
func (s *noopStore) UpdateJob(_ context.Context, _ string, _ db.JobUpdate) error   { return nil }
func (s *noopStore) UpdateJobStatus(_ context.Context, _ string, _ db.JobStatus) error {
	return nil
}
func (s *noopStore) CreateTask(_ context.Context, _ *db.Task) error        { return nil }
func (s *noopStore) GetTask(_ context.Context, _ string) (*db.Task, error) { return nil, nil }
func (s *noopStore) ListTasksForJob(_ context.Context, _ string) ([]*db.Task, error) {
	return nil, nil
}
func (s *noopStore) UpdateTaskStatus(_ context.Context, _ string, _ db.TaskStatus, _ string) error {
	return nil
}
func (s *noopStore) UpdateTaskResult(_ context.Context, _, _, _ string) error { return nil }
func (s *noopStore) CompleteTask(_ context.Context, _ string, _ db.TaskStatus, _, _ string) error {
	return nil
}
func (s *noopStore) AssignTask(_ context.Context, _, _ string) error        { return nil }
func (s *noopStore) PreAssignTaskTeam(_ context.Context, _, _ string) error { return nil }
func (s *noopStore) AddTaskDependency(_ context.Context, _, _ string) error { return nil }
func (s *noopStore) GetReadyTasks(_ context.Context, _ string) ([]*db.Task, error) {
	return nil, nil
}
func (s *noopStore) ReportProgress(_ context.Context, _ *db.ProgressReport) error { return nil }
func (s *noopStore) GetRecentProgress(_ context.Context, _ string, _ int) ([]*db.ProgressReport, error) {
	return nil, nil
}
func (s *noopStore) UpsertSkill(_ context.Context, _ *db.Skill) error        { return nil }
func (s *noopStore) GetSkill(_ context.Context, _ string) (*db.Skill, error) { return nil, nil }
func (s *noopStore) ListSkills(_ context.Context) ([]*db.Skill, error)       { return nil, nil }
func (s *noopStore) DeleteAllSkills(_ context.Context) error                 { return nil }
func (s *noopStore) UpsertAgent(_ context.Context, _ *db.Agent) error        { return nil }
func (s *noopStore) GetAgent(_ context.Context, _ string) (*db.Agent, error) { return nil, nil }
func (s *noopStore) ListAgents(_ context.Context) ([]*db.Agent, error)       { return nil, nil }
func (s *noopStore) DeleteAllAgents(_ context.Context) error                 { return nil }
func (s *noopStore) UpsertTeam(_ context.Context, _ *db.Team) error          { return nil }
func (s *noopStore) GetTeam(_ context.Context, _ string) (*db.Team, error)   { return nil, nil }
func (s *noopStore) ListTeams(_ context.Context) ([]*db.Team, error)         { return nil, nil }
func (s *noopStore) DeleteAllTeams(_ context.Context) error                  { return nil }
func (s *noopStore) AddTeamAgent(_ context.Context, _ *db.TeamAgent) error   { return nil }
func (s *noopStore) ListTeamAgents(_ context.Context, _ string) ([]*db.TeamAgent, error) {
	return nil, nil
}
func (s *noopStore) DeleteAllTeamAgents(_ context.Context) error              { return nil }
func (s *noopStore) CreateFeedEntry(_ context.Context, _ *db.FeedEntry) error { return nil }
func (s *noopStore) ListFeedEntries(_ context.Context, _ string, _ int) ([]*db.FeedEntry, error) {
	return nil, nil
}
func (s *noopStore) ListRecentFeedEntries(_ context.Context, _ int) ([]*db.FeedEntry, error) {
	return nil, nil
}
func (s *noopStore) RebuildDefinitions(_ context.Context, _ []*db.Skill, _ []*db.Agent, _ []*db.Team, _ []*db.TeamAgent) error {
	return nil
}
func (s *noopStore) CreateSession(_ context.Context, _ *db.AgentSession) error { return nil }
func (s *noopStore) UpdateSession(_ context.Context, _ string, _ db.SessionUpdate) error {
	return nil
}
func (s *noopStore) GetActiveSessions(_ context.Context) ([]*db.AgentSession, error) {
	return nil, nil
}
func (s *noopStore) LogArtifact(_ context.Context, _ *db.Artifact) error { return nil }
func (s *noopStore) ListArtifactsForJob(_ context.Context, _ string) ([]*db.Artifact, error) {
	return nil, nil
}
func (s *noopStore) Close() error { return nil }
