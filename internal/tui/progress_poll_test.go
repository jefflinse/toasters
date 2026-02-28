package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/db"
)

// ---------------------------------------------------------------------------
// mockStore — minimal db.Store implementation for progress-poll tests.
// Only the methods called by progressPollCmd need real implementations;
// all others return zero values so the struct satisfies the interface.
// ---------------------------------------------------------------------------

type mockStore struct {
	listJobs          func(ctx context.Context, filter db.JobFilter) ([]*db.Job, error)
	listTasksForJob   func(ctx context.Context, jobID string) ([]*db.Task, error)
	getRecentProgress func(ctx context.Context, jobID string, limit int) ([]*db.ProgressReport, error)
	getActiveSessions func(ctx context.Context) ([]*db.AgentSession, error)
}

func (m *mockStore) CreateJob(ctx context.Context, job *db.Job) error { return nil }
func (m *mockStore) GetJob(ctx context.Context, id string) (*db.Job, error) {
	return nil, nil
}
func (m *mockStore) ListJobs(ctx context.Context, filter db.JobFilter) ([]*db.Job, error) {
	if m.listJobs != nil {
		return m.listJobs(ctx, filter)
	}
	return nil, nil
}
func (m *mockStore) ListAllJobs(ctx context.Context) ([]*db.Job, error) {
	return m.ListJobs(ctx, db.JobFilter{})
}
func (m *mockStore) UpdateJob(ctx context.Context, id string, update db.JobUpdate) error {
	return nil
}
func (m *mockStore) UpdateJobStatus(ctx context.Context, id string, status db.JobStatus) error {
	return nil
}
func (m *mockStore) CreateTask(ctx context.Context, task *db.Task) error { return nil }
func (m *mockStore) GetTask(ctx context.Context, id string) (*db.Task, error) {
	return nil, nil
}
func (m *mockStore) ListTasksForJob(ctx context.Context, jobID string) ([]*db.Task, error) {
	if m.listTasksForJob != nil {
		return m.listTasksForJob(ctx, jobID)
	}
	return nil, nil
}
func (m *mockStore) UpdateTaskStatus(ctx context.Context, id string, status db.TaskStatus, summary string) error {
	return nil
}
func (m *mockStore) UpdateTaskResult(ctx context.Context, id string, resultSummary, recommendations string) error {
	return nil
}
func (m *mockStore) AssignTask(ctx context.Context, id string, teamID string) error {
	return nil
}
func (m *mockStore) PreAssignTaskTeam(ctx context.Context, id string, teamID string) error {
	return nil
}
func (m *mockStore) AddTaskDependency(ctx context.Context, taskID, dependsOn string) error {
	return nil
}
func (m *mockStore) GetReadyTasks(ctx context.Context, jobID string) ([]*db.Task, error) {
	return nil, nil
}
func (m *mockStore) ReportProgress(ctx context.Context, report *db.ProgressReport) error {
	return nil
}
func (m *mockStore) GetRecentProgress(ctx context.Context, jobID string, limit int) ([]*db.ProgressReport, error) {
	if m.getRecentProgress != nil {
		return m.getRecentProgress(ctx, jobID, limit)
	}
	return nil, nil
}
func (m *mockStore) UpsertAgent(ctx context.Context, agent *db.Agent) error { return nil }
func (m *mockStore) GetAgent(ctx context.Context, id string) (*db.Agent, error) {
	return nil, nil
}
func (m *mockStore) ListAgents(ctx context.Context) ([]*db.Agent, error) { return nil, nil }
func (m *mockStore) UpsertTeam(ctx context.Context, team *db.Team) error { return nil }
func (m *mockStore) GetTeam(ctx context.Context, id string) (*db.Team, error) {
	return nil, nil
}
func (m *mockStore) ListTeams(ctx context.Context) ([]*db.Team, error)        { return nil, nil }
func (m *mockStore) DeleteAllTeams(ctx context.Context) error                 { return nil }
func (m *mockStore) AddTeamAgent(ctx context.Context, ta *db.TeamAgent) error { return nil }
func (m *mockStore) ListTeamAgents(ctx context.Context, teamID string) ([]*db.TeamAgent, error) {
	return nil, nil
}
func (m *mockStore) DeleteAllTeamAgents(ctx context.Context) error          { return nil }
func (m *mockStore) UpsertSkill(ctx context.Context, skill *db.Skill) error { return nil }
func (m *mockStore) GetSkill(ctx context.Context, id string) (*db.Skill, error) {
	return nil, nil
}
func (m *mockStore) ListSkills(ctx context.Context) ([]*db.Skill, error)            { return nil, nil }
func (m *mockStore) DeleteAllSkills(ctx context.Context) error                      { return nil }
func (m *mockStore) DeleteAllAgents(ctx context.Context) error                      { return nil }
func (m *mockStore) CreateFeedEntry(ctx context.Context, entry *db.FeedEntry) error { return nil }
func (m *mockStore) ListFeedEntries(ctx context.Context, jobID string, limit int) ([]*db.FeedEntry, error) {
	return nil, nil
}
func (m *mockStore) ListRecentFeedEntries(ctx context.Context, limit int) ([]*db.FeedEntry, error) {
	return nil, nil
}
func (m *mockStore) RebuildDefinitions(ctx context.Context, skills []*db.Skill, agents []*db.Agent, teams []*db.Team, teamAgents []*db.TeamAgent) error {
	return nil
}
func (m *mockStore) CreateSession(ctx context.Context, session *db.AgentSession) error {
	return nil
}
func (m *mockStore) UpdateSession(ctx context.Context, id string, update db.SessionUpdate) error {
	return nil
}
func (m *mockStore) GetActiveSessions(ctx context.Context) ([]*db.AgentSession, error) {
	if m.getActiveSessions != nil {
		return m.getActiveSessions(ctx)
	}
	return nil, nil
}
func (m *mockStore) LogArtifact(ctx context.Context, artifact *db.Artifact) error { return nil }
func (m *mockStore) ListArtifactsForJob(ctx context.Context, jobID string) ([]*db.Artifact, error) {
	return nil, nil
}
func (m *mockStore) Close() error { return nil }

