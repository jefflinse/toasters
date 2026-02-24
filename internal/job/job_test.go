package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeOverviewMD writes a minimal OVERVIEW.md into dir and returns the path.
// The caller controls the frontmatter fields via the content string.
func writeOverviewMD(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "OVERVIEW.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing OVERVIEW.md: %v", err)
	}
	return path
}

// readFrontmatter is a test helper that loads the OVERVIEW.md from dir and
// returns the parsed Frontmatter, failing the test on any error.
func readFrontmatter(t *testing.T, dir string) Frontmatter {
	t.Helper()
	j, err := Load(dir)
	if err != nil {
		t.Fatalf("Load after UpdateFrontmatter: %v", err)
	}
	return j.Frontmatter
}

// TestUpdateFrontmatter_SetCompleted verifies that passing a "completed" key
// in the updates map writes the value into the OVERVIEW.md frontmatter.
func TestUpdateFrontmatter_SetCompleted(t *testing.T) {
	dir := t.TempDir()
	writeOverviewMD(t, dir, `---
id: test-job
name: Test Job
description: A test job
status: active
created: 2026-01-01T00:00:00Z
updated: 2026-01-01T00:00:00Z
completed:
---

Some body text.
`)

	const wantCompleted = "2026-01-01T00:00:00Z"
	if err := UpdateFrontmatter(dir, map[string]string{"completed": wantCompleted}); err != nil {
		t.Fatalf("UpdateFrontmatter: %v", err)
	}

	fm := readFrontmatter(t, dir)
	if fm.Completed != wantCompleted {
		t.Errorf("Completed: got %q, want %q", fm.Completed, wantCompleted)
	}
}

// TestUpdateFrontmatter_UpdatedAlwaysBumped verifies that UpdateFrontmatter
// always advances the "updated" timestamp, even when the change is unrelated.
func TestUpdateFrontmatter_UpdatedAlwaysBumped(t *testing.T) {
	dir := t.TempDir()

	const originalUpdated = "2025-06-15T10:00:00Z"
	writeOverviewMD(t, dir, `---
id: bump-job
name: Bump Job
description: Testing updated bump
status: active
created: 2025-06-15T10:00:00Z
updated: `+originalUpdated+`
completed:
---
`)

	// Parse the original timestamp so we can compare after the call.
	origTime, err := time.Parse(time.RFC3339, originalUpdated)
	if err != nil {
		t.Fatalf("parsing original updated timestamp: %v", err)
	}

	if err := UpdateFrontmatter(dir, map[string]string{"status": "done"}); err != nil {
		t.Fatalf("UpdateFrontmatter: %v", err)
	}

	fm := readFrontmatter(t, dir)

	if fm.Updated == originalUpdated {
		t.Errorf("updated: expected timestamp to change from %q, but it did not", originalUpdated)
	}

	newTime, err := time.Parse(time.RFC3339, fm.Updated)
	if err != nil {
		t.Errorf("updated %q is not valid RFC3339: %v", fm.Updated, err)
	}

	if !newTime.After(origTime) {
		t.Errorf("updated: new timestamp %q is not after original %q", fm.Updated, originalUpdated)
	}
}

// TestUpdateFrontmatter_NilMapNoOp verifies that passing a nil updates map
// does not clear existing frontmatter fields — in particular, "completed".
func TestUpdateFrontmatter_NilMapNoOp(t *testing.T) {
	dir := t.TempDir()

	const wantCompleted = "2026-01-01T00:00:00Z"
	writeOverviewMD(t, dir, `---
id: nil-map-job
name: Nil Map Job
description: Testing nil map behaviour
status: active
created: 2026-01-01T00:00:00Z
updated: 2026-01-01T00:00:00Z
completed: `+wantCompleted+`
---
`)

	if err := UpdateFrontmatter(dir, nil); err != nil {
		t.Fatalf("UpdateFrontmatter(nil): %v", err)
	}

	fm := readFrontmatter(t, dir)

	// completed must be preserved; updated is allowed to change.
	if fm.Completed != wantCompleted {
		t.Errorf("Completed: got %q, want %q — nil map must not clear existing fields", fm.Completed, wantCompleted)
	}
}

// --- List tests ---

