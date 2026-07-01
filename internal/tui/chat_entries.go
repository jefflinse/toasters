package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jefflinse/toasters/internal/service"
)

// appendHelpMessage adds a help message to the chat as an assistant turn.
func (m *Model) appendHelpMessage() {
	helpText := "**Toasters — Help**\n\n" +
		"**Slash Commands**\n" +
		"- `/help` — Show this help message\n" +
		"- `/new` — Start a new session (clears chat history)\n" +
		"- `/exit`, `/quit` — Exit the application\n\n" +
		"**Keyboard Shortcuts**\n" +
		"- `Enter` — Send message\n" +
		"- `Shift+Enter` — New line in message\n" +
		"- `Esc` — Cancel current response\n" +
		"- `Ctrl+C` — Quit\n\n" +
		"**Slash Command Autocomplete**\n" +
		"Type `/` to open the command picker. Use ↑↓ to navigate, Tab or Enter to select, Esc to dismiss."

	m.appendEntry(service.ChatEntry{
		Message:   service.ChatMessage{Role: service.MessageRoleAssistant, Content: helpText},
		Timestamp: time.Now(),
	})
	m.stats.MessageCount++
	m.updateViewportContent()
	if !m.scroll.userScrolled {
		m.chatViewport.GotoBottom()
	}
}

// newSession resets the conversation and all session statistics.
// initMessages resets m.chat.entries and seeds it with the system prompt as entries[0]
// (if a system prompt is set). Call this at startup and on /new.
func (m *Model) initMessages() {
	m.chat.entries = nil
	if m.systemPrompt != "" {
		m.appendEntry(service.ChatEntry{
			Message:   service.ChatMessage{Role: service.MessageRoleSystem, Content: m.systemPrompt},
			Timestamp: time.Now(),
		})
		m.stats.SystemPromptTokens = estimateTokens(m.systemPrompt)
	} else {
		m.stats.SystemPromptTokens = 0
	}
	m.chat.completionMsgIdx = make(map[int]bool)
	m.chat.expandedMsgs = make(map[int]bool)
	m.chat.selectedMsgIdx = -1
	m.chat.expandedReasoning = make(map[int]bool)
	m.chat.collapsedTools = make(map[int]bool)
}

// appendEntry adds a new chat entry to the conversation history.
func (m *Model) appendEntry(e service.ChatEntry) {
	m.chat.entries = append(m.chat.entries, e)
}

// selectableEntryIndices returns the indices of chat entries that
// participate in Up/Down selection — currently job results and worker
// stream blocks. Both are deep-link targets into the Jobs modal, so
// they share one selection cursor and the same Enter behavior.
func (m *Model) selectableEntryIndices() []int {
	var out []int
	for i, e := range m.chat.entries {
		switch e.Kind {
		case service.ChatEntryKindJobResult:
			if e.JobResult != nil {
				out = append(out, i)
			}
		case service.ChatEntryKindWorkerStream:
			if e.WorkerStream != nil {
				out = append(out, i)
			}
		}
	}
	return out
}

// stepBlockSelection moves the chat selection one step (delta = -1 for
// previous, +1 for next) through the selectable entries (job results
// and worker stream blocks). Returns true when the selection changed;
// false (and leaves state untouched) when there's nothing to select or
// the move would walk off the end. Selection wraps off the start to
// "no selection" so the user can return to free typing.
func (m *Model) stepBlockSelection(delta int) bool {
	indices := m.selectableEntryIndices()
	if len(indices) == 0 {
		return false
	}
	cur := -1
	for i, idx := range indices {
		if idx == m.chat.selectedMsgIdx {
			cur = i
			break
		}
	}
	switch {
	case cur < 0 && delta < 0:
		// Entering selection mode from "no selection" via Up — land on
		// the most recent result, which is what the user intuits.
		m.chat.selectedMsgIdx = indices[len(indices)-1]
		return true
	case cur < 0 && delta > 0:
		// Down with no current selection is a no-op (nothing below the
		// input area to walk into).
		return false
	}
	next := cur + delta
	if next < 0 {
		// Stepping past the oldest result clears selection — user is
		// back at "free chat", not stuck cycling.
		m.chat.selectedMsgIdx = -1
		return true
	}
	if next >= len(indices) {
		// Stepping past the newest also clears selection so Down feels
		// symmetric with Up.
		m.chat.selectedMsgIdx = -1
		return true
	}
	m.chat.selectedMsgIdx = indices[next]
	return true
}

