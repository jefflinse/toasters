package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jefflinse/toasters/internal/runtime"
)

// queryGraphsExecutor exposes only query_graphs from SystemTools.
// Used as an ExtraTools overlay for the decomposer so it gets query_graphs
// layered on top of the runtime's CoreTools (which provides spawn_worker).
type queryGraphsExecutor struct {
	systemTools *SystemTools
}

// Execute dispatches query_graphs to SystemTools.
func (q *queryGraphsExecutor) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if name == "query_graphs" {
		return q.systemTools.Execute(ctx, "query_graphs", args)
	}
	return "", fmt.Errorf("%w: %s", runtime.ErrUnknownTool, name)
}

// Definitions returns only the query_graphs tool definition.
func (q *queryGraphsExecutor) Definitions() []runtime.ToolDef {
	for _, td := range q.systemTools.Definitions() {
		if td.Name == "query_graphs" {
			return []runtime.ToolDef{td}
		}
	}
	return nil
}

// isDecomposer reports whether agentName refers to the decomposer agent
// (case-insensitive).
func isDecomposer(agentName string) bool {
	return strings.EqualFold(agentName, "decomposer")
}