func TestList_EmptyWorkspace(t *testing.T) {
	ws := t.TempDir()
	jobs, err := List(ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("expected 0 jobs, got %d", len(jobs))
	}
	// Verify jobs/ directory was created.
	if _, err := os.Stat(JobsDir(ws)); os.IsNotExist(err) {
		t.Error("expected jobs/ directory to be created")
	}
}

func TestList_ReturnsJobsSortedByCreated(t *testing.T) {
	ws := t.TempDir()

	// Create two jobs with known timestamps.
	jobDir1 := filepath.Join(JobsDir(ws), "job-older")
	jobDir2 := filepath.Join(JobsDir(ws), "job-newer")
	if err := os.MkdirAll(jobDir1, 0755); err != nil {
		t.Fatalf("creating job dir 1: %v", err)
	}
	if err := os.MkdirAll(jobDir2, 0755); err != nil {
		t.Fatalf("creating job dir 2: %v", err)
	}

	writeOverviewMD(t, jobDir1, `---
id: job-older
name: Older Job
description: Created first
status: active
created: 2025-01-01T00:00:00Z
updated: 2025-01-01T00:00:00Z
completed:
---
`)
	writeOverviewMD(t, jobDir2, `---
id: job-newer
name: Newer Job
description: Created second
status: active
created: 2025-06-01T00:00:00Z
updated: 2025-06-01T00:00:00Z
completed:
---
`)

	jobs, err := List(ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}
	if jobs[0].ID != "job-older" {
		t.Errorf("jobs[0].ID = %q, want %q (older first)", jobs[0].ID, "job-older")
	}
	if jobs[1].ID != "job-newer" {
		t.Errorf("jobs[1].ID = %q, want %q (newer second)", jobs[1].ID, "job-newer")
	}
}

func TestList_SkipsMalformedEntries(t *testing.T) {
	ws := t.TempDir()

	// Create a valid job.
	goodDir := filepath.Join(JobsDir(ws), "good-job")
	if err := os.MkdirAll(goodDir, 0755); err != nil {
		t.Fatalf("creating good job dir: %v", err)
	}
	writeOverviewMD(t, goodDir, `---
id: good-job
name: Good Job
description: Valid
status: active
created: 2025-01-01T00:00:00Z
updated: 2025-01-01T00:00:00Z
completed:
---
`)

	// Create a malformed job (no OVERVIEW.md).
	badDir := filepath.Join(JobsDir(ws), "bad-job")
	if err := os.MkdirAll(badDir, 0755); err != nil {
		t.Fatalf("creating bad job dir: %v", err)
	}

	// Create a non-directory entry (should be skipped).
	if err := os.WriteFile(filepath.Join(JobsDir(ws), "not-a-dir.txt"), []byte("hi"), 0644); err != nil {
		t.Fatalf("writing non-dir file: %v", err)
	}

	jobs, err := List(ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job (malformed and non-dir skipped), got %d", len(jobs))
	}
	if jobs[0].ID != "good-job" {
		t.Errorf("jobs[0].ID = %q, want %q", jobs[0].ID, "good-job")
	}
}

// --- ReadOverview / WriteOverview tests ---

func TestReadOverview(t *testing.T) {
	dir := t.TempDir()
	content := "---\nid: test\n---\nSome overview content."
	writeOverviewMD(t, dir, content)

	got, err := ReadOverview(dir)
	if err != nil {
		t.Fatalf("ReadOverview: %v", err)
	}
	if got != content {
		t.Errorf("ReadOverview: got %q, want %q", got, content)
	}
}

func TestReadOverview_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadOverview(dir)
	if err == nil {
		t.Error("expected error for missing OVERVIEW.md, got nil")
	}
}

func TestWriteOverview(t *testing.T) {
	dir := t.TempDir()
	// Create initial file so we can verify overwrite.
	writeOverviewMD(t, dir, "old content")

	newContent := "---\nid: new\n---\nNew overview."
	if err := WriteOverview(dir, newContent); err != nil {
		t.Fatalf("WriteOverview: %v", err)
	}

	got, err := ReadOverview(dir)
	if err != nil {
		t.Fatalf("ReadOverview after WriteOverview: %v", err)
	}
	if got != newContent {
		t.Errorf("got %q, want %q", got, newContent)
	}
}

// --- AppendOverview tests ---

