package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/job"
	"github.com/jefflinse/toasters/internal/llm"
	"github.com/jefflinse/toasters/internal/orchestration"
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

// makeJobDirWithTodo creates a job directory with both OVERVIEW.md and TODO.md.
func makeJobDirWithTodo(t *testing.T, configDir, jobID string, todos []string) string {
	t.Helper()

	jobDir := makeJobDir(t, configDir, jobID, "active", "")

	var sb strings.Builder
	sb.WriteString("# TODOs\n")
	for _, todo := range todos {
		fmt.Fprintf(&sb, "- [ ] %s\n", todo)
	}

	if err := os.WriteFile(filepath.Join(jobDir, "TODO.md"), []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("writing TODO.md: %v", err)
	}

	return jobDir
}

// toolCall builds a ToolCall for job_set_status with the given job ID and status.
func jobSetStatusCall(jobID, status string) llm.ToolCall {
	args, _ := json.Marshal(map[string]string{"id": jobID, "status": status})
	return llm.ToolCall{
		Function: llm.ToolCallFunction{
			Name:      "job_set_status",
			Arguments: string(args),
		},
	}
}

// makeToolCall builds a generic ToolCall with the given name and arguments map.
func makeToolCall(name string, args any) llm.ToolCall {
	b, _ := json.Marshal(args)
	return llm.ToolCall{
		Function: llm.ToolCallFunction{
			Name:      name,
			Arguments: string(b),
		},
	}
}

// makeToolCallRaw builds a ToolCall with raw string arguments.
func makeToolCallRaw(name, rawArgs string) llm.ToolCall {
	return llm.ToolCall{
		Function: llm.ToolCallFunction{
			Name:      name,
			Arguments: rawArgs,
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

	te := NewToolExecutor(nil, nil, configDir, nil, nil)
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

// --- Mock AgentSpawner ---

type mockSpawner struct {
	spawnTeamFn   func(teamName, jobID, task string, team agents.Team) (int, bool, error)
	slotSummaries []orchestration.GatewaySlot
	killFn        func(slotID int) error
}

func (m *mockSpawner) SpawnTeam(teamName, jobID, task string, team agents.Team) (int, bool, error) {
	if m.spawnTeamFn != nil {
		return m.spawnTeamFn(teamName, jobID, task, team)
	}
	return 0, false, nil
}

func (m *mockSpawner) SlotSummaries() []orchestration.GatewaySlot {
	return m.slotSummaries
}

func (m *mockSpawner) Kill(slotID int) error {
	if m.killFn != nil {
		return m.killFn(slotID)
	}
	return nil
}

// ============================================================================
// job_set_status tests (existing)
// ============================================================================

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

// TestJobSetStatus_InvalidStatus verifies that an invalid status returns an
// error message (not an error) indicating the valid options.
func TestJobSetStatus_InvalidStatus(t *testing.T) {
	te, configDir := newTestExecutor(t)
	makeJobDir(t, configDir, "test-job", "active", "")

	result, err := te.ExecuteTool(jobSetStatusCall("test-job", "invalid"))
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "invalid status") {
		t.Errorf("expected result to contain 'invalid status', got %q", result)
	}
}

// TestJobSetStatus_BadJSON verifies that malformed JSON arguments return an error.
func TestJobSetStatus_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCallRaw("job_set_status", "not valid json")
	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
	if !strings.Contains(err.Error(), "parsing job_set_status args") {
		t.Errorf("expected error about parsing args, got: %v", err)
	}
}

// TestJobSetStatus_NonexistentJob verifies that setting status on a nonexistent
// job returns an error.
func TestJobSetStatus_NonexistentJob(t *testing.T) {
	te, _ := newTestExecutor(t)

	_, err := te.ExecuteTool(jobSetStatusCall("nonexistent-job", "done"))
	if err == nil {
		t.Fatal("expected error for nonexistent job, got nil")
	}
}

// ============================================================================
// job_list tests
// ============================================================================

func TestJobList_EmptyWorkspace(t *testing.T) {
	te, _ := newTestExecutor(t)

	result, err := te.ExecuteTool(makeToolCall("job_list", map[string]any{}))
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}

	// Should return an empty JSON array.
	var items []map[string]string
	if err := json.Unmarshal([]byte(result), &items); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 jobs, got %d", len(items))
	}
}

