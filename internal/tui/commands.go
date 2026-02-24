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
	{Name: "/claude", Description: "Ask Claude (via claude CLI)"},
	{Name: "/anthropic", Description: "Ask Claude (via Anthropic API)"},
	{Name: "/new", Description: "Start a new session"},
	{Name: "/kill", Description: "Kill a running background agent"},
	{Name: "/teams", Description: "Browse and manage agent teams"},
	{Name: "/job", Description: "Create a new job"},
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
