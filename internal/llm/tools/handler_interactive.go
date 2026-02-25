package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/job"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

func handleAssignTeam(_ context.Context, te *ToolExecutor, call provider.ToolCall) (string, error) {
	if te.Gateway == nil {
		return "", fmt.Errorf("gateway not initialized")
	}
	var args struct {
		TeamName string `json:"team_name"`
		JobID    string `json:"job_id"`
		Task     string `json:"task"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return "", fmt.Errorf("parsing assign_team args: %w", err)
	}
	// Guard: verify the job exists before dispatching to a team.
	jobDir := filepath.Join(job.JobsDir(te.WorkspaceDir), args.JobID)
	if _, loadErr := job.Load(jobDir); loadErr != nil {
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
	// Persist team assignment to the first task.
	if tasks, err := job.ListTasks(jobDir); err == nil && len(tasks) > 0 {
		_ = job.SetTaskTeam(tasks[0].Dir, args.TeamName)
	}
	// Try runtime path first if available and configured.
	if te.Runtime != nil && te.DefaultProvider != "" {
		prompt := agents.BuildTeamCoordinatorPrompt(team, jobDir)
		opts := runtime.SpawnOpts{
			AgentID:        team.Name,
			ProviderName:   te.DefaultProvider,
			Model:          te.DefaultModel,
			SystemPrompt:   prompt,
			JobID:          args.JobID,
			InitialMessage: args.Task,
			WorkDir:        jobDir,
			MaxDepth:       1, // coordinators may spawn workers; workers may not spawn further
		}
		sess, err := te.Runtime.SpawnAgent(context.Background(), opts)
		if err != nil {
			log.Printf("runtime spawn failed, falling back to gateway: %v", err)
			// Fall through to gateway path below.
		} else {
			return fmt.Sprintf("started runtime session %s for team %s", sess.ID(), team.Name), nil
		}
	}
	// Fall through to gateway path.
	slotID, alreadyRunning, err := te.Gateway.SpawnTeam(args.TeamName, args.JobID, args.Task, team)
	if err != nil {
		return "", fmt.Errorf("spawning team: %w", err)
	}
	if alreadyRunning {
		return fmt.Sprintf("already running: slot %d (do not call assign_team again for this team)", slotID), nil
	}
	return fmt.Sprintf("started: slot %d", slotID), nil
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
