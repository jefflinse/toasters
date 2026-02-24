package job

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Status represents the lifecycle state of a job.
type Status string

const (
	StatusActive Status = "active"
	StatusDone   Status = "done"
	StatusPaused Status = "paused"
)

// Frontmatter holds the structured metadata from an OVERVIEW.md file.
type Frontmatter struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Status      Status `yaml:"status"`
	Created     string `yaml:"created"`   // RFC3339
	Updated     string `yaml:"updated"`   // RFC3339
	Completed   string `yaml:"completed"` // RFC3339 or ""
}

// Job represents a single job on disk.
type Job struct {
	Frontmatter
	Dir string // absolute path to the job directory
}

var validID = regexp.MustCompile(`^[a-z0-9-]+$`)

// JobsDir returns the path to the jobs directory within workspaceDir.
func JobsDir(workspaceDir string) string {
	return filepath.Join(workspaceDir, "jobs")
}

// List returns all jobs in workspaceDir, sorted by Created ascending.
// It creates the jobs directory if it does not exist.
func List(workspaceDir string) ([]Job, error) {
	dir := JobsDir(workspaceDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating jobs dir: %w", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading jobs dir: %w", err)
	}

	var jobs []Job
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		j, err := Load(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue // skip entries that fail to load
		}
		jobs = append(jobs, j)
	}

	sort.Slice(jobs, func(i, k int) bool {
		return jobs[i].Created < jobs[k].Created
	})

	return jobs, nil
}

// Load reads and parses the OVERVIEW.md from dir, returning a Job.
func Load(dir string) (Job, error) {
	overviewPath := filepath.Join(dir, "OVERVIEW.md")
	data, err := os.ReadFile(overviewPath)
	if err != nil {
		return Job{}, fmt.Errorf("reading OVERVIEW.md: %w", err)
	}

	fm, _, err := parseFrontmatter(string(data))
	if err != nil {
		return Job{}, fmt.Errorf("parsing frontmatter in %s: %w", overviewPath, err)
	}

	return Job{Frontmatter: fm, Dir: dir}, nil
}

// Create initialises a new job directory with OVERVIEW.md and TODO.md.
func Create(workspaceDir, id, name, description string) (Job, error) {
	if id == "" {
		return Job{}, errors.New("id must not be empty")
	}
	if !validID.MatchString(id) {
		return Job{}, fmt.Errorf("id %q contains invalid characters (only [a-z0-9-] allowed)", id)
	}

	dir := filepath.Join(JobsDir(workspaceDir), id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return Job{}, fmt.Errorf("creating job dir: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	fm := Frontmatter{
		ID:          id,
		Name:        name,
		Description: description,
		Status:      StatusActive,
		Created:     now,
		Updated:     now,
		Completed:   "",
	}

	overview := serializeFrontmatter(fm) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "OVERVIEW.md"), []byte(overview), 0644); err != nil {
		return Job{}, fmt.Errorf("writing OVERVIEW.md: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "TODO.md"), []byte("# TODOs\n"), 0644); err != nil {
		return Job{}, fmt.Errorf("writing TODO.md: %w", err)
	}

	if _, err := CreateTask(dir, name, description); err != nil {
		return Job{}, fmt.Errorf("creating initial task: %w", err)
	}

	return Load(dir)
}

// ReadOverview returns the verbatim contents of OVERVIEW.md.
func ReadOverview(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "OVERVIEW.md"))
	if err != nil {
		return "", fmt.Errorf("reading OVERVIEW.md: %w", err)
	}
	return string(data), nil
}

// WriteOverview overwrites OVERVIEW.md with content verbatim.
func WriteOverview(dir, content string) error {
	if err := os.WriteFile(filepath.Join(dir, "OVERVIEW.md"), []byte(content), 0644); err != nil {
		return fmt.Errorf("writing OVERVIEW.md: %w", err)
	}
	return nil
}

