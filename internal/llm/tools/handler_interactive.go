package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

func handleAssignTeam(ctx context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	var args struct {
		TeamName string `json:"team_name"`
		JobID    string `json:"job_id"`
		Task     string `json:"task"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing assign_team args: %w", err)
	}
	// Guard: verify the job exists and get its workspace directory.
	if te.Store == nil {
		return "", fmt.Errorf("database not available")
	}
	job, err := te.Store.GetJob(ctx, args.JobID)
	if err != nil {
		return fmt.Sprintf("job %q does not exist; call job_create first", args.JobID), nil
	}
	// Look up team by name.
	var team agents.Team
	found := false
	for _, t := range te.getTeams() {
		if t.Name == args.TeamName {
			team = t
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("team %q not found", args.TeamName)
	}
	// Use runtime path if available and configured.
	if te.Runtime != nil && te.DefaultProvider != "" {
		prompt := agents.BuildTeamCoordinatorPrompt(team, job.WorkspaceDir)
		opts := runtime.SpawnOpts{
			AgentID:        team.Name,
			ProviderName:   te.DefaultProvider,
			Model:          te.DefaultModel,
			SystemPrompt:   prompt,
			JobID:          args.JobID,
			TeamName:       team.Name,
			InitialMessage: args.Task,
			WorkDir:        job.WorkspaceDir,
			MaxDepth:       1, // coordinators may spawn workers; workers may not spawn further
		}
		sess, err := te.Runtime.SpawnAgent(ctx, opts)
		if err != nil {
			return "", fmt.Errorf("spawning team: %w", err)
		}
		return fmt.Sprintf("started runtime session %s for team %s", sess.ID(), team.Name), nil
	}
	return "", fmt.Errorf("runtime not available: no provider configured")
}

func handleEscalateToUser(_ context.Context, _ *ToolExecutor, call provider.ToolCall) (string, error) {
	// The TUI intercepts escalate_to_user before ExecuteTool is called.
	// If we reach here, return the question as a plain string so the operator can relay it.
	var args struct {
		Question string `json:"question"`
		Context  string `json:"context"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing escalate_to_user args: %w", err)
	}
	return fmt.Sprintf("__escalate__:%s\n\nContext: %s", args.Question, args.Context), nil
}

func handleAskUser(_ context.Context, _ *ToolExecutor, _ provider.ToolCall) (string, error) {
	// ask_user is normally intercepted by the TUI before ExecuteTool is called.
	// This case is a safety fallback.
	return "ask_user was handled by the TUI", nil
}
