package operator

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jefflinse/toasters/internal/compose"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/runtime"
)

// --- Test helpers ---

// mockSpawner records SpawnTeamLead calls.
type mockSpawner struct {
	mu    sync.Mutex
	calls []spawnCall
}

type spawnCall struct {
	Composed *compose.ComposedAgent
	TaskID   string
	JobID    string
}

func (m *mockSpawner) SpawnTeamLead(_ context.Context, composed *compose.ComposedAgent, taskID string, jobID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, spawnCall{Composed: composed, TaskID: taskID, JobID: jobID})
	return nil
}

func (m *mockSpawner) getCalls() []spawnCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]spawnCall, len(m.calls))
	copy(cp, m.calls)
	return cp
}

// newTestStore opens a real SQLite store in a temp directory.
func newTestStore(t *testing.T) db.Store {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// newTestSystemTools creates a SystemTools with a real store, mock spawner,
// and buffered event channel.
func newTestSystemTools(t *testing.T) (*SystemTools, db.Store, *mockSpawner, chan Event) {
	t.Helper()
	store := newTestStore(t)
	spawner := &mockSpawner{}
	eventCh := make(chan Event, 64)

	// Create a composer with the real store.
	composer := compose.New(store, "test-provider", "test-model")

	st := NewSystemTools(store, composer, eventCh, spawner)
	return st, store, spawner, eventCh
}

// seedTeam inserts a team, its lead agent, and team membership into the store.
func seedTeam(t *testing.T, ctx context.Context, store db.Store, teamID, teamName, leadAgentID string) {
	t.Helper()

	if err := store.UpsertAgent(ctx, &db.Agent{
		ID:           leadAgentID,
		Name:         leadAgentID + "-name",
		Description:  "Test lead agent",
		Mode:         "lead",
		SystemPrompt: "You are a test lead.",
	}); err != nil {
		t.Fatalf("upserting agent: %v", err)
	}

	if err := store.UpsertTeam(ctx, &db.Team{
		ID:          teamID,
		Name:        teamName,
		Description: "A test team",
		LeadAgent:   leadAgentID,
	}); err != nil {
		t.Fatalf("upserting team: %v", err)
	}

	if err := store.AddTeamAgent(ctx, &db.TeamAgent{
		TeamID:  teamID,
		AgentID: leadAgentID,
		Role:    "lead",
	}); err != nil {
		t.Fatalf("adding team agent: %v", err)
	}
}

// --- Tests ---

func TestCreateJob(t *testing.T) {
	st, store, _, _ := newTestSystemTools(t)
	ctx := context.Background()

	workDir := t.TempDir()
	args, _ := json.Marshal(map[string]string{
		"title":         "Build web app",
		"description":   "Create a new web application",
		"workspace_dir": workDir,
	})
	result, err := st.Execute(ctx, "create_job", args)
	assertNoError(t, err)

	// Parse result to get job_id.
	var res map[string]string
	if err := json.Unmarshal([]byte(result), &res); err != nil {
		t.Fatalf("parsing result: %v", err)
	}
	jobID := res["job_id"]
	if jobID == "" {
		t.Fatal("expected non-empty job_id")
	}

	// Verify job in DB.
	job, err := store.GetJob(ctx, jobID)
	assertNoError(t, err)
	assertEqual(t, "Build web app", job.Title)
	assertEqual(t, "Create a new web application", job.Description)
	assertEqual(t, workDir, job.WorkspaceDir)
	assertEqual(t, string(db.JobStatusPending), string(job.Status))
}

func TestCreateJob_MissingParams(t *testing.T) {
	st, _, _, _ := newTestSystemTools(t)
	ctx := context.Background()

	// Missing title.
	_, err := st.Execute(ctx, "create_job", json.RawMessage(`{"description": "desc"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "title is required")

	// Missing description.
	_, err = st.Execute(ctx, "create_job", json.RawMessage(`{"title": "title"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "description is required")
}

func TestCreateTask(t *testing.T) {
	st, store, _, _ := newTestSystemTools(t)
	ctx := context.Background()

	// First create a job.
	jobResult, err := st.Execute(ctx, "create_job", json.RawMessage(`{
		"title": "Test job",
		"description": "A test job"
	}`))
	assertNoError(t, err)

	var jobRes map[string]string
	if err := json.Unmarshal([]byte(jobResult), &jobRes); err != nil {
		t.Fatalf("parsing job result: %v", err)
	}
	jobID := jobRes["job_id"]

	// Create a task on the job.
	taskResult, err := st.Execute(ctx, "create_task", json.RawMessage(`{
		"job_id": "`+jobID+`",
		"title": "Implement feature X"
	}`))
	assertNoError(t, err)

	var taskRes map[string]string
	if err := json.Unmarshal([]byte(taskResult), &taskRes); err != nil {
		t.Fatalf("parsing task result: %v", err)
	}
	taskID := taskRes["task_id"]
	if taskID == "" {
		t.Fatal("expected non-empty task_id")
	}

	// Verify task in DB.
	task, err := store.GetTask(ctx, taskID)
	assertNoError(t, err)
	assertEqual(t, "Implement feature X", task.Title)
	assertEqual(t, jobID, task.JobID)
	assertEqual(t, string(db.TaskStatusPending), string(task.Status))
}

func TestCreateTask_WithTeamID(t *testing.T) {
	st, store, _, _ := newTestSystemTools(t)
	ctx := context.Background()

	// Create a job.
	jobResult, err := st.Execute(ctx, "create_job", json.RawMessage(`{
		"title": "Test job",
		"description": "A test job"
	}`))
	assertNoError(t, err)

	var jobRes map[string]string
	if err := json.Unmarshal([]byte(jobResult), &jobRes); err != nil {
		t.Fatalf("parsing job result: %v", err)
	}
	jobID := jobRes["job_id"]

	// Create a task with pre-assigned team.
	taskResult, err := st.Execute(ctx, "create_task", json.RawMessage(`{
		"job_id": "`+jobID+`",
		"title": "Review code",
		"team_id": "backend-team"
	}`))
	assertNoError(t, err)

	var taskRes map[string]string
	if err := json.Unmarshal([]byte(taskResult), &taskRes); err != nil {
		t.Fatalf("parsing task result: %v", err)
	}

	task, err := store.GetTask(ctx, taskRes["task_id"])
	assertNoError(t, err)
	assertEqual(t, "backend-team", task.TeamID)
}

func TestCreateTask_MissingParams(t *testing.T) {
	st, _, _, _ := newTestSystemTools(t)
	ctx := context.Background()

	// Missing job_id.
	_, err := st.Execute(ctx, "create_task", json.RawMessage(`{"title": "task"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "job_id is required")

	// Missing title.
	_, err = st.Execute(ctx, "create_task", json.RawMessage(`{"job_id": "j1"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "title is required")
}

func TestAssignTask(t *testing.T) {
	st, store, spawner, eventCh := newTestSystemTools(t)
	ctx := context.Background()

	// Seed a team.
	seedTeam(t, ctx, store, "backend", "Backend Team", "lead-agent")

	// Create a job and task.
	jobResult, err := st.Execute(ctx, "create_job", json.RawMessage(`{
		"title": "Test job",
		"description": "A test job"
	}`))
	assertNoError(t, err)

	var jobRes map[string]string
	if err := json.Unmarshal([]byte(jobResult), &jobRes); err != nil {
		t.Fatalf("parsing job result: %v", err)
	}
	jobID := jobRes["job_id"]

	taskResult, err := st.Execute(ctx, "create_task", json.RawMessage(`{
		"job_id": "`+jobID+`",
		"title": "Build API"
	}`))
	assertNoError(t, err)

	var taskRes map[string]string
	if err := json.Unmarshal([]byte(taskResult), &taskRes); err != nil {
		t.Fatalf("parsing task result: %v", err)
	}
	taskID := taskRes["task_id"]

	// Assign the task.
	result, err := st.Execute(ctx, "assign_task", json.RawMessage(`{
		"task_id": "`+taskID+`",
		"team_id": "backend"
	}`))
	assertNoError(t, err)
	assertContains(t, result, "Backend Team")

	// Verify task status changed to in_progress.
	task, err := store.GetTask(ctx, taskID)
	assertNoError(t, err)
	assertEqual(t, string(db.TaskStatusInProgress), string(task.Status))
	assertEqual(t, "backend", task.TeamID)

	// Verify spawner was called.
	calls := spawner.getCalls()
	if len(calls) != 1 {
		t.Fatalf("want 1 spawn call, got %d", len(calls))
	}
	assertEqual(t, taskID, calls[0].TaskID)
	assertEqual(t, jobID, calls[0].JobID)
	assertEqual(t, "lead-agent", calls[0].Composed.AgentID)

	// Verify event was sent.
	select {
	case ev := <-eventCh:
		if ev.Type != EventTaskStarted {
			t.Fatalf("want EventTaskStarted, got %s", ev.Type)
		}
		payload, ok := ev.Payload.(TaskStartedPayload)
		if !ok {
			t.Fatal("invalid payload type")
		}
		assertEqual(t, taskID, payload.TaskID)
		assertEqual(t, "backend", payload.TeamID)
		assertEqual(t, "Build API", payload.Title)
	default:
		t.Fatal("expected event on channel")
	}
}

func TestAssignTask_NotPending(t *testing.T) {
	st, store, _, _ := newTestSystemTools(t)
	ctx := context.Background()

	// Seed a team.
	seedTeam(t, ctx, store, "backend", "Backend Team", "lead-agent")

	// Create a job and task.
	jobResult, err := st.Execute(ctx, "create_job", json.RawMessage(`{
		"title": "Test job",
		"description": "A test job"
	}`))
	assertNoError(t, err)

	var jobRes map[string]string
	if err := json.Unmarshal([]byte(jobResult), &jobRes); err != nil {
		t.Fatalf("parsing job result: %v", err)
	}
	jobID := jobRes["job_id"]

	taskResult, err := st.Execute(ctx, "create_task", json.RawMessage(`{
		"job_id": "`+jobID+`",
		"title": "Build API"
	}`))
	assertNoError(t, err)

	var taskRes map[string]string
	if err := json.Unmarshal([]byte(taskResult), &taskRes); err != nil {
		t.Fatalf("parsing task result: %v", err)
	}
	taskID := taskRes["task_id"]

	// Move task to in_progress manually.
	if err := store.UpdateTaskStatus(ctx, taskID, db.TaskStatusInProgress, ""); err != nil {
		t.Fatalf("updating task status: %v", err)
	}

	// Try to assign — should fail.
	_, err = st.Execute(ctx, "assign_task", json.RawMessage(`{
		"task_id": "`+taskID+`",
		"team_id": "backend"
	}`))
	assertError(t, err)
	assertContains(t, err.Error(), "not pending")
}

func TestAssignTask_MissingParams(t *testing.T) {
	st, _, _, _ := newTestSystemTools(t)
	ctx := context.Background()

	// Missing task_id.
	_, err := st.Execute(ctx, "assign_task", json.RawMessage(`{"team_id": "t1"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "task_id is required")

	// Missing team_id.
	_, err = st.Execute(ctx, "assign_task", json.RawMessage(`{"task_id": "t1"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "team_id is required")
}

func TestQueryTeams(t *testing.T) {
	st, store, _, _ := newTestSystemTools(t)
	ctx := context.Background()

	// Seed two teams.
	seedTeam(t, ctx, store, "backend", "Backend Team", "backend-lead")

	// Add a second team with a worker.
	if err := store.UpsertAgent(ctx, &db.Agent{
		ID:   "frontend-lead",
		Name: "Frontend Lead",
		Mode: "lead",
	}); err != nil {
		t.Fatalf("upserting agent: %v", err)
	}
	if err := store.UpsertAgent(ctx, &db.Agent{
		ID:   "frontend-worker",
		Name: "Frontend Worker",
		Mode: "worker",
	}); err != nil {
		t.Fatalf("upserting agent: %v", err)
	}
	if err := store.UpsertTeam(ctx, &db.Team{
		ID:          "frontend",
		Name:        "Frontend Team",
		Description: "Handles UI work",
		LeadAgent:   "frontend-lead",
	}); err != nil {
		t.Fatalf("upserting team: %v", err)
	}
	if err := store.AddTeamAgent(ctx, &db.TeamAgent{
		TeamID:  "frontend",
		AgentID: "frontend-lead",
		Role:    "lead",
	}); err != nil {
		t.Fatalf("adding team agent: %v", err)
	}
	if err := store.AddTeamAgent(ctx, &db.TeamAgent{
		TeamID:  "frontend",
		AgentID: "frontend-worker",
		Role:    "worker",
	}); err != nil {
		t.Fatalf("adding team agent: %v", err)
	}

	result, err := st.Execute(ctx, "query_teams", json.RawMessage(`{}`))
	assertNoError(t, err)

	assertContains(t, result, "Backend Team")
	assertContains(t, result, "backend")
	assertContains(t, result, "Frontend Team")
	assertContains(t, result, "Handles UI work")
	assertContains(t, result, "frontend-lead")
	assertContains(t, result, "Members: 2")
	assertContains(t, result, "Members: 1")
}

func TestQueryTeams_Empty(t *testing.T) {
	st, _, _, _ := newTestSystemTools(t)
	ctx := context.Background()

	result, err := st.Execute(ctx, "query_teams", json.RawMessage(`{}`))
	assertNoError(t, err)
	assertEqual(t, "No teams available.", result)
}

func TestQueryJob(t *testing.T) {
	st, store, _, _ := newTestSystemTools(t)
	ctx := context.Background()

	// Create a job with tasks.
	jobResult, err := st.Execute(ctx, "create_job", json.RawMessage(`{
		"title": "Build web app",
		"description": "Create a new web application"
	}`))
	assertNoError(t, err)

	var jobRes map[string]string
	if err := json.Unmarshal([]byte(jobResult), &jobRes); err != nil {
		t.Fatalf("parsing job result: %v", err)
	}
	jobID := jobRes["job_id"]

	// Create two tasks.
	_, err = st.Execute(ctx, "create_task", json.RawMessage(`{
		"job_id": "`+jobID+`",
		"title": "Setup project"
	}`))
	assertNoError(t, err)

	task2Result, err := st.Execute(ctx, "create_task", json.RawMessage(`{
		"job_id": "`+jobID+`",
		"title": "Build API",
		"team_id": "backend"
	}`))
	assertNoError(t, err)

	// Move second task to in_progress.
	var task2Res map[string]string
	if err := json.Unmarshal([]byte(task2Result), &task2Res); err != nil {
		t.Fatalf("parsing task result: %v", err)
	}
	if err := store.UpdateTaskStatus(ctx, task2Res["task_id"], db.TaskStatusInProgress, ""); err != nil {
		t.Fatalf("updating task status: %v", err)
	}

	// Query the job.
	result, err := st.Execute(ctx, "query_job", json.RawMessage(`{"job_id": "`+jobID+`"}`))
	assertNoError(t, err)

	assertContains(t, result, "Build web app")
	assertContains(t, result, string(db.JobStatusPending))
	assertContains(t, result, "Setup project")
	assertContains(t, result, "Build API")
	assertContains(t, result, string(db.TaskStatusPending))
	assertContains(t, result, string(db.TaskStatusInProgress))
	assertContains(t, result, "backend")
	assertContains(t, result, "Tasks (2)")
}

func TestQueryJob_MissingJobID(t *testing.T) {
	st, _, _, _ := newTestSystemTools(t)
	ctx := context.Background()

	_, err := st.Execute(ctx, "query_job", json.RawMessage(`{}`))
	assertError(t, err)
	assertContains(t, err.Error(), "job_id is required")
}

func TestQueryJob_NotFound(t *testing.T) {
	st, _, _, _ := newTestSystemTools(t)
	ctx := context.Background()

	_, err := st.Execute(ctx, "query_job", json.RawMessage(`{"job_id": "nonexistent"}`))
	assertError(t, err)
}

func TestSurfaceToUser(t *testing.T) {
	st, store, _, _ := newTestSystemTools(t)
	ctx := context.Background()

	result, err := st.Execute(ctx, "surface_to_user", json.RawMessage(`{
		"text": "The build is complete!"
	}`))
	assertNoError(t, err)
	assertContains(t, result, "Surfaced to user")
	assertContains(t, result, "The build is complete!")

	// Verify feed entry was created.
	entries, err := store.ListRecentFeedEntries(ctx, 10)
	assertNoError(t, err)
	if len(entries) != 1 {
		t.Fatalf("want 1 feed entry, got %d", len(entries))
	}
	assertEqual(t, "The build is complete!", entries[0].Content)
	assertEqual(t, string(db.FeedEntrySystemEvent), string(entries[0].EntryType))
}

func TestSurfaceToUser_MissingText(t *testing.T) {
	st, _, _, _ := newTestSystemTools(t)
	ctx := context.Background()

	_, err := st.Execute(ctx, "surface_to_user", json.RawMessage(`{}`))
	assertError(t, err)
	assertContains(t, err.Error(), "text is required")
}

func TestRelayToTeam(t *testing.T) {
	st, _, _, _ := newTestSystemTools(t)
	ctx := context.Background()

	result, err := st.Execute(ctx, "relay_to_team", json.RawMessage(`{
		"team_id": "backend",
		"message": "Please prioritize the API endpoint"
	}`))
	assertNoError(t, err)
	assertContains(t, result, "Relayed to team backend")
	assertContains(t, result, "Please prioritize the API endpoint")
}

func TestRelayToTeam_MissingParams(t *testing.T) {
	st, _, _, _ := newTestSystemTools(t)
	ctx := context.Background()

	// Missing team_id.
	_, err := st.Execute(ctx, "relay_to_team", json.RawMessage(`{"message": "hello"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "team_id is required")

	// Missing message.
	_, err = st.Execute(ctx, "relay_to_team", json.RawMessage(`{"team_id": "t1"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "message is required")
}

func TestSystemToolsUnknownTool(t *testing.T) {
	st, _, _, _ := newTestSystemTools(t)
	ctx := context.Background()

	_, err := st.Execute(ctx, "nonexistent", json.RawMessage(`{}`))
	assertError(t, err)
	if !errors.Is(err, runtime.ErrUnknownTool) {
		t.Fatalf("want ErrUnknownTool, got %v", err)
	}
}

func TestSystemToolDefinitions(t *testing.T) {
	st, _, _, _ := newTestSystemTools(t)
	defs := st.Definitions()

	expectedTools := []string{
		"create_job",
		"create_task",
		"assign_task",
		"query_teams",
		"query_job",
		"surface_to_user",
		"relay_to_team",
	}

	if len(defs) != len(expectedTools) {
		t.Fatalf("want %d tool definitions, got %d", len(expectedTools), len(defs))
	}

	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true

		// Verify each definition has a description and valid JSON schema.
		if d.Description == "" {
			t.Errorf("tool %q has empty description", d.Name)
		}
		var schema map[string]any
		if err := json.Unmarshal(d.Parameters, &schema); err != nil {
			t.Errorf("tool %q has invalid parameter schema: %v", d.Name, err)
		}
	}

	for _, name := range expectedTools {
		if !names[name] {
			t.Errorf("expected %q in definitions", name)
		}
	}
}

func TestNewID_Uniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := newID()
		if seen[id] {
			t.Fatalf("duplicate ID generated: %s", id)
		}
		seen[id] = true

		// Verify format: 5 hex segments separated by dashes.
		parts := strings.Split(id, "-")
		if len(parts) != 5 {
			t.Fatalf("expected 5 parts in ID, got %d: %s", len(parts), id)
		}
	}
}
