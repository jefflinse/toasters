package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
	"github.com/jefflinse/toasters/internal/service"
)

// handleKeyPress dispatches a key-press message: modal/mode interception,
// global keybindings, and (when chat is focused) textarea input. Extracted
// verbatim from Update's tea.KeyPressMsg case.
func (m *Model) handleKeyPress(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	// Operator modal key handling.
	if m.operatorModal.show {
		return m.updateOperatorModal(msg)
	}

	// Catalog modal key handling — intercept all keys when modal is open.
	if m.catalogModal.show {
		return m.updateCatalogModal(msg)
	}

	// MCP modal key handling — intercept all keys when modal is open.
	if m.mcpModal.show {
		return m.updateMCPModal(msg)
	}

	// Skills modal key handling — intercept all keys when modal is open.
	if m.skillsModal.show {
		return m.updateSkillsModal(msg)
	}

	// Jobs modal key handling — intercept all keys when modal is open.
	if m.jobsModal.show {
		return m.updateJobsModal(msg)
	}

	// Blockers selection modal — intercept all keys when open.
	if m.blockersModal.show {
		return m.updateBlockersModal(msg)
	}

	// Settings modal key handling — intercept all keys when modal is open.
	if m.settingsModal.show {
		return m.updateSettingsModal(msg)
	}

	// Presets modal key handling — intercept all keys when modal is open.
	if m.presetsModal.show {
		return m.updatePresetsModal(msg)
	}

	// Graph map modal key handling — intercept all keys when modal is open.
	if m.graphMapModal.show {
		return m.updateGraphMapModal(msg)
	}

	// Prompt mode key handling — highest priority.
	if m.prompt.promptMode {
		return m.updatePromptMode(msg)
	}

	// When the prompt modal is visible, intercept all keys before any other handling.
	if m.promptModal.show {
		return m.updatePromptModal(msg)
	}

	// When the output modal is visible, intercept all keys before grid navigation.
	if m.outputModal.show {
		return m.updateOutputModal(msg)
	}

	// When the grid screen is visible, handle navigation and dismiss it.
	if m.grid.showGrid {
		return m.updateGrid(msg)
	}

	// When the log view is visible, handle navigation and dismiss it.
	if m.logView.show {
		return m.updateLogView(msg)
	}

	// When the slash command popup is visible, intercept navigation keys
	// before any other handling so they don't fall through to the textarea.
	if m.cmdPopup.show {
		if handled, cmd := m.updateCmdPopup(msg); handled {
			return m, cmd
		}
	}

	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit

	case "tab":
		// Cycle focus: chat → jobs → blockers → workers → chat.
		// Skip hidden panels.
		// (Tab inside the slash command popup is handled above and returns early.)
		next := m.focused
		for {
			switch next {
			case focusChat:
				next = focusJobs
			case focusJobs:
				next = focusBlockers
			case focusBlockers:
				next = focusWorkers
			case focusWorkers:
				next = focusChat
			default:
				next = focusChat
			}
			// Skip left-panel targets when left panel is hidden or empty.
			if !m.shouldShowLeftPanel() && (next == focusJobs || next == focusBlockers || next == focusWorkers) {
				continue
			}
			break
		}
		focusCmd := m.setFocus(next)
		if next == focusChat {
			return m, tea.Batch(m.input.Focus(), focusCmd)
		}
		m.input.Blur()
		return m, focusCmd

	case "shift+tab":
		// Reverse cycle: chat → workers → blockers → jobs → chat.
		next := m.focused
		for {
			switch next {
			case focusChat:
				next = focusWorkers
			case focusWorkers:
				next = focusBlockers
			case focusBlockers:
				next = focusJobs
			case focusJobs:
				next = focusChat
			default:
				next = focusChat
			}
			// Skip left-panel targets when left panel is hidden or empty.
			if !m.shouldShowLeftPanel() && (next == focusJobs || next == focusBlockers || next == focusWorkers) {
				continue
			}
			break
		}
		focusCmd := m.setFocus(next)
		if next == focusChat {
			return m, tea.Batch(m.input.Focus(), focusCmd)
		}
		m.input.Blur()
		return m, focusCmd

	case "pgup":
		// Scroll chat viewport up by one page.
		if m.focused == focusChat && !m.stream.streaming {
			m.chatViewport.PageUp()
			m.scroll.userScrolled = true
			return m, m.showScrollbar()
		}

	case "pgdown":
		// Scroll chat viewport down by one page.
		if m.focused == focusChat && !m.stream.streaming {
			m.chatViewport.PageDown()
			if m.chatViewport.AtBottom() {
				m.scroll.userScrolled = false
				m.scroll.hasNewMessages = false
			} else {
				m.scroll.userScrolled = true
			}
			return m, m.showScrollbar()
		}

	case "home":
		// Scroll chat viewport to top.
		if m.focused == focusChat && !m.stream.streaming {
			m.chatViewport.GotoTop()
			m.scroll.userScrolled = true
			return m, m.showScrollbar()
		}

	case "end":
		// Scroll chat viewport to bottom.
		if m.focused == focusChat && !m.stream.streaming {
			m.chatViewport.GotoBottom()
			m.scroll.userScrolled = false
			m.scroll.hasNewMessages = false
			return m, m.showScrollbar()
		}

	case "ctrl+u":
		// Scroll chat viewport up half page.
		if m.focused == focusChat && !m.stream.streaming {
			m.chatViewport.HalfPageUp()
			m.scroll.userScrolled = true
			return m, m.showScrollbar()
		}

	case "ctrl+d":
		// Scroll chat viewport down half page.
		if m.focused == focusChat && !m.stream.streaming {
			m.chatViewport.HalfPageDown()
			if m.chatViewport.AtBottom() {
				m.scroll.userScrolled = false
				m.scroll.hasNewMessages = false
			} else {
				m.scroll.userScrolled = true
			}
			return m, m.showScrollbar()
		}

	case "up":
		// Navigate jobs when that panel is focused.
		if m.focused == focusJobs {
			dj := m.displayJobs()
			if len(dj) > 0 && m.selectedJob > 0 {
				m.selectedJob--
			}
			return m, nil
		}
		// Navigate blockers when that panel is focused.
		if m.focused == focusBlockers {
			if m.blockersSel > 0 {
				m.blockersSel--
			}
			return m, nil
		}
		// Navigate worker slots when workers pane is focused.
		if m.focused == focusWorkers {
			if m.selectedWorkerSlot > 0 {
				m.selectedWorkerSlot--
			}
			return m, nil
		}
		// Chat focus + at least one JobResult → walk the result-block
		// selection backward. Blurs the input on first selection so
		// the action keys (w/d/Enter) aren't swallowed by the textarea.
		if m.focused == focusChat && !m.stream.streaming {
			if m.stepBlockSelection(-1) {
				if m.chat.selectedMsgIdx >= 0 {
					m.input.Blur()
				}
				m.updateViewportContent()
				return m, nil
			}
		}
	case "down":
		// Navigate jobs when that panel is focused.
		if m.focused == focusJobs {
			dj := m.displayJobs()
			if len(dj) > 0 && m.selectedJob < len(dj)-1 {
				m.selectedJob++
			}
			return m, nil
		}
		// Navigate blockers when that panel is focused.
		if m.focused == focusBlockers {
			if m.blockersSel < len(m.blockers)-1 {
				m.blockersSel++
			}
			return m, nil
		}
		// Navigate worker slots when workers pane is focused.
		if m.focused == focusWorkers {
			if m.selectedWorkerSlot < maxGridSlots-1 {
				m.selectedWorkerSlot++
			}
			return m, nil
		}
		// Mirror of the Up handler above for symmetry: walk forward
		// through result-block selection, returning to free chat
		// after the newest result.
		if m.focused == focusChat && !m.stream.streaming {
			if m.chat.selectedMsgIdx >= 0 && m.stepBlockSelection(+1) {
				if m.chat.selectedMsgIdx < 0 {
					cmds = append(cmds, m.input.Focus())
				}
				m.updateViewportContent()
				return m, tea.Batch(cmds...)
			}
		}
	case "w":
		// Open the workspace directory of the selected JobResult.
		// The action keys are intentionally only live while a result
		// is selected — typing 'w' in chat normally goes to the input.
		if m.focused == focusChat {
			if res := m.selectedJobResult(); res != nil {
				return m, m.openWorkspaceDir(res.Workspace)
			}
		}
	case "x":
		// Dismiss the selected blocker when the Blockers panel is focused:
		// answer the waiting caller with a cancellation so it stops blocking.
		if m.focused == focusBlockers && m.blockersSel < len(m.blockers) {
			return m, m.dismissBlocker(m.blockers[m.blockersSel].RequestID)
		}
	case "ctrl+x":
		// Toggle expand/collapse on the selected completion message when chat is focused.
		if m.focused == focusChat && !m.stream.streaming && m.chat.selectedMsgIdx >= 0 && m.chat.completionMsgIdx[m.chat.selectedMsgIdx] {
			m.chat.expandedMsgs[m.chat.selectedMsgIdx] = !m.chat.expandedMsgs[m.chat.selectedMsgIdx]
			m.updateViewportContent()
			return m, nil
		}
		// Toggle expand/collapse on tool-call indicator or tool result messages.
		if m.focused == focusChat && !m.stream.streaming && m.chat.selectedMsgIdx >= 0 && m.chat.selectedMsgIdx < len(m.chat.entries) {
			msg := m.chat.entries[m.chat.selectedMsgIdx].Message
			isToolIndicator := msg.Role == "assistant" && m.isToolCallIndicatorIdx(m.chat.selectedMsgIdx)
			isToolResult := msg.Role == "tool"
			if isToolIndicator || isToolResult {
				m.chat.collapsedTools[m.chat.selectedMsgIdx] = !m.chat.collapsedTools[m.chat.selectedMsgIdx]
				m.updateViewportContent()
				return m, nil
			}
		}

	case "ctrl+t":
		// Toggle expand/collapse of the reasoning trace for the most recent assistant message
		// that has a non-empty reasoning block.
		if m.focused == focusChat && !m.stream.streaming {
			// Find the last entry index with reasoning.
			lastReasoningIdx := -1
			for i, entry := range m.chat.entries {
				if entry.Message.Role == "assistant" && entry.Reasoning != "" {
					lastReasoningIdx = i
				}
			}
			if lastReasoningIdx >= 0 {
				m.chat.expandedReasoning[lastReasoningIdx] = !m.chat.expandedReasoning[lastReasoningIdx]
				m.updateViewportContent()
				return m, nil
			}
		}

	case "ctrl+v":
		// Paste clipboard text into the chat input when chat is focused.
		if m.focused == focusChat && !m.stream.streaming && !m.prompt.promptMode {
			if text, err := clipboard.ReadAll(); err == nil && text != "" {
				m.input.InsertString(text)
			}
		}

	case "ctrl+y":
		// Copy the last assistant message to the clipboard when chat is focused.
		if m.focused == focusChat && !m.stream.streaming && !m.prompt.promptMode {
			for i := len(m.chat.entries) - 1; i >= 0; i-- {
				if m.chat.entries[i].Message.Role == "assistant" {
					_ = clipboard.WriteAll(m.chat.entries[i].Message.Content)
					m.flashText = "  ✓ copied to clipboard"
					cmds = append(cmds, clearFlash())
					cmds = append(cmds, m.addToast("🍞 Copied to clipboard!", toastInfo))
					break
				}
			}
		}

	case "ctrl+g":
		m.grid.showGrid = !m.grid.showGrid
		return m, nil

	case `ctrl+\`:
		if m.logView.show {
			m.closeLogView()
			return m, nil
		}
		cmd := m.openLogView()
		return m, cmd

	case "ctrl+j":
		// Toggle left panel (Jobs + Workers) visibility. Flip from the
		// currently *effective* state, not the override field — when no
		// override is set, the user's mental model is "the panel I see
		// right now", which may be the auto-hidden empty state.
		next := !m.shouldShowLeftPanel()
		m.leftPanelOverride = &next
		if !next && (m.focused == focusJobs || m.focused == focusWorkers) {
			cmds = append(cmds, m.setFocus(focusChat))
			cmds = append(cmds, m.input.Focus())
		}
		m.resizeComponents()
		return m, tea.Batch(cmds...)

	case "ctrl+o":
		// Toggle right sidebar (Operator stats) visibility. Same
		// effective-state semantics as ctrl+j above.
		next := !m.shouldShowSidebar()
		m.sidebarOverride = &next
		m.resizeComponents()
		return m, tea.Batch(cmds...)

	case "alt+[":
		// Decrease left panel width.
		if m.shouldShowLeftPanel() {
			if m.leftPanelWidthOverride == 0 {
				m.leftPanelWidthOverride = leftPanelWidth(m.width)
			}
			m.leftPanelWidthOverride -= 2
			if m.leftPanelWidthOverride < minLeftPanelWidth {
				m.leftPanelWidthOverride = minLeftPanelWidth
			}
			m.resizeComponents()
		}
		return m, nil

	case "alt+]":
		// Increase left panel width.
		if m.shouldShowLeftPanel() {
			if m.leftPanelWidthOverride == 0 {
				m.leftPanelWidthOverride = leftPanelWidth(m.width)
			}
			m.leftPanelWidthOverride += 2
			maxW := m.width / 2
			if m.leftPanelWidthOverride > maxW {
				m.leftPanelWidthOverride = maxW
			}
			m.resizeComponents()
		}
		return m, nil

	case "esc":
		// Drop block selection back to free chat. Sits ahead of the
		// grid + stream guards because the user's mental model is
		// "esc = back out of the most-immediate context", and a
		// chat-selected block is more recent than a streaming turn.
		if m.focused == focusChat && (m.selectedJobResult() != nil || m.selectedWorkerStream() != nil) {
			m.chat.selectedMsgIdx = -1
			cmds = append(cmds, m.input.Focus())
			m.updateViewportContent()
			return m, tea.Batch(cmds...)
		}
		// Exit grid screen.
		if m.grid.showGrid {
			m.grid.showGrid = false
			return m, nil
		}
		// Cancel an in-flight operator stream.
		if m.stream.streaming {
			m.stream.streaming = false
			if m.stream.currentResponse != "" {
				m.appendEntry(service.ChatEntry{
					Message: service.ChatMessage{
						Role:    service.MessageRoleAssistant,
						Content: m.stream.currentResponse,
					},
					Timestamp:  time.Now(),
					Reasoning:  m.stream.currentReasoning,
					ClaudeMeta: m.stream.operatorByline,
				})
				m.stream.operatorByline = ""
				m.stream.currentResponse = ""
				m.stream.currentReasoning = ""
			}
			m.stats.CompletionTokensLive = 0
			m.stats.ReasoningTokensLive = 0
			m.updateViewportContent()
			return m, m.input.Focus()
		}

	case "enter":
		// Block deep link: Enter on a chat-selected JobResult or
		// WorkerStream jumps into the Jobs modal at that job. Sits
		// before the jobs-pane handler so chat selection wins when
		// the user is in block-selection mode.
		if m.focused == focusChat && !m.stream.streaming {
			if res := m.selectedJobResult(); res != nil {
				return m, m.openJobsModalForJob(res.JobID)
			}
			if ws := m.selectedWorkerStream(); ws != nil {
				return m, m.openJobsModalForWorkerStream(ws)
			}
		}
		// Open jobs modal pre-selected on current job.
		if m.focused == focusJobs {
			dj := m.displayJobs()
			if len(dj) == 0 || m.selectedJob >= len(dj) {
				return m, nil
			}
			m.jobsModal = jobsModalState{
				show:   true,
				jobIdx: m.selectedJob,
			}
			m.loadJobsForModal()
			m.loadJobDetail()
			var tickCmd tea.Cmd
			if !m.spinnerRunning {
				m.spinnerRunning = true
				tickCmd = spinnerTick()
			}
			return m, tickCmd
		}
		// Open the blocker selection modal when the blockers pane is focused.
		if m.focused == focusBlockers {
			if len(m.blockers) == 0 {
				return m, nil
			}
			sel := m.blockersSel
			if sel >= len(m.blockers) {
				sel = 0
			}
			m.blockersModal = blockersModalState{show: true, sel: sel}
			return m, nil
		}
		// Open grid view when workers pane is focused.
		if m.focused == focusWorkers {
			m.grid.showGrid = true
			return m, nil
		}
		// focusOperator, focusChat: handled above or fall through to send.
		// Shift+enter inserts a newline (handled by textarea). Local
		// slash commands execute immediately even during an operator
		// turn; anything else goes to the queue while streaming.
		if strings.TrimSpace(m.input.Value()) != "" {
			text := strings.TrimSpace(m.input.Value())
			switch text {
			case "/exit", "/quit":
				return m, tea.Quit
			case "/help":
				m.input.Reset()
				m.cmdPopup.show = false
				m.appendHelpMessage()
				return m, nil
			case "/new":
				m.input.Reset()
				m.cmdPopup.show = false
				m.newSession()
				return m, nil
			case "/skills":
				m.input.Reset()
				m.cmdPopup.show = false
				m.skillsModal = skillsModalState{show: true}
				m.reloadSkillsForModal()
				return m, nil
			case "/jobs":
				m.input.Reset()
				m.cmdPopup.show = false
				m.jobsModal = jobsModalState{
					show: true,
				}
				m.loadJobsForModal()
				if len(m.jobsModal.jobs) > 0 {
					m.loadJobDetail()
				}
				var tickCmd tea.Cmd
				if !m.spinnerRunning {
					m.spinnerRunning = true
					tickCmd = spinnerTick()
				}
				return m, tickCmd
			case "/graphmap":
				m.input.Reset()
				m.cmdPopup.show = false
				m.graphMapModal = graphMapModalState{show: true}
				return m, nil
			case "/mcp":
				m.input.Reset()
				m.cmdPopup.show = false
				m.mcpModal = mcpModalState{show: true}
				// servers field will be populated when mcpModal is updated to use service types
				return m, nil
			case "/models", "/providers":
				m.input.Reset()
				m.cmdPopup.show = false
				m.catalogModal = catalogModalState{show: true, loading: true}
				return m, m.fetchCatalog()
			case "/operator":
				m.input.Reset()
				m.cmdPopup.show = false
				m.operatorModal = operatorModalState{show: true, loading: true}
				return m, m.fetchConfiguredProviders()
			case "/settings":
				m.input.Reset()
				m.cmdPopup.show = false
				m.settingsModal = settingsModalState{show: true, loading: true}
				return m, m.fetchSettings()
			case "/presets":
				m.input.Reset()
				m.cmdPopup.show = false
				m.presetsModal = presetsModalState{show: true}
				return m, nil
			}

			// Remaining cases send a message to the operator. If a turn
			// is already in progress, queue it for auto-send on done.
			if m.stream.streaming {
				m.chat.queuedMessages = append(m.chat.queuedMessages, text)
				m.input.Reset()
				m.cmdPopup.show = false
				return m, nil
			}

			// /job <prompt> — create a new job via the operator LLM.
			if strings.HasPrefix(text, "/job ") {
				prompt := strings.TrimSpace(strings.TrimPrefix(text, "/job "))
				if prompt == "" {
					m.input.Reset()
					m.cmdPopup.show = false
					return m, nil
				}
				m.cmdPopup.show = false
				m.input.SetValue("[JOB REQUEST] " + prompt)
				return m, m.sendMessage()
			}
			// Not a recognized slash command — send to LLM.
			if m.operatorDisabled {
				m.cmdPopup.show = false
				return m, m.addToast("No operator — use /providers", toastWarning)
			}
			m.cmdPopup.show = false
			return m, m.sendMessage()
		}
	}

	// Delegate to textarea only when the chat pane is focused. Typing
	// is allowed even while the operator is streaming (the message
	// will be queued on Enter), but when the user has tabbed over to
	// a side pane like Jobs, keystrokes should not leak into the
	// input box.
	if m.focused == focusChat {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)

		// Update slash command popup state based on current input value.
		inputVal := m.input.Value()
		if strings.HasPrefix(inputVal, "/") {
			m.cmdPopup.filteredCmds = filterCommands(inputVal)
			m.cmdPopup.show = len(m.cmdPopup.filteredCmds) > 0
			if m.cmdPopup.show && m.cmdPopup.selectedIdx >= len(m.cmdPopup.filteredCmds) {
				m.cmdPopup.selectedIdx = 0
			}
		} else {
			m.cmdPopup.show = false
			m.cmdPopup.filteredCmds = nil
			m.cmdPopup.selectedIdx = 0
		}
	}
	return m, tea.Batch(cmds...)
}
