package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/service"
)

// generateTeamAwareness builds ~/.config/toasters/team-awareness.md
// with one-sentence dispatch summaries for each team.
// Returns the file content, or "" on error.
func generateTeamAwareness(ctx context.Context, client provider.Provider, teams []service.TeamView, configDir string) string {
	if len(teams) == 0 {
		return ""
	}

	// Use fallback if no LLM provider is available.
	// This occurs in client mode where the LLM runs server-side.
	if client == nil {
		var sb strings.Builder
		sb.WriteString("# Teams\n\n")
		for _, team := range teams {
			fmt.Fprintf(&sb, "## %s\n\n", team.Name())
			fmt.Fprintf(&sb, "%s\n\n", teamFallbackText(team.Name()))
		}
		content := strings.TrimRight(sb.String(), "\n") + "\n"

		// Write to disk
		outPath := filepath.Join(configDir, "team-awareness.md")
		if err := os.WriteFile(outPath, []byte(content), 0644); err != nil {
			slog.Warn("failed to write team-awareness.md", "error", err)
		}
		return content
	}

	var sb strings.Builder
	sb.WriteString("# Teams\n\n")

	for _, team := range teams {
		fmt.Fprintf(&sb, "## %s\n\n", team.Name())
		sb.WriteString(summarizeTeam(ctx, client, team))
		sb.WriteString("\n\n")
	}

	content := strings.TrimRight(sb.String(), "\n") + "\n"

	// Write to disk so it's inspectable.
	outPath := filepath.Join(configDir, "team-awareness.md")
	if err := os.WriteFile(outPath, []byte(content), 0644); err != nil {
		slog.Warn("failed to write team-awareness.md", "error", err)
	}

	return content
}

// summarizeTeam asks the LLM for a one-sentence "Use this team when..."
// summary suitable for operator dispatch decisions.
func summarizeTeam(ctx context.Context, client provider.Provider, team service.TeamView) string {
	var prompt string
	if team.Coordinator != nil && strings.TrimSpace(team.Coordinator.SystemPrompt) != "" {
		prompt = fmt.Sprintf(
			`You are summarizing a software development team for an AI dispatcher.

Team name: %q
Coordinator instructions:
%s

Write exactly one sentence starting with "Use this team when" that describes what kinds of tasks should be routed to this team. Be specific and concise. Output only the sentence, nothing else.`,
			team.Name(),
			strings.TrimSpace(team.Coordinator.SystemPrompt),
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
			team.Name(),
			strings.Join(workerNames, ", "),
		)
	}

	msgs := []provider.Message{{Role: "user", Content: prompt}}
	resp, err := provider.ChatCompletion(ctx, client, msgs)
	if err != nil {
		slog.Warn("team awareness inference failed", "team", team.Name(), "error", err)
		return teamFallbackText(team.Name())
	}
	return strings.TrimSpace(resp)
}

// teamFallbackText returns a generic fallback description for a team
// when LLM summarization is unavailable or fails.
func teamFallbackText(teamName string) string {
	return fmt.Sprintf("Use this team when you need help from the %s team.", teamName)
}
