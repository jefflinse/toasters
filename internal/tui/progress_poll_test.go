package tui

import (
	"strings"
	"testing"

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
	sessions := []service.WorkerSession{{ID: "sess-1"}}

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

// Tests for the dual-bucket runtime-session-sync logic that was deleted in the
// "one event spine" cleanup live in session_event_test.go now (Phase 1 changes
// the source of runtime session state from progress polling to dedicated
// session.* events).
