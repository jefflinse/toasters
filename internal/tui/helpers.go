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

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/gateway"
	"github.com/jefflinse/toasters/internal/operator"
	"github.com/jefflinse/toasters/internal/provider"
)

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
		// Truncate message to fit within toast max width (inner ~36 chars after padding).
		maxMsg := 36
		if len([]rune(msg)) > maxMsg {
			msg = string([]rune(msg)[:maxMsg-1]) + "…"
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

	m.appendEntry(ChatEntry{
		Message:   provider.Message{Role: "assistant", Content: helpText},
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
		m.appendEntry(ChatEntry{
			Message:   provider.Message{Role: "system", Content: m.systemPrompt},
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
	m.prompt.pendingDispatch = provider.ToolCall{}
	m.prompt.confirmKill = false
	m.prompt.pendingKillSlot = 0
	m.prompt.confirmTimeout = false
	m.prompt.pendingTimeoutSlot = 0
}

// appendEntry adds a new chat entry to the conversation history.
func (m *Model) appendEntry(e ChatEntry) {
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
func isDisplayOnly(e ChatEntry) bool {
	switch e.ClaudeMeta {
	case "ask-user-prompt", "dispatch-confirm", "kill-confirm", "escalate-prompt", "feed-event":
		return true
	case "tool-call-indicator":
		// Keep entries that carry actual tool calls; drop text-only indicators.
		return len(e.Message.ToolCalls) == 0
	}
	return false
}

// messagesFromEntries extracts the provider.Message slice from entries for passing to the LLM client.
// Display-only entries (visual indicators, confirmation prompts) are filtered out.
func (m *Model) messagesFromEntries() []provider.Message {
	msgs := make([]provider.Message, 0, len(m.chat.entries))
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
		if entry.Message.Role == "user" {
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
	if p == focusJobs || p == focusTeams || p == focusAgents {
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
	m.systemPrompt = agents.BuildOperatorPrompt(m.teams, m.awareness)
	m.initMessages()
	// entries is already reset by initMessages.
	m.stream.claudeActiveMeta = ""
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
func (m Model) displayJobs() []*db.Job {
	now := time.Now()
	cutoff := now.Add(-24 * time.Hour)

	var active, paused, done []*db.Job
	for _, j := range m.jobs {
		switch j.Status {
		case db.JobStatusCompleted, db.JobStatusFailed, db.JobStatusCancelled:
			if !j.UpdatedAt.IsZero() && j.UpdatedAt.Before(cutoff) {
				continue // hide stale terminal-state jobs
			}
			done = append(done, j)
		case db.JobStatusPaused:
			paused = append(paused, j)
		default:
			active = append(active, j)
		}
	}

	result := make([]*db.Job, 0, len(active)+len(paused)+len(done))
	result = append(result, active...)
	result = append(result, paused...)
	result = append(result, done...)
	return result
}

// jobByID returns the job with the given ID, or nil, false if not found.
func (m *Model) jobByID(id string) (*db.Job, bool) {
	for _, j := range m.jobs {
		if j.ID == id {
			return j, true
		}
	}
	return nil, false
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

// runtimeSessionForGridCell returns the runtime session displayed in the given
// grid cell index (within the current page), or nil if the cell does not
// contain a runtime session. This replicates the overlay logic used in renderGrid:
// iterate gateway slots for the current page (using the same sorted order), and
// for each inactive slot, assign the next sorted runtime session.
//
// slots and sortedIndices must be the same snapshot/ordering already used by the
// caller (renderGrid or updateGrid) so that rtIdx arithmetic is consistent.
func (m *Model) runtimeSessionForGridCell(cellIdx int, slots [gateway.MaxSlots]gateway.SlotSnapshot, sortedIndices []int) *runtimeSlot {
	cols := m.grid.gridCols
	rows := m.grid.gridRows
	// Safety floor: mirrors the floor applied in renderGrid and updateGrid.
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	cellsPerPage := cols * rows
	pageOffset := m.grid.gridPage * cellsPerPage

	// Pre-skip runtime sessions consumed by earlier pages (mirrors renderGrid).
	rtIdx := 0
	for i := range pageOffset {
		if i >= gateway.MaxSlots {
			break
		}
		snap := slots[sortedIndices[i]]
		if !snap.Active {
			rtIdx++
		}
	}

	sortedRT := m.sortedRuntimeSessions()

	// Find the runtime session for cellIdx.
	for i := range cellsPerPage {
		absIdxPos := pageOffset + i
		if absIdxPos >= gateway.MaxSlots {
			break
		}
		snap := slots[sortedIndices[absIdxPos]]
		if snap.Active {
			// Gateway slot — skip.
			continue
		}
		// Runtime slot.
		if i == cellIdx {
			if rtIdx < len(sortedRT) {
				return sortedRT[rtIdx]
			}
			return nil
		}
		rtIdx++
	}
	return nil
}

// formatOperatorEvent returns a styled single-line string for an operator event,
// or empty string if the event type should not be displayed in the feed.
func formatOperatorEvent(ev operator.Event) string {
	switch ev.Type {
	case operator.EventTaskStarted:
		if p, ok := ev.Payload.(operator.TaskStartedPayload); ok {
			return FeedTaskStartedStyle.Render(fmt.Sprintf("⚡ %s started task: %q", p.TeamID, p.Title))
		}
		return FeedTaskStartedStyle.Render("⚡ task started")

	case operator.EventTaskCompleted:
		if p, ok := ev.Payload.(operator.TaskCompletedPayload); ok {
			return FeedTaskCompletedStyle.Render(fmt.Sprintf("✓ %s completed task", p.TeamID))
		}
		return FeedTaskCompletedStyle.Render("✓ task completed")

	case operator.EventTaskFailed:
		if p, ok := ev.Payload.(operator.TaskFailedPayload); ok {
			return FeedTaskFailedStyle.Render(fmt.Sprintf("✗ %s failed task: %s", p.TeamID, p.Error))
		}
		return FeedTaskFailedStyle.Render("✗ task failed")

	case operator.EventBlockerReported:
		if p, ok := ev.Payload.(operator.BlockerReportedPayload); ok {
			return FeedBlockerReportedStyle.Render(fmt.Sprintf("🚫 %s reported blocker: %s", p.TeamID, p.Description))
		}
		return FeedBlockerReportedStyle.Render("🚫 blocker reported")

	case operator.EventJobComplete:
		if p, ok := ev.Payload.(operator.JobCompletePayload); ok {
			return FeedJobCompleteStyle.Render(fmt.Sprintf("✅ Job %q complete", p.Title))
		}
		return FeedJobCompleteStyle.Render("✅ job complete")

	case operator.EventProgressUpdate:
		// Progress updates are too noisy for the main feed — skip.
		return ""

	default:
		slog.Debug("unhandled operator event type in feed", "type", ev.Type)
		return ""
	}
}

// formatFeedEntry returns a styled single-line string for a db.FeedEntry.
func formatFeedEntry(entry *db.FeedEntry) string {
	switch entry.EntryType {
	case db.FeedEntrySystemEvent:
		return FeedSystemEventStyle.Render("  ⚙ " + entry.Content)
	case db.FeedEntryConsultationTrace:
		return FeedConsultationTraceStyle.Render("    ↳ " + entry.Content)
	case db.FeedEntryTaskStarted:
		return FeedTaskStartedStyle.Render("⚡ " + entry.Content)
	case db.FeedEntryTaskCompleted:
		return FeedTaskCompletedStyle.Render("✓ " + entry.Content)
	case db.FeedEntryTaskFailed:
		return FeedTaskFailedStyle.Render("✗ " + entry.Content)
	case db.FeedEntryBlockerReported:
		return FeedBlockerReportedStyle.Render("🚫 " + entry.Content)
	case db.FeedEntryJobComplete:
		return FeedJobCompleteStyle.Render("✅ " + entry.Content)
	case db.FeedEntryUserMessage, db.FeedEntryOperatorMessage:
		// These are already rendered as chat entries; skip to avoid duplication.
		return ""
	default:
		return DimStyle.Render(entry.Content)
	}
}
