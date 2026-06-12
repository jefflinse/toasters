package graphexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	Question  string
	Options   []string
	Questions []PromptQuestion
}

// PromptQuestion is a single question in a multi-question ask_user round.
type PromptQuestion struct {
	Question string
	Options  []string
}

// askUserSchema is the JSON Schema for the ask_user tool's argument.
var askUserSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "questions": {
      "type": "array",
      "description": "Several questions to ask the user at once as one round. Prefer this when you need more than one piece of information so the user answers them together instead of one at a time.",
      "items": {
        "type": "object",
        "properties": {
          "question": {"type": "string", "description": "The question text."},
          "options": {
            "type": "array",
            "items": {"type": "string"},
            "description": "Optional 2–4 suggested answers. The user may type a custom response."
          }
        },
        "required": ["question"]
      }
    },
    "question": {
      "type": "string",
      "description": "A single question to ask. Shorthand for a one-question round; prefer the questions array when asking more than one thing."
    },
    "options": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Optional list of 2–4 suggested answers for the single question. The user may type a custom response."
    }
  }
}`)

// AskUserTool returns the mid-loop ask_user tool. When the model calls it,
// the handler blocks the mycelium agent loop via rhizome.Interrupt; the executor's
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
		Description: "Ask the user one or more clarifying questions and wait for their answers. Use this only when the task is genuinely ambiguous and you cannot proceed without human input. If you need several pieces of information, pass them all in `questions` so the user answers them in one round rather than one at a time. Prefer 2–4 suggested options per question.",
		Parameters:  askUserSchema,
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			payload, err := parseAskUserPayload(args)
			if err != nil {
				return "", fmt.Errorf("parsing ask_user args: %w", err)
			}
			if payload.Question == "" && len(payload.Questions) == 0 {
				return "", fmt.Errorf("ask_user: a question is required")
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

// parseAskUserPayload decodes ask_user arguments, tolerating the inconsistent
// shapes small local models emit for "questions" (a bare string, an array of
// strings, a single object, or the intended array of objects). Without this a
// strict unmarshal rejects all but the canonical form and the node can't ask.
func parseAskUserPayload(args json.RawMessage) (AskUserPayload, error) {
	var payload AskUserPayload
	if len(bytes.TrimSpace(args)) == 0 {
		return payload, nil
	}
	var raw struct {
		Question  string          `json:"question"`
		Options   []string        `json:"options"`
		Questions json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(args, &raw); err != nil {
		return payload, err
	}
	payload.Question = raw.Question
	payload.Options = raw.Options
	qs, err := parsePromptQuestions(raw.Questions)
	if err != nil {
		return payload, err
	}
	payload.Questions = qs
	// The model sometimes packs the whole questions array into the single
	// "question" string field too; route it through the same parser.
	if len(payload.Questions) == 0 && payload.Question != "" {
		if fromQ, perr := questionsFromString(payload.Question); perr == nil && len(fromQ) > 0 {
			if !(len(fromQ) == 1 && fromQ[0].Question == payload.Question) {
				payload.Questions = fromQ
				payload.Question = ""
			}
		}
	}
	return payload, nil
}

// parsePromptQuestions leniently decodes the ask_user "questions" field into
// graph-node PromptQuestions. See operator.parsePromptQuestions for the same
// logic on the operator side.
func parsePromptQuestions(raw json.RawMessage) ([]PromptQuestion, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	switch raw[0] {
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return questionsFromString(s)
	case '{':
		var q PromptQuestion
		if err := json.Unmarshal(raw, &q); err != nil {
			return nil, err
		}
		return []PromptQuestion{q}, nil
	case '[':
		var elems []json.RawMessage
		if err := json.Unmarshal(raw, &elems); err != nil {
			return nil, err
		}
		var out []PromptQuestion
		for _, el := range elems {
			el = bytes.TrimSpace(el)
			if len(el) == 0 {
				continue
			}
			if el[0] == '"' {
				var s string
				if err := json.Unmarshal(el, &s); err != nil {
					return nil, err
				}
				qs, err := questionsFromString(s)
				if err != nil {
					return nil, err
				}
				out = append(out, qs...)
			} else {
				var q PromptQuestion
				if err := json.Unmarshal(el, &q); err != nil {
					return nil, err
				}
				out = append(out, q)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unexpected questions JSON: %s", string(raw))
	}
}

// questionsFromString turns a string value into questions, recursing when the
// model double-encoded the array as a JSON string. Mirrors operator.go.
func questionsFromString(s string) ([]PromptQuestion, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if s[0] == '[' || s[0] == '{' {
		if qs, err := parsePromptQuestions(json.RawMessage(s)); err == nil && len(qs) > 0 {
			return qs, nil
		}
	}
	return []PromptQuestion{{Question: s}}, nil
}
