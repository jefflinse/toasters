package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/httputil"
	"github.com/jefflinse/toasters/internal/progress"
	"github.com/jefflinse/toasters/internal/prompt"
)

// ErrUnknownTool is returned by Execute when the tool name is not recognized.
var ErrUnknownTool = errors.New("unknown tool")

// ToolExecutor executes tool calls by name.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, args json.RawMessage) (string, error)
	// Definitions returns the tool definitions for the LLM.
	Definitions() []ToolDef
}

// WorkerSpawner creates child worker sessions.
type WorkerSpawner interface {
	SpawnAndWait(ctx context.Context, opts SpawnOpts) (string, error)
}

// CoreTools implements the standard worker tool set.
type CoreTools struct {
	workDir      string
	allowShell   bool
	spawner      WorkerSpawner  // for spawn_worker; may be nil
	depth        int            // current spawn depth
	maxDepth     int            // max spawn depth
	httpClient   *http.Client   // for web_fetch; nil uses webFetchClient
	store        db.Store       // required; for progress tools
	promptEngine *prompt.Engine // for spawn_worker; may be nil
	graphCatalog GraphCatalog   // for query_graphs; may be nil
	denylist     map[string]bool
	sessionID    string
	workerID     string
	jobID        string
	taskID       string
	providerName string
	model        string
}

// GraphCatalog is a read-only view over the loaded graph Definitions,
// used by the query_graphs tool so roles (typically the fine-decomposer)
// can see what graphs are available for task dispatch. The actual shape
// is defined by graphexec.Definition; kept generic here so runtime stays
// independent of graphexec.
type GraphCatalog interface {
	Graphs() []GraphSummary
}

// GraphSummary is the minimal graph-catalog-entry shape query_graphs
// surfaces to the LLM. Mirror of graphexec.Definition's identity fields.
type GraphSummary struct {
	ID          string
	Name        string
	Description string
	Tags        []string
}

// CoreToolsOption configures a CoreTools instance.
type CoreToolsOption func(*CoreTools)

// WithShell enables the shell tool.
func WithShell(allow bool) CoreToolsOption {
	return func(ct *CoreTools) { ct.allowShell = allow }
}

// WithSpawner sets the worker spawner for spawn_worker.
func WithSpawner(s WorkerSpawner, depth, maxDepth int) CoreToolsOption {
	return func(ct *CoreTools) {
		ct.spawner = s
		ct.depth = depth
		ct.maxDepth = maxDepth
	}
}

// WithStore enables progress tools by providing a database store.
func WithStore(store db.Store) CoreToolsOption {
	return func(ct *CoreTools) { ct.store = store }
}

// WithSessionContext sets the session context for progress tool calls.
func WithSessionContext(sessionID, workerID, jobID, taskID string) CoreToolsOption {
	return func(ct *CoreTools) {
		ct.sessionID = sessionID
		ct.workerID = workerID
		ct.jobID = jobID
		ct.taskID = taskID
	}
}

// WithDenylist sets the tool denylist. Tools in the denylist are excluded from
// Definitions() and rejected by Execute().
func WithDenylist(names []string) CoreToolsOption {
	return func(ct *CoreTools) {
		if len(names) == 0 {
			return
		}
		ct.denylist = make(map[string]bool, len(names))
		for _, n := range names {
			ct.denylist[n] = true
		}
	}
}

// WithPromptEngine sets the prompt engine for spawn_worker.
func WithPromptEngine(e *prompt.Engine) CoreToolsOption {
	return func(ct *CoreTools) { ct.promptEngine = e }
}

// WithProvider sets the provider name and model for child worker spawns.
func WithProvider(providerName, model string) CoreToolsOption {
	return func(ct *CoreTools) {
		ct.providerName = providerName
		ct.model = model
	}
}

// WithGraphCatalog enables the query_graphs tool by supplying the loaded
// graph catalog. When unset, query_graphs is absent from Definitions().
func WithGraphCatalog(cat GraphCatalog) CoreToolsOption {
	return func(ct *CoreTools) { ct.graphCatalog = cat }
}

