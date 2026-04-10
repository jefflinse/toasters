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
	{Name: "/teams", Description: "Browse and manage agent teams"},
	{Name: "/skills", Description: "Browse and manage skills"},
	{Name: "/agents", Description: "Browse and manage agents"},
	{Name: "/mcp", Description: "View MCP server status and tools"},
	{Name: "/models", Description: "Browse the models.dev provider catalog"},
	{Name: "/providers", Description: "Browse and configure providers"},
	{Name: "/operator", Description: "Select the operator's provider"},
	{Name: "/job", Description: "Create a new job"},
	{Name: "/jobs", Description: "Browse and manage jobs"},
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
