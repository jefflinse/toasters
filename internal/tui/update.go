// Update sub-handlers: extracted key handling and message processing for grid, modals, command popup, and agent output.
package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/gateway"
	"github.com/jefflinse/toasters/internal/provider"
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
	absSlot := m.grid.gridPage*4 + m.grid.gridFocusCell
	switch msg.String() {
	case "ctrl+g", "esc":
		m.grid.showGrid = false
		return m, nil
	case "k", "ctrl+k":
		if m.gateway != nil {
			_ = m.gateway.Kill(absSlot)
		}
		return m, nil
	case "enter":
		if m.gateway != nil {
			slots := m.gateway.Slots()
			snap := slots[absSlot]
			if snap.Active && snap.Output != "" {
				m.outputModal.show = true
				m.outputModal.content = snap.Output
				m.outputModal.scroll = len(strings.Split(snap.Output, "\n")) // auto-tail: start at bottom
				m.outputModal.sessionID = ""
				return m, nil
			}
		}
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
		if m.gateway != nil {
			slots := m.gateway.Slots()
			snap := slots[absSlot]
			if snap.Active && snap.Prompt != "" {
				m.promptModal.show = true
				m.promptModal.content = snap.Prompt
				m.promptModal.scroll = 0
				return m, nil
			}
		}
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
		}
		m.grid.gridFocusCell = 0
		return m, nil
	case "]":
		if m.grid.gridPage < 3 {
			m.grid.gridPage++
		}
		m.grid.gridFocusCell = 0
		return m, nil
	case "left":
		if m.grid.gridFocusCell%2 == 1 {
			m.grid.gridFocusCell--
		}
		return m, nil
	case "right":
		if m.grid.gridFocusCell%2 == 0 {
			m.grid.gridFocusCell++
		}
		return m, nil
	case "up":
		if m.grid.gridFocusCell >= 2 {
			m.grid.gridFocusCell -= 2
		}
		return m, nil
	case "down":
		if m.grid.gridFocusCell < 2 {
			m.grid.gridFocusCell += 2
		}
		return m, nil
	}
	return m, nil
}

// updateKillModal handles key events when the kill confirmation modal is visible.
func (m *Model) updateKillModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up":
		if len(m.killModal.slots) > 0 {
			m.killModal.selectedIdx = (m.killModal.selectedIdx - 1 + len(m.killModal.slots)) % len(m.killModal.slots)
		}
		return m, nil
	case "down":
		if len(m.killModal.slots) > 0 {
			m.killModal.selectedIdx = (m.killModal.selectedIdx + 1) % len(m.killModal.slots)
		}
		return m, nil
	case "enter":
		if m.gateway != nil && len(m.killModal.slots) > 0 {
			_ = m.gateway.Kill(m.killModal.slots[m.killModal.selectedIdx])
		}
		m.killModal.show = false
		return m, nil
	case "esc":
		m.killModal.show = false
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

// handleAgentOutput processes AgentOutputMsg — detects slot transitions and
// notifies the operator LLM when agents complete.
func (m *Model) handleAgentOutput(msg AgentOutputMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// Re-arm the poller.
	if m.agentNotifyCh != nil {
		cmds = append(cmds, waitForAgentUpdate(m.agentNotifyCh))
	}

	if m.gateway != nil {
		slots := m.gateway.Slots()

		// Detect Running→Done transitions and notify the operator LLM.
		for i, snap := range slots {
			wasRunning := m.prevSlotActive[i] && m.prevSlotStatus[i] == gateway.SlotRunning
			isDone := snap.Active && snap.Status == gateway.SlotDone
			if wasRunning && isDone {
				// Build a concise completion notification for the operator.
				outputTail := snap.Output
				const maxTail = 2000
				if len(outputTail) > maxTail {
					outputTail = "…" + outputTail[len(outputTail)-maxTail:]
				}
				var notification string
				if snap.ExitSummary != "" {
					notification = fmt.Sprintf(
						"Team '%s' in slot %d has completed (job: %s).\n\nExit Summary:\n%s\n\nOutput (last 2000 chars):\n%s",
						snap.AgentName, i, snap.JobID, snap.ExitSummary, outputTail,
					)
				} else {
					notification = fmt.Sprintf(
						"Team '%s' in slot %d has completed (job: %s).\n\nOutput (last 2000 chars):\n%s",
						snap.AgentName, i, snap.JobID, outputTail,
					)
				}

				// Toast: agent completed.
				cmds = append(cmds, m.addToast("🍞 "+snap.AgentName+" is done. Extra crispy.", toastSuccess))

				if m.stream.streaming {
					// Buffer the notification — drain it after the current stream ends.
					m.chat.pendingCompletions = append(m.chat.pendingCompletions, pendingCompletion{
						notification: notification,
					})
				} else {
					// Inject immediately and start a new stream.
					m.appendEntry(ChatEntry{
						Message:   provider.Message{Role: "user", Content: notification},
						Timestamp: time.Now(),
					})
					// Tag this message as a collapsible completion entry and auto-select it.
					completionIdx := len(m.chat.entries) - 1
					m.chat.completionMsgIdx[completionIdx] = true
					m.chat.selectedMsgIdx = completionIdx
					m.updateViewportContent()
					if !m.scroll.userScrolled {
						m.chatViewport.GotoBottom()
					}
					cmds = append(cmds, m.startStream(m.messagesFromEntries()))
				}

				// Blocker detection and task status management are handled
				// via SQLite tools in the agent runtime.
			}
			// Update tracked state.
			m.prevSlotActive[i] = snap.Active
			m.prevSlotStatus[i] = snap.Status
		}

		// If attached to a slot, update the agent viewport.
		if m.attachedSlot >= 0 {
			snap := slots[m.attachedSlot]
			if snap.Active {
				m.agentViewport.SetContent(m.renderMarkdown(snap.Output))
				m.agentViewport.GotoBottom()
			}
		}
	}
	return m, tea.Batch(cmds...)
}
