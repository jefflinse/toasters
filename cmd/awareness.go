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

// generateTeamAwareness builds ~/.config/toasters/team-awareness.md
// with one-sentence dispatch summaries for each team.
// Returns the file content, or "" on error.
func generateTeamAwareness(ctx context.Context, client *llm.Client, teams []agents.Team, configDir string) string {
	if len(teams) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# Teams\n\n")

	for _, team := range teams {
		sb.WriteString(fmt.Sprintf("## %s\n\n", team.Name))
		sb.WriteString(summarizeTeam(ctx, client, team))
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

// summarizeTeam asks the LLM for a one-sentence "Use this team when..."
// summary suitable for operator dispatch decisions.
func summarizeTeam(ctx context.Context, client *llm.Client, team agents.Team) string {
	var prompt string
	if team.Coordinator != nil && strings.TrimSpace(team.Coordinator.Body) != "" {
		prompt = fmt.Sprintf(
			`You are summarizing a software development team for an AI dispatcher.

Team name: %q
Coordinator instructions:
%s

Write exactly one sentence starting with "Use this team when" that describes what kinds of tasks should be routed to this team. Be specific and concise. Output only the sentence, nothing else.`,
			team.Name,
			strings.TrimSpace(team.Coordinator.Body),
		)
	} else {
		workerNames := make([]string, len(team.Workers))
		for i, w := range team.Workers {
			workerNames[i] = w.Name
		}
		prompt = fmt.Sprintf(
			`You are summarizing a software development team for an AI dispatcher.

Team name: %q
Workers: %s

Write exactly one sentence starting with "Use this team when" that describes what kinds of tasks should be routed to this team. Be specific and concise. Output only the sentence, nothing else.`,
			team.Name,
			strings.Join(workerNames, ", "),
		)
	}

	msgs := []llm.Message{{Role: "user", Content: prompt}}
	resp, err := client.ChatCompletion(ctx, msgs)
	if err != nil {
		log.Printf("warning: team awareness inference failed for %q: %v", team.Name, err)
		// Fallback: generic sentence
		return fmt.Sprintf("Use this team when you need help from the %s team.", team.Name)
	}
	return strings.TrimSpace(resp)
}
