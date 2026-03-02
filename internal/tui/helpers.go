// Helpers: text utilities, scrollbar rendering, toast overlay, session management, and chat entry operations.
package tui

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// ChatEntry is a package-level alias for service.ChatEntry so that files not
// yet migrated to the service layer can continue to use the unqualified name.
type ChatEntry = service.ChatEntry

// wrapText wraps s to fit within maxWidth columns.
func wrapText(s string, maxWidth int) string {
	if maxWidth <= 0 {
		maxWidth = 40
	}

	var result strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if lipgloss.Width(line) <= maxWidth {
			if result.Len() > 0 {
				result.WriteString("\n")
			}
			result.WriteString(line)
			continue
		}

		// Word-wrap long lines.
		words := strings.Fields(line)
		var currentLine strings.Builder
		for _, word := range words {
			wordW := lipgloss.Width(word)
			currentW := lipgloss.Width(currentLine.String())

			if currentW == 0 {
				// Break very long words.
				for wordW > maxWidth {
					if result.Len() > 0 || currentLine.Len() > 0 {
						result.WriteString("\n")
					}
					// Take as many runes as fit.
					runes := []rune(word)
					cut := maxWidth
					if cut > len(runes) {
						cut = len(runes)
					}
					result.WriteString(string(runes[:cut]))
					word = string(runes[cut:])
					wordW = lipgloss.Width(word)
				}
				currentLine.WriteString(word)
			} else if currentW+1+wordW <= maxWidth {
				currentLine.WriteString(" ")
				currentLine.WriteString(word)
			} else {
				if result.Len() > 0 {
					result.WriteString("\n")
				}
				result.WriteString(currentLine.String())
				currentLine.Reset()
				currentLine.WriteString(word)
			}
		}
		if currentLine.Len() > 0 {
			if result.Len() > 0 {
				result.WriteString("\n")
			}
			result.WriteString(currentLine.String())
		}
	}

	return result.String()
}

// truncateStr truncates s to maxLen runes, adding "..." if truncated.
// Note: truncation is rune-based, not display-width-based. Characters with
// display width > 1 (e.g. CJK, emoji) may still overflow the visual budget.
func truncateStr(s string, maxLen int) string {
	if maxLen <= 3 {
		maxLen = 3
	}
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen-3]) + "..."
}

// firstLineOf returns the first non-empty line of s.
func firstLineOf(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return s
}

// renderCompletionBlock renders the full content of a completion message as a
// dimmed indented block, skipping the first line (already shown in the header).
func renderCompletionBlock(content string) string {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	var sb strings.Builder
	for _, line := range lines {
		sb.WriteString(DimStyle.Render("  "+line) + "\n")
	}
	return sb.String()
}

// isToolCallIndicatorIdx reports whether the entry at index i is a tool-call indicator.
func (m *Model) isToolCallIndicatorIdx(i int) bool {
	if i < 0 || i >= len(m.chat.entries) {
		return false
	}
	return m.chat.entries[i].ClaudeMeta == "tool-call-indicator"
}

// extractToolName extracts the tool/function name from a tool-call indicator
// message like "⚙ calling `function_name`…". Returns the name or a fallback.
func extractToolName(content string) string {
	// Look for backtick-delimited name.
	start := strings.Index(content, "`")
	if start >= 0 {
		end := strings.Index(content[start+1:], "`")
		if end >= 0 {
			return content[start+1 : start+1+end]
		}
	}
	return "tool call"
}

// formatToolName formats a tool name for display. MCP-namespaced tools
// (containing "__") are rendered as "tool_name (via server)" instead of
// "server__tool_name". Built-in tool names are returned unchanged.
func formatToolName(name string) string {
	if parts := strings.SplitN(name, "__", 2); len(parts) == 2 {
		return fmt.Sprintf("%s (via %s)", parts[1], parts[0])
	}
	return name
}

