package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jefflinse/toasters/internal/compose"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// operatorTools implements runtime.ToolExecutor for the operator's tool set.
// It provides consult_agent (spawn a system agent), surface_to_user (relay
// information to the user), query_job, and query_teams.
type operatorTools struct {
	rt          *runtime.Runtime
	composer    *compose.Composer
	store       db.Store
	systemTools *SystemTools
	workDir     string
}

func newOperatorTools(rt *runtime.Runtime, composer *compose.Composer, store db.Store, systemTools *SystemTools, workDir string) *operatorTools {
	return &operatorTools{
		rt:          rt,
		composer:    composer,
		store:       store,
		systemTools: systemTools,
		workDir:     workDir,
	}
}

// Definitions returns the tool definitions available to the operator LLM.
func (ot *operatorTools) Definitions() []runtime.ToolDef {
	defs := []runtime.ToolDef{
		{
			Name:        "consult_agent",
			Description: "Consult a specialized system agent. Spawns a fresh agent session, blocks until it completes, and returns the agent's response. Use this to delegate analysis, planning, or review tasks.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"agent_name": {
						"type": "string",
						"description": "Name of the system agent to consult (e.g. 'planner', 'decomposer')"
					},
					"message": {
						"type": "string",
						"description": "The message or task to send to the agent"
					},
					"job_id": {
						"type": "string",
						"description": "Optional job ID. Required when consulting the decomposer — sets the job status to decomposing."
					}
				},
				"required": ["agent_name", "message"]
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

	// Append create_job, create_task, and assign_task from SystemTools so the
	// operator can create jobs and act directly on decomposer output without
	// routing through the planner.
	wantFromSystem := map[string]bool{"create_job": true, "create_task": true, "assign_task": true}
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
	case "consult_agent":
		return ot.consultAgent(ctx, args)
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
	default:
		return "", fmt.Errorf("%w: %s", runtime.ErrUnknownTool, name)
	}
}

func (ot *operatorTools) consultAgent(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		AgentName string `json:"agent_name"`
		Message   string `json:"message"`
		JobID     string `json:"job_id"` // optional; used to update job status for decomposer
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing consult_agent args: %w", err)
	}

	if params.AgentName == "" {
		return "", fmt.Errorf("agent_name is required")
	}
	if params.Message == "" {
		return "", fmt.Errorf("message is required")
	}

	// Guard against oversized messages. The decomposer (and other system agents)
	// have tools to explore the workspace themselves — the message should be a
	// brief task description, not embedded file contents.
	const maxConsultMessageBytes = 32 * 1024 // 32 KB
	if len(params.Message) > maxConsultMessageBytes {
		return "", fmt.Errorf(
			"consult_agent message too large (%d bytes, max %d): provide a brief task description only — the agent has glob/grep/read_file tools to explore the workspace itself",
			len(params.Message), maxConsultMessageBytes,
		)
	}

	// When consulting the decomposer, transition the job to decomposing status.
	if isDecomposer(params.AgentName) && params.JobID != "" {
		if err := ot.store.UpdateJobStatus(ctx, params.JobID, db.JobStatusDecomposing); err != nil {
			slog.Warn("failed to set job status to decomposing",
				"job_id", params.JobID,
				"error", err,
			)
			// non-fatal: continue with the decomposer session regardless
		}
	}

	// Verify the agent exists and is a system agent before composing.
	// This prevents user-defined or auto-detected agents from gaining
	// system-level tools (create_job, assign_task, etc.) via consult_agent.
	agent, err := ot.store.GetAgent(ctx, params.AgentName)
	if err != nil {
		return "", fmt.Errorf("unknown agent %q: %w", params.AgentName, err)
	}
	if agent.Source != "system" {
		return "", fmt.Errorf("agent %q is not a system agent (source: %s)", params.AgentName, agent.Source)
	}

	// Compose the agent with teamID="system" for role-based tool injection.
	composed, err := ot.composer.Compose(ctx, params.AgentName, "system")
	if err != nil {
		return "", fmt.Errorf("composing agent %q: %w", params.AgentName, err)
	}

	slog.Info("consulting system agent",
		"agent", params.AgentName,
		"provider", composed.Provider,
		"model", composed.Model,
	)

	// Select the tool executor for this agent. The decomposer gets a combined
	// executor (read-only CoreTools + query_teams from SystemTools); all other
	// system agents get SystemTools directly.
	var toolExecutor runtime.ToolExecutor
	if isDecomposer(params.AgentName) {
		toolExecutor = newDecomposerToolExecutor(ot.systemTools, ot.workDir)
	} else {
		toolExecutor = ot.systemTools
	}

	// Build the filtered tool list from the agent's declared tools. This
	// ensures each system agent only sees the tools it's supposed to have
	// (e.g. planner gets create_job/create_task/assign_task/query_teams/query_job_context,
	// not surface_to_user or query_job).
	var agentTools []runtime.ToolDef
	if len(composed.Tools) > 0 {
		allDefs := toolExecutor.Definitions()
		defsByName := make(map[string]runtime.ToolDef, len(allDefs))
		for _, d := range allDefs {
			defsByName[d.Name] = d
		}
		for _, name := range composed.Tools {
			if d, ok := defsByName[name]; ok {
				agentTools = append(agentTools, d)
			} else {
				slog.Warn("system agent declared unknown tool, skipping",
					"agent", params.AgentName,
					"tool", name,
				)
			}
		}
	}

	// Build SpawnOpts from the composed agent. System agents get SystemTools
	// as their tool executor (not CoreTools/filesystem tools); the decomposer
	// gets decomposerToolExecutor which adds read-only filesystem access.
	// Hidden=false so system agent sessions appear in the TUI Agents panel and grid.
	opts := runtime.SpawnOpts{
		AgentID:        composed.AgentID,
		ProviderName:   composed.Provider,
		Model:          composed.Model,
		SystemPrompt:   composed.SystemPrompt,
		Tools:          agentTools,
		ToolExecutor:   toolExecutor,
		InitialMessage: params.Message,
		WorkDir:        ot.workDir,
	}

	if composed.MaxTurns != nil {
		opts.MaxTurns = *composed.MaxTurns
	}

	result, err := ot.rt.SpawnAndWait(ctx, opts)
	if err != nil {
		return "", fmt.Errorf("consulting agent %q: %w", params.AgentName, err)
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
