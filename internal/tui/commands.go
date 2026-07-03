package tui

import "strings"

// SlashCommand defines a slash command available in the input box.
type SlashCommand struct {
	Name        string // e.g. "/help"
	Description string // short description shown in popup
}

// allCommands is the full list of available slash commands.
var allCommands = []SlashCommand{
	{Name: "/exit", Description: "Exit the application"},
	{Name: "/quit", Description: "Exit the application"},
	{Name: "/help", Description: "Show help information"},
	{Name: "/new", Description: "Start a new session"},
	{Name: "/skills", Description: "Browse and manage skills"},
	{Name: "/workers", Description: "Browse and manage workers"},
	{Name: "/mcp", Description: "View MCP server status and tools"},
	{Name: "/models", Description: "Browse the models.dev provider catalog"},
	{Name: "/providers", Description: "Browse and configure providers"},
	{Name: "/operator", Description: "Select the operator's provider"},
	{Name: "/job", Description: "Create a new job"},
	{Name: "/jobs", Description: "Browse and manage jobs"},
	{Name: "/blockers", Description: "Answer pending blockers"},
	{Name: "/fleet", Description: "Open the fleet nodes screen"},
	{Name: "/graphmap", Description: "View the live graph map for the active task"},
	{Name: "/presets", Description: "Pick a preset prompt to send as a job"},
	{Name: "/settings", Description: "View and edit runtime settings"},
	{Name: "/metrics", Description: "View node execution and session statistics"},
}

// filterCommands returns commands whose Name has the given prefix.
func filterCommands(prefix string) []SlashCommand {
	if prefix == "/" {
		return allCommands
	}
	var out []SlashCommand
	for _, c := range allCommands {
		if strings.HasPrefix(c.Name, prefix) {
			out = append(out, c)
		}
	}
	return out
}