// formatToolCallContent transforms the content of a tool-call indicator message
// so that any MCP-namespaced tool name inside backticks is displayed in the
// "tool_name (via server)" format. Non-MCP content is returned unchanged.
func formatToolCallContent(content string) string {
	start := strings.Index(content, "`")
	if start < 0 {
		return content
	}
	end := strings.Index(content[start+1:], "`")
	if end < 0 {
		return content
	}
	name := content[start+1 : start+1+end]
	formatted := formatToolName(name)
	if formatted == name {
		return content
	}
	return content[:start+1] + formatted + content[start+1+end:]
}

// renderScrollbar builds a vertical scrollbar string of the given height.
// The thumb position is determined by scrollPercent (0.0–1.0). This is meant
// to be placed alongside the viewport content via lipgloss.JoinHorizontal.
func renderScrollbar(viewportHeight int, totalLines int, scrollPercent float64) string {
	// Calculate thumb size: proportional to visible/total, minimum 2 lines.
	thumbSize := viewportHeight * viewportHeight / totalLines
	if thumbSize < 2 {
		thumbSize = 2
	}
	if thumbSize > viewportHeight {
		thumbSize = viewportHeight
	}

	// Calculate thumb position.
	trackSpace := viewportHeight - thumbSize
	thumbStart := int(scrollPercent * float64(trackSpace))
	if thumbStart < 0 {
		thumbStart = 0
	}
	if thumbStart > trackSpace {
		thumbStart = trackSpace
	}
	thumbEnd := thumbStart + thumbSize

	thumbStyle := lipgloss.NewStyle().Foreground(ColorPrimary)
	trackStyle := lipgloss.NewStyle().Foreground(ColorBorder)

	lines := make([]string, viewportHeight)
	for i := range viewportHeight {
		if i >= thumbStart && i < thumbEnd {
			lines[i] = thumbStyle.Render("█")
		} else {
			lines[i] = trackStyle.Render("░")
		}
	}
	return strings.Join(lines, "\n")
}

// renderToasts renders the toast notification stack as a single string block.
// Newest toasts appear at the top.
func (m *Model) renderToasts() string {
	if len(m.toasts) == 0 {
		return ""
	}
	var lines []string
	// Render newest first (reverse order).
	for i := len(m.toasts) - 1; i >= 0; i-- {
		t := m.toasts[i]
		msg := t.message
		// Truncate by display width, not rune count, so wide characters (emoji,
		// CJK) don't overflow the toast box. Inner budget = MaxWidth(40) minus
		// horizontal padding (Padding(0,1) → 1+1 = 2 columns).
		innerBudget := 40 - ToastBaseStyle.GetHorizontalPadding()
		for lipgloss.Width(msg) > innerBudget {
			runes := []rune(msg)
			msg = string(runes[:len(runes)-1]) + "…"
		}
		var rendered string
		switch t.level {
		case toastSuccess:
			rendered = ToastSuccessStyle.Render(msg)
		case toastWarning:
			rendered = ToastWarningStyle.Render(msg)
		default:
			rendered = ToastInfoStyle.Render(msg)
		}
		lines = append(lines, rendered)
	}
	return strings.Join(lines, "\n")
}

// overlayToasts splices the toast block into the top-right corner of the screen.
func overlayToasts(screen string, toastBlock string, screenWidth int) string {
	if toastBlock == "" {
		return screen
	}
	screenLines := strings.Split(screen, "\n")
	toastLines := strings.Split(toastBlock, "\n")

	for i, tl := range toastLines {
		if i >= len(screenLines) {
			break
		}
		toastW := lipgloss.Width(tl)
		screenLineW := lipgloss.Width(screenLines[i])

		if toastW >= screenWidth {
			// Toast is wider than screen — just replace the line.
			screenLines[i] = tl
			continue
		}

		// Pad the screen line if it's shorter than the screen width.
		if screenLineW < screenWidth {
			screenLines[i] = screenLines[i] + strings.Repeat(" ", screenWidth-screenLineW)
		}

		// Right-align: place toast at the right edge.
		// We need to work with the raw string, but ANSI codes make character
		// counting tricky. Use a simpler approach: build the line as
		// [left portion] + [toast].
		leftWidth := screenWidth - toastW
		if leftWidth < 0 {
			leftWidth = 0
		}

		// Truncate the screen line to leftWidth visible characters.
		// Walk runes and ANSI sequences to find the cut point.
		sl := screenLines[i]
		var result strings.Builder
		visible := 0
		inEsc := false
		for _, r := range sl {
			if r == '\x1b' {
				inEsc = true
				result.WriteRune(r)
				continue
			}
			if inEsc {
				result.WriteRune(r)
				if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
					inEsc = false
				}
				continue
			}
			if visible >= leftWidth {
				break
			}
			result.WriteRune(r)
			visible++
		}
		// Pad if we didn't reach leftWidth.
		for visible < leftWidth {
			result.WriteRune(' ')
			visible++
		}
		result.WriteString(tl)
		screenLines[i] = result.String()
	}
	return strings.Join(screenLines, "\n")
}

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
	m.prompt.confirmDispatch = false
	m.prompt.changingTeam = false
	m.prompt.pendingDispatch = service.ToolCall{}
}

