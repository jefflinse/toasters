package operator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/runtime"
)

// --- Test helpers ---

// newTestTeamLeadTools creates a TeamLeadTools with a real store and buffered event channel.
// It also seeds a job, task, and team in the store.
func newTestTeamLeadTools(t *testing.T) (*TeamLeadTools, db.Store, chan Event) {
	t.Helper()
	store := newTestStore(t)
	eventCh := make(chan Event, 64)
	ctx := context.Background()

	// Seed a job.
	job := &db.Job{
		ID:          "job-1",
		Title:       "Test Job",
		Description: "A test job for team tools",
		Status:      db.JobStatusActive,
	}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("creating job: %v", err)
	}

	// Seed a task.
	task := &db.Task{
		ID:     "task-1",
		JobID:  "job-1",
		Title:  "Implement feature",
		Status: db.TaskStatusInProgress,
		TeamID: "team-1",
	}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	// Seed a team with culture.
	team := &db.Team{
		ID:      "team-1",
		Name:    "Backend Team",
		Culture: "We write clean, tested Go code. Always run tests before completing.",
	}
	if err := store.UpsertTeam(ctx, team); err != nil {
		t.Fatalf("upserting team: %v", err)
	}

	tl := NewTeamLeadTools(store, eventCh, "task-1", "job-1", "team-1")
	return tl, store, eventCh
}

// newTestWorkerTools creates a WorkerTools with a real store and buffered event channel.
func newTestWorkerTools(t *testing.T) (*WorkerTools, db.Store, chan Event) {
	t.Helper()
	store := newTestStore(t)
	eventCh := make(chan Event, 64)
	ctx := context.Background()

	// Seed a task (worker needs a task for progress reporting).
	job := &db.Job{
		ID:     "job-1",
		Title:  "Test Job",
		Status: db.JobStatusActive,
	}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatalf("creating job: %v", err)
	}

	task := &db.Task{
		ID:     "task-w1",
		JobID:  "job-1",
		Title:  "Worker task",
		Status: db.TaskStatusInProgress,
		TeamID: "team-1",
	}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	// Seed a team with culture.
	team := &db.Team{
		ID:      "team-1",
		Name:    "Backend Team",
		Culture: "We value simplicity and correctness.",
	}
	if err := store.UpsertTeam(ctx, team); err != nil {
		t.Fatalf("upserting team: %v", err)
	}

	wt := NewWorkerTools(store, eventCh, "task-w1", "job-1", "team-1")
	return wt, store, eventCh
}

// --- TeamLeadTools tests ---

func TestCompleteTask(t *testing.T) {
	tl, store, eventCh := newTestTeamLeadTools(t)
	ctx := context.Background()

	result, err := tl.Execute(ctx, "complete_task", json.RawMessage(`{
		"summary": "Feature implemented and tested"
	}`))
	assertNoError(t, err)
	assertEqual(t, "Task completed successfully", result)

	// Verify task status in DB.
	task, err := store.GetTask(ctx, "task-1")
	assertNoError(t, err)
	assertEqual(t, string(db.TaskStatusCompleted), string(task.Status))
	assertEqual(t, "Feature implemented and tested", task.Summary)
	assertEqual(t, "Feature implemented and tested", task.ResultSummary)

	// Verify event was sent.
	select {
	case ev := <-eventCh:
		if ev.Type != EventTaskCompleted {
			t.Fatalf("want EventTaskCompleted, got %s", ev.Type)
		}
		payload, ok := ev.Payload.(TaskCompletedPayload)
		if !ok {
			t.Fatal("invalid payload type")
		}
		assertEqual(t, "task-1", payload.TaskID)
		assertEqual(t, "team-1", payload.TeamID)
		assertEqual(t, "Feature implemented and tested", payload.Summary)
		assertEqual(t, "", payload.Recommendations)
	default:
		t.Fatal("expected event on channel")
	}
}