func TestJobList_WithJobs(t *testing.T) {
	te, configDir := newTestExecutor(t)
	makeJobDir(t, configDir, "alpha", "active", "")
	makeJobDir(t, configDir, "beta", "done", "2026-01-01T00:00:00Z")

	result, err := te.ExecuteTool(makeToolCall("job_list", map[string]any{}))
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}

	var items []map[string]string
	if err := json.Unmarshal([]byte(result), &items); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(items))
	}

	// Verify the items contain expected fields.
	ids := map[string]bool{}
	for _, item := range items {
		ids[item["id"]] = true
		if item["name"] == "" {
			t.Errorf("expected non-empty name for job %q", item["id"])
		}
	}
	if !ids["alpha"] || !ids["beta"] {
		t.Errorf("expected jobs alpha and beta, got %v", ids)
	}
}

// ============================================================================
// job_create tests
// ============================================================================

func TestJobCreate_Success(t *testing.T) {
	te, configDir := newTestExecutor(t)

	call := makeToolCall("job_create", map[string]string{
		"id":          "new-job",
		"name":        "New Job",
		"description": "A brand new job.",
	})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "new-job") {
		t.Errorf("expected result to contain job ID, got %q", result)
	}

	// Verify the job directory was created.
	jobDir := filepath.Join(configDir, "jobs", "new-job")
	if _, err := os.Stat(filepath.Join(jobDir, "OVERVIEW.md")); err != nil {
		t.Errorf("OVERVIEW.md not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(jobDir, "TODO.md")); err != nil {
		t.Errorf("TODO.md not created: %v", err)
	}
}

func TestJobCreate_InvalidID(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("job_create", map[string]string{
		"id":          "INVALID ID!",
		"name":        "Bad Job",
		"description": "Should fail.",
	})

	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for invalid job ID, got nil")
	}
}

func TestJobCreate_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCallRaw("job_create", "{bad json")
	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
	if !strings.Contains(err.Error(), "parsing job_create args") {
		t.Errorf("expected error about parsing args, got: %v", err)
	}
}

// ============================================================================
// job_read_overview tests
// ============================================================================

func TestJobReadOverview_Success(t *testing.T) {
	te, configDir := newTestExecutor(t)
	makeJobDir(t, configDir, "read-test", "active", "")

	call := makeToolCall("job_read_overview", map[string]string{"id": "read-test"})
	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "read-test") {
		t.Errorf("expected overview to contain job ID, got %q", result)
	}
	if !strings.Contains(result, "---") {
		t.Errorf("expected overview to contain frontmatter delimiters, got %q", result)
	}
}

func TestJobReadOverview_NonexistentJob(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("job_read_overview", map[string]string{"id": "nonexistent"})
	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for nonexistent job, got nil")
	}
}

func TestJobReadOverview_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCallRaw("job_read_overview", "not json")
	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

// ============================================================================
// job_read_todos tests
// ============================================================================

func TestJobReadTodos_Success(t *testing.T) {
	te, configDir := newTestExecutor(t)
	makeJobDirWithTodo(t, configDir, "todo-read", []string{"First task", "Second task"})

	call := makeToolCall("job_read_todos", map[string]string{"id": "todo-read"})
	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "First task") {
		t.Errorf("expected result to contain 'First task', got %q", result)
	}
	if !strings.Contains(result, "Second task") {
		t.Errorf("expected result to contain 'Second task', got %q", result)
	}
}

func TestJobReadTodos_NonexistentJob(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("job_read_todos", map[string]string{"id": "nonexistent"})
	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for nonexistent job, got nil")
	}
}

func TestJobReadTodos_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCallRaw("job_read_todos", "bad")
	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

// ============================================================================
// job_update_overview tests
// ============================================================================