// appendEntry adds a new chat entry to the conversation history.
func (m *Model) appendEntry(e service.ChatEntry) {
	m.chat.entries = append(m.chat.entries, e)
}

// isDisplayOnly reports whether an entry is UI-only chrome that must never be
// sent to the LLM API. Two categories:
//
//  1. Pure confirmation/prompt assistant messages (dispatch-confirm, kill-confirm,
//     ask-user-prompt, escalate-prompt) — these are text-only assistant messages
//     injected for the user's benefit; they have no ToolCalls and no matching
//     tool_result, so sending them would confuse the API.
//
//  2. Visual tool-call indicator messages — entries with ClaudeMeta "tool-call-indicator"
//     that have no ToolCalls set (i.e. the "⚙ calling foo…" text lines). Entries
//     with ToolCalls set ARE real tool_use records and must be kept.
func isDisplayOnly(e service.ChatEntry) bool {
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

// setFocus changes the focused panel and triggers the title burst animation
// if the panel is actually changing. Returns a spinnerTick cmd to ensure the
// ticker is running during the animation window.
func (m *Model) setFocus(p focusedPanel) tea.Cmd {
	if p == m.focused {
		return nil
	}
	m.focused = p
	if p == focusJobs || p == focusTeams || p == focusAgents || p == focusOperator || p == focusMCP {
		m.focusAnimPanel = p
		m.focusAnimFrames = 13 // ~1s at 80ms/tick
		// Only arm the ticker if it isn't already running — firing a second
		// spinnerTick() while one is live would create a second concurrent loop,
		// causing spinnerFrame to increment twice per tick and the animation to
		// run at double (or more) speed.
		if !m.spinnerRunning {
			m.spinnerRunning = true
			return spinnerTick()
		}
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

// displayJobs returns the filtered and sorted list of jobs for display in the left panel.
// Rules:
//   - Completed, failed, and cancelled jobs updated more than 24 hours ago are hidden.
//   - Sort order: Active first (by CreatedAt asc), then Paused (by CreatedAt asc),
//     then Completed/Failed/Cancelled (by CreatedAt asc).
func (m Model) displayJobs() []service.Job {
	now := time.Now()
	cutoff := now.Add(-24 * time.Hour)

	var active, paused, done []service.Job
	for _, j := range m.jobs {
		switch j.Status {
		case service.JobStatusCompleted, service.JobStatusFailed, service.JobStatusCancelled:
			if !j.UpdatedAt.IsZero() && j.UpdatedAt.Before(cutoff) {
				continue // hide stale terminal-state jobs
			}
			done = append(done, j)
		case service.JobStatusPaused:
			paused = append(paused, j)
		default:
			active = append(active, j)
		}
	}

	result := make([]service.Job, 0, len(active)+len(paused)+len(done))
	result = append(result, active...)
	result = append(result, paused...)
	result = append(result, done...)
	return result
}

// jobByID returns the job with the given ID, or zero value and false if not found.
func (m *Model) jobByID(id string) (service.Job, bool) {
	for _, j := range m.jobs {
		if j.ID == id {
			return j, true
		}
	}
	return service.Job{}, false
}

// sortedRuntimeSessions returns the runtime sessions sorted for display:
// active sessions first, then completed/failed/cancelled, with startTime
// as the tiebreaker within each group. sessionID is used as a final stable
// tiebreaker to ensure deterministic ordering when two sessions share the
// same startTime (Go map iteration is randomized).
func (m *Model) sortedRuntimeSessions() []*runtimeSlot {
	slots := make([]*runtimeSlot, 0, len(m.runtimeSessions))
	for _, rs := range m.runtimeSessions {
		slots = append(slots, rs)
	}
	slices.SortFunc(slots, func(a, b *runtimeSlot) int {
		aActive := a.status == "active"
		bActive := b.status == "active"
		if aActive != bActive {
			if aActive {
				return -1 // active before inactive
			}
			return 1
		}
		if cmp := a.startTime.Compare(b.startTime); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.sessionID, b.sessionID) // stable tiebreaker
	})
	return slots
}

// runtimeSessionsForTask returns all runtime sessions associated with the given task ID,
// sorted with active sessions first, then completed, ordered by start time within each group.
func (m *Model) runtimeSessionsForTask(taskID string) []*runtimeSlot {
	var slots []*runtimeSlot
	for _, rs := range m.runtimeSessions {
		if rs.taskID == taskID {
			slots = append(slots, rs)
		}
	}
	slices.SortFunc(slots, func(a, b *runtimeSlot) int {
		aActive := a.status == "active"
		bActive := b.status == "active"
		if aActive != bActive {
			if aActive {
				return -1 // active before inactive
			}
			return 1
		}
		if cmp := a.startTime.Compare(b.startTime); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.sessionID, b.sessionID) // stable tiebreaker
	})
	if slots == nil {
		return []*runtimeSlot{}
	}
	return slots
}

// runtimeSessionForGridCell returns the runtime session displayed in the given
// grid cell index (within the current page), or nil if the cell does not
// contain a runtime session.
func (m *Model) runtimeSessionForGridCell(cellIdx int) *runtimeSlot {
	cols := m.grid.gridCols
	rows := m.grid.gridRows
	// Safety floor: mirrors the floor applied in renderGrid.
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	cellsPerPage := cols * rows
	pageOffset := m.grid.gridPage * cellsPerPage

	sortedRT := m.sortedRuntimeSessions()

	// The absolute index into the sorted runtime session list for the given cell.
	absIdx := pageOffset + cellIdx
	if absIdx < len(sortedRT) {
		return sortedRT[absIdx]
	}
	return nil
}

// formatFeedEntry returns a styled single-line string for a service.FeedEntry.
func formatFeedEntry(entry service.FeedEntry) string {
	switch entry.EntryType {
	case service.FeedEntryTypeSystemEvent:
		return FeedSystemEventStyle.Render("  ⚙ " + entry.Content)
	case service.FeedEntryTypeConsultationTrace:
		return FeedConsultationTraceStyle.Render("    ↳ " + entry.Content)
	case service.FeedEntryTypeTaskStarted:
		return FeedTaskStartedStyle.Render("⚡ " + entry.Content)
	case service.FeedEntryTypeTaskCompleted:
		return FeedTaskCompletedStyle.Render("✓ " + entry.Content)
	case service.FeedEntryTypeTaskFailed:
		return FeedTaskFailedStyle.Render("✗ " + entry.Content)
	case service.FeedEntryTypeBlockerReported:
		return FeedBlockerReportedStyle.Render("🚫 " + entry.Content)
	case service.FeedEntryTypeJobComplete:
		return FeedJobCompleteStyle.Render("✅ " + entry.Content)
	case service.FeedEntryTypeUserMessage, service.FeedEntryTypeOperatorMessage:
		// These are already rendered as chat entries; skip to avoid duplication.
		return ""
	default:
		slog.Debug("unhandled feed entry type", "type", entry.EntryType)
		return DimStyle.Render(entry.Content)
	}
}
