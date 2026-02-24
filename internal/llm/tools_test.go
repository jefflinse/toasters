package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/job"
)

// makeJobDir creates a minimal job directory under configDir/jobs/<jobID> with
// an OVERVIEW.md containing the given frontmatter fields. It returns the job
// directory path.
func makeJobDir(t *testing.T, configDir, jobID, status, completed string) string {
	t.Helper()

	jobDir := filepath.Join(configDir, "jobs", jobID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatalf("creating job dir: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	overview := fmt.Sprintf("---\nid: %s\nname: Test Job\ndescription: A test job.\nstatus: %s\ncreated: %s\nupdated: %s\ncompleted: %s\n---\n",
		jobID, status, now, now, completed)

	if err := os.WriteFile(filepath.Join(jobDir, "OVERVIEW.md"), []byte(overview), 0o644); err != nil {
		t.Fatalf("writing OVERVIEW.md: %v", err)
	}

	return jobDir
}

// toolCall builds a ToolCall for job_set_status with the given job ID and status.
func jobSetStatusCall(jobID, status string) ToolCall {
	args, _ := json.Marshal(map[string]string{"id": jobID, "status": status})
	return ToolCall{
		Function: ToolCallFunction{
			Name:      "job_set_status",
			Arguments: string(args),
		},
	}
}

// newTestExecutor creates a ToolExecutor wired to a temp directory so that
// ExecuteTool resolves job paths under <tempDir>/.config/toasters.
// It returns the executor and the config dir path.
func newTestExecutor(t *testing.T) (*ToolExecutor, string) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir := filepath.Join(home, ".config", "toasters")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	te := NewToolExecutor(nil, nil, configDir)
	return te, configDir
}

// loadFrontmatter reads and parses the OVERVIEW.md from jobDir, returning the
// Frontmatter. It fails the test if the file cannot be read or parsed.
func loadFrontmatter(t *testing.T, jobDir string) job.Frontmatter {
	t.Helper()

	j, err := job.Load(jobDir)
	if err != nil {
		t.Fatalf("loading job from %s: %v", jobDir, err)
	}

	return j.Frontmatter
}

// TestJobSetStatus_DoneSetCompleted verifies that calling job_set_status with
// status "done" auto-populates the completed field with an RFC3339 timestamp.
func TestJobSetStatus_DoneSetCompleted(t *testing.T) {
	te, configDir := newTestExecutor(t)
	jobDir := makeJobDir(t, configDir, "test-job-done", "active", "")

	before := time.Now().UTC().Add(-time.Second) // allow for sub-second skew

	result, err := te.ExecuteTool(jobSetStatusCall("test-job-done", "done"))
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result == "" {
		t.Fatal("ExecuteTool returned empty result")
	}

	fm := loadFrontmatter(t, jobDir)

	if fm.Status != job.StatusDone {
		t.Errorf("status: got %q, want %q", fm.Status, job.StatusDone)
	}

	if fm.Completed == "" {
		t.Fatal("completed: expected non-empty RFC3339 timestamp, got empty string")
	}

	completedAt, err := time.Parse(time.RFC3339, fm.Completed)
	if err != nil {
		t.Fatalf("completed: %q is not a valid RFC3339 timestamp: %v", fm.Completed, err)
	}

	after := time.Now().UTC().Add(time.Second)
	if completedAt.Before(before) || completedAt.After(after) {
		t.Errorf("completed timestamp %v is outside the expected range [%v, %v]",
			completedAt, before, after)
	}
}

// TestJobSetStatus_ActiveDoesNotSetCompleted verifies that calling job_set_status
// with status "active" leaves the completed field empty.
func TestJobSetStatus_ActiveDoesNotSetCompleted(t *testing.T) {
	te, configDir := newTestExecutor(t)
	jobDir := makeJobDir(t, configDir, "test-job-active", "done", "")

	_, err := te.ExecuteTool(jobSetStatusCall("test-job-active", "active"))
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}

	fm := loadFrontmatter(t, jobDir)

	if fm.Status != job.StatusActive {
		t.Errorf("status: got %q, want %q", fm.Status, job.StatusActive)
	}

	if fm.Completed != "" {
		t.Errorf("completed: expected empty string, got %q", fm.Completed)
	}
}

// TestJobSetStatus_PausedDoesNotSetCompleted verifies that calling job_set_status
// with status "paused" on a previously-done job preserves the original completed
// timestamp without clearing or updating it.
func TestJobSetStatus_PausedDoesNotSetCompleted(t *testing.T) {
	originalCompleted := "2026-01-15T10:30:00Z"

	te, configDir := newTestExecutor(t)
	jobDir := makeJobDir(t, configDir, "test-job-paused", "done", originalCompleted)

	_, err := te.ExecuteTool(jobSetStatusCall("test-job-paused", "paused"))
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}

	fm := loadFrontmatter(t, jobDir)

	if fm.Status != job.StatusPaused {
		t.Errorf("status: got %q, want %q", fm.Status, job.StatusPaused)
	}

	if fm.Completed != originalCompleted {
		t.Errorf("completed: got %q, want original value %q", fm.Completed, originalCompleted)
	}
}