func TestJobUpdateOverview_Overwrite(t *testing.T) {
	te, configDir := newTestExecutor(t)
	makeJobDir(t, configDir, "update-ow", "active", "")

	call := makeToolCall("job_update_overview", map[string]string{
		"id":      "update-ow",
		"content": "New content here",
		"mode":    "overwrite",
	})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %q", result)
	}

	// Verify the file was overwritten.
	data, err := os.ReadFile(filepath.Join(configDir, "jobs", "update-ow", "OVERVIEW.md"))
	if err != nil {
		t.Fatalf("reading OVERVIEW.md: %v", err)
	}
	if string(data) != "New content here" {
		t.Errorf("expected overwritten content, got %q", string(data))
	}
}

func TestJobUpdateOverview_Append(t *testing.T) {
	te, configDir := newTestExecutor(t)
	makeJobDir(t, configDir, "update-ap", "active", "")

	call := makeToolCall("job_update_overview", map[string]string{
		"id":      "update-ap",
		"content": "Appended text",
		"mode":    "append",
	})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %q", result)
	}

	// Verify the file was appended to.
	data, err := os.ReadFile(filepath.Join(configDir, "jobs", "update-ap", "OVERVIEW.md"))
	if err != nil {
		t.Fatalf("reading OVERVIEW.md: %v", err)
	}
	if !strings.Contains(string(data), "Appended text") {
		t.Errorf("expected appended content, got %q", string(data))
	}
}

func TestJobUpdateOverview_InvalidMode(t *testing.T) {
	te, configDir := newTestExecutor(t)
	makeJobDir(t, configDir, "update-bad", "active", "")

	call := makeToolCall("job_update_overview", map[string]string{
		"id":      "update-bad",
		"content": "content",
		"mode":    "delete",
	})

	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for invalid mode, got nil")
	}
	if !strings.Contains(err.Error(), "invalid mode") {
		t.Errorf("expected error about invalid mode, got: %v", err)
	}
}

func TestJobUpdateOverview_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCallRaw("job_update_overview", "bad")
	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

func TestJobUpdateOverview_NonexistentJob(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("job_update_overview", map[string]string{
		"id":      "nonexistent",
		"content": "content",
		"mode":    "overwrite",
	})

	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for nonexistent job, got nil")
	}
}

// ============================================================================
// job_add_todo tests
// ============================================================================

func TestJobAddTodo_Success(t *testing.T) {
	te, configDir := newTestExecutor(t)
	makeJobDirWithTodo(t, configDir, "add-todo", nil)

	call := makeToolCall("job_add_todo", map[string]string{
		"id":   "add-todo",
		"task": "Write more tests",
	})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %q", result)
	}

	// Verify the TODO was added.
	data, err := os.ReadFile(filepath.Join(configDir, "jobs", "add-todo", "TODO.md"))
	if err != nil {
		t.Fatalf("reading TODO.md: %v", err)
	}
	if !strings.Contains(string(data), "- [ ] Write more tests") {
		t.Errorf("expected todo item in file, got %q", string(data))
	}
}

func TestJobAddTodo_NonexistentJob(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("job_add_todo", map[string]string{
		"id":   "nonexistent",
		"task": "Should fail",
	})

	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for nonexistent job, got nil")
	}
}

func TestJobAddTodo_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCallRaw("job_add_todo", "bad")
	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

// ============================================================================
// job_complete_todo tests
// ============================================================================

func TestJobCompleteTodo_ByIndex(t *testing.T) {
	te, configDir := newTestExecutor(t)
	makeJobDirWithTodo(t, configDir, "complete-idx", []string{"First", "Second", "Third"})

	call := makeToolCall("job_complete_todo", map[string]string{
		"id":            "complete-idx",
		"index_or_text": "2",
	})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %q", result)
	}

	// Verify the second item was completed.
	data, err := os.ReadFile(filepath.Join(configDir, "jobs", "complete-idx", "TODO.md"))
	if err != nil {
		t.Fatalf("reading TODO.md: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "- [ ] First") {
		t.Error("first item should remain unchecked")
	}
	if !strings.Contains(content, "- [x] Second") {
		t.Error("second item should be checked")
	}
	if !strings.Contains(content, "- [ ] Third") {
		t.Error("third item should remain unchecked")
	}
}

