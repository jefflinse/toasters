// Message types for LLM-generated skill, agent, and team definitions.
package tui

// skillGeneratedMsg is sent when the LLM finishes generating a skill definition.
type skillGeneratedMsg struct {
	content string
	err     error
}

// teamGeneratedMsg is sent when the LLM finishes generating a team definition.
type teamGeneratedMsg struct {
	content    string   // the team.md content
	agentNames []string // names of existing agents to assign
	err        error
}