func TestCompleteTask_WithRecommendations(t *testing.T) {
	tl, store, eventCh := newTestTeamLeadTools(t)
	ctx := context.Background()

	result, err := tl.Execute(ctx, "complete_task", json.RawMessage(`{
		"summary": "API endpoints built",
		"recommendations": "Consider adding rate limiting and caching"
	}`))
	assertNoError(t, err)
	assertEqual(t, "Task completed successfully", result)

	// Verify recommendations in DB.
	task, err := store.GetTask(ctx, "task-1")
	assertNoError(t, err)
	assertEqual(t, "API endpoints built", task.ResultSummary)
	assertEqual(t, "Consider adding rate limiting and caching", task.Recommendations)

	// Verify event includes recommendations.
	select {
	case ev := <-eventCh:
		payload := ev.Payload.(TaskCompletedPayload)
		assertEqual(t, "Consider adding rate limiting and caching", payload.Recommendations)
	default:
		t.Fatal("expected event on channel")
	}
}

func TestCompleteTask_NoMoreTasks(t *testing.T) {
	tl, _, eventCh := newTestTeamLeadTools(t)
	ctx := context.Background()

	// No other pending tasks exist, so HasNextTask should be false.
	result, err := tl.Execute(ctx, "complete_task", json.RawMessage(`{
		"summary": "All done"
	}`))
	assertNoError(t, err)
	assertEqual(t, "Task completed successfully", result)

	select {
	case ev := <-eventCh:
		payload := ev.Payload.(TaskCompletedPayload)
		if payload.HasNextTask {
			t.Fatal("expected HasNextTask=false when no pending tasks")
		}
	default:
		t.Fatal("expected event on channel")
	}
}

func TestCompleteTask_HasNextTask(t *testing.T) {
	tl, store, eventCh := newTestTeamLeadTools(t)
	ctx := context.Background()

	// Create another pending task on the same job.
	nextTask := &db.Task{
		ID:     "task-2",
		JobID:  "job-1",
		Title:  "Next task",
		Status: db.TaskStatusPending,
	}
	if err := store.CreateTask(ctx, nextTask); err != nil {
		t.Fatalf("creating next task: %v", err)
	}

	result, err := tl.Execute(ctx, "complete_task", json.RawMessage(`{
		"summary": "First task done"
	}`))
	assertNoError(t, err)
	assertEqual(t, "Task completed successfully", result)

	select {
	case ev := <-eventCh:
		payload := ev.Payload.(TaskCompletedPayload)
		if !payload.HasNextTask {
			t.Fatal("expected HasNextTask=true when pending tasks exist")
		}
	default:
		t.Fatal("expected event on channel")
	}
}

func TestTeamLeadTools_CompletionTracker(t *testing.T) {
	// Verifies the runtime.TeamLeadCompletionTracker contract: CompletedCalled
	// flips after the LLM invokes complete_task, and ForceComplete provides a
	// synthetic-completion path for the safety-net watcher in
	// runtime.SpawnTeamLead.
	t.Run("CompletedCalled false until complete_task fires", func(t *testing.T) {
		tl, _, _ := newTestTeamLeadTools(t)
		if tl.CompletedCalled() {
			t.Fatal("CompletedCalled should be false on a fresh TeamLeadTools")
		}
		_, err := tl.Execute(context.Background(), "complete_task", json.RawMessage(`{"summary":"done"}`))
		assertNoError(t, err)
		if !tl.CompletedCalled() {
			t.Fatal("CompletedCalled should be true after complete_task")
		}
	})

	t.Run("CompletedCalled flips even when complete_task is called with bad json", func(t *testing.T) {
		// The watcher should NOT trigger when the LLM tried to call the tool,
		// even if the call was malformed. Otherwise we'd double-complete.
		tl, _, _ := newTestTeamLeadTools(t)
		_, err := tl.Execute(context.Background(), "complete_task", json.RawMessage(`{not json`))
		if err == nil {
			t.Fatal("expected JSON parse error")
		}
		if !tl.CompletedCalled() {
			t.Fatal("CompletedCalled should still flip on attempted-but-failed calls")
		}
	})

	t.Run("ForceComplete marks the task done and emits the event", func(t *testing.T) {
		tl, store, eventCh := newTestTeamLeadTools(t)
		if err := tl.ForceComplete(context.Background(), "synthetic summary"); err != nil {
			t.Fatalf("ForceComplete: %v", err)
		}
		task, err := store.GetTask(context.Background(), "task-1")
		assertNoError(t, err)
		if task.Status != db.TaskStatusCompleted {
			t.Fatalf("task status = %q, want completed", task.Status)
		}
		if !strings.Contains(task.ResultSummary, "synthetic") {
			t.Fatalf("task summary = %q, want it to include 'synthetic'", task.ResultSummary)
		}
		select {
		case ev := <-eventCh:
			if ev.Type != EventTaskCompleted {
				t.Fatalf("event type = %q, want %q", ev.Type, EventTaskCompleted)
			}
		default:
			t.Fatal("expected EventTaskCompleted on the channel")
		}
	})
}