func TestJobCompleteTodo_ByText(t *testing.T) {
	te, configDir := newTestExecutor(t)
	makeJobDirWithTodo(t, configDir, "complete-txt", []string{"Write tests", "Fix bugs"})

	call := makeToolCall("job_complete_todo", map[string]string{
		"id":            "complete-txt",
		"index_or_text": "Fix bugs",
	})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "ok" {
		t.Errorf("expected 'ok', got %q", result)
	}

	data, err := os.ReadFile(filepath.Join(configDir, "jobs", "complete-txt", "TODO.md"))
	if err != nil {
		t.Fatalf("reading TODO.md: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "- [x] Fix bugs") {
		t.Error("'Fix bugs' should be checked")
	}
	if !strings.Contains(content, "- [ ] Write tests") {
		t.Error("'Write tests' should remain unchecked")
	}
}

func TestJobCompleteTodo_NoMatch(t *testing.T) {
	te, configDir := newTestExecutor(t)
	makeJobDirWithTodo(t, configDir, "complete-nomatch", []string{"Only task"})

	call := makeToolCall("job_complete_todo", map[string]string{
		"id":            "complete-nomatch",
		"index_or_text": "nonexistent task",
	})

	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for no matching todo, got nil")
	}
}

func TestJobCompleteTodo_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCallRaw("job_complete_todo", "bad")
	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

// ============================================================================
// list_directory tests
// ============================================================================

func TestListDirectory_Success(t *testing.T) {
	dir := t.TempDir()

	// Create some files and a subdirectory.
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("creating file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatalf("creating subdir: %v", err)
	}

	te, _ := newTestExecutor(t)
	call := makeToolCall("list_directory", map[string]string{"path": dir})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "file.txt") {
		t.Errorf("expected result to contain 'file.txt', got %q", result)
	}
	if !strings.Contains(result, "[file]") {
		t.Errorf("expected result to contain '[file]' prefix, got %q", result)
	}
	if !strings.Contains(result, "subdir/") {
		t.Errorf("expected result to contain 'subdir/', got %q", result)
	}
	if !strings.Contains(result, "[dir]") {
		t.Errorf("expected result to contain '[dir]' prefix, got %q", result)
	}
	if !strings.Contains(result, "5 bytes") {
		t.Errorf("expected result to contain file size '5 bytes', got %q", result)
	}
}

func TestListDirectory_NonexistentPath(t *testing.T) {
	te, _ := newTestExecutor(t)
	call := makeToolCall("list_directory", map[string]string{"path": "/nonexistent/path/xyz"})

	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for nonexistent directory, got nil")
	}
}

func TestListDirectory_EmptyDirectory(t *testing.T) {
	dir := t.TempDir()

	te, _ := newTestExecutor(t)
	call := makeToolCall("list_directory", map[string]string{"path": dir})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result for empty directory, got %q", result)
	}
}

func TestListDirectory_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)
	call := makeToolCallRaw("list_directory", "bad")

	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

// ============================================================================
// list_slots tests
// ============================================================================

func TestListSlots_NilGateway(t *testing.T) {
	te, _ := newTestExecutor(t)
	// Gateway is nil by default from newTestExecutor.

	call := makeToolCall("list_slots", map[string]any{})
	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "gateway not initialized" {
		t.Errorf("expected 'gateway not initialized', got %q", result)
	}
}

func TestListSlots_NoActiveSlots(t *testing.T) {
	te, _ := newTestExecutor(t)
	te.Gateway = &mockSpawner{slotSummaries: nil}

	call := makeToolCall("list_slots", map[string]any{})
	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "no active slots" {
		t.Errorf("expected 'no active slots', got %q", result)
	}
}

