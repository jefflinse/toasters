package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jefflinse/toasters/internal/runtime"
)

// queryTeamsExecutor exposes only query_teams from SystemTools.
// Used as an ExtraTools overlay for the decomposer so it gets query_teams
// layered on top of the runtime's CoreTools (which provides spawn_worker).
type queryTeamsExecutor struct {
	systemTools *SystemTools
}

// Execute dispatches query_teams to SystemTools.
func (q *queryTeamsExecutor) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if name == "query_teams" {
		return q.systemTools.Execute(ctx, "query_teams", args)
	}
	return "", fmt.Errorf("%w: %s", runtime.ErrUnknownTool, name)
}

// Definitions returns only the query_teams tool definition.
func (q *queryTeamsExecutor) Definitions() []runtime.ToolDef {
	for _, td := range q.systemTools.Definitions() {
		if td.Name == "query_teams" {
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
