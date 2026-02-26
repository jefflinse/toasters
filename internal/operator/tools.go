package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

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
	return []runtime.ToolDef{
		{
			Name:        "consult_agent",
			Description: "Consult a specialized system agent. Spawns a fresh agent session, blocks until it completes, and returns the agent's response. Use this to delegate analysis, planning, or review tasks.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"agent_name": {
						"type": "string",
						"description": "Name of the system agent to consult (e.g. 'planner', 'reviewer')"
					},
					"message": {
						"type": "string",
						"description": "The message or task to send to the agent"
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
			Name:        "query_teams",
			Description: "List all available teams with their descriptions, lead agents, and member counts.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {}
			}`),
		},
	}
}

// Execute dispatches a tool call by name.
func (ot *operatorTools) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	case "consult_agent":
		return ot.consultAgent(ctx, args)
	case "surface_to_user":
		return ot.surfaceToUser(ctx, args)
	case "query_job":
		return ot.queryJob(ctx, args)
	case "query_teams":
		return ot.queryTeams(ctx)
	default:
		return "", fmt.Errorf("%w: %s", runtime.ErrUnknownTool, name)
	}
}

func (ot *operatorTools) consultAgent(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		AgentName string `json:"agent_name"`
		Message   string `json:"message"`
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

	// Look up and compose the agent from the DB via the Composer.
	// System agents use teamID="system" for role-based tool injection.
	composed, err := ot.composer.Compose(ctx, params.AgentName, "system")
	if err != nil {
		return "", fmt.Errorf("unknown agent %q: %w", params.AgentName, err)
	}

	slog.Info("consulting system agent",
		"agent", params.AgentName,
		"provider", composed.Provider,
		"model", composed.Model,
	)

	// Build SpawnOpts from the composed agent. System agents get SystemTools
	// as their tool executor (not CoreTools/filesystem tools).
	opts := runtime.SpawnOpts{
		AgentID:        composed.AgentID,
		ProviderName:   composed.Provider,
		Model:          composed.Model,
		SystemPrompt:   composed.SystemPrompt,
		ToolExecutor:   ot.systemTools,
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
	var params struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing surface_to_user args: %w", err)
	}

	if params.Text == "" {
		return "", fmt.Errorf("text is required")
	}

	// Create a feed entry in the DB so the TUI can display it.
	if ot.store != nil {
		entry := &db.FeedEntry{
			EntryType: db.FeedEntrySystemEvent,
			Content:   params.Text,
		}
		if err := ot.store.CreateFeedEntry(ctx, entry); err != nil {
			slog.Warn("failed to create feed entry for surface_to_user", "error", err)
		}
	}

	return fmt.Sprintf("Surfaced to user: %s", params.Text), nil
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
