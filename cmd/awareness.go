package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/llm"
)

// generateTeamAwareness builds a team-awareness.md file summarizing what each
// team does. For teams with a coordinator agent, the coordinator's full Body is
// used directly. For teams without a coordinator, a short LLM inference is made
// from the team name and worker names. The assembled content is written to
// configDir/team-awareness.md and returned as a string. Returns "" on any error.
func generateTeamAwareness(ctx context.Context, client *llm.Client, teams []agents.Team, configDir string) string {
	if len(teams) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# Team Awareness\n\n")

	for _, team := range teams {
		sb.WriteString(fmt.Sprintf("## `%s`\n", team.Name))

		if team.Coordinator != nil && strings.TrimSpace(team.Coordinator.Body) != "" {
			// Use the coordinator's full instructions directly.
			sb.WriteString(strings.TrimSpace(team.Coordinator.Body))
		} else {
			// No coordinator — infer from team name + worker names via LLM.
			workerNames := make([]string, len(team.Workers))
			for i, w := range team.Workers {
				workerNames[i] = w.Name
			}
			inference := inferTeamCapability(ctx, client, team.Name, workerNames)
			sb.WriteString(inference)
		}

		sb.WriteString("\n\n")
	}

	content := strings.TrimRight(sb.String(), "\n") + "\n"

	// Write to disk so it's inspectable.
	outPath := filepath.Join(configDir, "team-awareness.md")
	if err := os.WriteFile(outPath, []byte(content), 0644); err != nil {
		log.Printf("warning: failed to write team-awareness.md: %v", err)
	}

	return content
}

// inferTeamCapability asks the LLM to describe what a team does based on its
// name and worker names. Returns a plain-text description or a fallback string
// on error.
func inferTeamCapability(ctx context.Context, client *llm.Client, teamName string, workerNames []string) string {
	workers := strings.Join(workerNames, ", ")
	if workers == "" {
		workers = "(none)"
	}
	prompt := fmt.Sprintf(
		"In 2-3 sentences, what does a software development team called %q with workers [%s] do? Be concise and specific.",
		teamName, workers,
	)
	msgs := []llm.Message{{Role: "user", Content: prompt}}
	resp, err := client.ChatCompletion(ctx, msgs)
	if err != nil {
		log.Printf("warning: team awareness inference failed for %q: %v", teamName, err)
		return fmt.Sprintf("Team with workers: %s", workers)
	}
	return strings.TrimSpace(resp)
}