// UpdateFrontmatter reads OVERVIEW.md, applies the given field updates, bumps
// the updated timestamp, and rewrites the file.  Keys in updates must match
// frontmatter field names (id, name, description, status, created, updated,
// completed).
func UpdateFrontmatter(dir string, updates map[string]string) error {
	path := filepath.Join(dir, "OVERVIEW.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading OVERVIEW.md: %w", err)
	}

	fm, body, err := parseFrontmatter(string(data))
	if err != nil {
		return fmt.Errorf("parsing frontmatter: %w", err)
	}

	for k, v := range updates {
		switch k {
		case "id":
			fm.ID = v
		case "name":
			fm.Name = v
		case "description":
			fm.Description = v
		case "status":
			fm.Status = Status(v)
		case "created":
			fm.Created = v
		case "updated":
			fm.Updated = v
		case "completed":
			fm.Completed = v
		}
	}

	fm.Updated = time.Now().UTC().Format(time.RFC3339)

	content := serializeFrontmatter(fm) + body
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing OVERVIEW.md: %w", err)
	}
	return nil
}

// AppendOverview appends content to OVERVIEW.md and bumps the updated timestamp.
func AppendOverview(dir, content string) error {
	path := filepath.Join(dir, "OVERVIEW.md")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening OVERVIEW.md for append: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := fmt.Fprintf(f, "\n%s", content); err != nil {
		return fmt.Errorf("appending to OVERVIEW.md: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing OVERVIEW.md: %w", err)
	}

	return UpdateFrontmatter(dir, nil)
}

// ReadTodos returns the verbatim contents of TODO.md.
func ReadTodos(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "TODO.md"))
	if err != nil {
		return "", fmt.Errorf("reading TODO.md: %w", err)
	}
	return string(data), nil
}

// AddTodo appends an unchecked task line to TODO.md.
func AddTodo(dir, task string) error {
	f, err := os.OpenFile(filepath.Join(dir, "TODO.md"), os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening TODO.md: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := fmt.Fprintf(f, "- [ ] %s\n", task); err != nil {
		return fmt.Errorf("writing to TODO.md: %w", err)
	}
	return nil
}

// CompleteTodo marks a task in TODO.md as done.  indexOrText is either a
// 1-based index among unchecked items or a substring to match against.
func CompleteTodo(dir, indexOrText string) error {
	path := filepath.Join(dir, "TODO.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading TODO.md: %w", err)
	}

	lines := strings.Split(string(data), "\n")

	// Try to parse as 1-based integer index.
	idx, parseErr := strconv.Atoi(indexOrText)
	useIndex := parseErr == nil && idx >= 1

	uncheckedCount := 0
	matched := false
	for i, line := range lines {
		if !strings.HasPrefix(line, "- [ ] ") {
			continue
		}
		uncheckedCount++
		if useIndex {
			if uncheckedCount == idx {
				lines[i] = "- [x] " + line[len("- [ ] "):]
				matched = true
				break
			}
		} else {
			if strings.Contains(line, indexOrText) {
				lines[i] = "- [x] " + line[len("- [ ] "):]
				matched = true
				break
			}
		}
	}

	if !matched {
		return fmt.Errorf("no matching unchecked todo found for %q", indexOrText)
	}

	newContent := strings.Join(lines, "\n")

	// Atomic write via temp file + rename.
	tmp, err := os.CreateTemp(dir, "todo-*.md")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.WriteString(newContent); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming temp file: %w", err)
	}

	return nil
}

// parseFrontmatter splits content into a Frontmatter and the body text that
// follows the closing "---" delimiter.  Returns an error if the frontmatter
// block is absent or malformed.
func parseFrontmatter(content string) (Frontmatter, string, error) {
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
		return Frontmatter{}, "", errors.New("no frontmatter delimiter found")
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
		return Frontmatter{}, "", errors.New("frontmatter closing delimiter not found")
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

	fm := Frontmatter{
		ID:          kv["id"],
		Name:        kv["name"],
		Description: kv["description"],
		Status:      Status(kv["status"]),
		Created:     kv["created"],
		Updated:     kv["updated"],
		Completed:   kv["completed"],
	}

	body := strings.Join(lines[end+1:], "\n")
	return fm, body, nil
}

// serializeFrontmatter renders a Frontmatter block as a YAML-fenced string
// (including the trailing "---\n").
func serializeFrontmatter(fm Frontmatter) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("id: " + fm.ID + "\n")
	sb.WriteString("name: " + fm.Name + "\n")
	sb.WriteString("description: " + fm.Description + "\n")
	sb.WriteString("status: " + string(fm.Status) + "\n")
	sb.WriteString("created: " + fm.Created + "\n")
	sb.WriteString("updated: " + fm.Updated + "\n")
	sb.WriteString("completed: " + fm.Completed + "\n")
	sb.WriteString("---")
	return sb.String()
}
