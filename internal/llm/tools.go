package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/job"
)

// GatewaySlot holds a summary of a single gateway slot for operator visibility.
type GatewaySlot struct {
	Index   int
	Team    string
	JobID   string
	Status  string // "running", "done", "idle"
	Elapsed string
}

// AgentSpawner is the interface satisfied by *gateway.Gateway.
// Using an interface here avoids an import cycle (gateway imports llm).
type AgentSpawner interface {
	SpawnTeam(teamName, jobID, task string, team agents.Team) (slotID int, alreadyRunning bool, err error)
	SlotSummaries() []GatewaySlot
	Kill(slotID int) error
}

// ToolExecutor holds the dependencies needed to execute operator tool calls.
type ToolExecutor struct {
	Gateway      AgentSpawner
	Teams        []agents.Team
	WorkspaceDir string
	Tools        []Tool
}

// NewToolExecutor creates a ToolExecutor with the default static tools.
func NewToolExecutor(gateway AgentSpawner, teams []agents.Team, workspaceDir string) *ToolExecutor {
	return &ToolExecutor{
		Gateway:      gateway,
		Teams:        teams,
		WorkspaceDir: workspaceDir,
		Tools:        staticTools,
	}
}

// staticTools contains all tools available to the operator LLM.
var staticTools = []Tool{
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "assign_team",
			Description: "Assign a task to a team to work on autonomously. The job_id must be the ID of an existing job — call job_create first if no job exists yet.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"team_name": map[string]any{
						"type":        "string",
						"description": "The name of the team to assign the task to.",
					},
					"job_id": map[string]any{
						"type":        "string",
						"description": "The ID of the job this task belongs to.",
					},
					"task": map[string]any{
						"type":        "string",
						"description": "A clear description of what the team should accomplish.",
					},
				},
				"required": []string{"team_name", "job_id", "task"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "escalate_to_user",
			Description: "Surface a blocker or question to the user that requires human input before work can continue.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"question": map[string]any{
						"type":        "string",
						"description": "The question or blocker to present to the user.",
					},
					"context": map[string]any{
						"type":        "string",
						"description": "Additional context about why this is blocking.",
					},
				},
				"required": []string{"question", "context"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "fetch_webpage",
			Description: "Fetches the content of a web page and returns it as plain text.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "The URL of the web page to fetch.",
					},
				},
				"required": []string{"url"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "list_directory",
			Description: "Lists the contents of a local directory.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "The absolute or relative path to the directory.",
					},
				},
				"required": []string{"path"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "job_list",
			Description: "List all jobs.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "job_create",
			Description: "Create a new job.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":          map[string]any{"type": "string", "description": "Slug identifier (lowercase letters, digits, hyphens only, e.g. 'auth-refactor')."},
					"name":        map[string]any{"type": "string", "description": "Human-readable name."},
					"description": map[string]any{"type": "string", "description": "1-3 sentence summary of the job."},
				},
				"required": []string{"id", "name", "description"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "job_read_overview",
			Description: "Read the OVERVIEW.md file for a job.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string", "description": "The job ID."},
				},
				"required": []string{"id"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "job_read_todos",
			Description: "Read the TODO.md file for a job.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string", "description": "The job ID."},
				},
				"required": []string{"id"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "job_update_overview",
			Description: "Overwrite or append to the OVERVIEW.md body for a job. Does not touch frontmatter.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":      map[string]any{"type": "string", "description": "The job ID."},
					"content": map[string]any{"type": "string", "description": "Markdown content to write."},
					"mode":    map[string]any{"type": "string", "enum": []string{"overwrite", "append"}, "description": "Whether to overwrite or append."},
				},
				"required": []string{"id", "content", "mode"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "job_add_todo",
			Description: "Append a new TODO item to the TODO.md file for a job.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":   map[string]any{"type": "string", "description": "The job ID."},
					"task": map[string]any{"type": "string", "description": "Task description (plain text)."},
				},
				"required": []string{"id", "task"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "job_complete_todo",
			Description: "Mark a TODO item as done in the TODO.md file for a job.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":            map[string]any{"type": "string", "description": "The job ID."},
					"index_or_text": map[string]any{"type": "string", "description": "1-based index of the TODO item, or a substring of the task text to match."},
				},
				"required": []string{"id", "index_or_text"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "ask_user",
			Description: "Pause execution and ask the user a question with a set of options to choose from. Use this when you need the user to make a decision before proceeding. The user can select one of the provided options or type a custom response.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"question": map[string]any{
						"type":        "string",
						"description": "The question to ask the user.",
					},
					"options": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "A list of options for the user to choose from. A 'Custom response...' option is always appended automatically.",
					},
				},
				"required": []string{"question", "options"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "list_slots",
			Description: "List all gateway slots with their current status, team, job, and elapsed time.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "kill_slot",
			Description: "Kill a running agent slot by its slot index. Use list_slots to find the slot index.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"slot_id": map[string]any{
						"type":        "integer",
						"description": "The index of the slot to kill.",
					},
				},
				"required": []string{"slot_id"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "task_set_status",
			Description: "Update the status of a specific task within a job. Valid statuses: active, done, paused.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"job_id": map[string]any{
						"type":        "string",
						"description": "The job ID.",
					},
					"task_id": map[string]any{
						"type":        "string",
						"description": "The task UUID.",
					},
					"status": map[string]any{
						"type":        "string",
						"description": "The new status: active, done, or paused.",
						"enum":        []string{"active", "done", "paused"},
					},
				},
				"required": []string{"job_id", "task_id", "status"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "job_set_status",
			Description: "Update the status of a job. Valid statuses: active, done, cancelled, paused.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "The job ID.",
					},
					"status": map[string]any{
						"type":        "string",
						"description": "The new status: active, done, cancelled, or paused.",
						"enum":        []string{"active", "done", "cancelled", "paused"},
					},
				},
				"required": []string{"id", "status"},
			},
		},
	},
}

