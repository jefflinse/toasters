package graphexec

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jefflinse/mycelium/agent"
	"github.com/jefflinse/rhizome"
	"github.com/jefflinse/toasters/internal/runtime"
)

// Tool name constants for building allowlists.
const (
	ToolReadFile    = "read_file"
	ToolWriteFile   = "write_file"
	ToolEditFile    = "edit_file"
	ToolGlob        = "glob"
	ToolGrep        = "grep"
	ToolShell       = "shell"
	ToolWebFetch    = "web_fetch"
	ToolQueryGraphs = "query_graphs"
)

// Common tool sets for node builders. Roles that need tools outside
// their access base (e.g. fine-decomposer needing query_graphs under
// readonly access) opt in via the role frontmatter's `tools:` list —
// see toolsForRole in nodes.go.
var (
	// ReadOnlyTools allows only non-mutating, workspace-oriented tools.
	ReadOnlyTools = []string{ToolReadFile, ToolGlob, ToolGrep}

	// WriteTools allows mutation plus reading.
	WriteTools = []string{ToolReadFile, ToolWriteFile, ToolEditFile, ToolGlob, ToolGrep, ToolShell}

	// TestTools allows running tests.
	TestTools = []string{ToolReadFile, ToolGlob, ToolGrep, ToolShell}
)

// AdaptTools converts a runtime.ToolExecutor — the pre-mycelium interface
// used by toasters tools — into a slice of mycelium agent.Tool values.
//
// When allowed is non-empty, only tools whose names appear in allowed are
// exposed. An empty allowed slice means "no tools from the inner executor"
// (not "all tools") — pass nil if you want to skip filtering entirely.
func AdaptTools(inner runtime.ToolExecutor, allowed []string) []agent.Tool {
	if inner == nil {
		return nil
	}
	var allow map[string]bool
	if allowed != nil {
		allow = make(map[string]bool, len(allowed))
		for _, n := range allowed {
			allow[n] = true
		}
	}

	defs := inner.Definitions()
	tools := make([]agent.Tool, 0, len(defs))
	for _, d := range defs {
		if allow != nil && !allow[d.Name] {
			continue
		}
		name := d.Name
		tools = append(tools, agent.Tool{
			Name:        name,
			Description: d.Description,
			Parameters:  d.Parameters,
			Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
				return inner.Execute(ctx, name, args)
			},
		})
	}
	return tools
}

// --- ask_user tool ---

// InterruptKindAskUser is the req.Kind graph nodes use to surface a HITL
// question. The executor's interrupt handler routes it through the HITL
// broker and the TUI.
const InterruptKindAskUser = "ask_user"

// AskUserPayload is the rhizome.InterruptRequest.Payload for an ask_user
// interrupt. Mirrors the old graphexec payload — kept so the executor's
// interruptHandler contract is unchanged.
type AskUserPayload struct {
	Question string
	Options  []string
}

// askUserSchema is the JSON Schema for the ask_user tool's argument.
var askUserSchema = json.RawMessage(`{
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
}`)

// AskUserTool returns the mid-loop ask_user tool. When the model calls it,
// the handler blocks the agent loop via rhizome.Interrupt; the executor's
// interrupt handler surfaces the question through the HITL broker and
// returns the user's response as the tool result. The model then continues
// its loop with the answer in hand.
//
// Distinct from mycelium's request_context terminal — ask_user is a
// narrow clarifying question the model expects to answer and keep going,
// while request_context terminates the run asking for broader structural
// context upstream in the graph.
func AskUserTool() agent.Tool {
	return agent.Tool{
		Name:        InterruptKindAskUser,
		Description: "Ask the user a clarifying question and wait for their response. Use this only when the task is genuinely ambiguous and you cannot proceed without human input — prefer a concise question and 2–4 suggested options when possible.",
		Parameters:  askUserSchema,
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
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
		},
	}
}