// ---------------------------------------------------------------------------
// progressPollCmd tests
// ---------------------------------------------------------------------------

func TestProgressPollCmd_HappyPath(t *testing.T) {
	t.Parallel()

	job1 := &db.Job{ID: "job-1"}
	job2 := &db.Job{ID: "job-2"}

	task1 := &db.Task{ID: "task-1", JobID: "job-1", Status: db.TaskStatusCompleted}
	task2 := &db.Task{ID: "task-2", JobID: "job-2", Status: db.TaskStatusInProgress}

	prog1 := &db.ProgressReport{ID: 1, JobID: "job-1", Message: "done"}
	prog2 := &db.ProgressReport{ID: 2, JobID: "job-2", Message: "in progress"}

	sess1 := &db.AgentSession{ID: "sess-1", Status: db.SessionStatusActive}

	store := &mockStore{
		listJobs: func(_ context.Context, _ db.JobFilter) ([]*db.Job, error) {
			return []*db.Job{job1, job2}, nil
		},
		listTasksForJob: func(_ context.Context, jobID string) ([]*db.Task, error) {
			switch jobID {
			case "job-1":
				return []*db.Task{task1}, nil
			case "job-2":
				return []*db.Task{task2}, nil
			}
			return nil, nil
		},
		getRecentProgress: func(_ context.Context, jobID string, limit int) ([]*db.ProgressReport, error) {
			if limit != 5 {
				t.Errorf("GetRecentProgress called with limit=%d, want 5", limit)
			}
			switch jobID {
			case "job-1":
				return []*db.ProgressReport{prog1}, nil
			case "job-2":
				return []*db.ProgressReport{prog2}, nil
			}
			return nil, nil
		},
		getActiveSessions: func(_ context.Context) ([]*db.AgentSession, error) {
			return []*db.AgentSession{sess1}, nil
		},
	}

	cmd := progressPollCmd(store, nil)
	if cmd == nil {
		t.Fatal("progressPollCmd returned nil cmd")
	}

	raw := cmd()
	msg, ok := raw.(progressPollMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want progressPollMsg", raw)
	}

	// Jobs
	if len(msg.Jobs) != 2 {
		t.Errorf("Jobs len = %d, want 2", len(msg.Jobs))
	}

	// Tasks per job
	if tasks, ok := msg.Tasks["job-1"]; !ok || len(tasks) != 1 {
		t.Errorf("Tasks[job-1] = %v (ok=%v), want 1 task", tasks, ok)
	}
	if tasks, ok := msg.Tasks["job-2"]; !ok || len(tasks) != 1 {
		t.Errorf("Tasks[job-2] = %v (ok=%v), want 1 task", tasks, ok)
	}

	// Progress per job
	if reports, ok := msg.Progress["job-1"]; !ok || len(reports) != 1 {
		t.Errorf("Progress[job-1] = %v (ok=%v), want 1 report", reports, ok)
	}
	if reports, ok := msg.Progress["job-2"]; !ok || len(reports) != 1 {
		t.Errorf("Progress[job-2] = %v (ok=%v), want 1 report", reports, ok)
	}

	// Sessions
	if len(msg.Sessions) != 1 {
		t.Errorf("Sessions len = %d, want 1", len(msg.Sessions))
	}
}

