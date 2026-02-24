package job

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TaskFrontmatter holds the structured metadata from a TASK.md file.
type TaskFrontmatter struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Status      Status `yaml:"status"`
	Team        string `yaml:"team"`
	Created     string `yaml:"created"` // RFC3339
	Updated     string `yaml:"updated"` // RFC3339
}

// Task represents a single task on disk.
type Task struct {
	TaskFrontmatter
	Dir string // absolute path to the task subdirectory
}

// TasksDir returns the path to the tasks directory within jobDir.
func TasksDir(jobDir string) string {
	return filepath.Join(jobDir, "tasks")
}

// newUUID generates a random UUID v4.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// parseTaskFrontmatter splits content into a TaskFrontmatter and the body text
// that follows the closing "---" delimiter. Returns an error if the frontmatter
// block is absent or malformed.
func parseTaskFrontmatter(content string) (TaskFrontmatter, string, error) {
	lines := strings.Split(content, "\n")

	// Find opening "---".
	start := -1
	for i, l := range lines {
		if l == "---" {
			start = i
			break
		}
	}
	if start == -1 {
		return TaskFrontmatter{}, "", errors.New("no frontmatter delimiter found")
	}

	// Find closing "---".
	end := -1
	for i := start + 1; i < len(lines); i++ {
		if lines[i] == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return TaskFrontmatter{}, "", errors.New("frontmatter closing delimiter not found")
	}

	fmLines := lines[start+1 : end]
	kv := make(map[string]string, len(fmLines))
	for _, l := range fmLines {
		if l == "" {
			continue
		}
		parts := strings.SplitN(l, ": ", 2)
		if len(parts) != 2 {
			// Allow keys with empty values written as "key: " or just "key:"
			key := strings.TrimSuffix(strings.TrimSpace(l), ":")
			kv[key] = ""
			continue
		}
		kv[strings.TrimSpace(parts[0])] = parts[1]
	}

	fm := TaskFrontmatter{
		ID:          kv["id"],
		Name:        kv["name"],
		Description: kv["description"],
		Status:      Status(kv["status"]),
		Team:        kv["team"],
		Created:     kv["created"],
		Updated:     kv["updated"],
	}

	body := strings.Join(lines[end+1:], "\n")
	return fm, body, nil
}

// serializeTaskFrontmatter renders a TaskFrontmatter block as a YAML-fenced
// string (including the trailing "---\n"). Always writes team: even if empty.
func serializeTaskFrontmatter(fm TaskFrontmatter) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("id: " + fm.ID + "\n")
	sb.WriteString("name: " + fm.Name + "\n")
	sb.WriteString("description: " + fm.Description + "\n")
	sb.WriteString("status: " + string(fm.Status) + "\n")
	sb.WriteString("team: " + fm.Team + "\n")
	sb.WriteString("created: " + fm.Created + "\n")
	sb.WriteString("updated: " + fm.Updated + "\n")
	sb.WriteString("---")
	return sb.String()
}

// CreateTask creates a new task directory under jobDir/tasks/<uuid>/ and writes
// TASK.md with the given name and description.
func CreateTask(jobDir, name, description string) (Task, error) {
	id := newUUID()
	taskDir := filepath.Join(TasksDir(jobDir), id)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		return Task{}, fmt.Errorf("creating task dir: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	fm := TaskFrontmatter{
		ID:          id,
		Name:        name,
		Description: description,
		Status:      StatusActive,
		Team:        "",
		Created:     now,
		Updated:     now,
	}

	content := serializeTaskFrontmatter(fm) + "\n"
	if err := os.WriteFile(filepath.Join(taskDir, "TASK.md"), []byte(content), 0644); err != nil {
		return Task{}, fmt.Errorf("writing TASK.md: %w", err)
	}

	return LoadTask(taskDir)
}

// LoadTask reads and parses TASK.md from taskDir, returning a Task.
func LoadTask(taskDir string) (Task, error) {
	taskPath := filepath.Join(taskDir, "TASK.md")
	data, err := os.ReadFile(taskPath)
	if err != nil {
		return Task{}, fmt.Errorf("reading TASK.md: %w", err)
	}

	fm, _, err := parseTaskFrontmatter(string(data))
	if err != nil {
		return Task{}, fmt.Errorf("parsing frontmatter in %s: %w", taskPath, err)
	}

	return Task{TaskFrontmatter: fm, Dir: taskDir}, nil
}

// ListTasks returns all tasks in jobDir/tasks/, sorted by Created ascending.
// If the tasks directory does not exist, returns an empty slice and nil error.
func ListTasks(jobDir string) ([]Task, error) {
	tasksDir := TasksDir(jobDir)
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading tasks dir: %w", err)
	}

	var tasks []Task
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		t, err := LoadTask(filepath.Join(tasksDir, entry.Name()))
		if err != nil {
			continue // skip entries that fail to load
		}
		tasks = append(tasks, t)
	}

	sort.Slice(tasks, func(i, k int) bool {
		return tasks[i].Created < tasks[k].Created
	})

	return tasks, nil
}

// SetTaskTeam reads TASK.md, updates the team field and updated timestamp, and
// rewrites TASK.md.
func SetTaskTeam(taskDir, team string) error {
	taskPath := filepath.Join(taskDir, "TASK.md")
	data, err := os.ReadFile(taskPath)
	if err != nil {
		return fmt.Errorf("reading TASK.md: %w", err)
	}

	fm, _, err := parseTaskFrontmatter(string(data))
	if err != nil {
		return fmt.Errorf("parsing frontmatter: %w", err)
	}

	fm.Team = team
	fm.Updated = time.Now().UTC().Format(time.RFC3339)

	content := serializeTaskFrontmatter(fm) + "\n"
	if err := os.WriteFile(taskPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing TASK.md: %w", err)
	}
	return nil
}

// SetTaskStatus reads TASK.md, updates the status field and updated timestamp,
// and rewrites TASK.md.
func SetTaskStatus(taskDir string, status Status) error {
	taskPath := filepath.Join(taskDir, "TASK.md")
	data, err := os.ReadFile(taskPath)
	if err != nil {
		return fmt.Errorf("reading TASK.md: %w", err)
	}

	fm, _, err := parseTaskFrontmatter(string(data))
	if err != nil {
		return fmt.Errorf("parsing frontmatter: %w", err)
	}

	fm.Status = status
	fm.Updated = time.Now().UTC().Format(time.RFC3339)

	content := serializeTaskFrontmatter(fm) + "\n"
	if err := os.WriteFile(taskPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing TASK.md: %w", err)
	}
	return nil
}

// GetFirstTaskTeam returns the Team field of the first task in jobDir (sorted
// by Created ascending). Returns "", nil if no tasks exist.
func GetFirstTaskTeam(jobDir string) (string, error) {
	tasks, err := ListTasks(jobDir)
	if err != nil {
		return "", err
	}
	if len(tasks) == 0 {
		return "", nil
	}
	return tasks[0].Team, nil
}
