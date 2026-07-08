package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jefflinse/toasters/internal/runtime"
)

// kbScope is the only scope the operator reads/writes through these tools.
// System-scope population and worker-side KB tools are later increments.
const kbScope = "user"

var kbSearchDef = runtime.ToolDef{
	Name:        "kb_search",
	Description: "Search durable user memory for standing preferences/conventions relevant to the work. Results are similarity-ranked and may be irrelevant — judge each hit yourself.",
	Parameters: json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "What to search memory for, e.g. a task description or topic"
			},
			"top_k": {
				"type": "integer",
				"description": "Maximum number of results to return (optional; a sensible default is used if omitted)"
			}
		},
		"required": ["query"]
	}`),
}

var kbWriteUserDef = runtime.ToolDef{
	Name:        "kb_write_user",
	Description: "Store a durable user-scope fact the user has EXPLICITLY asked to remember. Do not use for facts you infer on your own.",
	Parameters: json.RawMessage(`{
		"type": "object",
		"properties": {
			"content": {
				"type": "string",
				"description": "The fact to remember, in the user's own terms"
			},
			"source": {
				"type": "string",
				"description": "Where this fact came from (optional; defaults to \"operator\")"
			}
		},
		"required": ["content"]
	}`),
}

// kbSearch implements the kb_search tool. Failures degrade to a plain-text
// message rather than a propagated error — a raw tool error can derail a
// small local model's turn, and "memory is unavailable" is something the
// model can reason past on its own.
func (ot *operatorTools) kbSearch(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Query string `json:"query"`
		TopK  int    `json:"top_k"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing kb_search args: %w", err)
	}
	if strings.TrimSpace(params.Query) == "" {
		return "kb_search needs a non-empty query — nothing was searched.", nil
	}
	if ot.kb == nil {
		return "Memory is currently unavailable (the embedding backend did not respond). Proceed without it.", nil
	}

	hits, err := ot.kb.Recall(ctx, kbScope, params.Query, params.TopK)
	if err != nil {
		return "Memory is currently unavailable (the embedding backend did not respond). Proceed without it.", nil
	}

	if len(hits) == 0 {
		return fmt.Sprintf("No relevant facts found in memory for %q.", params.Query), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d memory result(s) for %q — ranked by similarity. A result is returned even when nothing truly matches; judge each on its own merits and use it only if it genuinely applies:\n", len(hits), params.Query)
	for i, h := range hits {
		fmt.Fprintf(&b, "%d. [score %.2f] (source: %s) %s\n", i+1, h.Score, h.Source, h.Content)
	}

	return strings.TrimRight(b.String(), "\n"), nil
}

// kbWriteUser implements the kb_write_user tool. Like kbSearch, backend
// failures degrade to a plain-text message with a nil error rather than
// propagating — the fact simply wasn't stored, which the model can mention
// to the user rather than derailing its turn.
func (ot *operatorTools) kbWriteUser(ctx context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Content string `json:"content"`
		Source  string `json:"source"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parsing kb_write_user args: %w", err)
	}
	content := strings.TrimSpace(params.Content)
	if content == "" {
		return "kb_write_user requires non-empty content — nothing was stored.", nil
	}
	if ot.kb == nil {
		return "Could not save to memory right now (the embedding backend is unavailable). The fact was not stored.", nil
	}

	source := params.Source
	if source == "" {
		source = "operator"
	}

	id, err := ot.kb.Remember(ctx, kbScope, source, content)
	if err != nil {
		return "Could not save to memory right now (the embedding backend is unavailable). The fact was not stored.", nil
	}

	return fmt.Sprintf("Remembered (id %s).", id), nil
}
