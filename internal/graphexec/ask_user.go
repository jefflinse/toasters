package graphexec

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jefflinse/rhizome"
	"github.com/jefflinse/toasters/internal/runtime"
)

// InterruptKindAskUser is the req.Kind that graph nodes use to surface a
// HITL question to the user. Paired with AskUserPayload. v1 is the only
// interrupt kind handled by the executor.
const InterruptKindAskUser = "ask_user"

// AskUserPayload is the rhizome.InterruptRequest.Payload for an ask_user
// interrupt. The executor's handler type-asserts to this shape.
type AskUserPayload struct {
	Question string
	Options  []string
}

// askUserTool returns a ToolExecutor exposing a single "ask_user" tool.
// When invoked, the tool pauses the current graph node via rhizome.Interrupt;
// the executor's interruptHandler surfaces the question through the HITL
// broker and blocks until the user responds. The response is returned as
// the tool result so the LLM sees the user's answer inline with its
// conversation.
//
// Composed via mergeTools into nodes whose roles are allowed to ask —
// investigator, planner, reviewer. Omitted from implement (plan should
// have covered ambiguity) and test (outcomes are pass/fail, not
// negotiable mid-run).
func askUserTool() runtime.ToolExecutor {
	return &askUserExecutor{}
}

type askUserExecutor struct{}

func (a *askUserExecutor) Definitions() []runtime.ToolDef {
	return []runtime.ToolDef{
		{
			Name:        InterruptKindAskUser,
			Description: "Ask the user a clarifying question and wait for their response. Use this only when the task is genuinely ambiguous and you cannot proceed without human input — prefer a concise question and 2–4 suggested options when possible.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"question": {
						"type": "string",
						"description": "The question to ask the user."
					},
					"options": {
						"type": "array",
						"items": {"type": "string"},
						"description": "Optional list of 2–4 suggested answers. The user may type a custom response."
					}
				},
				"required": ["question"]
			}`),
		},
	}
}

func (a *askUserExecutor) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if name != InterruptKindAskUser {
		return "", fmt.Errorf("%w: %s", runtime.ErrUnknownTool, name)
	}
	var payload AskUserPayload
	if len(args) > 0 {
		if err := json.Unmarshal(args, &payload); err != nil {
			return "", fmt.Errorf("parsing ask_user args: %w", err)
		}
	}
	if payload.Question == "" {
		return "", fmt.Errorf("ask_user: question is required")
	}
	resp, err := rhizome.Interrupt(ctx, rhizome.InterruptRequest{
		Kind:    InterruptKindAskUser,
		Payload: payload,
	})
	if err != nil {
		return "", fmt.Errorf("ask_user interrupt: %w", err)
	}
	text, _ := resp.Value.(string)
	return text, nil
}

// Compile-time interface assertion.
var _ runtime.ToolExecutor = (*askUserExecutor)(nil)
