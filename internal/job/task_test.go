package job

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateTask_CreatesTaskMDWithCorrectFields(t *testing.T) {
	dir := t.TempDir()

	task, err := CreateTask(dir, "My Task", "A description")
	if err != nil {
		t.Fatalf("CreateTask: unexpected error: %v", err)
	}

	if task.ID == "" {
		t.Error("ID: expected non-empty, got empty string")
	}
	if task.Name != "My Task" {
		t.Errorf("Name: got %q, want %q", task.Name, "My Task")
	}
	if task.Description != "A description" {
		t.Errorf("Description: got %q, want %q", task.Description, "A description")
	}
	if task.Status != StatusActive {
		t.Errorf("Status: got %q, want %q", task.Status, StatusActive)
	}
	if task.Team != "" {
		t.Errorf("Team: got %q, want empty string", task.Team)
	}
	if task.Created == "" {
		t.Error("Created: expected non-empty RFC3339 string, got empty")
	}
	if task.Updated == "" {
		t.Error("Updated: expected non-empty RFC3339 string, got empty")
	}

	// Verify Created and Updated are valid RFC3339.
	if _, err := time.Parse(time.RFC3339, task.Created); err != nil {
		t.Errorf("Created %q is not valid RFC3339: %v", task.Created, err)
	}
	if _, err := time.Parse(time.RFC3339, task.Updated); err != nil {
		t.Errorf("Updated %q is not valid RFC3339: %v", task.Updated, err)
	}

	// Verify TASK.md exists at the expected path.
	expectedTaskMD := filepath.Join(dir, "tasks", task.ID, "TASK.md")
	if _, err := os.Stat(expectedTaskMD); os.IsNotExist(err) {
		t.Errorf("TASK.md not found at expected path %q", expectedTaskMD)
	}
}

func TestCreateTask_GeneratesUniqueIDs(t *testing.T) {
	dir := t.TempDir()

	task1, err := CreateTask(dir, "Task One", "First task")
	if err != nil {
		t.Fatalf("CreateTask (first): %v", err)
	}

	task2, err := CreateTask(dir, "Task Two", "Second task")
	if err != nil {
		t.Fatalf("CreateTask (second): %v", err)
	}

	if task1.ID == task2.ID {
		t.Errorf("expected unique IDs, but both tasks have ID %q", task1.ID)
	}
}

func TestLoadTask_ReturnsCorrectTask(t *testing.T) {
	dir := t.TempDir()

	created, err := CreateTask(dir, "Load Me", "Load description")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	loaded, err := LoadTask(created.Dir)
	if err != nil {
		t.Fatalf("LoadTask: %v", err)
	}

	if loaded.ID != created.ID {
		t.Errorf("ID: got %q, want %q", loaded.ID, created.ID)
	}
	if loaded.Name != created.Name {
		t.Errorf("Name: got %q, want %q", loaded.Name, created.Name)
	}
	if loaded.Description != created.Description {
		t.Errorf("Description: got %q, want %q", loaded.Description, created.Description)
	}
	if loaded.Status != created.Status {
		t.Errorf("Status: got %q, want %q", loaded.Status, created.Status)
	}
	if loaded.Team != created.Team {
		t.Errorf("Team: got %q, want %q", loaded.Team, created.Team)
	}
}

func TestLoadTask_MissingFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	// dir exists but has no TASK.md inside it.

	_, err := LoadTask(dir)
	if err == nil {
		t.Fatal("LoadTask: expected error for missing TASK.md, got nil")
	}
}

func TestListTasks_EmptyDir_ReturnsNil(t *testing.T) {
	dir := t.TempDir()
	// No tasks/ subdirectory exists.

	tasks, err := ListTasks(dir)
	if err != nil {
		t.Fatalf("ListTasks: unexpected error: %v", err)
	}
	if tasks != nil {
		t.Errorf("ListTasks: expected nil slice, got %v", tasks)
	}
}