// NewCoreTools creates a CoreTools with the given work directory and options.
func NewCoreTools(workDir string, opts ...CoreToolsOption) *CoreTools {
	ct := &CoreTools{
		workDir:    workDir,
		allowShell: false, // secure default — require explicit opt-in
		maxDepth:   defaultMaxDepth,
	}
	for _, opt := range opts {
		opt(ct)
	}
	return ct
}

// Execute dispatches a tool call by name.
func (ct *CoreTools) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if ct.denylist[name] {
		return "", fmt.Errorf("tool %q is not allowed for this worker", name)
	}

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
	case "spawn_worker":
		return ct.spawnWorker(ctx, args)
	case "query_graphs":
		return ct.queryGraphs()
	case "report_task_progress":
		var params progress.ReportTaskProgressParams
		if err := json.Unmarshal(args, &params); err != nil {
			return "", fmt.Errorf("parsing report_task_progress args: %w", err)
		}
		params.JobID, params.TaskID = ct.normalizeProgressIDs(params.JobID, params.TaskID)
		if params.WorkerID == "" {
			params.WorkerID = ct.workerID
		}
		return progress.ReportTaskProgress(ctx, ct.store, params)
	case "update_task_status":
		var params progress.UpdateTaskStatusParams
		if err := json.Unmarshal(args, &params); err != nil {
			return "", fmt.Errorf("parsing update_task_status args: %w", err)
		}
		return progress.UpdateTaskStatus(ctx, ct.store, params)
	case "request_review":
		var params progress.RequestReviewParams
		if err := json.Unmarshal(args, &params); err != nil {
			return "", fmt.Errorf("parsing request_review args: %w", err)
		}
		params.JobID, params.TaskID = ct.normalizeProgressIDs(params.JobID, params.TaskID)
		if params.WorkerID == "" {
			params.WorkerID = ct.workerID
		}
		return progress.RequestReview(ctx, ct.store, params)
	case "query_job_context":
		var params progress.QueryJobContextParams
		if err := json.Unmarshal(args, &params); err != nil {
			return "", fmt.Errorf("parsing query_job_context args: %w", err)
		}
		params.JobID = ct.normalizeProgressJobID(params.JobID)
		return progress.QueryJobContext(ctx, ct.store, params)
	case "log_artifact":
		var params progress.LogArtifactParams
		if err := json.Unmarshal(args, &params); err != nil {
			return "", fmt.Errorf("parsing log_artifact args: %w", err)
		}
		params.JobID, params.TaskID = ct.normalizeProgressIDs(params.JobID, params.TaskID)
		return progress.LogArtifact(ctx, ct.store, params)
	default:
		return "", fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}
}

func (ct *CoreTools) normalizeProgressIDs(jobID, taskID string) (string, string) {
	if ct.hasSessionBoundProgressContext() {
		return ct.jobID, ct.taskID
	}
	if jobID == "" {
		jobID = ct.jobID
	}
	if taskID == "" {
		taskID = ct.taskID
	}
	return jobID, taskID
}

func (ct *CoreTools) normalizeProgressJobID(jobID string) string {
	if ct.hasSessionBoundProgressContext() {
		return ct.jobID
	}
	if jobID == "" {
		return ct.jobID
	}
	return jobID
}