func TestProgressPollCmd_ListJobsError(t *testing.T) {
	t.Parallel()

	store := &mockStore{
		listJobs: func(_ context.Context, _ db.JobFilter) ([]*db.Job, error) {
			return nil, errors.New("db unavailable")
		},
		getActiveSessions: func(_ context.Context) ([]*db.AgentSession, error) {
			return []*db.AgentSession{{ID: "sess-1"}}, nil
		},
	}

	cmd := progressPollCmd(store, nil)
	raw := cmd()
	msg, ok := raw.(progressPollMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want progressPollMsg", raw)
	}

	// Graceful degradation: Jobs is nil, no panic.
	if msg.Jobs != nil {
		t.Errorf("Jobs = %v, want nil on ListJobs error", msg.Jobs)
	}
	// Tasks and Progress maps should be empty (no jobs to iterate).
	if len(msg.Tasks) != 0 {
		t.Errorf("Tasks len = %d, want 0 when no jobs", len(msg.Tasks))
	}
	if len(msg.Progress) != 0 {
		t.Errorf("Progress len = %d, want 0 when no jobs", len(msg.Progress))
	}
	// Sessions should still be populated.
	if len(msg.Sessions) != 1 {
		t.Errorf("Sessions len = %d, want 1 (independent of jobs error)", len(msg.Sessions))
	}
}

func TestProgressPollCmd_GetActiveSessionsError(t *testing.T) {
	t.Parallel()

	store := &mockStore{
		listJobs: func(_ context.Context, _ db.JobFilter) ([]*db.Job, error) {
			return []*db.Job{{ID: "job-1"}}, nil
		},
		getActiveSessions: func(_ context.Context) ([]*db.AgentSession, error) {
			return nil, errors.New("sessions unavailable")
		},
	}

	cmd := progressPollCmd(store, nil)
	raw := cmd()
	msg, ok := raw.(progressPollMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want progressPollMsg", raw)
	}

	// Graceful degradation: Sessions is nil, no panic.
	if msg.Sessions != nil {
		t.Errorf("Sessions = %v, want nil on GetActiveSessions error", msg.Sessions)
	}
	// Jobs should still be populated.
	if len(msg.Jobs) != 1 {
		t.Errorf("Jobs len = %d, want 1 (independent of sessions error)", len(msg.Jobs))
	}
}

