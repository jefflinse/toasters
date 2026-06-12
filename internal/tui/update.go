// Update sub-handlers: extracted key handling and message processing for grid, modals, command popup, and worker output.
package tui

import (
	"context"
	"strings"
	"time"

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
// Any upward movement sets userScrolled so new session events don't yank the
// view back to the bottom; the view-path clamp (renderOutputModal) is what
// ultimately bounds the forward-moving keys.
func (m *Model) updateOutputModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "o", "q":
		m.outputModal.show = false
		m.outputModal.sessionID = ""
		m.outputModal.userScrolled = false
	case "up", "k":
		if m.outputModal.scroll > 0 {
			m.outputModal.scroll--
			m.outputModal.userScrolled = true
		}
	case "down", "j":
		m.outputModal.scroll++
	case "ctrl+u":
		m.outputModal.scroll -= 10
		if m.outputModal.scroll < 0 {
			m.outputModal.scroll = 0
		}
		m.outputModal.userScrolled = true
	case "ctrl+d":
		m.outputModal.scroll += 10
	case "g":
		m.outputModal.scroll = 0
		m.outputModal.userScrolled = true
	case "G", "end":
		// Jump to bottom; renderOutputModal will clamp to maxScroll.
		m.outputModal.scroll = 1 << 30
		m.outputModal.userScrolled = false
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
	totalPages := m.gridTotalPages(cellsPerPage)

	// While capturing a filter, keystrokes edit the query rather than drive
	// navigation. Esc clears and exits; Enter applies (keeps the filter, exits
	// capture); printable runes and backspace edit the query.
	if m.grid.filterActive {
		return m.updateGridFilter(msg)
	}

	switch msg.String() {
	case "/":
		m.grid.filterActive = true
		return m, nil
	case "ctrl+g", "esc":
		// Esc first dismisses a pending kill confirmation rather than closing
		// the grid, mirroring the jobs modal's confirm-cancel behavior.
		if m.grid.confirmKill {
			m.grid.confirmKill = false
			m.grid.confirmKillSessionID = ""
			return m, nil
		}
		m.grid.showGrid = false
		return m, nil
	case "x":
		// Arm the kill confirmation for the focused cell, but only for a live,
		// real worker session. Graph nodes are stateless pseudo-sessions
		// ("graph:<task>:<node>") with no runtime.Session to cancel.
		if rs := m.runtimeSessionForGridCell(m.grid.gridFocusCell); rs != nil &&
			rs.status == "active" && !strings.HasPrefix(rs.sessionID, "graph:") {
			m.grid.confirmKill = true
			m.grid.confirmKillSessionID = rs.sessionID
		}
		return m, nil
	case "enter":
		// Enter confirms a pending kill before its normal output-modal role.
		if m.grid.confirmKill {
			sid := m.grid.confirmKillSessionID
			m.grid.confirmKill = false
			m.grid.confirmKillSessionID = ""
			return m, m.killWorkerSession(sid)
		}
		// Check for runtime session in this cell.
		if rs := m.runtimeSessionForGridCell(m.grid.gridFocusCell); rs != nil {
			output := rs.outputText()
			if output != "" {
				m.outputModal.show = true
				m.outputModal.content = output
				m.outputModal.scroll = len(strings.Split(output, "\n")) // auto-tail: start at bottom
				m.outputModal.sessionID = rs.sessionID
				m.outputModal.userScrolled = false
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

// updateGridFilter handles keystrokes while the grid filter is being typed.
// Any query change resets the page and focus so navigation never lands off the
// end of a now-shorter list.
func (m *Model) updateGridFilter(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.grid.filterActive = false
		m.grid.filterQuery = ""
		m.grid.gridPage = 0
		m.grid.gridFocusCell = 0
		return m, nil
	case "enter":
		// Apply: keep the query, leave capture mode.
		m.grid.filterActive = false
		return m, nil
	case "backspace":
		if n := len(m.grid.filterQuery); n > 0 {
			m.grid.filterQuery = m.grid.filterQuery[:n-1]
			m.grid.gridPage = 0
			m.grid.gridFocusCell = 0
		}
		return m, nil
	}
	// Printable single runes extend the query.
	if msg.Text != "" {
		m.grid.filterQuery += msg.Text
		m.grid.gridPage = 0
		m.grid.gridFocusCell = 0
	}
	return m, nil
}

// killWorkerSession returns a command that cancels a running worker session
// through the service and reports the outcome as a toast. The network call
// runs inside the command (off the update loop) — in remote-client mode it's
// an HTTP round-trip that would otherwise freeze the UI for up to 2s.
// Cancellation is cooperative — the worker stops at its next tool-call
// boundary — so the toast says so. The resulting session.done(cancelled)
// event repaints the cell on its own.
func (m *Model) killWorkerSession(sessionID string) tea.Cmd {
	if sessionID == "" {
		return nil
	}
	svc := m.svc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := svc.Sessions().Cancel(ctx, sessionID); err != nil {
			return asyncToastMsg{message: "⚠ Kill failed: " + err.Error(), level: toastWarning}
		}
		return asyncToastMsg{message: "🔪 Worker killed (stops at next tool boundary)", level: toastInfo}
	}
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
