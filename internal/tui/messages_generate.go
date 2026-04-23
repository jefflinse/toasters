// Message types for LLM-generated skill definitions.
package tui

// skillGeneratedMsg is sent when the LLM finishes generating a skill definition.
type skillGeneratedMsg struct {
	content string
	err     error
}