func TestCompleteTask_MissingSummary(t *testing.T) {
	// Regression: previously this returned an error ("summary is required").
	// Small models frequently call complete_task without a summary, and
	// erroring out stranded the entire job at "active" forever — the
	// orchestrator never received EventTaskCompleted, so it never advanced
	// to the next task or marked the job done. We now accept the call and
	// substitute a placeholder summary so the job can advance.
	tl, store, eventCh := newTestTeamLeadTools(t)
	ctx := context.Background()

	result, err := tl.Execute(ctx, "complete_task", json.RawMessage(`{}`))
	assertNoError(t, err)
	assertContains(t, result, "Task completed successfully")

	// The task should be marked completed in the store with the placeholder
	// summary.
	task, err := store.GetTask(ctx, "task-1")
	assertNoError(t, err)
	if task.Status != db.TaskStatusCompleted {
		t.Fatalf("task status = %q, want completed", task.Status)
	}
	if task.ResultSummary == "" || !strings.Contains(task.ResultSummary, "no summary") {
		t.Fatalf("task summary = %q, want a placeholder mentioning 'no summary'", task.ResultSummary)
	}

	// EventTaskCompleted should still fire so the operator can advance.
	select {
	case ev := <-eventCh:
		if ev.Type != EventTaskCompleted {
			t.Fatalf("event type = %q, want %q", ev.Type, EventTaskCompleted)
		}
	default:
		t.Fatal("expected EventTaskCompleted on the event channel")
	}
}

func TestRequestNewTask(t *testing.T) {
	tl, _, eventCh := newTestTeamLeadTools(t)
	ctx := context.Background()

	result, err := tl.Execute(ctx, "request_new_task", json.RawMessage(`{
		"description": "Add integration tests",
		"reason": "Found untested edge cases during implementation"
	}`))
	assertNoError(t, err)
	assertContains(t, result, "New task request submitted")
	assertContains(t, result, "Add integration tests")

	// Verify event was sent.
	select {
	case ev := <-eventCh:
		if ev.Type != EventNewTaskRequest {
			t.Fatalf("want EventNewTaskRequest, got %s", ev.Type)
		}
		payload, ok := ev.Payload.(NewTaskRequestPayload)
		if !ok {
			t.Fatal("invalid payload type")
		}
		assertEqual(t, "job-1", payload.JobID)
		assertEqual(t, "team-1", payload.TeamID)
		assertEqual(t, "Add integration tests", payload.Description)
		assertEqual(t, "Found untested edge cases during implementation", payload.Reason)
	default:
		t.Fatal("expected event on channel")
	}
}

