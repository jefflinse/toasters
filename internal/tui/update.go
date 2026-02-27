// Update sub-handlers: extracted key handling and message processing for grid, modals, command popup, and agent output.
package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// updatePromptModal handles key events when the prompt modal is visible.
func (m *Model) updatePromptModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "p", "q":
		m.promptModal.show = false
		return m, nil
	case "up", "k":
		if m.promptModal.scroll > 0 {
			m.promptModal.scroll--
		}
		return m, nil
	case "down", "j":
		m.promptModal.scroll++
		return m, nil
	case "ctrl+u":
		m.promptModal.scroll -= 10
		if m.promptModal.scroll < 0 {
			m.promptModal.scroll = 0
		}
		return m, nil
	case "ctrl+d":
		m.promptModal.scroll += 10
		return m, nil
	}
	return m, nil
}

// updateOutputModal handles key events when the output modal is visible.
func (m *Model) updateOutputModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "o", "q":
		m.outputModal.show = false
		m.outputModal.sessionID = ""
	case "up", "k":
		if m.outputModal.scroll > 0 {
			m.outputModal.scroll--
		}
	case "down", "j":
		m.outputModal.scroll++
	case "ctrl+u":
		m.outputModal.scroll -= 10
		if m.outputModal.scroll < 0 {
			m.outputModal.scroll = 0
		}
	case "ctrl+d":
		m.outputModal.scroll += 10
	}
	return m, nil
}

// updateGrid handles key events when the grid screen is visible.
func (m *Model) updateGrid(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	cols := m.grid.gridCols
	rows := m.grid.gridRows
	// Safety floor: mirrors the floor applied in renderGrid and runtimeSessionForGridCell.
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	cellsPerPage := cols * rows
	totalPages := (maxGridSlots + cellsPerPage - 1) / cellsPerPage

	switch msg.String() {
	case "ctrl+g", "esc":
		m.grid.showGrid = false
		return m, nil
	case "enter":
		// Check for runtime session in this cell.
		if rs := m.runtimeSessionForGridCell(m.grid.gridFocusCell); rs != nil {
			output := rs.output.String()
			if output != "" {
				m.outputModal.show = true
				m.outputModal.content = output
				m.outputModal.scroll = len(strings.Split(output, "\n")) // auto-tail: start at bottom
				m.outputModal.sessionID = rs.sessionID
			}
		}
		return m, nil
	case "p":
		// Check for runtime session in this cell.
		if rs := m.runtimeSessionForGridCell(m.grid.gridFocusCell); rs != nil {
			// Build a combined prompt view: system prompt + initial message.
			var promptContent strings.Builder
			if rs.systemPrompt != "" {
				promptContent.WriteString("=== System Prompt ===\n\n")
				promptContent.WriteString(rs.systemPrompt)
				promptContent.WriteString("\n\n")
			}
			if rs.initialMessage != "" {
				promptContent.WriteString("=== Initial Message ===\n\n")
				promptContent.WriteString(rs.initialMessage)
			}
			content := promptContent.String()
			if content != "" {
				m.promptModal.show = true
				m.promptModal.content = content
				m.promptModal.scroll = 0
			}
		}
		return m, nil
	case "[":
		if m.grid.gridPage > 0 {
			m.grid.gridPage--
			m.grid.gridFocusCell = 0
		}
		return m, nil
	case "]":
		if m.grid.gridPage < totalPages-1 {
			m.grid.gridPage++
			m.grid.gridFocusCell = 0
		}
		return m, nil
	case "left":
		if m.grid.gridFocusCell%cols > 0 {
			m.grid.gridFocusCell--
		}
		return m, nil
	case "right":
		if m.grid.gridFocusCell%cols < cols-1 {
			m.grid.gridFocusCell++
		}
		return m, nil
	case "up":
		if m.grid.gridFocusCell >= cols {
			m.grid.gridFocusCell -= cols
		}
		return m, nil
	case "down":
		if m.grid.gridFocusCell < cols*(rows-1) {
			m.grid.gridFocusCell += cols
		}
		return m, nil
	}
	return m, nil
}

// updateCmdPopup handles key events when the slash command popup is visible.
// Returns (true, cmd) if the key was consumed, (false, nil) if it should fall through.
func (m *Model) updateCmdPopup(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "up":
		if len(m.cmdPopup.filteredCmds) > 0 {
			m.cmdPopup.selectedIdx = (m.cmdPopup.selectedIdx - 1 + len(m.cmdPopup.filteredCmds)) % len(m.cmdPopup.filteredCmds)
		}
		return true, nil
	case "down":
		if len(m.cmdPopup.filteredCmds) > 0 {
			m.cmdPopup.selectedIdx = (m.cmdPopup.selectedIdx + 1) % len(m.cmdPopup.filteredCmds)
		}
		return true, nil
	case "tab", "enter":
		if len(m.cmdPopup.filteredCmds) > 0 {
			m.input.SetValue(m.cmdPopup.filteredCmds[m.cmdPopup.selectedIdx].Name + " ")
		}
		m.cmdPopup.show = false
		return true, nil
	case "esc":
		m.cmdPopup.show = false
		return true, nil
	}
	return false, nil
}