func (ct *CoreTools) hasSessionBoundProgressContext() bool {
	return ct.jobID != "" && ct.taskID != ""
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

	// query_graphs is present when a graph catalog is wired in. Used by
	// decomposition roles to see what graphs are available for task
	// dispatch; informational only, no side effects.
	if ct.graphCatalog != nil {
		defs = append(defs, ToolDef{
			Name:        "query_graphs",
			Description: "List all available graphs with their ids, names, descriptions, and tags. Use this when deciding which graph should execute a task.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {}
			}`),
		})
	}

	// Only include spawn_worker if spawner, engine, and depth all allow it.
	if ct.spawner != nil && ct.promptEngine != nil && ct.depth < ct.maxDepth {
		defs = append(defs, ToolDef{
			Name:        "spawn_worker",
			Description: "Spawn a worker with a role-based system prompt composed by the prompt engine. Blocks until the worker completes and returns its final text output.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"role":    {"type": "string", "description": "Worker role name (e.g. 'coder', 'tester', 'reviewer'). Must match a role loaded in the prompt engine."},
					"message": {"type": "string", "description": "Task instruction to send to the worker."},
					"task":    {"type": "string", "description": "Short human-readable description of what this worker is doing (\u226460 chars), shown in the TUI card."}
				},
				"required": ["role", "message"]
			}`),
		})
	}

	// Progress tools — always included (store is required).
	defs = append(defs, progress.ProgressToolDefs()...)

	// Filter out denylisted tools.
	if len(ct.denylist) > 0 {
		filtered := make([]ToolDef, 0, len(defs))
		for _, d := range defs {
			if !ct.denylist[d.Name] {
				filtered = append(filtered, d)
			}
		}
		defs = filtered
	}

	return defs
}

// queryGraphs renders the loaded graph catalog as markdown. Used by
// decomposition roles to see graph ids, names, descriptions, and tags.
//
// Graphs tagged `system:true` are filtered out — those are meta-graphs
// (coarse-decompose, fine-decompose) that implement the decomposition
// flow itself, not candidate dispatch targets. Listing them would
// invite a decomposer to assign decomposition-of-decomposition, which
// is nonsense.
func (ct *CoreTools) queryGraphs() (string, error) {
	if ct.graphCatalog == nil {
		return "No graphs are currently loaded.", nil
	}
	all := ct.graphCatalog.Graphs()
	graphs := make([]GraphSummary, 0, len(all))
	for _, g := range all {
		if graphHasTag(g.Tags, "system:true") {
			continue
		}
		graphs = append(graphs, g)
	}
	if len(graphs) == 0 {
		return "No graphs are currently loaded.", nil
	}
	var b strings.Builder
	b.WriteString("Available graphs:\n")
	for _, g := range graphs {
		name := g.Name
		if name == "" {
			name = g.ID
		}
		fmt.Fprintf(&b, "\n- %s (id: %s)\n", name, g.ID)
		if g.Description != "" {
			fmt.Fprintf(&b, "  Description: %s\n", strings.TrimSpace(g.Description))
		}
		if len(g.Tags) > 0 {
			fmt.Fprintf(&b, "  Tags: %s\n", strings.Join(g.Tags, ", "))
		}
	}
	return b.String(), nil
}

// graphHasTag reports whether a graph's tag list contains an exact
// match for the given tag string. Tags are simple strings today
// ("language:go", "system:true", …); matching is byte-equal, not a
// namespace-aware lookup.
func graphHasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// DefinitionsByName returns tool definitions keyed by tool name.
func (ct *CoreTools) DefinitionsByName() map[string]ToolDef {
	defs := ct.Definitions()
	byName := make(map[string]ToolDef, len(defs))
	for _, td := range defs {
		byName[td.Name] = td
	}
	return byName
}