func TestListSlots_WithActiveSlots(t *testing.T) {
	te, _ := newTestExecutor(t)
	te.Gateway = &mockSpawner{
		slotSummaries: []orchestration.GatewaySlot{
			{Index: 0, Team: "coding", JobID: "job-1", Status: "running", Elapsed: "2m30s"},
			{Index: 1, Team: "testing", JobID: "job-2", Status: "running", Elapsed: "1m15s"},
		},
	}

	call := makeToolCall("list_slots", map[string]any{})
	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "slot 0: coding on job-1") {
		t.Errorf("expected slot 0 info, got %q", result)
	}
	if !strings.Contains(result, "slot 1: testing on job-2") {
		t.Errorf("expected slot 1 info, got %q", result)
	}
	if !strings.Contains(result, "2m30s") {
		t.Errorf("expected elapsed time, got %q", result)
	}
}

// ============================================================================
// kill_slot tests
// ============================================================================

func TestKillSlot_NilGateway(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("kill_slot", map[string]any{"slot_id": 0})
	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "gateway not initialized" {
		t.Errorf("expected 'gateway not initialized', got %q", result)
	}
}

func TestKillSlot_Success(t *testing.T) {
	killedSlot := -1
	te, _ := newTestExecutor(t)
	te.Gateway = &mockSpawner{
		killFn: func(slotID int) error {
			killedSlot = slotID
			return nil
		},
	}

	call := makeToolCall("kill_slot", map[string]any{"slot_id": 2})
	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "killed slot 2" {
		t.Errorf("expected 'killed slot 2', got %q", result)
	}
	if killedSlot != 2 {
		t.Errorf("expected Kill called with slot 2, got %d", killedSlot)
	}
}

func TestKillSlot_Error(t *testing.T) {
	te, _ := newTestExecutor(t)
	te.Gateway = &mockSpawner{
		killFn: func(slotID int) error {
			return fmt.Errorf("slot %d not found", slotID)
		},
	}

	call := makeToolCall("kill_slot", map[string]any{"slot_id": 99})
	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	// kill_slot returns the error as a result string, not as an error.
	if !strings.Contains(result, "error killing slot 99") {
		t.Errorf("expected error message in result, got %q", result)
	}
}

func TestKillSlot_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)
	te.Gateway = &mockSpawner{}

	call := makeToolCallRaw("kill_slot", "bad")
	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

// ============================================================================
// ask_user tests
// ============================================================================

func TestAskUser_ReturnsFallbackMessage(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("ask_user", map[string]any{
		"question": "Which option?",
		"options":  []string{"A", "B", "C"},
	})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if result != "ask_user was handled by the TUI" {
		t.Errorf("expected fallback message, got %q", result)
	}
}

// ============================================================================
// escalate_to_user tests
// ============================================================================

func TestEscalateToUser_Success(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("escalate_to_user", map[string]string{
		"question": "What should I do?",
		"context":  "The build is failing.",
	})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.HasPrefix(result, "__escalate__:") {
		t.Errorf("expected result to start with '__escalate__:', got %q", result)
	}
	if !strings.Contains(result, "What should I do?") {
		t.Errorf("expected result to contain question, got %q", result)
	}
	if !strings.Contains(result, "The build is failing.") {
		t.Errorf("expected result to contain context, got %q", result)
	}
}

func TestEscalateToUser_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCallRaw("escalate_to_user", "bad")
	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
	if !strings.Contains(err.Error(), "parsing escalate_to_user args") {
		t.Errorf("expected error about parsing args, got: %v", err)
	}
}

// ============================================================================
// task_set_status tests
// ============================================================================

func TestTaskSetStatus_Success(t *testing.T) {
	te, configDir := newTestExecutor(t)

	// Use job.Create to get a real job with a task.
	j, err := job.Create(filepath.Join(configDir), "task-status-job", "Task Status Job", "Testing task status")
	if err != nil {
		t.Fatalf("creating job: %v", err)
	}

	// List tasks to get the task ID.
	tasks, err := job.ListTasks(j.Dir)
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("expected at least one task after job creation")
	}
	taskID := tasks[0].ID

	call := makeToolCall("task_set_status", map[string]string{
		"job_id":  "task-status-job",
		"task_id": taskID,
		"status":  "done",
	})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "status set to done") {
		t.Errorf("expected success message, got %q", result)
	}

	// Verify the task status was updated.
	updatedTask, err := job.LoadTask(tasks[0].Dir)
	if err != nil {
		t.Fatalf("loading updated task: %v", err)
	}
	if updatedTask.Status != job.StatusDone {
		t.Errorf("task status: got %q, want %q", updatedTask.Status, job.StatusDone)
	}
}

