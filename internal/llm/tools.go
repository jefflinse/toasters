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

	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/workeffort"
)

// AgentSpawner is the interface satisfied by *gateway.Gateway.
// Using an interface here avoids an import cycle (gateway imports llm).
type AgentSpawner interface {
	Spawn(agentName, workEffortID, task string) (int, error)
}

// activeGateway is the gateway instance used by the run_agent tool.
// Set via SetGateway before the TUI starts.
var activeGateway AgentSpawner

// SetGateway wires the gateway instance into the tool executor.
func SetGateway(g AgentSpawner) {
	activeGateway = g
}

// AvailableTools is the set of tools exposed to the LLM.
var AvailableTools = []Tool{
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
			Name:        "work_effort_list",
			Description: "List all work efforts.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "work_effort_create",
			Description: "Create a new work effort.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":          map[string]any{"type": "string", "description": "Slug identifier (lowercase letters, digits, hyphens only, e.g. 'auth-refactor')."},
					"name":        map[string]any{"type": "string", "description": "Human-readable name."},
					"description": map[string]any{"type": "string", "description": "1-3 sentence summary of the work effort."},
				},
				"required": []string{"id", "name", "description"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "work_effort_read_overview",
			Description: "Read the OVERVIEW.md file for a work effort.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string", "description": "The work effort ID."},
				},
				"required": []string{"id"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "work_effort_read_todos",
			Description: "Read the TODO.md file for a work effort.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string", "description": "The work effort ID."},
				},
				"required": []string{"id"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "work_effort_update_overview",
			Description: "Overwrite or append to the OVERVIEW.md body for a work effort. Does not touch frontmatter.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":      map[string]any{"type": "string", "description": "The work effort ID."},
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
			Name:        "work_effort_add_todo",
			Description: "Append a new TODO item to the TODO.md file for a work effort.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":   map[string]any{"type": "string", "description": "The work effort ID."},
					"task": map[string]any{"type": "string", "description": "Task description (plain text)."},
				},
				"required": []string{"id", "task"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "work_effort_complete_todo",
			Description: "Mark a TODO item as done in the TODO.md file for a work effort.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":            map[string]any{"type": "string", "description": "The work effort ID."},
					"index_or_text": map[string]any{"type": "string", "description": "1-based index of the TODO item, or a substring of the task text to match."},
				},
				"required": []string{"id", "index_or_text"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "run_agent",
			Description: "Spawn a named background Claude agent to work on a work effort. Available agents: investigator, planner, executor, summarizer.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent_name": map[string]any{
						"type":        "string",
						"description": "The agent to run. One of: investigator, planner, executor, summarizer.",
					},
					"work_effort_id": map[string]any{
						"type":        "string",
						"description": "The ID of the work effort to operate on.",
					},
					"task": map[string]any{
						"type":        "string",
						"description": "Optional extra instruction appended to the agent prompt.",
					},
				},
				"required": []string{"agent_name", "work_effort_id"},
			},
		},
	},
}

// ExecuteTool dispatches a tool call to the appropriate handler and returns
// the result as plain text.
func ExecuteTool(call ToolCall) (string, error) {
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
	case "work_effort_list":
		configDir, err := config.Dir()
		if err != nil {
			return "", fmt.Errorf("resolving config dir: %w", err)
		}
		efforts, err := workeffort.List(configDir)
		if err != nil {
			return "", fmt.Errorf("listing work efforts: %w", err)
		}
		type item struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Status      string `json:"status"`
		}
		items := make([]item, len(efforts))
		for i, e := range efforts {
			items[i] = item{ID: e.ID, Name: e.Name, Description: e.Description, Status: string(e.Status)}
		}
		b, _ := json.Marshal(items)
		return string(b), nil

	case "work_effort_create":
		var args struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing work_effort_create args: %w", err)
		}
		configDir, err := config.Dir()
		if err != nil {
			return "", fmt.Errorf("resolving config dir: %w", err)
		}
		we, err := workeffort.Create(configDir, args.ID, args.Name, args.Description)
		if err != nil {
			return "", fmt.Errorf("creating work effort: %w", err)
		}
		return "created: " + we.ID, nil

	case "work_effort_read_overview":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing work_effort_read_overview args: %w", err)
		}
		configDir, err := config.Dir()
		if err != nil {
			return "", fmt.Errorf("resolving config dir: %w", err)
		}
		dir := filepath.Join(workeffort.WorkEffortsDir(configDir), args.ID)
		return workeffort.ReadOverview(dir)

	case "work_effort_read_todos":
		var args struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing work_effort_read_todos args: %w", err)
		}
		configDir, err := config.Dir()
		if err != nil {
			return "", fmt.Errorf("resolving config dir: %w", err)
		}
		dir := filepath.Join(workeffort.WorkEffortsDir(configDir), args.ID)
		return workeffort.ReadTodos(dir)

	case "work_effort_update_overview":
		var args struct {
			ID      string `json:"id"`
			Content string `json:"content"`
			Mode    string `json:"mode"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing work_effort_update_overview args: %w", err)
		}
		if args.Mode != "overwrite" && args.Mode != "append" {
			return "", fmt.Errorf("invalid mode %q: must be 'overwrite' or 'append'", args.Mode)
		}
		configDir, err := config.Dir()
		if err != nil {
			return "", fmt.Errorf("resolving config dir: %w", err)
		}
		dir := filepath.Join(workeffort.WorkEffortsDir(configDir), args.ID)
		if args.Mode == "overwrite" {
			err = workeffort.WriteOverview(dir, args.Content)
		} else {
			err = workeffort.AppendOverview(dir, args.Content)
		}
		if err != nil {
			return "", err
		}
		return "ok", nil

	case "work_effort_add_todo":
		var args struct {
			ID   string `json:"id"`
			Task string `json:"task"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing work_effort_add_todo args: %w", err)
		}
		configDir, err := config.Dir()
		if err != nil {
			return "", fmt.Errorf("resolving config dir: %w", err)
		}
		dir := filepath.Join(workeffort.WorkEffortsDir(configDir), args.ID)
		if err := workeffort.AddTodo(dir, args.Task); err != nil {
			return "", err
		}
		return "ok", nil

	case "work_effort_complete_todo":
		var args struct {
			ID          string `json:"id"`
			IndexOrText string `json:"index_or_text"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing work_effort_complete_todo args: %w", err)
		}
		configDir, err := config.Dir()
		if err != nil {
			return "", fmt.Errorf("resolving config dir: %w", err)
		}
		dir := filepath.Join(workeffort.WorkEffortsDir(configDir), args.ID)
		if err := workeffort.CompleteTodo(dir, args.IndexOrText); err != nil {
			return "", err
		}
		return "ok", nil

	case "run_agent":
		if activeGateway == nil {
			return "", fmt.Errorf("gateway not initialized")
		}
		var args struct {
			AgentName    string `json:"agent_name"`
			WorkEffortID string `json:"work_effort_id"`
			Task         string `json:"task"`
		}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("parsing run_agent args: %w", err)
		}
		slotID, err := activeGateway.Spawn(args.AgentName, args.WorkEffortID, args.Task)
		if err != nil {
			return "", fmt.Errorf("spawning agent: %w", err)
		}
		return fmt.Sprintf("started: slot %d", slotID), nil

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
	defer resp.Body.Close()

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
