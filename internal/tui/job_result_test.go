package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/service"
)

func TestFormatJobDuration(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		start, end time.Time
		want       string
	}{
		{"missing start", time.Time{}, now, ""},
		{"missing end", now, time.Time{}, ""},
		{"sub-minute", now, now.Add(45 * time.Second), "45s"},
		{"minutes and seconds", now, now.Add(4*time.Minute + 12*time.Second), "4m12s"},
		{"hours", now, now.Add(2*time.Hour + 5*time.Minute), "2h05m"},
		{"negative window clamps to zero", now.Add(time.Minute), now, "0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatJobDuration(tt.start, tt.end)
			if got != tt.want {
				t.Errorf("formatJobDuration() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSummarizeFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files []service.FileTouch
		extra int
		want  string
	}{
		{
			name:  "added only",
			files: []service.FileTouch{{Path: "a", IsNew: true}, {Path: "b", IsNew: true}},
			want:  "2 files added",
		},
		{
			name:  "modified only",
			files: []service.FileTouch{{Path: "a"}, {Path: "b"}, {Path: "c"}},
			want:  "3 files modified",
		},
		{
			name: "mixed",
			files: []service.FileTouch{
				{Path: "a", IsNew: true},
				{Path: "b", IsNew: true},
				{Path: "c"},
			},
			want: "3 files: 2 added · 1 modified",
		},
		{
			name:  "extra inflates total but doesn't reshuffle add/modify",
			files: []service.FileTouch{{Path: "a", IsNew: true}},
			extra: 5,
			want:  "6 files added",
		},
		{
			name:  "singular noun for one file",
			files: []service.FileTouch{{Path: "a", IsNew: true}},
			want:  "1 file added",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := summarizeFiles(tt.files, tt.extra)
			if got != tt.want {
				t.Errorf("summarizeFiles() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncateLeft(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		maxWidth int
		want     string
	}{
		{"empty when budget zero", "anything", 0, ""},
		{"fits unchanged", "/abc", 10, "/abc"},
		{"truncates with leading ellipsis", "/very/long/path/file.go", 10, "…h/file.go"},
		{"single ellipsis when budget exhausted", "abcdef", 1, "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncateLeft(tt.input, tt.maxWidth)
			if got != tt.want {
				t.Errorf("truncateLeft(%q, %d) = %q, want %q", tt.input, tt.maxWidth, got, tt.want)
			}
		})
	}
}

func TestContractHomeDir(t *testing.T) {
	t.Parallel()
	if got := contractHomeDir(""); got != "" {
		t.Errorf("empty path: got %q, want empty string", got)
	}
	// We don't depend on os.UserHomeDir() returning anything specific —
	// only that "/some/absolute/path" round-trips unchanged when it
	// doesn't begin with $HOME.
	if got := contractHomeDir("/tmp/x"); !strings.Contains(got, "/tmp/x") {
		t.Errorf("non-home path: got %q, expected to contain '/tmp/x'", got)
	}
}

func TestStepJobResultSelection(t *testing.T) {
	t.Parallel()
	mk := func(jobID string) service.ChatEntry {
		return service.ChatEntry{
			Kind:      service.ChatEntryKindJobResult,
			JobResult: &service.JobResultSnapshot{JobID: jobID},
		}
	}

	t.Run("no results: noop", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.chat.entries = []service.ChatEntry{
			{Message: service.ChatMessage{Role: "user", Content: "hi"}},
		}
		m.chat.selectedMsgIdx = -1
		if changed := m.stepBlockSelection(-1); changed {
			t.Error("expected stepBlockSelection to be a noop without results")
		}
	})

	t.Run("up from no selection lands on most recent", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.chat.entries = []service.ChatEntry{mk("job-A"), mk("job-B")}
		m.chat.selectedMsgIdx = -1
		if !m.stepBlockSelection(-1) {
			t.Fatal("expected selection change")
		}
		if m.chat.selectedMsgIdx != 1 {
			t.Errorf("expected selection on most recent (idx 1), got %d", m.chat.selectedMsgIdx)
		}
	})

	t.Run("up walks backward; off-the-start clears", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.chat.entries = []service.ChatEntry{mk("job-A"), mk("job-B")}
		m.chat.selectedMsgIdx = 1 // most recent selected
		if !m.stepBlockSelection(-1) {
			t.Fatal("expected step")
		}
		if m.chat.selectedMsgIdx != 0 {
			t.Errorf("got %d, want 0", m.chat.selectedMsgIdx)
		}
		// Step further back clears.
		if !m.stepBlockSelection(-1) {
			t.Fatal("expected step")
		}
		if m.chat.selectedMsgIdx != -1 {
			t.Errorf("expected selection cleared, got %d", m.chat.selectedMsgIdx)
		}
	})

	t.Run("down from selection moves forward; off-the-end clears", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.chat.entries = []service.ChatEntry{mk("job-A"), mk("job-B")}
		m.chat.selectedMsgIdx = 0
		if !m.stepBlockSelection(+1) {
			t.Fatal("expected step")
		}
		if m.chat.selectedMsgIdx != 1 {
			t.Errorf("got %d, want 1", m.chat.selectedMsgIdx)
		}
		if !m.stepBlockSelection(+1) {
			t.Fatal("expected step")
		}
		if m.chat.selectedMsgIdx != -1 {
			t.Errorf("expected selection cleared, got %d", m.chat.selectedMsgIdx)
		}
	})
}

func TestRenderJobResultBlock_PaintsHeaderWorkspaceAndHints(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 25, 23, 34, 12, 0, time.UTC)
	res := &service.JobResultSnapshot{
		JobID:     "job-1",
		Title:     "To-Do Management Web App",
		Status:    service.JobStatusCompleted,
		Workspace: "/tmp/toasters/jobs/abc",
		StartedAt: now.Add(-4*time.Minute - 12*time.Second),
		EndedAt:   now,
		FilesTouched: []service.FileTouch{
			{Path: "todo/cmd/main.go", IsNew: true},
			{Path: "todo/internal/db/database.go", IsNew: true},
		},
		TokensIn:  8200,
		TokensOut: 2100,
	}
	out := renderJobResultBlock(res, 70, false)
	if !strings.Contains(out, "To-Do Management Web App") {
		t.Errorf("missing title in: %s", out)
	}
	if !strings.Contains(out, "4m12s") {
		t.Errorf("missing duration in: %s", out)
	}
	if !strings.Contains(out, "/tmp/toasters/jobs/abc") {
		t.Errorf("missing workspace in: %s", out)
	}
	if !strings.Contains(out, "[w] workspace") {
		t.Errorf("missing workspace hint in: %s", out)
	}
	if !strings.Contains(out, "todo/cmd/main.go") {
		t.Errorf("missing file entry in: %s", out)
	}
}

func TestRenderJobResultBlock_FailureSurfacesReason(t *testing.T) {
	t.Parallel()
	res := &service.JobResultSnapshot{
		JobID:     "job-1",
		Title:     "Broken Job",
		Status:    service.JobStatusFailed,
		Summary:   "scaffold node returned StatusError: missing module",
		Workspace: "/tmp/x",
		StartedAt: time.Now().Add(-time.Minute),
		EndedAt:   time.Now(),
	}
	out := renderJobResultBlock(res, 70, false)
	if !strings.Contains(out, "failed") {
		t.Errorf("expected status word 'failed' in: %s", out)
	}
	if !strings.Contains(out, "StatusError") {
		t.Errorf("expected failure reason in: %s", out)
	}
}
