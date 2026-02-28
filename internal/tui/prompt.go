// Prompt mode UI: handles key presses when the operator has asked the user a question.
package tui

import (
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
				// Selected a pre-defined option — send the response to the operator.
				result := allOptions[m.prompt.promptSelected]
				m.prompt.promptMode = false
				m.prompt.promptCustom = false
				m.prompt.promptQuestion = ""
				m.prompt.promptOptions = nil
				m.prompt.promptSelected = 0
				m.input.Reset()
				m.input.SetValue(result)
				cmds = append(cmds, m.sendMessage())
			}
		} else {
			// Custom text submitted — send the response to the operator.
			result := strings.TrimSpace(m.input.Value())
			if result == "" {
				result = "User provided no response."
			}
			m.prompt.promptMode = false
			m.prompt.promptCustom = false
			m.prompt.promptQuestion = ""
			m.prompt.promptOptions = nil
			m.prompt.promptSelected = 0
			m.input.Reset()
			m.input.SetValue(result)
			cmds = append(cmds, m.sendMessage())
		}
	case "esc":
		if m.prompt.promptCustom {
			// Go back to option selection.
			m.prompt.promptCustom = false
			m.input.Reset()
		} else {
			// Cancel entirely — send cancellation to the operator.
			m.prompt.promptMode = false
			m.prompt.promptCustom = false
			m.prompt.promptQuestion = ""
			m.prompt.promptOptions = nil
			m.prompt.promptSelected = 0
			m.input.Reset()
			m.input.SetValue("User cancelled.")
			cmds = append(cmds, m.sendMessage())
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