func TestTaskSetStatus_InvalidStatus(t *testing.T) {
	te, configDir := newTestExecutor(t)

	j, err := job.Create(filepath.Join(configDir), "task-invalid", "Task Invalid", "Testing invalid status")
	if err != nil {
		t.Fatalf("creating job: %v", err)
	}

	tasks, err := job.ListTasks(j.Dir)
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("expected at least one task")
	}

	call := makeToolCall("task_set_status", map[string]string{
		"job_id":  "task-invalid",
		"task_id": tasks[0].ID,
		"status":  "bogus",
	})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "invalid status") {
		t.Errorf("expected invalid status message, got %q", result)
	}
}

func TestTaskSetStatus_TaskNotFound(t *testing.T) {
	te, configDir := newTestExecutor(t)

	_, err := job.Create(filepath.Join(configDir), "task-notfound", "Task Not Found", "Testing task not found")
	if err != nil {
		t.Fatalf("creating job: %v", err)
	}

	call := makeToolCall("task_set_status", map[string]string{
		"job_id":  "task-notfound",
		"task_id": "nonexistent-uuid",
		"status":  "done",
	})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "not found") {
		t.Errorf("expected 'not found' message, got %q", result)
	}
}

func TestTaskSetStatus_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCallRaw("task_set_status", "bad")
	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

// ============================================================================
// fetch_webpage tests
// ============================================================================

func TestFetchWebpage_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
<h1>Hello World</h1>
<p>This is a test paragraph.</p>
</body>
</html>`)
	}))
	defer srv.Close()

	te, _ := newTestExecutor(t)
	call := makeToolCall("fetch_webpage", map[string]string{"url": srv.URL})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "Hello World") {
		t.Errorf("expected result to contain 'Hello World', got %q", result)
	}
	if !strings.Contains(result, "This is a test paragraph.") {
		t.Errorf("expected result to contain paragraph text, got %q", result)
	}
}

func TestFetchWebpage_StripsScriptAndStyle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html>
<head>
<title>Test</title>
<style>body { color: red; }</style>
<script>alert('xss');</script>
</head>
<body>
<p>Visible text</p>
<script>console.log('hidden');</script>
<style>.hidden { display: none; }</style>
</body>
</html>`)
	}))
	defer srv.Close()

	te, _ := newTestExecutor(t)
	call := makeToolCall("fetch_webpage", map[string]string{"url": srv.URL})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "Visible text") {
		t.Errorf("expected result to contain 'Visible text', got %q", result)
	}
	if strings.Contains(result, "alert") {
		t.Errorf("expected script content to be stripped, got %q", result)
	}
	if strings.Contains(result, "color: red") {
		t.Errorf("expected style content to be stripped, got %q", result)
	}
	if strings.Contains(result, "console.log") {
		t.Errorf("expected inline script to be stripped, got %q", result)
	}
}

func TestFetchWebpage_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	te, _ := newTestExecutor(t)
	call := makeToolCall("fetch_webpage", map[string]string{"url": srv.URL})

	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for 404 response, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected status 404") {
		t.Errorf("expected error about status 404, got: %v", err)
	}
}

func TestFetchWebpage_InvalidURL(t *testing.T) {
	te, _ := newTestExecutor(t)
	call := makeToolCall("fetch_webpage", map[string]string{"url": "http://localhost:1"})

	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for unreachable URL, got nil")
	}
}

func TestFetchWebpage_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)
	call := makeToolCallRaw("fetch_webpage", "bad")

	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

func TestFetchWebpage_CollapsesWhitespace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><body>
<p>Word1</p>