func TestListTasks_ReturnsSortedByCreated(t *testing.T) {
	dir := t.TempDir()

	task1, err := CreateTask(dir, "First Task", "Created first")
	if err != nil {
		t.Fatalf("CreateTask (first): %v", err)
	}

	// Sleep 1 second to ensure distinct RFC3339 timestamps (second-level precision).
	time.Sleep(time.Second)

	task2, err := CreateTask(dir, "Second Task", "Created second")
	if err != nil {
		t.Fatalf("CreateTask (second): %v", err)
	}

	tasks, err := ListTasks(dir)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("ListTasks: got %d tasks, want 2", len(tasks))
	}

	// Verify ascending Created order.
	if tasks[0].ID != task1.ID {
		t.Errorf("tasks[0].ID: got %q, want %q (first created)", tasks[0].ID, task1.ID)
	}
	if tasks[1].ID != task2.ID {
		t.Errorf("tasks[1].ID: got %q, want %q (second created)", tasks[1].ID, task2.ID)
	}
}

func TestSetTaskTeam_UpdatesTeamField(t *testing.T) {
	dir := t.TempDir()

	task, err := CreateTask(dir, "Team Task", "Needs a team")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.Team != "" {
		t.Errorf("Team before SetTaskTeam: got %q, want empty string", task.Team)
	}

	originalUpdated := task.Updated

	// Sleep 1 second so the Updated timestamp will differ.
	time.Sleep(time.Second)

	if err := SetTaskTeam(task.Dir, "my-team"); err != nil {
		t.Fatalf("SetTaskTeam: %v", err)
	}

	reloaded, err := LoadTask(task.Dir)
	if err != nil {
		t.Fatalf("LoadTask after SetTaskTeam: %v", err)
	}

	if reloaded.Team != "my-team" {
		t.Errorf("Team after SetTaskTeam: got %q, want %q", reloaded.Team, "my-team")
	}
	if reloaded.Updated == "" {
		t.Error("Updated: expected non-empty after SetTaskTeam")
	}
	if reloaded.Updated == originalUpdated {
		t.Errorf("Updated timestamp should have changed after SetTaskTeam; still %q", reloaded.Updated)
	}
}

func TestGetFirstTaskTeam_NoTasks_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()

	team, err := GetFirstTaskTeam(dir)
	if err != nil {
		t.Fatalf("GetFirstTaskTeam: unexpected error: %v", err)
	}
	if team != "" {
		t.Errorf("GetFirstTaskTeam: got %q, want empty string", team)
	}
}

func TestGetFirstTaskTeam_ReturnsFirstTaskTeam(t *testing.T) {
	dir := t.TempDir()

	task, err := CreateTask(dir, "Alpha Task", "First task")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	if err := SetTaskTeam(task.Dir, "alpha-team"); err != nil {
		t.Fatalf("SetTaskTeam: %v", err)
	}

	team, err := GetFirstTaskTeam(dir)
	if err != nil {
		t.Fatalf("GetFirstTaskTeam: %v", err)
	}
	if team != "alpha-team" {
		t.Errorf("GetFirstTaskTeam: got %q, want %q", team, "alpha-team")
	}
}

func TestJobCreate_CreatesInitialTask(t *testing.T) {
	configDir := t.TempDir()

	job, err := Create(configDir, "my-job", "My Job", "A job description")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify tasks/ directory exists under the job dir.
	tasksDir := TasksDir(job.Dir)
	if _, err := os.Stat(tasksDir); os.IsNotExist(err) {
		t.Errorf("tasks/ directory not found at %q", tasksDir)
	}

	// Verify ListTasks returns exactly 1 task.
	tasks, err := ListTasks(job.Dir)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("ListTasks: got %d tasks, want 1", len(tasks))
	}

	task := tasks[0]
	if task.Name != "My Job" {
		t.Errorf("task.Name: got %q, want %q", task.Name, "My Job")
	}
	if task.Description != "A job description" {
		t.Errorf("task.Description: got %q, want %q", task.Description, "A job description")
	}
	if task.Status != StatusActive {
		t.Errorf("task.Status: got %q, want %q", task.Status, StatusActive)
	}
}
