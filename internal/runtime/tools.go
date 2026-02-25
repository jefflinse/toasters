package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ToolExecutor executes tool calls by name.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, args json.RawMessage) (string, error)
	// Definitions returns the tool definitions for the LLM.
	Definitions() []ToolDef
}

// AgentSpawner creates child agent sessions.
type AgentSpawner interface {
	SpawnAndWait(ctx context.Context, opts SpawnOpts) (string, error)
}

// CoreTools implements the standard agent tool set.
type CoreTools struct {
	workDir    string
	allowShell bool
	spawner    AgentSpawner // for spawn_agent; may be nil
	depth      int          // current spawn depth
	maxDepth   int          // max spawn depth
}

// CoreToolsOption configures a CoreTools instance.
type CoreToolsOption func(*CoreTools)

// WithShell enables the shell tool.
func WithShell(allow bool) CoreToolsOption {
	return func(ct *CoreTools) { ct.allowShell = allow }
}

// WithSpawner sets the agent spawner for spawn_agent.
func WithSpawner(s AgentSpawner, depth, maxDepth int) CoreToolsOption {
	return func(ct *CoreTools) {
		ct.spawner = s
		ct.depth = depth
		ct.maxDepth = maxDepth
	}
}

// NewCoreTools creates a CoreTools with the given work directory and options.
func NewCoreTools(workDir string, opts ...CoreToolsOption) *CoreTools {
	ct := &CoreTools{
		workDir:    workDir,
		allowShell: true,
		maxDepth:   defaultMaxDepth,
	}
	for _, opt := range opts {
		opt(ct)
	}
	return ct
}

// Execute dispatches a tool call by name.
func (ct *CoreTools) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	case "read_file":
		return ct.readFile(ctx, args)
	case "write_file":
		return ct.writeFile(ctx, args)
	case "edit_file":
		return ct.editFile(ctx, args)
	case "glob":
		return ct.glob(ctx, args)
	case "grep":
		return ct.grepFiles(ctx, args)
	case "shell":
		return ct.shell(ctx, args)
	case "web_fetch":
		return ct.webFetch(ctx, args)
	case "spawn_agent":
		return ct.spawnAgent(ctx, args)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// Definitions returns tool definitions for the LLM.
func (ct *CoreTools) Definitions() []ToolDef {
	defs := []ToolDef{
		{
			Name:        "read_file",
			Description: "Read a file's contents. Returns content with line numbers prefixed. Use offset and limit to read specific sections.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path":   {"type": "string", "description": "File path relative to working directory"},
					"offset": {"type": "integer", "description": "Starting line number (1-based). Default: 1"},
					"limit":  {"type": "integer", "description": "Maximum number of lines to return. Default: 2000"}
				},
				"required": ["path"]
			}`),
		},
		{
			Name:        "write_file",
			Description: "Write content to a file, creating parent directories as needed. Returns bytes written.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path":    {"type": "string", "description": "File path relative to working directory"},
					"content": {"type": "string", "description": "Content to write"}
				},
				"required": ["path", "content"]
			}`),
		},
		{
			Name:        "edit_file",
			Description: "Find old_string in a file and replace it with new_string. The old_string must appear exactly once in the file.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path":       {"type": "string", "description": "File path relative to working directory"},
					"old_string": {"type": "string", "description": "Exact text to find (must be unique in file)"},
					"new_string": {"type": "string", "description": "Replacement text"}
				},
				"required": ["path", "old_string", "new_string"]
			}`),
		},
		{
			Name:        "glob",
			Description: "Find files matching a glob pattern under the working directory. Supports ** for recursive matching.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"pattern": {"type": "string", "description": "Glob pattern (e.g. '**/*.go', 'src/*.ts')"}
				},
				"required": ["pattern"]
			}`),
		},
		{
			Name:        "grep",
			Description: "Search file contents using a regular expression. Returns matching files with line numbers and context.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"pattern": {"type": "string", "description": "Regular expression to search for"},
					"path":    {"type": "string", "description": "Directory to search in (relative to working directory). Default: '.'"},
					"include": {"type": "string", "description": "File pattern to include (e.g. '*.go', '*.{ts,tsx}')"}
				},
				"required": ["pattern"]
			}`),
		},
		{
			Name:        "shell",
			Description: "Execute a shell command. Returns stdout and stderr combined.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "Shell command to execute"},
					"timeout": {"type": "integer", "description": "Timeout in seconds. Default: 120"}
				},
				"required": ["command"]
			}`),
		},
		{
			Name:        "web_fetch",
			Description: "Fetch content from a URL via HTTP GET. Returns the response body as text.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"url": {"type": "string", "description": "URL to fetch"}
				},
				"required": ["url"]
			}`),
		},
	}

	// Only include spawn_agent if spawner is available and depth allows it.
	if ct.spawner != nil && ct.depth < ct.maxDepth {
		defs = append(defs, ToolDef{
			Name:        "spawn_agent",
			Description: "Spawn a child agent session. Blocks until the child completes and returns its final text output.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"system_prompt": {"type": "string", "description": "System prompt for the child agent"},
					"message":       {"type": "string", "description": "Initial message to send to the child agent"},
					"tools":         {"type": "array", "items": {"type": "string"}, "description": "Tool names to make available to the child agent"}
				},
				"required": ["system_prompt", "message"]
			}`),
		})
	}

	return defs
}

