package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/mcp"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// ToolExecutor holds the dependencies needed to execute operator tool calls.
type ToolExecutor struct {
	teams        []agents.Team
	teamsMu      sync.RWMutex
	WorkspaceDir string
	Tools        []provider.Tool
	Store        db.Store         // may be nil
	Runtime      *runtime.Runtime // may be nil
	MCPManager   mcp.MCPCaller    // may be nil

	// Runtime agent configuration — set after construction.
	DefaultProvider  string                      // default provider name for runtime agents
	DefaultModel     string                      // default model for runtime agents
	OnSessionStarted func(sess *runtime.Session) // callback when a runtime session starts
}

// SetTeams replaces the team list. Safe for concurrent use.
func (te *ToolExecutor) SetTeams(teams []agents.Team) {
	te.teamsMu.Lock()
	te.teams = teams
	te.teamsMu.Unlock()
}

// getTeams returns a copy of the current team list. Safe for concurrent use.
func (te *ToolExecutor) getTeams() []agents.Team {
	te.teamsMu.RLock()
	defer te.teamsMu.RUnlock()
	teams := make([]agents.Team, len(te.teams))
	copy(teams, te.teams)
	return teams
}

// NewToolExecutor creates a ToolExecutor with the default static tools.
func NewToolExecutor(teams []agents.Team, workspaceDir string, store db.Store, rt *runtime.Runtime) *ToolExecutor {
	return &ToolExecutor{
		teams:        teams,
		WorkspaceDir: workspaceDir,
		Tools:        staticTools,
		Store:        store,
		Runtime:      rt,
	}
}

// mustMarshalJSON marshals v to json.RawMessage, panicking on error.
// Used for static tool parameter definitions that are known at compile time.
func mustMarshalJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// staticTools contains all tools available to the operator LLM.
var staticTools = []provider.Tool{
	{
		Name:        "assign_team",
		Description: "Assign a task to a team to work on autonomously. The job_id must be the ID of an existing job — call job_create first if no job exists yet.",
		Parameters: mustMarshalJSON(map[string]any{
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
		}),
	},
	{
		Name:        "escalate_to_user",
		Description: "Surface a blocker or question to the user that requires human input before work can continue.",
		Parameters: mustMarshalJSON(map[string]any{
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
		}),
	},
	{
		Name:        "fetch_webpage",
		Description: "Fetches the content of a web page and returns it as plain text.",
		Parameters: mustMarshalJSON(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The URL of the web page to fetch.",
				},
			},
			"required": []string{"url"},
		}),
	},
	{
		Name:        "list_directory",
		Description: "Lists the contents of a local directory.",
		Parameters: mustMarshalJSON(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "The absolute or relative path to the directory.",
				},
			},
			"required": []string{"path"},
		}),
	},
	{
		Name:        "job_list",
		Description: "List all jobs.",
		Parameters: mustMarshalJSON(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
	},
	{
		Name:        "job_create",
		Description: "Create a new job. A UUID is auto-generated and a per-job workspace directory is created.",
		Parameters: mustMarshalJSON(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "Human-readable name."},
				"description": map[string]any{"type": "string", "description": "1-3 sentence summary of the job."},
			},
			"required": []string{"name", "description"},
		}),
	},
	{
		Name:        "job_read_overview",
		Description: "Read the overview/description for a job.",
		Parameters: mustMarshalJSON(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "The job ID."},
			},
			"required": []string{"id"},
		}),
	},
	{
		Name:        "job_read_todos",
		Description: "List the tasks for a job.",
		Parameters: mustMarshalJSON(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "The job ID."},
			},
			"required": []string{"id"},
		}),
	},
	{
		Name:        "job_update_overview",
		Description: "Update the description for a job.",
		Parameters: mustMarshalJSON(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":      map[string]any{"type": "string", "description": "The job ID."},
				"content": map[string]any{"type": "string", "description": "Markdown content to write."},
				"mode":    map[string]any{"type": "string", "enum": []string{"overwrite", "append"}, "description": "Whether to overwrite or append."},
			},
			"required": []string{"id", "content", "mode"},
		}),
	},
	{
		Name:        "job_add_todo",
		Description: "Add a new task to a job.",
		Parameters: mustMarshalJSON(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":   map[string]any{"type": "string", "description": "The job ID."},
				"task": map[string]any{"type": "string", "description": "Task description (plain text)."},
			},
			"required": []string{"id", "task"},
		}),
	},
	{
		Name:        "job_complete_todo",
		Description: "Mark a task as completed in a job.",
		Parameters: mustMarshalJSON(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":            map[string]any{"type": "string", "description": "The job ID."},
				"index_or_text": map[string]any{"type": "string", "description": "1-based index of the TODO item, or a substring of the task text to match."},
			},
			"required": []string{"id", "index_or_text"},
		}),
	},
	{
		Name:        "ask_user",
		Description: "Pause execution and ask the user a question with a set of options to choose from. Use this when you need the user to make a decision before proceeding. The user can select one of the provided options or type a custom response.",
		Parameters: mustMarshalJSON(map[string]any{
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
		}),
	},
	{
		Name:        "task_set_status",
		Description: "Update the status of a specific task within a job. Valid statuses: active, done, paused.",
		Parameters: mustMarshalJSON(map[string]any{
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
		}),
	},
	{
		Name:        "job_set_status",
		Description: "Update the status of a job. Valid statuses: active, done, cancelled, paused.",
		Parameters: mustMarshalJSON(map[string]any{
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
		}),
	},
	{
		Name:        "list_sessions",
		Description: "List all active runtime agent sessions with their status, agent name, model, and elapsed time.",
		Parameters:  mustMarshalJSON(map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}),
	},
	{
		Name:        "cancel_session",
		Description: "Cancel a running runtime agent session by its session ID (or prefix).",
		Parameters: mustMarshalJSON(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{
					"type":        "string",
					"description": "The session ID or prefix to cancel.",
				},
			},
			"required": []string{"session_id"},
		}),
	},
}

