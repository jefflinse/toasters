package runtime

import (
	"context"

	"github.com/jefflinse/toasters/internal/compose"
)

// TeamLeadSpawner is the interface for spawning team lead sessions.
// It is defined here (in runtime) so that *Runtime can implement it without
// creating an import cycle: operator → runtime is fine; runtime → operator
// would be a cycle.
type TeamLeadSpawner interface {
	SpawnTeamLead(ctx context.Context, composed *compose.ComposedAgent, taskID, jobID, workDir, taskDescription string) error
}

// SpawnTeamLead implements TeamLeadSpawner. It spawns a team lead agent session
// from a fully composed agent definition. The session runs fire-and-forget at
// depth 0 (team leads may spawn workers; workers may not spawn further agents).
// taskDescription is sent as the initial user message to kick off the conversation.
func (r *Runtime) SpawnTeamLead(ctx context.Context, composed *compose.ComposedAgent, taskID, jobID, workDir, taskDescription string) error {
	// Resolve tool definitions from the composed tool name list. Team leads
	// receive the full default CoreTools set filtered to the composed tool names.
	// The actual ToolDef schemas are provided by CoreTools.Definitions() at
	// session start; here we pass nil Tools so the session builds its own
	// CoreTools stack (which will include spawn_agent at depth 0).
	opts := SpawnOpts{
		AgentID:        composed.AgentID,
		ProviderName:   composed.Provider,
		Model:          composed.Model,
		SystemPrompt:   composed.SystemPrompt,
		InitialMessage: taskDescription,
		JobID:          jobID,
		TaskID:         taskID,
		TeamName:       composed.TeamID,
		WorkDir:        workDir,
		Depth:          0,
		MaxDepth:       defaultMaxDepth,
	}

	// Apply tool filter from composition if specified.
	// Use nil check (not len > 0) so that an explicitly empty slice still
	// triggers the filter path, bypassing the denylist only when Tools is
	// truly unset (nil).
	if composed.Tools != nil {
		// Build a temporary CoreTools solely to call Definitions() and resolve
		// tool names to ToolDef values for SpawnOpts.Tools.
		//
		// WARNING — coupling risk: this tmp instance is constructed independently
		// from the CoreTools that SpawnAgent will build for the actual session.
		// If those two construction paths ever diverge (e.g. different options),
		// the ToolDef values here could describe tools the session's executor
		// doesn't actually have. Keep this construction in sync with how
		// SpawnAgent builds its own CoreTools.
		//
		// TODO: expose a ToolDefsByName() helper on CoreTools to eliminate this
		// throwaway instance and the coupling risk entirely.
		tmp := NewCoreTools(
			workDir,
			WithShell(true),
			WithSpawner(r, 0, defaultMaxDepth),
		)
		allDefs := tmp.Definitions()
		byName := make(map[string]ToolDef, len(allDefs))
		for _, td := range allDefs {
			byName[td.Name] = td
		}
		var toolDefs []ToolDef
		for _, name := range composed.Tools {
			if td, ok := byName[name]; ok {
				toolDefs = append(toolDefs, td)
			}
		}
		if len(toolDefs) > 0 {
			opts.Tools = toolDefs
		}
	}

	if composed.MaxTurns != nil {
		opts.MaxTurns = *composed.MaxTurns
	}

	_, err := r.SpawnAgent(ctx, opts)
	return err
}