// resolvePath resolves a path relative to workDir and validates it doesn't escape the sandbox.
func (ct *CoreTools) resolvePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}

	var resolved string
	if filepath.IsAbs(path) {
		resolved = filepath.Clean(path)
	} else {
		resolved = filepath.Clean(filepath.Join(ct.workDir, path))
	}

	// Ensure the resolved path is under workDir.
	absWorkDir, err := filepath.Abs(ct.workDir)
	if err != nil {
		return "", fmt.Errorf("resolving work directory: %w", err)
	}
	absResolved, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}

	if !strings.HasPrefix(absResolved, absWorkDir+string(filepath.Separator)) && absResolved != absWorkDir {
		return "", fmt.Errorf("path %q escapes working directory", path)
	}

	return absResolved, nil
}

// --- Tool implementations ---

func (ct *CoreTools) readFile(_ context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing arguments: %w", err)
	}

	resolved, err := ct.resolvePath(params.Path)
	if err != nil {
		return "", err
	}

	f, err := os.Open(resolved)
	if err != nil {
		return "", fmt.Errorf("opening file: %w", err)
	}
	defer f.Close()

	offset := params.Offset
	if offset < 1 {
		offset = 1
	}
	limit := params.Limit
	if limit < 1 {
		limit = 2000
	}

	var b strings.Builder
	scanner := bufio.NewScanner(f)
	lineNum := 0
	linesWritten := 0
	for scanner.Scan() {
		lineNum++
		if lineNum < offset {
			continue
		}
		if linesWritten >= limit {
			break
		}
		fmt.Fprintf(&b, "%d: %s\n", lineNum, scanner.Text())
		linesWritten++
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}

	if linesWritten == 0 {
		return "(empty file or offset beyond end)", nil
	}

	return b.String(), nil
}

func (ct *CoreTools) writeFile(_ context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing arguments: %w", err)
	}

	resolved, err := ct.resolvePath(params.Path)
	if err != nil {
		return "", err
	}

	dir := filepath.Dir(resolved)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating directories: %w", err)
	}

	n := len(params.Content)
	if err := os.WriteFile(resolved, []byte(params.Content), 0o644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}

	return fmt.Sprintf("wrote %d bytes to %s", n, params.Path), nil
}

func (ct *CoreTools) editFile(_ context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing arguments: %w", err)
	}

	resolved, err := ct.resolvePath(params.Path)
	if err != nil {
		return "", err
	}

	content, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}

	text := string(content)
	count := strings.Count(text, params.OldString)
	switch {
	case count == 0:
		return "", fmt.Errorf("old_string not found in %s", params.Path)
	case count > 1:
		return "", fmt.Errorf("old_string found %d times in %s (must be unique)", count, params.Path)
	}

	newText := strings.Replace(text, params.OldString, params.NewString, 1)
	if err := os.WriteFile(resolved, []byte(newText), 0o644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}

	return fmt.Sprintf("edited %s", params.Path), nil
}