func TestProgressPollCmd_ListTasksForJobError(t *testing.T) {
	t.Parallel()

	store := &mockStore{
		listJobs: func(_ context.Context, _ db.JobFilter) ([]*db.Job, error) {
			return []*db.Job{{ID: "job-1"}, {ID: "job-2"}}, nil
		},
		listTasksForJob: func(_ context.Context, jobID string) ([]*db.Task, error) {
			if jobID == "job-1" {
				return nil, errors.New("tasks unavailable for job-1")
			}
			return []*db.Task{{ID: "task-2", JobID: "job-2"}}, nil
		},
		getActiveSessions: func(_ context.Context) ([]*db.AgentSession, error) {
			return nil, nil
		},
	}

	cmd := progressPollCmd(store, nil)
	raw := cmd()
	msg, ok := raw.(progressPollMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want progressPollMsg", raw)
	}

	// job-1 tasks should be absent (error), job-2 tasks should be present.
	if _, exists := msg.Tasks["job-1"]; exists {
		t.Error("Tasks[job-1] should be absent when ListTasksForJob errors")
	}
	if tasks, exists := msg.Tasks["job-2"]; !exists || len(tasks) != 1 {
		t.Errorf("Tasks[job-2] = %v (exists=%v), want 1 task", tasks, exists)
	}
}

func TestProgressPollCmd_GetRecentProgressError(t *testing.T) {
	t.Parallel()

	store := &mockStore{
		listJobs: func(_ context.Context, _ db.JobFilter) ([]*db.Job, error) {
			return []*db.Job{{ID: "job-1"}, {ID: "job-2"}}, nil
		},
		getRecentProgress: func(_ context.Context, jobID string, _ int) ([]*db.ProgressReport, error) {
			if jobID == "job-1" {
				return nil, errors.New("progress unavailable for job-1")
			}
			return []*db.ProgressReport{{ID: 1, JobID: "job-2"}}, nil
		},
		getActiveSessions: func(_ context.Context) ([]*db.AgentSession, error) {
			return nil, nil
		},
	}

	cmd := progressPollCmd(store, nil)
	raw := cmd()
	msg, ok := raw.(progressPollMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want progressPollMsg", raw)
	}

	// job-1 progress should be absent (error), job-2 progress should be present.
	if _, exists := msg.Progress["job-1"]; exists {
		t.Error("Progress[job-1] should be absent when GetRecentProgress errors")
	}
	if reports, exists := msg.Progress["job-2"]; !exists || len(reports) != 1 {
		t.Errorf("Progress[job-2] = %v (exists=%v), want 1 report", reports, exists)
	}
}

func TestProgressPollCmd_EmptyStore(t *testing.T) {
	t.Parallel()

	store := &mockStore{
		listJobs: func(_ context.Context, _ db.JobFilter) ([]*db.Job, error) {
			return nil, nil
		},
		getActiveSessions: func(_ context.Context) ([]*db.AgentSession, error) {
			return nil, nil
		},
	}

	cmd := progressPollCmd(store, nil)
	raw := cmd()
	msg, ok := raw.(progressPollMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want progressPollMsg", raw)
	}

	if msg.Jobs != nil {
		t.Errorf("Jobs = %v, want nil for empty store", msg.Jobs)
	}
	if len(msg.Tasks) != 0 {
		t.Errorf("Tasks len = %d, want 0 for empty store", len(msg.Tasks))
	}
	if len(msg.Progress) != 0 {
		t.Errorf("Progress len = %d, want 0 for empty store", len(msg.Progress))
	}
	if msg.Sessions != nil {
		t.Errorf("Sessions = %v, want nil for empty store", msg.Sessions)
	}
}