func TestAppendOverview(t *testing.T) {
	dir := t.TempDir()
	writeOverviewMD(t, dir, `---
id: append-job
name: Append Job
description: Testing append
status: active
created: 2025-01-01T00:00:00Z
updated: 2025-01-01T00:00:00Z
completed:
---

Initial body.
`)

	if err := AppendOverview(dir, "Appended text."); err != nil {
		t.Fatalf("AppendOverview: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "OVERVIEW.md"))
	if err != nil {
		t.Fatalf("reading OVERVIEW.md: %v", err)
	}
	content := string(data)

	// The appended text should be present.
	if !strings.Contains(content, "Appended text.") {
		t.Error("appended text not found in OVERVIEW.md")
	}
}

// --- ReadTodos / AddTodo / CompleteTodo tests ---

func TestReadTodos(t *testing.T) {
	dir := t.TempDir()
	todoContent := "# TODOs\n- [ ] First task\n- [ ] Second task\n"
	if err := os.WriteFile(filepath.Join(dir, "TODO.md"), []byte(todoContent), 0644); err != nil {
		t.Fatalf("writing TODO.md: %v", err)
	}

	got, err := ReadTodos(dir)
	if err != nil {
		t.Fatalf("ReadTodos: %v", err)
	}
	if got != todoContent {
		t.Errorf("ReadTodos: got %q, want %q", got, todoContent)
	}
}

func TestReadTodos_MissingFile(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadTodos(dir)
	if err == nil {
		t.Error("expected error for missing TODO.md, got nil")
	}
}

func TestAddTodo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "TODO.md"), []byte("# TODOs\n"), 0644); err != nil {
		t.Fatalf("writing TODO.md: %v", err)
	}

	if err := AddTodo(dir, "Write tests"); err != nil {
		t.Fatalf("AddTodo: %v", err)
	}
	if err := AddTodo(dir, "Run linter"); err != nil {
		t.Fatalf("AddTodo (second): %v", err)
	}

	got, err := ReadTodos(dir)
	if err != nil {
		t.Fatalf("ReadTodos: %v", err)
	}
	if !strings.Contains(got, "- [ ] Write tests") {
		t.Error("missing first todo")
	}
	if !strings.Contains(got, "- [ ] Run linter") {
		t.Error("missing second todo")
	}
}

func TestAddTodo_MissingFile(t *testing.T) {
	dir := t.TempDir()
	err := AddTodo(dir, "Should fail")
	if err == nil {
		t.Error("expected error for missing TODO.md, got nil")
	}
}

func TestCompleteTodo_ByIndex(t *testing.T) {
	dir := t.TempDir()
	todoContent := "# TODOs\n- [ ] First task\n- [ ] Second task\n- [ ] Third task\n"
	if err := os.WriteFile(filepath.Join(dir, "TODO.md"), []byte(todoContent), 0644); err != nil {
		t.Fatalf("writing TODO.md: %v", err)
	}

	// Complete the second unchecked item (1-based index).
	if err := CompleteTodo(dir, "2"); err != nil {
		t.Fatalf("CompleteTodo: %v", err)
	}

	got, err := ReadTodos(dir)
	if err != nil {
		t.Fatalf("ReadTodos: %v", err)
	}
	if !strings.Contains(got, "- [ ] First task") {
		t.Error("first task should remain unchecked")
	}
	if !strings.Contains(got, "- [x] Second task") {
		t.Error("second task should be checked")
	}
	if !strings.Contains(got, "- [ ] Third task") {
		t.Error("third task should remain unchecked")
	}
}

func TestCompleteTodo_ByText(t *testing.T) {
	dir := t.TempDir()
	todoContent := "# TODOs\n- [ ] Write tests\n- [ ] Run linter\n"
	if err := os.WriteFile(filepath.Join(dir, "TODO.md"), []byte(todoContent), 0644); err != nil {
		t.Fatalf("writing TODO.md: %v", err)
	}

	if err := CompleteTodo(dir, "linter"); err != nil {
		t.Fatalf("CompleteTodo: %v", err)
	}

	got, err := ReadTodos(dir)
	if err != nil {
		t.Fatalf("ReadTodos: %v", err)
	}
	if !strings.Contains(got, "- [ ] Write tests") {
		t.Error("first task should remain unchecked")
	}
	if !strings.Contains(got, "- [x] Run linter") {
		t.Error("matched task should be checked")
	}
}

