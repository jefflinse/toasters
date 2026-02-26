package operator

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// operatorTools implements runtime.ToolExecutor for the operator's tool set.
// It provides consult_agent (spawn a system agent) and surface_to_user (relay
// information to the user).
type operatorTools struct {
	rt           *runtime.Runtime
	providerName string
	model        string
	workDir      string

	// agentPrompts maps agent names to their system prompts.
	agentPrompts map[string]string
}

func newOperatorTools(rt *runtime.Runtime, providerName, model, workDir string) *operatorTools {
	return &operatorTools{
		rt:           rt,
		providerName: providerName,
		model:        model,
		workDir:      workDir,
		agentPrompts: defaultAgentPrompts(),
	}
}

// defaultAgentPrompts returns hardcoded system prompts for spike agents.
func defaultAgentPrompts() map[string]string {
	return map[string]string{
		"planner":  "You are a planning agent. Analyze the user's request and describe what tasks would be needed. Be concise and actionable. List concrete steps.",
		"reviewer": "You are a code review agent. Analyze the provided code or description and provide feedback on quality, correctness, and potential improvements.",
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
	}
}

// Execute dispatches a tool call by name.
func (ot *operatorTools) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	case "consult_agent":
		return ot.consultAgent(ctx, args)
	case "surface_to_user":
		return ot.surfaceToUser(ctx, args)
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

	systemPrompt, ok := ot.agentPrompts[params.AgentName]
	if !ok {
		return "", fmt.Errorf("unknown agent %q (available: planner, reviewer)", params.AgentName)
	}

	result, err := ot.rt.SpawnAndWait(ctx, runtime.SpawnOpts{
		AgentID:        params.AgentName,
		ProviderName:   ot.providerName,
		Model:          ot.model,
		SystemPrompt:   systemPrompt,
		InitialMessage: params.Message,
		WorkDir:        ot.workDir,
	})
	if err != nil {
		return "", fmt.Errorf("consulting agent %q: %w", params.AgentName, err)
	}

	return result, nil
}

func (ot *operatorTools) surfaceToUser(_ context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing surface_to_user args: %w", err)
	}

	if params.Text == "" {
		return "", fmt.Errorf("text is required")
	}

	// The text is returned as the tool result. The operator will see it and
	// can incorporate it into its response. The OnText callback handles
	// streaming the operator's final response to the user.
	return fmt.Sprintf("Surfaced to user: %s", params.Text), nil
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