// TestProgressPollCmd_ReturnsPendingJobs is a regression test for the bug
// where jobs were not appearing in the TUI Jobs panel because progressPollCmd
// filtered ListJobs to only "active" status, hiding jobs that were still
// "pending".
//
// This test uses a real db.Store backed by a temp SQLite DB so that it catches
// regressions even if the filter is applied inside the store layer rather than
// at the call site. A mock store that ignores the filter would pass even if the
// active-only filter were re-introduced.
func TestProgressPollCmd_ReturnsPendingJobs(t *testing.T) {
	t.Parallel()

	// Open a real SQLite store in a temp directory.
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("opening test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()

	// Insert a job with status = "pending" directly into the store.
	pendingJob := &db.Job{
		ID:     "job-pending",
		Title:  "Pending Job",
		Status: db.JobStatusPending,
	}
	if err := store.CreateJob(ctx, pendingJob); err != nil {
		t.Fatalf("creating pending job: %v", err)
	}

	// Call progressPollCmd with the real store.
	cmd := progressPollCmd(store, nil)
	raw := cmd()
	msg, ok := raw.(progressPollMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want progressPollMsg", raw)
	}

	// The pending job must appear in the result.
	if len(msg.Jobs) != 1 {
		t.Fatalf("Jobs len = %d, want 1 (pending job must be included)", len(msg.Jobs))
	}
	if msg.Jobs[0].ID != pendingJob.ID {
		t.Errorf("Jobs[0].ID = %q, want %q", msg.Jobs[0].ID, pendingJob.ID)
	}
	if msg.Jobs[0].Status != db.JobStatusPending {
		t.Errorf("Jobs[0].Status = %q, want %q (regression: pending jobs must not be filtered out)",
			msg.Jobs[0].Status, db.JobStatusPending)
	}
}

func TestProgressPollCmd_ListJobsPassesNoStatusFilter(t *testing.T) {
	t.Parallel()

	var capturedFilter db.JobFilter
	store := &mockStore{
		listJobs: func(_ context.Context, filter db.JobFilter) ([]*db.Job, error) {
			capturedFilter = filter
			return nil, nil
		},
	}

	cmd := progressPollCmd(store, nil)
	cmd()

	if capturedFilter.Status != nil {
		t.Errorf("ListJobs called with Status filter %q, want no filter (nil)", *capturedFilter.Status)
	}
}

// ---------------------------------------------------------------------------
// renderJobProgressSummary tests
// ---------------------------------------------------------------------------

func TestRenderJobProgressSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		tasks     []*db.Task
		wantEmpty bool
		wantText  string // substring that must appear in the rendered output
	}{
		{
			name:      "empty slice returns empty string",
			tasks:     nil,
			wantEmpty: true,
		},
		{
			name:      "empty non-nil slice returns empty string",
			tasks:     []*db.Task{},
			wantEmpty: true,
		},
		{
			name: "all pending tasks shows 0/N tasks",
			tasks: []*db.Task{
				{Status: db.TaskStatusPending},
				{Status: db.TaskStatusPending},
				{Status: db.TaskStatusPending},
			},
			wantText: "0/3 tasks ✓",
		},
		{
			name: "some completed shows M/N tasks",
			tasks: []*db.Task{
				{Status: db.TaskStatusCompleted},
				{Status: db.TaskStatusPending},
				{Status: db.TaskStatusCompleted},
			},
			wantText: "2/3 tasks ✓",
		},
		{
			name: "all completed shows N/N tasks",
			tasks: []*db.Task{
				{Status: db.TaskStatusCompleted},
				{Status: db.TaskStatusCompleted},
			},
			wantText: "2/2 tasks ✓",
		},
		{
			name: "any blocked shows BLOCKED (takes priority)",
			tasks: []*db.Task{
				{Status: db.TaskStatusPending},
				{Status: db.TaskStatusBlocked},
			},
			wantText: "BLOCKED",
		},
		{
			name: "mix of completed and blocked shows BLOCKED",
			tasks: []*db.Task{
				{Status: db.TaskStatusCompleted},
				{Status: db.TaskStatusBlocked},
				{Status: db.TaskStatusCompleted},
			},
			wantText: "BLOCKED",
		},
		{
			name: "single in-progress task shows 0/1 tasks",
			tasks: []*db.Task{
				{Status: db.TaskStatusInProgress},
			},
			wantText: "0/1 tasks ✓",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := renderJobProgressSummary(tt.tasks)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("renderJobProgressSummary() = %q, want empty string", got)
				}
				return
			}
			if !strings.Contains(got, tt.wantText) {
				t.Errorf("renderJobProgressSummary() = %q, want it to contain %q", got, tt.wantText)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// taskStatusIndicator tests
// ---------------------------------------------------------------------------

func TestTaskStatusIndicator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      db.TaskStatus
		wantRune    string
		wantNonZero bool // style should be non-zero (has at least one attribute set)
	}{
		{
			name:        "pending returns circle",
			status:      db.TaskStatusPending,
			wantRune:    "○",
			wantNonZero: true,
		},
		{
			name:        "in_progress returns filled circle",
			status:      db.TaskStatusInProgress,
			wantRune:    "◉",
			wantNonZero: true,
		},
		{
			name:        "completed returns checkmark",
			status:      db.TaskStatusCompleted,
			wantRune:    "✓",
			wantNonZero: true,
		},
		{
			name:        "failed returns cross",
			status:      db.TaskStatusFailed,
			wantRune:    "✗",
			wantNonZero: true,
		},
		{
			name:        "blocked returns prohibition",
			status:      db.TaskStatusBlocked,
			wantRune:    "⊘",
			wantNonZero: true,
		},
		{
			name:        "cancelled returns dash",
			status:      db.TaskStatusCancelled,
			wantRune:    "—",
			wantNonZero: true,
		},
		{
			name:        "unknown status returns question mark",
			status:      db.TaskStatus("unknown_status"),
			wantRune:    "?",
			wantNonZero: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotRune, gotStyle := taskStatusIndicator(tt.status)
			if gotRune != tt.wantRune {
				t.Errorf("taskStatusIndicator(%q) rune = %q, want %q", tt.status, gotRune, tt.wantRune)
			}
			// Verify the style is non-zero by rendering something with it.
			// A non-zero lipgloss style will produce non-empty output when rendering.
			rendered := gotStyle.Render("x")
			if rendered == "" {
				t.Errorf("taskStatusIndicator(%q) style renders empty string", tt.status)
			}
		})
	}
}

