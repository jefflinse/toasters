package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// ---------------------------------------------------------------------------
// renderJobProgressSummary tests
// ---------------------------------------------------------------------------

func TestRenderJobProgressSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		tasks     []service.Task
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
			tasks:     []service.Task{},
			wantEmpty: true,
		},
		{
			name: "all pending tasks shows 0/N tasks",
			tasks: []service.Task{
				{Status: service.TaskStatusPending},
				{Status: service.TaskStatusPending},
				{Status: service.TaskStatusPending},
			},
			wantText: "0/3 tasks ✓",
		},
		{
			name: "some completed shows M/N tasks",
			tasks: []service.Task{
				{Status: service.TaskStatusCompleted},
				{Status: service.TaskStatusPending},
				{Status: service.TaskStatusCompleted},
			},
			wantText: "2/3 tasks ✓",
		},
		{
			name: "all completed shows N/N tasks",
			tasks: []service.Task{
				{Status: service.TaskStatusCompleted},
				{Status: service.TaskStatusCompleted},
			},
			wantText: "2/2 tasks ✓",
		},
		{
			name: "any blocked shows BLOCKED (takes priority)",
			tasks: []service.Task{
				{Status: service.TaskStatusPending},
				{Status: service.TaskStatusBlocked},
			},
			wantText: "BLOCKED",
		},
		{
			name: "mix of completed and blocked shows BLOCKED",
			tasks: []service.Task{
				{Status: service.TaskStatusCompleted},
				{Status: service.TaskStatusBlocked},
				{Status: service.TaskStatusCompleted},
			},
			wantText: "BLOCKED",
		},
		{
			name: "single in-progress task shows 0/1 tasks",
			tasks: []service.Task{
				{Status: service.TaskStatusInProgress},
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
		status      service.TaskStatus
		wantRune    string
		wantNonZero bool // style should be non-zero (has at least one attribute set)
	}{
		{
			name:        "pending returns circle",
			status:      service.TaskStatusPending,
			wantRune:    "○",
			wantNonZero: true,
		},
		{
			name:        "in_progress returns filled circle",
			status:      service.TaskStatusInProgress,
			wantRune:    "◉",
			wantNonZero: true,
		},
		{
			name:        "completed returns checkmark",
			status:      service.TaskStatusCompleted,
			wantRune:    "✓",
			wantNonZero: true,
		},
		{
			name:        "failed returns cross",
			status:      service.TaskStatusFailed,
			wantRune:    "✗",
			wantNonZero: true,
		},
		{
			name:        "blocked returns prohibition",
			status:      service.TaskStatusBlocked,
			wantRune:    "⊘",
			wantNonZero: true,
		},
		{
			name:        "cancelled returns dash",
			status:      service.TaskStatusCancelled,
			wantRune:    "—",
			wantNonZero: true,
		},
		{
			name:        "unknown status returns question mark",
			status:      service.TaskStatus("unknown_status"),
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
	_, unknownStyle := taskStatusIndicator(service.TaskStatus("bogus"))
	_, pendingStyle := taskStatusIndicator(service.TaskStatusPending)

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
// Model.Update() handler tests for progressPollMsg
// ---------------------------------------------------------------------------

func TestUpdate_ProgressPollMsg_UpdatesFields(t *testing.T) {
	t.Parallel()

	jobs := []service.Job{{ID: "job-1"}}
	tasks := map[string][]service.Task{
		"job-1": {{ID: "task-1", Status: service.TaskStatusCompleted}},
	}
	progress := map[string][]service.ProgressReport{
		"job-1": {{ID: 1, Message: "done"}},
	}
	sessions := []service.AgentSession{{ID: "sess-1"}}

	m := newMinimalModel(t)

	msg := progressPollMsg{
		Jobs:     jobs,
		Tasks:    tasks,
		Progress: progress,
		Sessions: sessions,
	}

	result, _ := m.Update(msg)
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

	result, _ := m.Update(msg)
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
}

func TestUpdate_ProgressPollMsg_ClientModeHydratesRuntimeSessionSources(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	start := time.Now().UTC()

	msg := progressPollMsg{
		RuntimeSessions: []service.SessionSnapshot{
			{
				ID:        "rt-1",
				AgentID:   "agent-alpha",
				TeamName:  "team-a",
				JobID:     "job-1",
				TaskID:    "task-1",
				Status:    "active",
				StartTime: start,
			},
		},
	}

	result, _ := m.Update(msg)
	got := result.(*Model)

	if len(got.runtimeSessions) == 0 {
		t.Fatal("runtimeSessions is empty, want hydrated session")
	}

	if len(got.sortedRuntimeSessions()) == 0 {
		t.Fatal("sortedRuntimeSessions is empty, want hydrated runtime session")
	}

	rs := got.runtimeSessionForGridCell(0)
	if rs == nil || rs.sessionID != "rt-1" {
		t.Fatalf("runtimeSessionForGridCell(0) = %+v, want session rt-1", rs)
	}

	taskSessions := got.runtimeSessionsForTask("task-1")
	if len(taskSessions) != 1 || taskSessions[0].sessionID != "rt-1" {
		t.Fatalf("runtimeSessionsForTask(task-1) = %+v, want [rt-1]", taskSessions)
	}
}

func TestUpdate_ProgressPollMsg_ClientModeMissingSnapshotWithinGraceDoesNotDropSession(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.runtimeSessions["stale"] = &runtimeSlot{sessionID: "stale", status: "active"}

	msg := progressPollMsg{
		RuntimeSessions: []service.SessionSnapshot{{ID: "keep", Status: "active"}},
	}

	result, _ := m.Update(msg)
	got := result.(*Model)

	if _, ok := got.runtimeSessions["stale"]; !ok {
		t.Fatal("stale runtime session dropped before grace threshold")
	}
	if _, ok := got.runtimeSessions["keep"]; !ok {
		t.Fatal("expected keep runtime session to be present after snapshot sync")
	}
}

func TestUpdate_ProgressPollMsg_ClientModeEventuallyRemovesStaleRuntimeSessionsAfterThreshold(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.runtimeSessions["stale"] = &runtimeSlot{sessionID: "stale", status: "active"}

	msg := progressPollMsg{RuntimeSessions: []service.SessionSnapshot{{ID: "keep", Status: "active"}}}

	for i := 0; i < runtimeSessionSnapshotMissThreshold; i++ {
		result, _ := m.Update(msg)
		m = *result.(*Model)
	}

	if _, ok := m.runtimeSessions["stale"]; ok {
		t.Fatal("stale runtime session still present after grace threshold")
	}
	if _, ok := m.runtimeSessions["keep"]; !ok {
		t.Fatal("expected keep runtime session to be present after snapshot sync")
	}
}

func TestUpdate_ProgressPollMsg_ClientModeUpdatesCanonicalSnapshotFieldsAndPreservesLocalFields(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	var out strings.Builder
	out.WriteString("existing output")
	start := time.Now().Add(-1 * time.Minute).UTC()
	updatedStart := time.Now().UTC()

	m.runtimeSessions["rt-1"] = &runtimeSlot{
		sessionID:      "rt-1",
		agentName:      "Friendly Agent",
		teamName:       "existing-team",
		task:           "rich local task description",
		jobID:          "job-1",
		taskID:         "task-1",
		status:         "active",
		output:         out,
		startTime:      start,
		systemPrompt:   "local system prompt",
		initialMessage: "local initial message",
		activities:     []activityItem{{label: "read: main.go", toolName: "read"}},
	}

	msg := progressPollMsg{
		RuntimeSessions: []service.SessionSnapshot{{
			ID:        "rt-1",
			AgentID:   "agent-alpha",
			TeamName:  "remote-team",
			JobID:     "job-2",
			TaskID:    "task-2",
			Status:    "completed",
			StartTime: updatedStart,
		}},
	}

	result, _ := m.Update(msg)
	got := result.(*Model)
	slot := got.runtimeSessions["rt-1"]
	if slot == nil {
		t.Fatal("expected runtime session rt-1 to exist")
	}

	if slot.agentName != "agent-alpha" {
		t.Fatalf("agentName = %q, want snapshot value", slot.agentName)
	}
	if slot.teamName != "remote-team" {
		t.Fatalf("teamName = %q, want snapshot value", slot.teamName)
	}
	if slot.jobID != "job-2" {
		t.Fatalf("jobID = %q, want snapshot value", slot.jobID)
	}
	if slot.taskID != "task-2" {
		t.Fatalf("taskID = %q, want snapshot value", slot.taskID)
	}
	if !slot.startTime.Equal(updatedStart) {
		t.Fatalf("startTime = %v, want updated snapshot startTime %v", slot.startTime, updatedStart)
	}
	if slot.task != "rich local task description" {
		t.Fatalf("task = %q, want preserved local task", slot.task)
	}
	if slot.systemPrompt != "local system prompt" {
		t.Fatalf("systemPrompt = %q, want preserved local prompt", slot.systemPrompt)
	}
	if slot.initialMessage != "local initial message" {
		t.Fatalf("initialMessage = %q, want preserved local initial message", slot.initialMessage)
	}
	if slot.output.String() != "existing output" {
		t.Fatalf("output = %q, want preserved local output", slot.output.String())
	}
	if len(slot.activities) != 1 || slot.activities[0].toolName != "read" {
		t.Fatalf("activities = %+v, want preserved local activities", slot.activities)
	}
	if start.Equal(updatedStart) {
		t.Fatalf("test setup bug: local and snapshot start times unexpectedly equal: %v", start)
	}
	if slot.status != "completed" {
		t.Fatalf("status = %q, want updated snapshot status", slot.status)
	}
}

func TestUpdate_ProgressPollMsg_EmbeddedModeDoesNotHydrateRuntimeSessionsFromSnapshots(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.openInEditor = func(string) tea.Cmd { return nil }
	m.runtimeSessions["local-1"] = &runtimeSlot{sessionID: "local-1", agentName: "local-agent", status: "active"}

	msg := progressPollMsg{
		RuntimeSessions: []service.SessionSnapshot{{
			ID:      "remote-1",
			AgentID: "remote-agent",
			Status:  "active",
		}},
	}

	result, _ := m.Update(msg)
	got := result.(*Model)

	if _, ok := got.runtimeSessions["local-1"]; !ok {
		t.Fatal("embedded mode local runtime session was unexpectedly removed")
	}
	if _, ok := got.runtimeSessions["remote-1"]; ok {
		t.Fatal("embedded mode unexpectedly hydrated snapshot session into runtimeSessions")
	}
}

func TestUpdate_ProgressPollMsg_ClientModeRenderPathsUseHydratedRuntimeSessions(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.width = 180
	m.height = 48

	msg := progressPollMsg{
		Jobs: []service.Job{{
			ID:     "job-1",
			Title:  "Job One",
			Status: service.JobStatusActive,
		}},
		Tasks: map[string][]service.Task{
			"job-1": {{
				ID:     "task-1",
				JobID:  "job-1",
				Title:  "Task One",
				Status: service.TaskStatusInProgress,
			}},
		},
		// Keep persisted sessions empty to ensure render paths rely on hydrated
		// runtimeSessions (from RuntimeSessions snapshots) in client mode.
		Sessions: nil,
		RuntimeSessions: []service.SessionSnapshot{{
			ID:        "rt-1",
			AgentID:   "snap-agent",
			JobID:     "job-1",
			TaskID:    "task-1",
			Status:    "active",
			StartTime: time.Now().UTC(),
		}},
	}

	result, _ := m.Update(msg)
	got := result.(*Model)

	t.Run("agents panel reads sorted runtime sessions source", func(t *testing.T) {
		left := stripANSI(got.renderLeftPanel(64, 40))
		if !strings.Contains(left, "snap-agent · job-1") {
			t.Fatalf("agents panel did not include hydrated runtime session, got:\n%s", left)
		}
	})

	t.Run("grid view reads runtime session grid source", func(t *testing.T) {
		got.grid.gridCols = 1
		got.grid.gridRows = 1
		got.grid.gridPage = 0
		got.grid.gridFocusCell = 0

		grid := stripANSI(got.renderGrid())
		if !strings.Contains(grid, "snap-agent") {
			t.Fatalf("grid view did not include hydrated runtime session, got:\n%s", grid)
		}
	})

	t.Run("jobs view reads runtimeSessionsForTask source", func(t *testing.T) {
		// renderJobsModal uses jobsModal data sources for job/task selection.
		got.jobsModal.jobs = got.progress.jobs
		got.jobsModal.tasks = got.progress.tasks
		got.jobsModal.jobIdx = 0
		got.jobsModal.taskIdx = 0

		modal := stripANSI(got.renderJobsModal())
		if !strings.Contains(modal, "snap-agent") {
			t.Fatalf("jobs modal did not include hydrated runtime session for selected task, got:\n%s", modal)
		}
	})
}