func (ct *CoreTools) glob(_ context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Pattern string `json:"pattern"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing arguments: %w", err)
	}

	absWorkDir, err := filepath.Abs(ct.workDir)
	if err != nil {
		return "", fmt.Errorf("resolving work directory: %w", err)
	}

	var matches []string

	if strings.Contains(params.Pattern, "**") {
		// Walk directory tree for ** patterns.
		// Split pattern on ** to get prefix and suffix.
		matches, err = ct.globRecursive(absWorkDir, params.Pattern)
		if err != nil {
			return "", fmt.Errorf("glob: %w", err)
		}
	} else {
		fullPattern := filepath.Join(absWorkDir, params.Pattern)
		matches, err = filepath.Glob(fullPattern)
		if err != nil {
			return "", fmt.Errorf("glob: %w", err)
		}
	}

	// Make paths relative to workDir.
	var results []string
	for _, m := range matches {
		rel, err := filepath.Rel(absWorkDir, m)
		if err != nil {
			rel = m
		}
		results = append(results, rel)
	}

	if len(results) == 0 {
		return "(no matches)", nil
	}

	return strings.Join(results, "\n"), nil
}

// globRecursive handles ** patterns by walking the directory tree.
func (ct *CoreTools) globRecursive(root, pattern string) ([]string, error) {
	// Split on "**/" or "**" to get the suffix pattern.
	parts := strings.SplitN(pattern, "**", 2)
	prefix := parts[0]
	suffix := ""
	if len(parts) > 1 {
		suffix = strings.TrimPrefix(parts[1], "/")
		suffix = strings.TrimPrefix(suffix, string(filepath.Separator))
	}

	baseDir := filepath.Join(root, prefix)
	if info, err := os.Stat(baseDir); err != nil || !info.IsDir() {
		baseDir = root
	}

	var matches []string
	err := filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if d.IsDir() {
			return nil
		}

		if suffix == "" {
			matches = append(matches, path)
			return nil
		}

		// Match the filename or relative path against the suffix.
		name := d.Name()
		matched, _ := filepath.Match(suffix, name)
		if matched {
			matches = append(matches, path)
		}
		return nil
	})

	return matches, err
}

func (ct *CoreTools) grepFiles(_ context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Include string `json:"include"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing arguments: %w", err)
	}

	re, err := regexp.Compile(params.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex: %w", err)
	}

	searchDir := ct.workDir
	if params.Path != "" {
		searchDir, err = ct.resolvePath(params.Path)
		if err != nil {
			return "", err
		}
	}

	absSearchDir, err := filepath.Abs(searchDir)
	if err != nil {
		return "", fmt.Errorf("resolving search directory: %w", err)
	}

	var b strings.Builder
	matchCount := 0
	const maxMatches = 500

	err = filepath.WalkDir(absSearchDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if matchCount >= maxMatches {
			return fs.SkipAll
		}

		// Apply include filter.
		if params.Include != "" {
			matched, _ := filepath.Match(params.Include, d.Name())
			if !matched {
				return nil
			}
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		relPath, _ := filepath.Rel(absSearchDir, path)

		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				fmt.Fprintf(&b, "%s:%d: %s\n", relPath, lineNum, line)
				matchCount++
				if matchCount >= maxMatches {
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("searching files: %w", err)
	}

	if matchCount == 0 {
		return "(no matches)", nil
	}

	return b.String(), nil
}

func (ct *CoreTools) shell(ctx context.Context, args json.RawMessage) (string, error) {
	if !ct.allowShell {
		return "", fmt.Errorf("shell tool is disabled")
	}

	var params struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing arguments: %w", err)
	}

	timeout := time.Duration(params.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", params.Command)
	cmd.Dir = ct.workDir

	output, err := cmd.CombinedOutput()
	result := string(output)

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return result, fmt.Errorf("command timed out after %s", timeout)
		}
		// Include exit code in result but don't return error — the LLM should see the output.
		return fmt.Sprintf("%s\nexit status: %s", result, err.Error()), nil
	}

	return result, nil
}

func (ct *CoreTools) webFetch(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing arguments: %w", err)
	}

	if params.URL == "" {
		return "", fmt.Errorf("url is required")
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, params.URL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "toasters-agent/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching URL: %w", err)
	}
	defer resp.Body.Close()

	// Limit response body to 1MB.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	return string(body), nil
}

func (ct *CoreTools) spawnAgent(ctx context.Context, args json.RawMessage) (string, error) {
	if ct.spawner == nil {
		return "", fmt.Errorf("spawn_agent is not available")
	}
	if ct.depth >= ct.maxDepth {
		return "", fmt.Errorf("max spawn depth (%d) exceeded", ct.maxDepth)
	}

	var params struct {
		SystemPrompt string   `json:"system_prompt"`
		Message      string   `json:"message"`
		Tools        []string `json:"tools"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing arguments: %w", err)
	}

	result, err := ct.spawner.SpawnAndWait(ctx, SpawnOpts{
		SystemPrompt:   params.SystemPrompt,
		InitialMessage: params.Message,
		WorkDir:        ct.workDir,
		MaxDepth:       ct.maxDepth,
	})
	if err != nil {
		return "", fmt.Errorf("spawning agent: %w", err)
	}

	return result, nil
}