func TestCompleteTodo_NoMatch(t *testing.T) {
	dir := t.TempDir()
	todoContent := "# TODOs\n- [ ] Write tests\n"
	if err := os.WriteFile(filepath.Join(dir, "TODO.md"), []byte(todoContent), 0644); err != nil {
		t.Fatalf("writing TODO.md: %v", err)
	}

	err := CompleteTodo(dir, "nonexistent task")
	if err == nil {
		t.Error("expected error for no matching todo, got nil")
	}
}

func TestCompleteTodo_MissingFile(t *testing.T) {
	dir := t.TempDir()
	err := CompleteTodo(dir, "1")
	if err == nil {
		t.Error("expected error for missing TODO.md, got nil")
	}
}

// --- Create tests ---

func TestCreate_InvalidID(t *testing.T) {
	ws := t.TempDir()

	tests := []struct {
		name string
		id   string
	}{
		{"empty ID", ""},
		{"uppercase", "MyJob"},
		{"spaces", "my job"},
		{"special chars", "my_job!"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Create(ws, tt.id, "Name", "Desc")
			if err == nil {
				t.Errorf("Create(%q): expected error, got nil", tt.id)
			}
		})
	}
}

func TestCreate_ValidJob(t *testing.T) {
	ws := t.TempDir()

	j, err := Create(ws, "test-job", "Test Job", "A test description")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if j.ID != "test-job" {
		t.Errorf("ID: got %q, want %q", j.ID, "test-job")
	}
	if j.Name != "Test Job" {
		t.Errorf("Name: got %q, want %q", j.Name, "Test Job")
	}
	if j.Description != "A test description" {
		t.Errorf("Description: got %q, want %q", j.Description, "A test description")
	}
	if j.Status != StatusActive {
		t.Errorf("Status: got %q, want %q", j.Status, StatusActive)
	}

	// Verify OVERVIEW.md exists.
	if _, err := os.Stat(filepath.Join(j.Dir, "OVERVIEW.md")); os.IsNotExist(err) {
		t.Error("OVERVIEW.md not found")
	}
	// Verify TODO.md exists.
	if _, err := os.Stat(filepath.Join(j.Dir, "TODO.md")); os.IsNotExist(err) {
		t.Error("TODO.md not found")
	}
}

// --- Load tests ---

func TestLoad_MissingOverview(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(dir)
	if err == nil {
		t.Error("expected error for missing OVERVIEW.md, got nil")
	}
}

func TestLoad_MalformedFrontmatter(t *testing.T) {
	dir := t.TempDir()
	// Write a file with no frontmatter delimiters.
	if err := os.WriteFile(filepath.Join(dir, "OVERVIEW.md"), []byte("no frontmatter here"), 0644); err != nil {
		t.Fatalf("writing OVERVIEW.md: %v", err)
	}
	_, err := Load(dir)
	if err == nil {
		t.Error("expected error for malformed frontmatter, got nil")
	}
}

// --- UpdateFrontmatter edge cases ---

func TestUpdateFrontmatter_MissingFile(t *testing.T) {
	dir := t.TempDir()
	err := UpdateFrontmatter(dir, map[string]string{"status": "done"})
	if err == nil {
		t.Error("expected error for missing OVERVIEW.md, got nil")
	}
}

func TestUpdateFrontmatter_MultipleFields(t *testing.T) {
	dir := t.TempDir()
	writeOverviewMD(t, dir, `---
id: multi-job
name: Original Name
description: Original Desc
status: active
created: 2025-01-01T00:00:00Z
updated: 2025-01-01T00:00:00Z
completed:
---
`)

	if err := UpdateFrontmatter(dir, map[string]string{
		"name":        "Updated Name",
		"description": "Updated Desc",
		"status":      "done",
	}); err != nil {
		t.Fatalf("UpdateFrontmatter: %v", err)
	}

	fm := readFrontmatter(t, dir)
	if fm.Name != "Updated Name" {
		t.Errorf("Name: got %q, want %q", fm.Name, "Updated Name")
	}
	if fm.Description != "Updated Desc" {
		t.Errorf("Description: got %q, want %q", fm.Description, "Updated Desc")
	}
	if fm.Status != StatusDone {
		t.Errorf("Status: got %q, want %q", fm.Status, StatusDone)
	}
}