// toolHandler is the function signature for individual tool handlers.
// Each handler receives the request context, the executor (for access to
// dependencies), and the full tool call.
type toolHandler func(ctx context.Context, te *ToolExecutor, call provider.ToolCall) (string, error)

// handlers maps tool names to their handler functions.
var handlers = map[string]toolHandler{
	"fetch_webpage":       handleFetchWebpage,
	"list_directory":      handleListDirectory,
	"job_list":            handleJobList,
	"job_create":          handleJobCreate,
	"job_read_overview":   handleJobReadOverview,
	"job_read_todos":      handleJobReadTodos,
	"job_update_overview": handleJobUpdateOverview,
	"job_add_todo":        handleJobAddTodo,
	"job_complete_todo":   handleJobCompleteTodo,
	"task_set_status":     handleTaskSetStatus,
	"job_set_status":      handleJobSetStatus,
	"assign_team":         handleAssignTeam,
	"escalate_to_user":    handleEscalateToUser,
	"ask_user":            handleAskUser,
	"list_sessions":       handleListSessions,
	"cancel_session":      handleCancelSession,
}

// ExecuteTool dispatches a tool call to the appropriate handler and returns
// the result as plain text.
func (te *ToolExecutor) ExecuteTool(ctx context.Context, call provider.ToolCall) (string, error) {
	if handler, ok := handlers[call.Name]; ok {
		return handler(ctx, te, call)
	}

	// Check if this is an MCP tool call (namespaced with __).
	if te.MCPManager != nil && strings.Contains(call.Name, "__") {
		result, err := te.MCPManager.Call(ctx, call.Name, call.Arguments)
		if err != nil {
			return "", fmt.Errorf("MCP tool %s: %w", call.Name, err)
		}
		return mcp.TruncateResult(result, mcp.DefaultMaxResultLen), nil
	}

	return "", fmt.Errorf("unknown tool: %s", call.Name)
}

// wsRe matches runs of whitespace for collapsing in fetchWebpage output.
var wsRe = regexp.MustCompile(`\s+`)

// privateNetworks lists IP ranges that should not be accessible via fetch_webpage.
var privateNetworks = []*net.IPNet{
	mustParseCIDR("127.0.0.0/8"),
	mustParseCIDR("10.0.0.0/8"),
	mustParseCIDR("172.16.0.0/12"),
	mustParseCIDR("192.168.0.0/16"),
	mustParseCIDR("169.254.0.0/16"),
	mustParseCIDR("::1/128"),
	mustParseCIDR("fc00::/7"),
	mustParseCIDR("fe80::/10"),
}

func mustParseCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

func isPrivateIP(ip net.IP) bool {
	for _, network := range privateNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// operatorFetchClient is a dedicated HTTP client with SSRF protection for the
// operator-level fetch_webpage tool. It resolves DNS and checks against private
// networks before connecting.
var operatorFetchClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if isPrivateIP(ip.IP) {
					return nil, fmt.Errorf("access to private/reserved IP %s is blocked", ip.IP)
				}
			}
			dialer := &net.Dialer{Timeout: 10 * time.Second}
			return dialer.DialContext(ctx, network, addr)
		},
	},
}

// fetchWebpage retrieves a URL and returns its content as plain text.
func fetchWebpage(url string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "toasters/0.1")

	resp, err := operatorFetchClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d fetching %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MB limit
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
	result = wsRe.ReplaceAllString(result, " ")
	result = strings.TrimSpace(result)

	const maxLen = 8000
	if len(result) > maxLen {
		result = result[:maxLen] + "...[truncated]"
	}

	return result, nil
}

// listDirectory returns a formatted listing of the directory at path.
// The path is validated against workspaceDir to prevent directory traversal.
// Relative paths are resolved relative to workspaceDir; absolute paths must
// be under workspaceDir.
func listDirectory(path, workspaceDir string) (string, error) {
	path = filepath.Clean(path)

	if filepath.IsAbs(path) {
		// Absolute path must be under the workspace dir.
		absWorkspace, err := filepath.Abs(workspaceDir)
		if err != nil {
			return "", fmt.Errorf("resolving workspace dir: %w", err)
		}
		absWorkspace = filepath.Clean(absWorkspace)
		if !strings.HasPrefix(path, absWorkspace+string(filepath.Separator)) && path != absWorkspace {
			return "", fmt.Errorf("access denied: path %q is outside workspace %q", path, absWorkspace)
		}
	} else {
		// Relative path — resolve relative to workspace dir.
		path = filepath.Join(workspaceDir, path)
		path = filepath.Clean(path)

		// Verify the resolved path is still under workspace dir (prevents ../.. traversal).
		absWorkspace, err := filepath.Abs(workspaceDir)
		if err != nil {
			return "", fmt.Errorf("resolving workspace dir: %w", err)
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolving path: %w", err)
		}
		absWorkspace = filepath.Clean(absWorkspace)
		absPath = filepath.Clean(absPath)
		if !strings.HasPrefix(absPath, absWorkspace+string(filepath.Separator)) && absPath != absWorkspace {
			return "", fmt.Errorf("access denied: resolved path %q is outside workspace %q", absPath, absWorkspace)
		}
	}

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

// shortID returns the first 8 characters of an ID, or the full ID if shorter.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