func TestRequestNewTask_MissingParams(t *testing.T) {
	tl, _, _ := newTestTeamLeadTools(t)
	ctx := context.Background()

	// Missing description.
	_, err := tl.Execute(ctx, "request_new_task", json.RawMessage(`{"reason": "because"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "description is required")

	// Missing reason.
	_, err = tl.Execute(ctx, "request_new_task", json.RawMessage(`{"description": "do stuff"}`))
	assertError(t, err)
	assertContains(t, err.Error(), "reason is required")
}

func TestReportBlocker(t *testing.T) {
	tl, store, eventCh := newTestTeamLeadTools(t)
	ctx := context.Background()

	result, err := tl.Execute(ctx, "report_blocker", json.RawMessage(`{
		"description": "Cannot access production database"
	}`))
	assertNoError(t, err)
	assertContains(t, result, "Blocker reported")
	assertContains(t, result, "Cannot access production database")

	// Verify task status changed to blocked.
	task, err := store.GetTask(ctx, "task-1")
	assertNoError(t, err)
	assertEqual(t, string(db.TaskStatusBlocked), string(task.Status))
	assertEqual(t, "Cannot access production database", task.Summary)

	// Verify event was sent.
	select {
	case ev := <-eventCh:
		if ev.Type != EventBlockerReported {
			t.Fatalf("want EventBlockerReported, got %s", ev.Type)
		}
		payload, ok := ev.Payload.(BlockerReportedPayload)
		if !ok {
			t.Fatal("invalid payload type")
		}
		assertEqual(t, "task-1", payload.TaskID)
		assertEqual(t, "team-1", payload.TeamID)
		assertEqual(t, "Cannot access production database", payload.Description)
	default:
		t.Fatal("expected event on channel")
	}
}

func TestReportBlocker_MissingDescription(t *testing.T) {
	tl, _, _ := newTestTeamLeadTools(t)
	ctx := context.Background()

	_, err := tl.Execute(ctx, "report_blocker", json.RawMessage(`{}`))
	assertError(t, err)
	assertContains(t, err.Error(), "description is required")
}

func TestReportProgress(t *testing.T) {
	tl, store, eventCh := newTestTeamLeadTools(t)
	ctx := context.Background()

	result, err := tl.Execute(ctx, "report_progress", json.RawMessage(`{
		"message": "Completed 3 of 5 endpoints"
	}`))
	assertNoError(t, err)
	assertEqual(t, "Progress reported", result)

	// Verify progress report in DB.
	reports, err := store.GetRecentProgress(ctx, "job-1", 10)
	assertNoError(t, err)
	if len(reports) != 1 {
		t.Fatalf("want 1 progress report, got %d", len(reports))
	}
	assertEqual(t, "Completed 3 of 5 endpoints", reports[0].Message)
	assertEqual(t, "task-1", reports[0].TaskID)
	assertEqual(t, "in_progress", reports[0].Status)

	// Verify event was sent.
	select {
	case ev := <-eventCh:
		if ev.Type != EventProgressUpdate {
			t.Fatalf("want EventProgressUpdate, got %s", ev.Type)
		}
		payload, ok := ev.Payload.(ProgressUpdatePayload)
		if !ok {
			t.Fatal("invalid payload type")
		}
		assertEqual(t, "task-1", payload.TaskID)
		assertEqual(t, "Completed 3 of 5 endpoints", payload.Message)
	default:
		t.Fatal("expected event on channel")
	}
}

func TestReportProgress_MissingMessage(t *testing.T) {
	tl, _, _ := newTestTeamLeadTools(t)
	ctx := context.Background()

	_, err := tl.Execute(ctx, "report_progress", json.RawMessage(`{}`))
	assertError(t, err)
	assertContains(t, err.Error(), "message is required")
}

func TestQueryJobContext(t *testing.T) {
	tl, store, _ := newTestTeamLeadTools(t)
	ctx := context.Background()

	// Create a second task to make the output more interesting.
	task2 := &db.Task{
		ID:     "task-2",
		JobID:  "job-1",
		Title:  "Write tests",
		Status: db.TaskStatusPending,
	}
	if err := store.CreateTask(ctx, task2); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	result, err := tl.Execute(ctx, "query_job_context", json.RawMessage(`{}`))
	assertNoError(t, err)

	assertContains(t, result, "Test Job")
	assertContains(t, result, string(db.JobStatusActive))
	assertContains(t, result, "A test job for team tools")
	assertContains(t, result, "Implement feature")
	assertContains(t, result, "Write tests")
	assertContains(t, result, string(db.TaskStatusInProgress))
	assertContains(t, result, string(db.TaskStatusPending))
	assertContains(t, result, "Tasks (2)")
}

func TestQueryTeamContext(t *testing.T) {
	tl, _, _ := newTestTeamLeadTools(t)
	ctx := context.Background()

	result, err := tl.Execute(ctx, "query_team_context", json.RawMessage(`{}`))
	assertNoError(t, err)
	assertEqual(t, "We write clean, tested Go code. Always run tests before completing.", result)
}

func TestQueryTeamContext_NoCulture(t *testing.T) {
	store := newTestStore(t)
	eventCh := make(chan Event, 64)
	ctx := context.Background()

	// Create a team with no culture document.
	team := &db.Team{
		ID:   "team-noculture",
		Name: "Minimal Team",
	}
	if err := store.UpsertTeam(ctx, team); err != nil {
		t.Fatalf("upserting team: %v", err)
	}

	tl := NewTeamLeadTools(store, eventCh, "task-1", "job-1", "team-noculture")

	result, err := tl.Execute(ctx, "query_team_context", json.RawMessage(`{}`))
	assertNoError(t, err)
	assertEqual(t, "No team culture document available", result)
}

// --- WorkerTools tests ---

func TestWorkerTools_ReportProgress(t *testing.T) {
	wt, store, eventCh := newTestWorkerTools(t)
	ctx := context.Background()

	result, err := wt.Execute(ctx, "report_progress", json.RawMessage(`{
		"message": "Finished writing unit tests"
	}`))
	assertNoError(t, err)
	assertEqual(t, "Progress reported", result)

	// Verify progress report in DB.
	reports, err := store.GetRecentProgress(ctx, "job-1", 10)
	assertNoError(t, err)
	if len(reports) != 1 {
		t.Fatalf("want 1 progress report, got %d", len(reports))
	}
	assertEqual(t, "Finished writing unit tests", reports[0].Message)
	assertEqual(t, "task-w1", reports[0].TaskID)
	assertEqual(t, "job-1", reports[0].JobID)

	// Verify event was sent.
	select {
	case ev := <-eventCh:
		if ev.Type != EventProgressUpdate {
			t.Fatalf("want EventProgressUpdate, got %s", ev.Type)
		}
		payload, ok := ev.Payload.(ProgressUpdatePayload)
		if !ok {
			t.Fatal("invalid payload type")
		}
		assertEqual(t, "task-w1", payload.TaskID)
		assertEqual(t, "Finished writing unit tests", payload.Message)
	default:
		t.Fatal("expected event on channel")
	}
}

func TestWorkerTools_ReportProgress_MissingMessage(t *testing.T) {
	wt, _, _ := newTestWorkerTools(t)
	ctx := context.Background()

	_, err := wt.Execute(ctx, "report_progress", json.RawMessage(`{}`))
	assertError(t, err)
	assertContains(t, err.Error(), "message is required")
}

func TestWorkerTools_QueryTeamContext(t *testing.T) {
	wt, _, _ := newTestWorkerTools(t)
	ctx := context.Background()

	result, err := wt.Execute(ctx, "query_team_context", json.RawMessage(`{}`))
	assertNoError(t, err)
	assertEqual(t, "We value simplicity and correctness.", result)
}

func TestWorkerTools_QueryTeamContext_NoCulture(t *testing.T) {
	store := newTestStore(t)
	eventCh := make(chan Event, 64)
	ctx := context.Background()

	// Create a team with no culture.
	team := &db.Team{
		ID:   "team-empty",
		Name: "Empty Team",
	}
	if err := store.UpsertTeam(ctx, team); err != nil {
		t.Fatalf("upserting team: %v", err)
	}

	wt := NewWorkerTools(store, eventCh, "task-1", "", "team-empty")

	result, err := wt.Execute(ctx, "query_team_context", json.RawMessage(`{}`))
	assertNoError(t, err)
	assertEqual(t, "No team culture document available", result)
}

// --- Unknown tool tests ---

func TestTeamLeadTools_UnknownTool(t *testing.T) {
	tl, _, _ := newTestTeamLeadTools(t)
	ctx := context.Background()

	_, err := tl.Execute(ctx, "nonexistent", json.RawMessage(`{}`))
	assertError(t, err)
	if !errors.Is(err, runtime.ErrUnknownTool) {
		t.Fatalf("want ErrUnknownTool, got %v", err)
	}
}

func TestWorkerTools_UnknownTool(t *testing.T) {
	wt, _, _ := newTestWorkerTools(t)
	ctx := context.Background()

	_, err := wt.Execute(ctx, "nonexistent", json.RawMessage(`{}`))
	assertError(t, err)
	if !errors.Is(err, runtime.ErrUnknownTool) {
		t.Fatalf("want ErrUnknownTool, got %v", err)
	}
}

// --- Definitions tests ---

func TestTeamLeadToolDefinitions(t *testing.T) {
	tl, _, _ := newTestTeamLeadTools(t)
	defs := tl.Definitions()

	expectedTools := []string{
		"complete_task",
		"request_new_task",
		"report_blocker",
		"report_progress",
		"query_job_context",
		"query_team_context",
	}

	if len(defs) != len(expectedTools) {
		t.Fatalf("want %d tool definitions, got %d", len(expectedTools), len(defs))
	}

	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true

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

func TestWorkerToolDefinitions(t *testing.T) {
	wt, _, _ := newTestWorkerTools(t)
	defs := wt.Definitions()

	expectedTools := []string{
		"report_progress",
		"query_team_context",
	}

	if len(defs) != len(expectedTools) {
		t.Fatalf("want %d tool definitions, got %d", len(expectedTools), len(defs))
	}

	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true

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
