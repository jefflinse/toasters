// Update sub-handlers: extracted key handling and message processing for grid, modals, command popup, and agent output.
package tui

import (
	"fmt"
	"log"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/gateway"
	"github.com/jefflinse/toasters/internal/job"
	"github.com/jefflinse/toasters/internal/llm"
)

// updatePromptModal handles key events when the prompt modal is visible.
func (m *Model) updatePromptModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "p", "q":
		m.showPromptModal = false
		return m, nil
	case "up", "k":
		if m.promptModalScroll > 0 {
			m.promptModalScroll--
		}
		return m, nil
	case "down", "j":
		m.promptModalScroll++
		return m, nil
	case "ctrl+u":
		m.promptModalScroll -= 10
		if m.promptModalScroll < 0 {
			m.promptModalScroll = 0
		}
		return m, nil
	case "ctrl+d":
		m.promptModalScroll += 10
		return m, nil
	}
	return m, nil
}

// updateOutputModal handles key events when the output modal is visible.
func (m *Model) updateOutputModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "o", "q":
		m.showOutputModal = false
		m.outputModalSessionID = ""
	case "up", "k":
		if m.outputModalScroll > 0 {
			m.outputModalScroll--
		}
	case "down", "j":
		m.outputModalScroll++
	case "ctrl+u":
		m.outputModalScroll -= 10
		if m.outputModalScroll < 0 {
			m.outputModalScroll = 0
		}
	case "ctrl+d":
		m.outputModalScroll += 10
	}
	return m, nil
}

// updateGrid handles key events when the grid screen is visible.
func (m *Model) updateGrid(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	absSlot := m.gridPage*4 + m.gridFocusCell
	switch msg.String() {
	case "ctrl+g", "esc":
		m.showGrid = false
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
				m.showOutputModal = true
				m.outputModalContent = snap.Output
				m.outputModalScroll = len(strings.Split(snap.Output, "\n")) // auto-tail: start at bottom
				m.outputModalSessionID = ""
				return m, nil
			}
		}
		// Check for runtime session in this cell.
		if rs := m.runtimeSessionForGridCell(m.gridFocusCell); rs != nil {
			output := rs.output.String()
			if output != "" {
				m.showOutputModal = true
				m.outputModalContent = output
				m.outputModalScroll = len(strings.Split(output, "\n")) // auto-tail: start at bottom
				m.outputModalSessionID = rs.sessionID
			}
		}
		return m, nil
	case "p":
		if m.gateway != nil {
			slots := m.gateway.Slots()
			snap := slots[absSlot]
			if snap.Active && snap.Prompt != "" {
				m.showPromptModal = true
				m.promptModalContent = snap.Prompt
				m.promptModalScroll = 0
				return m, nil
			}
		}
		// Check for runtime session in this cell.
		if rs := m.runtimeSessionForGridCell(m.gridFocusCell); rs != nil {
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
				m.showPromptModal = true
				m.promptModalContent = content
				m.promptModalScroll = 0
			}
		}
		return m, nil
	case "[":
		if m.gridPage > 0 {
			m.gridPage--
		}
		m.gridFocusCell = 0
		return m, nil
	case "]":
		if m.gridPage < 3 {
			m.gridPage++
		}
		m.gridFocusCell = 0
		return m, nil
	case "left":
		if m.gridFocusCell%2 == 1 {
			m.gridFocusCell--
		}
		return m, nil
	case "right":
		if m.gridFocusCell%2 == 0 {
			m.gridFocusCell++
		}
		return m, nil
	case "up":
		if m.gridFocusCell >= 2 {
			m.gridFocusCell -= 2
		}
		return m, nil
	case "down":
		if m.gridFocusCell < 2 {
			m.gridFocusCell += 2
		}
		return m, nil
	}
	return m, nil
}

// updateKillModal handles key events when the kill confirmation modal is visible.
func (m *Model) updateKillModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up":
		if len(m.killModalSlots) > 0 {
			m.selectedKillIdx = (m.selectedKillIdx - 1 + len(m.killModalSlots)) % len(m.killModalSlots)
		}
		return m, nil
	case "down":
		if len(m.killModalSlots) > 0 {
			m.selectedKillIdx = (m.selectedKillIdx + 1) % len(m.killModalSlots)
		}
		return m, nil
	case "enter":
		if m.gateway != nil && len(m.killModalSlots) > 0 {
			_ = m.gateway.Kill(m.killModalSlots[m.selectedKillIdx])
		}
		m.showKillModal = false
		return m, nil
	case "esc":
		m.showKillModal = false
		return m, nil
	}
	return m, nil
}

// updateCmdPopup handles key events when the slash command popup is visible.
// Returns (true, cmd) if the key was consumed, (false, nil) if it should fall through.
func (m *Model) updateCmdPopup(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case "up":
		if len(m.filteredCmds) > 0 {
			m.selectedCmdIdx = (m.selectedCmdIdx - 1 + len(m.filteredCmds)) % len(m.filteredCmds)
		}
		return true, nil
	case "down":
		if len(m.filteredCmds) > 0 {
			m.selectedCmdIdx = (m.selectedCmdIdx + 1) % len(m.filteredCmds)
		}
		return true, nil
	case "tab", "enter":
		if len(m.filteredCmds) > 0 {
			m.input.SetValue(m.filteredCmds[m.selectedCmdIdx].Name + " ")
		}
		m.showCmdPopup = false
		return true, nil
	case "esc":
		m.showCmdPopup = false
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

				if m.streaming {
					// Buffer the notification — drain it after the current stream ends.
					m.pendingCompletions = append(m.pendingCompletions, pendingCompletion{
						notification: notification,
					})
				} else {
					// Inject immediately and start a new stream.
					m.appendEntry(ChatEntry{
						Message:   llm.Message{Role: "user", Content: notification},
						Timestamp: time.Now(),
					})
					// Tag this message as a collapsible completion entry and auto-select it.
					completionIdx := len(m.entries) - 1
					m.completionMsgIdx[completionIdx] = true
					m.selectedMsgIdx = completionIdx
					m.updateViewportContent()
					if !m.userScrolled {
						m.chatViewport.GotoBottom()
					}
					cmds = append(cmds, m.startStream(m.messagesFromEntries()))
				}

				// Check for BLOCKER.md and mark first task done — always, not buffered.
				for _, j := range m.jobs {
					if j.ID == snap.JobID {
						if b, err := job.ReadBlocker(j.Dir); err == nil && b != nil {
							if _, alreadyKnown := m.blockers[j.ID]; !alreadyKnown {
								cmds = append(cmds, m.addToast("⚠ Blocker on "+j.ID, toastWarning))
							}
							m.blockers[j.ID] = b
						}
						// Mark the first task done only on a clean completion.
						if !snap.Killed && snap.ExitSummary != "" {
							if tasks, err := job.ListTasks(j.Dir); err == nil && len(tasks) > 0 {
								if err := job.SetTaskStatus(tasks[0].Dir, job.StatusDone); err != nil {
									log.Printf("failed to mark task done: %v", err)
								}
							}
						} else {
							log.Printf("slot %d completed without clean exit (killed=%v, exitSummary=%q), skipping task auto-mark", i, snap.Killed, snap.ExitSummary)
						}
						break
					}
				}
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
