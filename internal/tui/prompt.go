// Prompt mode UI: handles key presses when the operator has asked the user a question.
package tui

import (
	"context"
	"log/slog"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// updatePromptMode handles key presses when the model is in prompt mode
// (i.e. the operator has asked the user a question with numbered options).
func (m *Model) updatePromptMode(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	allOptions := append(m.prompt.promptOptions, "Custom response...")
	switch msg.String() {
	case "up", "k":
		if m.prompt.promptSelected > 0 {
			m.prompt.promptSelected--
		}
	case "down", "j":
		if m.prompt.promptSelected < len(allOptions)-1 {
			m.prompt.promptSelected++
		}
	case "enter":
		if !m.prompt.promptCustom {
			if m.prompt.promptSelected == len(allOptions)-1 {
				// Selected "Custom response..."
				m.prompt.promptCustom = true
				m.input.Reset()
				cmds = append(cmds, m.input.Focus())
			} else {
				// Selected a pre-defined option — send the response.
				cmds = append(cmds, m.submitPromptResponse(allOptions[m.prompt.promptSelected]))
			}
		} else {
			// Custom text submitted — send the response.
			result := strings.TrimSpace(m.input.Value())
			if result == "" {
				result = "User provided no response."
			}
			cmds = append(cmds, m.submitPromptResponse(result))
		}
	case "esc":
		if m.prompt.promptCustom {
			// Go back to option selection.
			m.prompt.promptCustom = false
			m.input.Reset()
		} else {
			// Cancel entirely.
			cmds = append(cmds, m.submitPromptResponse("User cancelled."))
		}
	default:
		if m.prompt.promptCustom {
			// Delegate to textarea.
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			cmds = append(cmds, inputCmd)
		}
	}
	return m, tea.Batch(cmds...)
}

// submitPromptResponse sends the user's answer and clears prompt mode.
// If there's a requestID (from ask_user), it routes through RespondToPrompt.
// Otherwise, it sends as a regular chat message.
func (m *Model) submitPromptResponse(text string) tea.Cmd {
	requestID := m.prompt.requestID

	// Clear prompt state.
	m.prompt.promptMode = false
	m.prompt.promptCustom = false
	m.prompt.promptQuestion = ""
	m.prompt.promptOptions = nil
	m.prompt.promptSelected = 0
	m.prompt.requestID = ""
	m.input.Reset()

	if requestID != "" && m.svc != nil {
		// Route through RespondToPrompt for ask_user prompts.
		return func() tea.Msg {
			if err := m.svc.Operator().RespondToPrompt(context.Background(), requestID, text); err != nil {
				slog.Warn("failed to respond to prompt", "request_id", requestID, "error", err)
			}
			return nil
		}
	}

	// Fallback: send as a regular chat message.
	m.input.SetValue(text)
	return m.sendMessage()
}