<p>Word2</p>
<p>  Word3  </p>
</body></html>`)
	}))
	defer srv.Close()

	te, _ := newTestExecutor(t)
	call := makeToolCall("fetch_webpage", map[string]string{"url": srv.URL})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	// Whitespace should be collapsed to single spaces.
	if strings.Contains(result, "  ") {
		t.Errorf("expected whitespace to be collapsed, got %q", result)
	}
	if !strings.Contains(result, "Word1") || !strings.Contains(result, "Word2") || !strings.Contains(result, "Word3") {
		t.Errorf("expected all words present, got %q", result)
	}
}

func TestFetchWebpage_TruncatesLongContent(t *testing.T) {
	// Generate content longer than 8000 chars.
	longText := strings.Repeat("A", 9000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, "<html><body><p>%s</p></body></html>", longText)
	}))
	defer srv.Close()

	te, _ := newTestExecutor(t)
	call := makeToolCall("fetch_webpage", map[string]string{"url": srv.URL})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.HasSuffix(result, "...[truncated]") {
		t.Errorf("expected result to end with '...[truncated]', got suffix %q", result[len(result)-20:])
	}
}

// ============================================================================
// assign_team tests
// ============================================================================

func TestAssignTeam_NilGateway(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("assign_team", map[string]string{
		"team_name": "coding",
		"job_id":    "some-job",
		"task":      "Do something",
	})

	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for nil gateway, got nil")
	}
	if !strings.Contains(err.Error(), "gateway not initialized") {
		t.Errorf("expected 'gateway not initialized' error, got: %v", err)
	}
}

func TestAssignTeam_JobDoesNotExist(t *testing.T) {
	te, _ := newTestExecutor(t)
	te.Gateway = &mockSpawner{}
	te.Teams = []agents.Team{{Name: "coding"}}

	call := makeToolCall("assign_team", map[string]string{
		"team_name": "coding",
		"job_id":    "nonexistent-job",
		"task":      "Do something",
	})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	// Should return a message (not error) telling the operator to create the job first.
	if !strings.Contains(result, "does not exist") {
		t.Errorf("expected 'does not exist' message, got %q", result)
	}
	if !strings.Contains(result, "job_create") {
		t.Errorf("expected 'job_create' hint, got %q", result)
	}
}

func TestAssignTeam_TeamNotFound(t *testing.T) {
	te, configDir := newTestExecutor(t)
	te.Gateway = &mockSpawner{}
	te.Teams = []agents.Team{{Name: "coding"}}

	makeJobDir(t, configDir, "team-test", "active", "")

	call := makeToolCall("assign_team", map[string]string{
		"team_name": "nonexistent-team",
		"job_id":    "team-test",
		"task":      "Do something",
	})

	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for nonexistent team, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestAssignTeam_SuccessfulDispatch(t *testing.T) {
	te, configDir := newTestExecutor(t)

	spawnCalled := false
	te.Gateway = &mockSpawner{
		spawnTeamFn: func(teamName, jobID, task string, team agents.Team) (int, bool, error) {
			spawnCalled = true
			if teamName != "coding" {
				t.Errorf("expected team 'coding', got %q", teamName)
			}
			if jobID != "dispatch-job" {
				t.Errorf("expected job 'dispatch-job', got %q", jobID)
			}
			return 1, false, nil
		},
	}
	te.Teams = []agents.Team{{Name: "coding"}}

	makeJobDir(t, configDir, "dispatch-job", "active", "")

	call := makeToolCall("assign_team", map[string]string{
		"team_name": "coding",
		"job_id":    "dispatch-job",
		"task":      "Implement feature X",
	})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !spawnCalled {
		t.Error("expected SpawnTeam to be called")
	}
	if !strings.Contains(result, "started: slot 1") {
		t.Errorf("expected 'started: slot 1', got %q", result)
	}
}

func TestAssignTeam_AlreadyRunning(t *testing.T) {
	te, configDir := newTestExecutor(t)
	te.Gateway = &mockSpawner{
		spawnTeamFn: func(_, _, _ string, _ agents.Team) (int, bool, error) {
			return 3, true, nil
		},
	}
	te.Teams = []agents.Team{{Name: "coding"}}

	makeJobDir(t, configDir, "running-job", "active", "")

	call := makeToolCall("assign_team", map[string]string{
		"team_name": "coding",
		"job_id":    "running-job",
		"task":      "Continue work",
	})

	result, err := te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}
	if !strings.Contains(result, "already running") {
		t.Errorf("expected 'already running' message, got %q", result)
	}
	if !strings.Contains(result, "slot 3") {
		t.Errorf("expected slot 3 in result, got %q", result)
	}
}

func TestAssignTeam_SpawnError(t *testing.T) {
	te, configDir := newTestExecutor(t)
	te.Gateway = &mockSpawner{
		spawnTeamFn: func(_, _, _ string, _ agents.Team) (int, bool, error) {
			return 0, false, fmt.Errorf("no available slots")
		},
	}
	te.Teams = []agents.Team{{Name: "coding"}}

	makeJobDir(t, configDir, "spawn-err-job", "active", "")

	call := makeToolCall("assign_team", map[string]string{
		"team_name": "coding",
		"job_id":    "spawn-err-job",
		"task":      "Do work",
	})

	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error from spawn failure, got nil")
	}
	if !strings.Contains(err.Error(), "spawning team") {
		t.Errorf("expected 'spawning team' error, got: %v", err)
	}
}

func TestAssignTeam_BadJSON(t *testing.T) {
	te, _ := newTestExecutor(t)
	te.Gateway = &mockSpawner{}

	call := makeToolCallRaw("assign_team", "bad")
	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for bad JSON, got nil")
	}
}

func TestAssignTeam_SetsTaskTeam(t *testing.T) {
	te, configDir := newTestExecutor(t)
	te.Gateway = &mockSpawner{
		spawnTeamFn: func(_, _, _ string, _ agents.Team) (int, bool, error) {
			return 0, false, nil
		},
	}
	te.Teams = []agents.Team{{Name: "coding"}}

	// Use job.Create to get a real job with tasks.
	j, err := job.Create(configDir, "team-assign-job", "Team Assign", "Test team assignment")
	if err != nil {
		t.Fatalf("creating job: %v", err)
	}

	call := makeToolCall("assign_team", map[string]string{
		"team_name": "coding",
		"job_id":    "team-assign-job",
		"task":      "Do work",
	})

	_, err = te.ExecuteTool(call)
	if err != nil {
		t.Fatalf("ExecuteTool returned error: %v", err)
	}

	// Verify the team was set on the first task.
	teamName, err := job.GetFirstTaskTeam(j.Dir)
	if err != nil {
		t.Fatalf("getting first task team: %v", err)
	}
	if teamName != "coding" {
		t.Errorf("expected task team 'coding', got %q", teamName)
	}
}

// ============================================================================
// Unknown tool tests
// ============================================================================

func TestUnknownTool_ReturnsError(t *testing.T) {
	te, _ := newTestExecutor(t)

	call := makeToolCall("nonexistent_tool", map[string]any{})
	_, err := te.ExecuteTool(call)
	if err == nil {
		t.Fatal("expected error for unknown tool, got nil")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("expected 'unknown tool' error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "nonexistent_tool") {
		t.Errorf("expected tool name in error, got: %v", err)
	}
}

// ============================================================================
// NewToolExecutor tests
// ============================================================================

func TestNewToolExecutor_SetsFields(t *testing.T) {
	spawner := &mockSpawner{}
	teams := []agents.Team{{Name: "test-team"}}

	te := NewToolExecutor(spawner, teams, "/tmp/workspace", nil, nil)

	if te.Gateway != spawner {
		t.Error("expected Gateway to be set")
	}
	if len(te.Teams) != 1 || te.Teams[0].Name != "test-team" {
		t.Error("expected Teams to be set")
	}
	if te.WorkspaceDir != "/tmp/workspace" {
		t.Errorf("expected WorkspaceDir '/tmp/workspace', got %q", te.WorkspaceDir)
	}
	if len(te.Tools) == 0 {
		t.Error("expected Tools to be populated with static tools")
	}
}