func TestTaskStatusIndicator_UnknownUsesPendingStyle(t *testing.T) {
	t.Parallel()

	// The unknown case should use dbTaskPendingStyle — verify it renders the same
	// as the pending case (same style object).
	_, unknownStyle := taskStatusIndicator(db.TaskStatus("bogus"))
	_, pendingStyle := taskStatusIndicator(db.TaskStatusPending)

	// Both should render "x" identically since they use the same style.
	if unknownStyle.Render("x") != pendingStyle.Render("x") {
		t.Error("unknown status should use the same style as pending")
	}
}

// ---------------------------------------------------------------------------
// formatTokenCount tests
// ---------------------------------------------------------------------------

func TestFormatTokenCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input int64
		want  string
	}{
		{
			name:  "zero",
			input: 0,
			want:  "0",
		},
		{
			name:  "small value",
			input: 42,
			want:  "42",
		},
		{
			name:  "just below 1000",
			input: 999,
			want:  "999",
		},
		{
			name:  "exactly 1000 uses k suffix",
			input: 1000,
			want:  "1.0k",
		},
		{
			name:  "1500 rounds to one decimal",
			input: 1500,
			want:  "1.5k",
		},
		{
			name:  "1234 rounds to one decimal",
			input: 1234,
			want:  "1.2k",
		},
		{
			name:  "10000",
			input: 10000,
			want:  "10.0k",
		},
		{
			name:  "large value",
			input: 123456,
			want:  "123.5k",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatTokenCount(tt.input)
			if got != tt.want {
				t.Errorf("formatTokenCount(%d) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Model.Update() handler tests for progressPollTickMsg and progressPollMsg
// ---------------------------------------------------------------------------

func TestUpdate_ProgressPollTickMsg_NilStore(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	// store is nil by default in newMinimalModel.

	result, cmd := m.Update(progressPollTickMsg{})
	_ = result

	if cmd != nil {
		t.Error("Update(progressPollTickMsg) with nil store should return nil cmd")
	}
}

func TestUpdate_ProgressPollTickMsg_NonNilStore(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.store = &mockStore{
		listJobs: func(_ context.Context, _ db.JobFilter) ([]*db.Job, error) {
			return nil, nil
		},
		getActiveSessions: func(_ context.Context) ([]*db.AgentSession, error) {
			return nil, nil
		},
	}

	result, cmd := m.Update(progressPollTickMsg{})
	_ = result

	if cmd == nil {
		t.Error("Update(progressPollTickMsg) with non-nil store should return a non-nil cmd")
	}
}

func TestUpdate_ProgressPollMsg_UpdatesFields(t *testing.T) {
	t.Parallel()

	jobs := []*db.Job{{ID: "job-1"}}
	tasks := map[string][]*db.Task{
		"job-1": {{ID: "task-1", Status: db.TaskStatusCompleted}},
	}
	progress := map[string][]*db.ProgressReport{
		"job-1": {{ID: 1, Message: "done"}},
	}
	sessions := []*db.AgentSession{{ID: "sess-1"}}

	m := newMinimalModel(t)

	msg := progressPollMsg{
		Jobs:     jobs,
		Tasks:    tasks,
		Progress: progress,
		Sessions: sessions,
	}

	result, cmd := m.Update(msg)
	got, ok := result.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", result)
	}

	// Fields should be updated.
	if len(got.progress.jobs) != 1 || got.progress.jobs[0].ID != "job-1" {
		t.Errorf("progressJobs = %v, want [{ID: job-1}]", got.progress.jobs)
	}
	if len(got.progress.tasks) != 1 {
		t.Errorf("progressTasks len = %d, want 1", len(got.progress.tasks))
	}
	if len(got.progress.reports) != 1 {
		t.Errorf("progressReports len = %d, want 1", len(got.progress.reports))
	}
	if len(got.progress.activeSessions) != 1 || got.progress.activeSessions[0].ID != "sess-1" {
		t.Errorf("activeSessions = %v, want [{ID: sess-1}]", got.progress.activeSessions)
	}

	// Should return a non-nil cmd (scheduleProgressPoll).
	if cmd == nil {
		t.Error("Update(progressPollMsg) should return a non-nil cmd (scheduleProgressPoll)")
	}
}

func TestUpdate_ProgressPollMsg_NilFields(t *testing.T) {
	t.Parallel()

	// Ensure nil fields in the message don't panic and are stored as-is.
	m := newMinimalModel(t)

	msg := progressPollMsg{
		Jobs:     nil,
		Tasks:    nil,
		Progress: nil,
		Sessions: nil,
	}

	result, cmd := m.Update(msg)
	got, ok := result.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", result)
	}

	if got.progress.jobs != nil {
		t.Errorf("progressJobs = %v, want nil", got.progress.jobs)
	}
	if got.progress.tasks != nil {
		t.Errorf("progressTasks = %v, want nil", got.progress.tasks)
	}
	if got.progress.reports != nil {
		t.Errorf("progressReports = %v, want nil", got.progress.reports)
	}
	if got.progress.activeSessions != nil {
		t.Errorf("activeSessions = %v, want nil", got.progress.activeSessions)
	}

	// Should still return a non-nil cmd.
	if cmd == nil {
		t.Error("Update(progressPollMsg) should return a non-nil cmd even with nil fields")
	}
}
