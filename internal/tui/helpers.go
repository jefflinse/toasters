// Helpers: text utilities, scrollbar rendering, toast overlay, session management, and chat entry operations.
package tui

import (
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/gateway"
	"github.com/jefflinse/toasters/internal/job"
	"github.com/jefflinse/toasters/internal/llm"
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

// truncateStr truncates s to maxLen, adding "..." if truncated.
func truncateStr(s string, maxLen int) string {
	if maxLen <= 3 {
		maxLen = 3
	}
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
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
	if i < 0 || i >= len(m.entries) {
		return false
	}
	return m.entries[i].ClaudeMeta == "tool-call-indicator"
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
		Message:   llm.Message{Role: "assistant", Content: helpText},
		Timestamp: time.Now(),
	})
	m.stats.MessageCount++
	m.updateViewportContent()
	if !m.userScrolled {
		m.chatViewport.GotoBottom()
	}
}

// newSession resets the conversation and all session statistics.
// initMessages resets m.entries and seeds it with the system prompt as entries[0]
// (if a system prompt is set). Call this at startup and on /new.
func (m *Model) initMessages() {
	m.entries = nil
	if m.systemPrompt != "" {
		m.appendEntry(ChatEntry{
			Message:   llm.Message{Role: "system", Content: m.systemPrompt},
			Timestamp: time.Now(),
		})
		m.stats.SystemPromptTokens = estimateTokens(m.systemPrompt)
	} else {
		m.stats.SystemPromptTokens = 0
	}
	m.completionMsgIdx = make(map[int]bool)
	m.expandedMsgs = make(map[int]bool)
	m.selectedMsgIdx = -1
	m.expandedReasoning = make(map[int]bool)
	m.collapsedTools = make(map[int]bool)
	m.confirmDispatch = false
	m.changingTeam = false
	m.pendingDispatch = llm.ToolCall{}
	m.confirmKill = false
	m.pendingKillSlot = 0
	m.confirmTimeout = false
	m.pendingTimeoutSlot = 0
}

// appendEntry adds a new chat entry to the conversation history.
func (m *Model) appendEntry(e ChatEntry) {
	m.entries = append(m.entries, e)
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
	case "ask-user-prompt", "dispatch-confirm", "kill-confirm", "escalate-prompt":
		return true
	case "tool-call-indicator":
		// Keep entries that carry actual tool calls; drop text-only indicators.
		return len(e.Message.ToolCalls) == 0
	}
	return false
}

// messagesFromEntries extracts the llm.Message slice from entries for passing to the LLM client.
// Display-only entries (visual indicators, confirmation prompts) are filtered out.
func (m *Model) messagesFromEntries() []llm.Message {
	msgs := make([]llm.Message, 0, len(m.entries))
	for _, e := range m.entries {
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
	for _, entry := range m.entries {
		if entry.Message.Role == "user" {
			return true
		}
	}
	return false
}

func (m *Model) newSession() {
	m.systemPrompt = agents.BuildOperatorPrompt(m.teams, m.awareness)
	m.initMessages()
	// entries is already reset by initMessages.
	m.claudeActiveMeta = ""
	m.currentResponse = ""
	m.currentReasoning = ""
	m.stats.MessageCount = 0
	m.stats.PromptTokens = 0
	m.stats.CompletionTokens = 0
	m.stats.ReasoningTokens = 0
	m.stats.TotalResponses = 0
	m.stats.TotalResponseTime = 0
	m.stats.LastResponseTime = 0
	m.err = nil
	m.userScrolled = false
	m.updateViewportContent()
	m.chatViewport.GotoBottom()
	m.input.Focus()
}

// displayJobs returns the filtered and sorted list of jobs for display in the left panel.
// Rules:
//   - StatusDone jobs completed more than 24 hours ago are hidden.
//   - Sort order: StatusActive first (by Created asc), then StatusPaused (by Created asc),
//     then StatusDone (by Created asc).
func (m Model) displayJobs() []job.Job {
	now := time.Now()
	cutoff := now.Add(-24 * time.Hour)

	var active, paused, done []job.Job
	for _, j := range m.jobs {
		if j.Status == job.StatusDone {
			if j.Completed != "" {
				t, err := time.Parse(time.RFC3339, j.Completed)
				if err == nil && t.Before(cutoff) {
					continue // hide stale completed jobs
				}
			}
			done = append(done, j)
		} else if j.Status == job.StatusPaused {
			paused = append(paused, j)
		} else {
			active = append(active, j)
		}
	}

	// Each group is already in Created-ascending order (job.List sorts by Created).
	result := make([]job.Job, 0, len(active)+len(paused)+len(done))
	result = append(result, active...)
	result = append(result, paused...)
	result = append(result, done...)
	return result
}

// jobByID returns the job with the given ID, or false if not found.
func (m *Model) jobByID(id string) (job.Job, bool) {
	for _, j := range m.jobs {
		if j.ID == id {
			return j, true
		}
	}
	return job.Job{}, false
}

// sortedRuntimeSessions returns the runtime sessions sorted by start time
// for stable, deterministic display ordering.
func (m *Model) sortedRuntimeSessions() []*runtimeSlot {
	slots := make([]*runtimeSlot, 0, len(m.runtimeSessions))
	for _, rs := range m.runtimeSessions {
		slots = append(slots, rs)
	}
	slices.SortFunc(slots, func(a, b *runtimeSlot) int {
		return a.startTime.Compare(b.startTime)
	})
	return slots
}

// runtimeSessionForGridCell returns the runtime session displayed in the given
// grid cell index (0-3 within the current page), or nil if the cell does not
// contain a runtime session. This replicates the overlay logic used in renderGrid:
// iterate gateway slots for the current page, and for each inactive slot, assign
// the next sorted runtime session.
func (m *Model) runtimeSessionForGridCell(cellIdx int) *runtimeSlot {
	var slots [gateway.MaxSlots]gateway.SlotSnapshot
	if m.gateway != nil {
		slots = m.gateway.Slots()
	}

	sortedRT := m.sortedRuntimeSessions()
	rtIdx := 0
	pageOffset := m.gridPage * 4

	for i := range 4 {
		absIdx := pageOffset + i
		snap := slots[absIdx]
		if !snap.Active {
			if rtIdx < len(sortedRT) {
				rs := sortedRT[rtIdx]
				rtIdx++
				if i == cellIdx {
					return rs
				}
				continue
			}
		}
		// Active gateway slot or no more runtime sessions — skip.
	}
	return nil
}