// resolvePath resolves a path relative to workDir and validates it doesn't escape the sandbox.
// It resolves symlinks to prevent symlink-based sandbox escapes.
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

	// Resolve symlinks in workDir to get the real base path.
	absWorkDir, err := filepath.EvalSymlinks(ct.workDir)
	if err != nil {
		return "", fmt.Errorf("resolving work directory: %w", err)
	}
	absWorkDir, err = filepath.Abs(absWorkDir)
	if err != nil {
		return "", fmt.Errorf("resolving work directory: %w", err)
	}

	// For existing paths, resolve symlinks to get the real path.
	// For new paths (write_file), walk up to find the nearest existing ancestor
	// and resolve symlinks from there.
	var absResolved string
	if evalResolved, evalErr := filepath.EvalSymlinks(resolved); evalErr == nil {
		absResolved, _ = filepath.Abs(evalResolved)
	} else {
		// Path doesn't exist yet — walk up to find the nearest existing ancestor.
		remaining := resolved
		var tail []string
		for {
			parent := filepath.Dir(remaining)
			tail = append([]string{filepath.Base(remaining)}, tail...)
			if parentResolved, err2 := filepath.EvalSymlinks(parent); err2 == nil {
				absParent, _ := filepath.Abs(parentResolved)
				absResolved = filepath.Join(append([]string{absParent}, tail...)...)
				break
			}
			if parent == remaining {
				// Reached filesystem root without finding an existing ancestor.
				absResolved, _ = filepath.Abs(resolved)
				break
			}
			remaining = parent
		}
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
	defer func() { _ = f.Close() }()

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

	const maxWriteContentSize = 50 * 1024 * 1024 // 50 MB
	if len(params.Content) > maxWriteContentSize {
		return "", fmt.Errorf("content too large to write: %d bytes (max %d)", len(params.Content), maxWriteContentSize)
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

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}
	const maxEditFileSize = 10 * 1024 * 1024 // 10 MB
	if info.Size() > maxEditFileSize {
		return "", fmt.Errorf("file too large to edit: %d bytes (max %d)", info.Size(), maxEditFileSize)
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
		// Validate that the pattern's base directory doesn't escape the workspace.
		fullPattern := filepath.Join(absWorkDir, params.Pattern)
		patternDir := filepath.Dir(fullPattern)
		absPatternDir, err2 := filepath.Abs(patternDir)
		if err2 != nil {
			return "", fmt.Errorf("resolving glob pattern directory: %w", err2)
		}
		if !strings.HasPrefix(absPatternDir, absWorkDir+string(filepath.Separator)) && absPatternDir != absWorkDir {
			return "", fmt.Errorf("glob base directory is outside workspace")
		}

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

	// Validate that the resolved base directory is within the workspace.
	absBaseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("resolving glob base directory: %w", err)
	}
	if !strings.HasPrefix(absBaseDir, root+string(filepath.Separator)) && absBaseDir != root {
		return nil, fmt.Errorf("glob base directory is outside workspace")
	}

	var matches []string
	err = filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
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
		defer func() { _ = f.Close() }()

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

// webFetchClient is a dedicated HTTP client with SSRF protection.
var webFetchClient = httputil.NewSafeClient(30 * time.Second)

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

	// Validate URL scheme.
	u, err := url.Parse(params.URL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported URL scheme %q (only http and https allowed)", u.Scheme)
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, params.URL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "toasters/1.0")

	client := ct.httpClient
	if client == nil {
		client = webFetchClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching URL: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

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

func (ct *CoreTools) spawnWorker(ctx context.Context, args json.RawMessage) (string, error) {
	if ct.spawner == nil {
		return "", fmt.Errorf("spawn_worker is not available")
	}
	if ct.promptEngine == nil {
		return "", fmt.Errorf("spawn_worker requires a prompt engine")
	}
	if ct.depth >= ct.maxDepth {
		return "", fmt.Errorf("max spawn depth (%d) exceeded", ct.maxDepth)
	}

	var params struct {
		Role    string `json:"role"`
		Message string `json:"message"`
		Task    string `json:"task"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing arguments: %w", err)
	}

	// Verify the role exists.
	role := ct.promptEngine.Role(params.Role)
	if role == nil {
		available := ct.promptEngine.Roles()
		return "", fmt.Errorf("role %q not found; available roles: %s", params.Role, strings.Join(available, ", "))
	}

	// Compose the system prompt from the role definition.
	systemPrompt, err := ct.promptEngine.Compose(params.Role, nil, nil)
	if err != nil {
		return "", fmt.Errorf("composing prompt for role %q: %w", params.Role, err)
	}

	result, err := ct.spawner.SpawnAndWait(ctx, SpawnOpts{
		SystemPrompt:   systemPrompt,
		InitialMessage: params.Message,
		WorkDir:        ct.workDir,
		MaxDepth:       ct.maxDepth,
		Depth:          ct.depth + 1,
		ProviderName:   ct.providerName,
		Model:          ct.model,
		WorkerID:       params.Role,
		JobID:          ct.jobID,
		TaskID:         ct.taskID,
		Task:           params.Task,
	})
	if err != nil {
		return "", fmt.Errorf("spawning worker %q: %w", params.Role, err)
	}

	return result, nil
}
