package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// operatorTools implements runtime.ToolExecutor for the operator's tool set.
// It provides consult_worker (spawn a system worker), surface_to_user (relay
// information to the user), query_job, and query_teams.
type operatorTools struct {
	rt              *runtime.Runtime
	promptEngine    *prompt.Engine
	defaultProvider string
	defaultModel    string
	store           db.Store
	systemTools     *SystemTools
	workDir         string
	promptUser      func(ctx context.Context, requestID, question string, options []string) (string, error) // set by Operator after construction
}

func newOperatorTools(rt *runtime.Runtime, promptEngine *prompt.Engine, defaultProvider, defaultModel string, store db.Store, systemTools *SystemTools, workDir string) *operatorTools {
	return &operatorTools{
		rt:              rt,
		promptEngine:    promptEngine,
		defaultProvider: defaultProvider,
		defaultModel:    defaultModel,
		store:           store,
		systemTools:     systemTools,
		workDir:         workDir,
	}
}

// Definitions returns the tool definitions available to the operator LLM.
func (ot *operatorTools) Definitions() []runtime.ToolDef {
	defs := []runtime.ToolDef{
		{
			Name:        "consult_worker",
			Description: "Consult a specialized system worker. Spawns a fresh worker session, blocks until it completes, and returns the worker's response. Use this to delegate analysis, planning, or review tasks.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"worker_name": {
						"type": "string",
						"description": "Name of the system worker to consult (e.g. 'decomposer', 'scheduler', 'blocker-handler')"
					},
					"message": {
						"type": "string",
						"description": "The message or task to send to the worker"
					},
					"job_id": {
						"type": "string",
						"description": "Optional job ID. Required when consulting the decomposer — sets the job status to decomposing."
					}
				},
				"required": ["worker_name", "message"]
			}`),
		},
		{
			Name:        "surface_to_user",
			Description: "Surface information to the user. Use this to relay important findings, summaries, or status updates that the user should see.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"text": {
						"type": "string",
						"description": "The text to show to the user"
					}
				},
				"required": ["text"]
			}`),
		},
		{
			Name:        "query_job",
			Description: "Get the current state of a job including all its tasks and their statuses.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"job_id": {
						"type": "string",
						"description": "ID of the job to query"
					}
				},
				"required": ["job_id"]
			}`),
		},
		{
			Name:        "list_jobs",
			Description: "List all jobs with their IDs, titles, statuses, and workspace directories. Use this to find a job by name when you don't have its ID.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {}
			}`),
		},
		{
			Name:        "query_teams",
			Description: "List all available teams with their descriptions, lead agents, and member counts.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {}
			}`),
		},
		{
			Name:        "setup_workspace",
			Description: "Clone one or more git repositories into the job's workspace directory before decomposition. Sets the job status to setting_up while running. Returns the workspace path and a summary of what was cloned.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"job_id": {
						"type": "string",
						"description": "The ID of the job whose workspace should be set up"
					},
					"repos": {
						"type": "array",
						"description": "List of repositories to clone",
						"items": {
							"type": "object",
							"properties": {
								"url": {"type": "string", "description": "Git repository URL to clone"},
								"name": {"type": "string", "description": "Directory name to clone into (defaults to repo name from URL if omitted)"}
							},
							"required": ["url"]
						}
					}
				},
				"required": ["job_id", "repos"]
			}`),
		},
	}

	defs = append(defs, runtime.ToolDef{
		Name:        "ask_user",
		Description: "Ask the user a question and wait for their response. Use this when you need clarification, confirmation, or a decision from the user. Provide suggested options when possible to make it easier for the user to respond.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"question": {
					"type": "string",
					"description": "The question to ask the user"
				},
				"options": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Optional list of suggested answers. The user can also type a custom response."
				}
			},
			"required": ["question"]
		}`),
	})

	// Append create_job, create_task, assign_task, save_work_request, and
	// start_job from SystemTools so the operator can create jobs, persist
	// work requests, and act directly on decomposer output.
	wantFromSystem := map[string]bool{"create_job": true, "create_task": true, "assign_task": true, "save_work_request": true, "start_job": true}
	for _, td := range ot.systemTools.Definitions() {
		if wantFromSystem[td.Name] {
			defs = append(defs, td)
		}
	}

	return defs
}

// Execute dispatches a tool call by name.
func (ot *operatorTools) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	case "consult_worker":
		return ot.consultWorker(ctx, args)
	case "surface_to_user":
		return ot.surfaceToUser(ctx, args)
	case "list_jobs":
		return ot.listJobs(ctx)
	case "query_job":
		return ot.queryJob(ctx, args)
	case "query_teams":
		return ot.queryTeams(ctx)
	case "setup_workspace":
		return ot.setupWorkspace(ctx, args)
	case "create_job":
		return ot.systemTools.Execute(ctx, "create_job", args)
	case "create_task":
		return ot.systemTools.Execute(ctx, "create_task", args)
	case "assign_task":
		return ot.systemTools.Execute(ctx, "assign_task", args)
	case "save_work_request":
		return ot.systemTools.Execute(ctx, "save_work_request", args)
	case "start_job":
		return ot.systemTools.Execute(ctx, "start_job", args)
	case "ask_user":
		return ot.askUser(ctx, args)
	default:
		return "", fmt.Errorf("%w: %s", runtime.ErrUnknownTool, name)
	}
}

func (ot *operatorTools) consultWorker(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		WorkerName string `json:"worker_name"`
		Message    string `json:"message"`
		JobID      string `json:"job_id"` // optional; used to update job status for decomposer
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing consult_worker args: %w", err)
	}

	if params.WorkerName == "" {
		return "", fmt.Errorf("worker_name is required")
	}
	if params.Message == "" {
		return "", fmt.Errorf("message is required")
	}

	// Guard against oversized messages. System workers have their own tools
	// to explore workspaces — the message should be a brief task description
	// or work request, not embedded file contents.
	const maxConsultMessageBytes = 32 * 1024 // 32 KB
	if len(params.Message) > maxConsultMessageBytes {
		return "", fmt.Errorf(
			"consult_worker message too large (%d bytes, max %d): provide a brief task description only — the worker has tools to explore the workspace itself",
			len(params.Message), maxConsultMessageBytes,
		)
	}

	// When consulting the decomposer, transition the job to decomposing status.
	if isDecomposer(params.WorkerName) && params.JobID != "" {
		if err := ot.store.UpdateJobStatus(ctx, params.JobID, db.JobStatusDecomposing); err != nil {
			slog.Warn("failed to set job status to decomposing",
				"job_id", params.JobID,
				"error", err,
			)
			// non-fatal: continue with the decomposer session regardless
		}
	}

	// Verify the role exists in the prompt engine and is a system role.
	// This prevents user-defined roles from gaining system-level tools
	// (create_job, assign_task, etc.) via consult_worker.
	if ot.promptEngine == nil {
		return "", fmt.Errorf("prompt engine not configured")
	}
	role := ot.promptEngine.Role(params.WorkerName)
	if role == nil {
		return "", fmt.Errorf("unknown system worker %q: no role found in prompt engine", params.WorkerName)
	}
	if role.Source != "system" {
		return "", fmt.Errorf("role %q is not a system role (source: %s)", params.WorkerName, role.Source)
	}

	systemPrompt, err := ot.promptEngine.Compose(params.WorkerName, nil)
	if err != nil {
		return "", fmt.Errorf("composing system worker %q: %w", params.WorkerName, err)
	}
	declaredTools := role.Tools
	workerProvider := ot.defaultProvider
	workerModel := ot.defaultModel

	slog.Info("consulting system worker",
		"worker", params.WorkerName,
		"provider", workerProvider,
		"model", workerModel,
	)

	// Determine tool executor and work directory. The decomposer uses CoreTools
	// (built by the runtime, which includes spawn_worker) with query_teams as
	// an extra-tools overlay. All other system workers get SystemTools directly.
	var toolExecutor runtime.ToolExecutor
	var extraTools runtime.ToolExecutor
	workDir := ot.workDir

	if isDecomposer(params.WorkerName) {
		// Decomposer: don't set toolExecutor so the runtime builds CoreTools
		// (including spawn_worker). Layer query_teams on top as ExtraTools.
		extraTools = &queryTeamsExecutor{systemTools: ot.systemTools}

		// Use the job's workspace directory so spawned explorers operate
		// in the right location.
		if params.JobID != "" {
			job, jobErr := ot.store.GetJob(ctx, params.JobID)
			if jobErr == nil && job.WorkspaceDir != "" {
				workDir = job.WorkspaceDir
			}
		}
	} else {
		toolExecutor = ot.systemTools
	}

	// Build the filtered tool list from the worker's declared tools. This
	// ensures each system worker only sees the tools it's supposed to have.
	//
	// For the decomposer, we use DisallowedTools instead of Tools because
	// spawn_worker comes from CoreTools (built by the runtime), and its
	// definition isn't available yet. We deny everything except spawn_worker
	// so the decomposer sees only spawn_worker + query_teams (from ExtraTools).
	var workerTools []runtime.ToolDef
	var disallowedTools []string

	if isDecomposer(params.WorkerName) {
		// Block file and shell tools — the decomposer delegates exploration
		// to Explorer workers via spawn_worker.
		disallowedTools = []string{
			"read_file", "write_file", "edit_file", "glob", "grep",
			"shell", "web_fetch", "report_task_progress",
		}
	} else if len(declaredTools) > 0 {
		allDefs := toolExecutor.Definitions()
		defsByName := make(map[string]runtime.ToolDef, len(allDefs))
		for _, d := range allDefs {
			defsByName[d.Name] = d
		}
		for _, name := range declaredTools {
			if d, ok := defsByName[name]; ok {
				workerTools = append(workerTools, d)
			} else {
				slog.Warn("system worker declared unknown tool, skipping",
					"worker", params.WorkerName,
					"tool", name,
				)
			}
		}
	}

	opts := runtime.SpawnOpts{
		WorkerID:        params.WorkerName,
		ProviderName:    workerProvider,
		Model:           workerModel,
		SystemPrompt:    systemPrompt,
		Tools:           workerTools,
		DisallowedTools: disallowedTools,
		ToolExecutor:    toolExecutor,
		ExtraTools:      extraTools,
		InitialMessage:  params.Message,
		WorkDir:         workDir,
	}

	result, err := ot.rt.SpawnAndWait(ctx, opts)
	if err != nil {
		return "", fmt.Errorf("consulting worker %q: %w", params.WorkerName, err)
	}

	return result, nil
}

func (ot *operatorTools) surfaceToUser(ctx context.Context, args json.RawMessage) (string, error) {
	if ot.systemTools == nil {
		return "", fmt.Errorf("surface_to_user unavailable: no system tools configured")
	}
	return ot.systemTools.Execute(ctx, "surface_to_user", args)
}

// listJobs returns all jobs with their IDs, titles, statuses, and workspace directories.
func (ot *operatorTools) listJobs(ctx context.Context) (string, error) {
	jobs, err := ot.store.ListAllJobs(ctx)
	if err != nil {
		return "", fmt.Errorf("listing jobs: %w", err)
	}

	if len(jobs) == 0 {
		return "No jobs.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Jobs (%d):\n", len(jobs))
	for _, job := range jobs {
		fmt.Fprintf(&b, "\n- %s (id: %s)\n", job.Title, job.ID)
		fmt.Fprintf(&b, "  Status: %s\n", job.Status)
		if job.WorkspaceDir != "" {
			fmt.Fprintf(&b, "  Workspace: %s\n", contractHome(job.WorkspaceDir))
		}
	}

	return b.String(), nil
}

// queryJob delegates to SystemTools.queryJob for DB-backed job queries.
func (ot *operatorTools) queryJob(ctx context.Context, args json.RawMessage) (string, error) {
	if ot.systemTools == nil {
		return "", fmt.Errorf("query_job unavailable: no system tools configured")
	}
	return ot.systemTools.Execute(ctx, "query_job", args)
}

// queryTeams delegates to SystemTools.queryTeams for DB-backed team queries.
func (ot *operatorTools) queryTeams(ctx context.Context) (string, error) {
	if ot.systemTools == nil {
		return "", fmt.Errorf("query_teams unavailable: no system tools configured")
	}
	return ot.systemTools.Execute(ctx, "query_teams", json.RawMessage(`{}`))
}

func (ot *operatorTools) askUser(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing ask_user args: %w", err)
	}
	if params.Question == "" {
		return "", fmt.Errorf("question is required")
	}
	if ot.promptUser == nil {
		return "", fmt.Errorf("ask_user is not available: no prompt handler configured")
	}

	requestID := fmt.Sprintf("ask-%d", time.Now().UnixNano())
	return ot.promptUser(ctx, requestID, params.Question, params.Options)
}

// operatorToolsToProviderTools converts operator tool definitions to provider.Tool format.
func operatorToolsToProviderTools(defs []runtime.ToolDef) []provider.Tool {
	tools := make([]provider.Tool, len(defs))
	for i, td := range defs {
		tools[i] = provider.Tool{
			Name:        td.Name,
			Description: td.Description,
			Parameters:  td.Parameters,
		}
	}
	return tools
}