// isDisplayOnly reports whether an entry is UI-only chrome that must never be
// sent to the LLM API. Categories:
//
//  1. Pure confirmation/prompt assistant messages (dispatch-confirm, kill-confirm,
//     ask-user-prompt, escalate-prompt) — these are text-only assistant messages
//     injected for the user's benefit; they have no ToolCalls and no matching
//     tool_result, so sending them would confuse the API.
//
//  2. Visual tool-call indicator messages — entries with ClaudeMeta "tool-call-indicator"
//     that have no ToolCalls set (i.e. the "⚙ calling foo…" text lines). Entries
//     with ToolCalls set ARE real tool_use records and must be kept.
//
//  3. Structured entries (Kind != ChatEntryKindMessage) such as job-update
//     blocks — they render from typed payloads and have no text content the
//     model should see.
func isDisplayOnly(e service.ChatEntry) bool {
	if e.Kind != service.ChatEntryKindMessage {
		return true
	}
	switch e.ClaudeMeta {
	case "ask-user-prompt", "dispatch-confirm", "kill-confirm", "escalate-prompt", "feed-event":
		return true
	case "tool-call-indicator":
		// Keep entries that carry actual tool calls; drop text-only indicators.
		return len(e.Message.ToolCalls) == 0
	}
	return false
}

// messagesFromEntries extracts the service.ChatMessage slice from entries.
// Display-only entries (visual indicators, confirmation prompts) are filtered out.
func (m *Model) messagesFromEntries() []service.ChatMessage {
	msgs := make([]service.ChatMessage, 0, len(m.chat.entries))
	for _, e := range m.chat.entries {
		if isDisplayOnly(e) {
			continue
		}
		msgs = append(msgs, e.Message)
	}
	return msgs
}

// hasConversation reports whether the conversation contains at least one user
// message (i.e. the welcome art should be hidden). Assistant-only messages
// (e.g. the startup greeting) are shown alongside the art.
func (m *Model) hasConversation() bool {
	for _, entry := range m.chat.entries {
		if entry.Message.Role == service.MessageRoleUser {
			return true
		}
	}
	return false
}

// setFocus changes the focused panel and arms the spinner tick if moving to
// a panel whose title should animate (rainbow-cycle while focused). The
// ticker is single-armed — a second spinnerTick while one is live would
// double-increment spinnerFrame and run the animation at 2×+ speed.
func (m *Model) setFocus(p focusedPanel) tea.Cmd {
	if p == m.focused {
		return nil
	}
	m.focused = p
	if (p == focusJobs || p == focusBlockers || p == focusFleet) && !m.spinnerRunning {
		m.spinnerRunning = true
		return spinnerTick()
	}
	return nil
}

func (m *Model) newSession() {
	m.initMessages()
	// entries is already reset by initMessages.
	m.stream.operatorByline = ""
	m.stream.currentResponse = ""
	m.stream.currentReasoning = ""
	m.stats.MessageCount = 0
	m.stats.PromptTokens = 0
	m.stats.CompletionTokens = 0
	m.stats.ReasoningTokens = 0
	m.stats.TotalResponses = 0
	m.stats.TotalResponseTime = 0
	m.stats.LastResponseTime = 0
	m.err = nil
	m.scroll.userScrolled = false
	m.updateViewportContent()
	m.chatViewport.GotoBottom()
	m.input.Focus()
}
