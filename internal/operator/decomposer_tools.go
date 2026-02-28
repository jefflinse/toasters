package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jefflinse/toasters/internal/runtime"
)

// decomposerToolExecutor combines read-only CoreTools with SystemTools query_teams
// for use by the decomposer agent. It exposes exactly four tools:
// glob, grep, read_file (from CoreTools) and query_teams (from SystemTools).
type decomposerToolExecutor struct {
	systemTools *SystemTools
	coreTools   *runtime.CoreTools
}

// newDecomposerToolExecutor creates a decomposerToolExecutor rooted at workDir.
func newDecomposerToolExecutor(systemTools *SystemTools, workDir string) *decomposerToolExecutor {
	return &decomposerToolExecutor{
		systemTools: systemTools,
		// Read-only: no shell, no spawner, no store (no progress tools).
		coreTools: runtime.NewCoreTools(workDir),
	}
}

// Execute dispatches to the appropriate underlying executor.
func (d *decomposerToolExecutor) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	case "query_teams":
		return d.systemTools.Execute(ctx, "query_teams", args)
	case "glob", "grep", "read_file":
		return d.coreTools.Execute(ctx, name, args)
	default:
		return "", fmt.Errorf("tool not available to decomposer: %s", name)
	}
}

// Definitions returns the union of definitions for the four decomposer tools.
func (d *decomposerToolExecutor) Definitions() []runtime.ToolDef {
	// Pull the exact definitions from each underlying executor so descriptions
	// and parameter schemas stay in sync with their implementations.
	wantCore := map[string]bool{"glob": true, "grep": true, "read_file": true}
	wantSystem := map[string]bool{"query_teams": true}

	var defs []runtime.ToolDef

	for _, td := range d.coreTools.Definitions() {
		if wantCore[td.Name] {
			defs = append(defs, td)
		}
	}
	for _, td := range d.systemTools.Definitions() {
		if wantSystem[td.Name] {
			defs = append(defs, td)
		}
	}

	return defs
}

// isDecomposer reports whether agentName refers to the decomposer agent
// (case-insensitive).
func isDecomposer(agentName string) bool {
	return strings.EqualFold(agentName, "decomposer")
}