// ExecuteTool dispatches a tool call to the appropriate handler and returns
// the result as plain text.
func (te *ToolExecutor) ExecuteTool(call ToolCall) (string, error) {
	switch call.Function.Name {
	case "fetch_webpage":
		var args struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing fetch_webpage args: %w", err)
		}
		return fetchWebpage(args.URL)
	case "list_directory":
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing list_directory args: %w", err)
		}
		return listDirectory(args.Path)
	case "job_list":
		jobs, err := job.List(te.WorkspaceDir)
		if err != nil {
			return "", fmt.Errorf("listing jobs: %w", err)
		}
		type item struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Status      string `json:"status"`
		}
		items := make([]item, len(jobs))
		for i, j := range jobs {
			items[i] = item{ID: j.ID, Name: j.Name, Description: j.Description, Status: string(j.Status)}
		}
		b, _ := json.Marshal(items)
		return string(b), nil

	case "job_create":
		var args struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing job_create args: %w", err)
		}
		j, err := job.Create(te.WorkspaceDir, args.ID, args.Name, args.Description)
		if err != nil {
			return "", fmt.Errorf("creating job: %w", err)
		}
		return "created: " + j.ID, nil

	case "job_read_overview":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing job_read_overview args: %w", err)
		}
		dir := filepath.Join(job.JobsDir(te.WorkspaceDir), args.ID)
		return job.ReadOverview(dir)

	case "job_read_todos":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing job_read_todos args: %w", err)
		}
		dir := filepath.Join(job.JobsDir(te.WorkspaceDir), args.ID)
		return job.ReadTodos(dir)

	case "job_update_overview":
		var args struct {
			ID      string `json:"id"`
			Content string `json:"content"`
			Mode    string `json:"mode"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing job_update_overview args: %w", err)
		}
		if args.Mode != "overwrite" && args.Mode != "append" {
			return "", fmt.Errorf("invalid mode %q: must be 'overwrite' or 'append'", args.Mode)
		}
		dir := filepath.Join(job.JobsDir(te.WorkspaceDir), args.ID)
		var overviewErr error
		if args.Mode == "overwrite" {
			overviewErr = job.WriteOverview(dir, args.Content)
		} else {
			overviewErr = job.AppendOverview(dir, args.Content)
		}
		if overviewErr != nil {
			return "", overviewErr
		}
		return "ok", nil

	case "job_add_todo":
		var args struct {
			ID   string `json:"id"`
			Task string `json:"task"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing job_add_todo args: %w", err)
		}
		dir := filepath.Join(job.JobsDir(te.WorkspaceDir), args.ID)
		if err := job.AddTodo(dir, args.Task); err != nil {
			return "", err
		}
		return "ok", nil

	case "job_complete_todo":
		var args struct {
			ID          string `json:"id"`
			IndexOrText string `json:"index_or_text"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing job_complete_todo args: %w", err)
		}
		dir := filepath.Join(job.JobsDir(te.WorkspaceDir), args.ID)
		if err := job.CompleteTodo(dir, args.IndexOrText); err != nil {
			return "", err
		}
		return "ok", nil

	case "assign_team":
		if te.Gateway == nil {
			return "", fmt.Errorf("gateway not initialized")
		}
		var args struct {
			TeamName string `json:"team_name"`
			JobID    string `json:"job_id"`
			Task     string `json:"task"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing assign_team args: %w", err)
		}
		// Guard: verify the job exists before dispatching to a team.
		jobDir := filepath.Join(job.JobsDir(te.WorkspaceDir), args.JobID)
		if _, loadErr := job.Load(jobDir); loadErr != nil {
			return fmt.Sprintf("job %q does not exist; call job_create first", args.JobID), nil
		}
		// Look up team by name.
		var team agents.Team
		found := false
		for _, t := range te.Teams {
			if t.Name == args.TeamName {
				team = t
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("team %q not found", args.TeamName)
		}
		// Persist team assignment to the first task.
		if tasks, err := job.ListTasks(jobDir); err == nil && len(tasks) > 0 {
			_ = job.SetTaskTeam(tasks[0].Dir, args.TeamName)
		}
		slotID, alreadyRunning, err := te.Gateway.SpawnTeam(args.TeamName, args.JobID, args.Task, team)
		if err != nil {
			return "", fmt.Errorf("spawning team: %w", err)
		}
		if alreadyRunning {
			return fmt.Sprintf("already running: slot %d (do not call assign_team again for this team)", slotID), nil
		}
		return fmt.Sprintf("started: slot %d", slotID), nil

	case "escalate_to_user":
		// The TUI intercepts escalate_to_user before ExecuteTool is called.
		// If we reach here, return the question as a plain string so the operator can relay it.
		var args struct {
			Question string `json:"question"`
			Context  string `json:"context"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing escalate_to_user args: %w", err)
		}
		return fmt.Sprintf("__escalate__:%s\n\nContext: %s", args.Question, args.Context), nil

	case "list_slots":
		if te.Gateway == nil {
			return "gateway not initialized", nil
		}
		slots := te.Gateway.SlotSummaries()
		if len(slots) == 0 {
			return "no active slots", nil
		}
		var lines []string
		for _, s := range slots {
			lines = append(lines, fmt.Sprintf("slot %d: %s on %s — %s (%s)", s.Index, s.Team, s.JobID, s.Status, s.Elapsed))
		}
		return strings.Join(lines, "\n"), nil

	case "kill_slot":
		if te.Gateway == nil {
			return "gateway not initialized", nil
		}
		var args struct {
			SlotID int `json:"slot_id"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing kill_slot args: %w", err)
		}
		if err := te.Gateway.Kill(args.SlotID); err != nil {
			return fmt.Sprintf("error killing slot %d: %v", args.SlotID, err), nil
		}
		return fmt.Sprintf("killed slot %d", args.SlotID), nil

	case "ask_user":
		// ask_user is normally intercepted by the TUI before ExecuteTool is called.
		// This case is a safety fallback.
		return "ask_user was handled by the TUI", nil

	case "task_set_status":
		var args struct {
			JobID  string `json:"job_id"`
			TaskID string `json:"task_id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing task_set_status args: %w", err)
		}
		validStatuses := map[string]bool{"active": true, "done": true, "paused": true}
		if !validStatuses[args.Status] {
			return fmt.Sprintf("invalid status %q: must be one of active, done, paused", args.Status), nil
		}
		jobDir := filepath.Join(job.JobsDir(te.WorkspaceDir), args.JobID)
		tasks, err := job.ListTasks(jobDir)
		if err != nil {
			return "", fmt.Errorf("listing tasks: %w", err)
		}
		for _, t := range tasks {
			if t.ID == args.TaskID {
				if err := job.SetTaskStatus(t.Dir, job.Status(args.Status)); err != nil {
					return "", fmt.Errorf("setting task status: %w", err)
				}
				return fmt.Sprintf("task %s status set to %s", args.TaskID, args.Status), nil
			}
		}
		return fmt.Sprintf("task %q not found in job %q", args.TaskID, args.JobID), nil

	case "job_set_status":
		var args struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing job_set_status args: %w", err)
		}
		validStatuses := map[string]bool{"active": true, "done": true, "cancelled": true, "paused": true}
		if !validStatuses[args.Status] {
			return fmt.Sprintf("invalid status %q: must be one of active, done, cancelled, paused", args.Status), nil
		}
		dir := filepath.Join(job.JobsDir(te.WorkspaceDir), args.ID)
		updates := map[string]string{"status": args.Status}
		if args.Status == "done" {
			updates["completed"] = time.Now().UTC().Format(time.RFC3339)
		}
		if err := job.UpdateFrontmatter(dir, updates); err != nil {
			return "", fmt.Errorf("updating job status: %w", err)
		}
		return fmt.Sprintf("job %s status set to %s", args.ID, args.Status), nil

	default:
		return "", fmt.Errorf("unknown tool: %s", call.Function.Name)
	}
}

// fetchWebpage retrieves a URL and returns its content as plain text.
func fetchWebpage(url string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "toasters/0.1")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d fetching %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response body: %w", err)
	}

	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("parsing HTML: %w", err)
	}

	var parts []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		// Skip subtrees rooted at script, style, or head nodes.
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "head":
				return
			}
		}
		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				parts = append(parts, text)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	result := strings.Join(parts, " ")

	// Collapse runs of whitespace and newlines.
	wsRe := regexp.MustCompile(`\s+`)
	result = wsRe.ReplaceAllString(result, " ")
	result = strings.TrimSpace(result)

	const maxLen = 8000
	if len(result) > maxLen {
		result = result[:maxLen] + "...[truncated]"
	}

	return result, nil
}

// listDirectory returns a formatted listing of the directory at path.
func listDirectory(path string) (string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("reading directory %s: %w", path, err)
	}

	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			lines = append(lines, fmt.Sprintf("[dir]  %s/", entry.Name()))
		} else {
			info, err := entry.Info()
			if err != nil {
				return "", fmt.Errorf("getting info for %s: %w", entry.Name(), err)
			}
			lines = append(lines, fmt.Sprintf("[file] %s  (%d bytes)", entry.Name(), info.Size()))
		}
	}

	return strings.Join(lines, "\n"), nil
}
