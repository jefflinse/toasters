package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/jefflinse/toasters/internal/service"
)

// updateViewportContent rebuilds the chat history string and sets it on the viewport.
func (m *Model) updateViewportContent() {
	var sb strings.Builder
	contentWidth := m.chatViewport.Width()
	if contentWidth < 1 {
		contentWidth = 40
	}

	// Show welcome message when there's no conversation yet.
	if !m.hasConversation() && !m.stream.streaming {
		// ASCII art: an angry toaster wielding a hammer.
		// Each line is rendered with HeaderStyle so it picks up the accent color.
		const toasterArt = `                     [###]
                       |
                       |
         ___________   |            xxx  
        |  |||  ||| |  O     ______  |
        |           | /|    | w  w | |
        |  {O}  {o} |/ |    | .  . |/|
        |   \_v_/   |  |    |  --- |
        |   -----   |       |______|
        |___________|         |  |
        |___________|
           |     |
           |     |`
		// Render the art as a single block with color but no per-line padding,
		// so lipgloss.Place can measure and center it correctly as a unit.
		artStyled := lipgloss.NewStyle().Foreground(ColorPrimary).Render(toasterArt)
		tagline := DimStyle.Render("Your personal army of toasters to ") + lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render("get shit done.")
		endpoint := DimStyle.Render("Operator connected to " + m.stats.Endpoint)
		hints := DimStyle.Render("Esc to cancel a response · Ctrl+C to quit.")
		block := lipgloss.JoinVertical(lipgloss.Center, artStyled, "", tagline, endpoint, "", hints)

		vpH := m.chatViewport.Height()
		if vpH < 1 {
			vpH = 24
		}
		// Count how many assistant messages (e.g. greeting) will render below.
		hasGreeting := false
		for _, entry := range m.chat.entries {
			if entry.Message.Role == "assistant" && entry.Message.Content != "" {
				hasGreeting = true
				break
			}
		}
		if hasGreeting {
			// When a greeting follows, center the art horizontally but only
			// use the space it needs so the greeting is visible below.
			blockLines := strings.Count(block, "\n") + 1
			topPad := (vpH - blockLines) / 3 // bias toward upper third
			if topPad < 1 {
				topPad = 1
			}
			sb.WriteString(strings.Repeat("\n", topPad))
			for _, line := range strings.Split(block, "\n") {
				sb.WriteString(lipgloss.PlaceHorizontal(contentWidth, lipgloss.Center, line) + "\n")
			}
			sb.WriteString("\n")
		} else {
			welcome := lipgloss.Place(contentWidth, vpH, lipgloss.Center, lipgloss.Center, block)
			sb.WriteString(welcome)
		}
	}

	// Within a contiguous run of worker-stream cards, draw finished ones first
	// so the still-running cards sink to the bottom of the run and stay the most
	// visible. i remains the real entry index, so selection/collapse maps and
	// the streaming tail below are unaffected — only render order changes.
	order := workerStreamDisplayOrder(m.chat.entries)
	for pos := 0; pos < len(order); pos++ {
		i := order[pos]
		entry := m.chat.entries[i]
		// Structured entries render from a typed payload, bypassing the
		// role-based message dispatch.
		if entry.Kind == service.ChatEntryKindJobUpdate {
			block := renderJobUpdateBlock(entry.JobUpdate, contentWidth, false, m.spinnerFrame, false)
			if block != "" {
				sb.WriteString(block + "\n\n")
			}
			continue
		}
		if entry.Kind == service.ChatEntryKindJobResult {
			selected := m.chat.selectedMsgIdx == i
			block := renderJobResultBlock(entry.JobResult, contentWidth, selected)
			if block != "" {
				sb.WriteString(block + "\n")
				// Affordance hint: appears inline under the most recent
				// unread result block when it's not selected, so the user
				// notices the [w]/[d] keys exist without having to read
				// the in-block hint line. Dismissed on selection (the
				// "selected" branch above hides it) or when a newer
				// result replaces this one (recentJobResult moves on).
				if hint := m.jobResultHintLine(entry.JobResult, selected); hint != "" {
					sb.WriteString(hint + "\n")
				}
				sb.WriteString("\n")
			}
			continue
		}
		if entry.Kind == service.ChatEntryKindWorkerStream {
			// Whole-task roll-up: a contiguous run of cards for one task that are
			// ALL done collapses to a single "✓ <task>" line. Takes precedence
			// over the fan-out group roll-up below (it subsumes it). Skipped when
			// the task is still running or a member is selected (then it expands
			// to its cards / fan-out groups).
			if taskID := entry.WorkerStream.TaskID; taskID != "" {
				members := []int{i}
				j := pos + 1
				for j < len(order) {
					e2 := m.chat.entries[order[j]]
					if e2.Kind == service.ChatEntryKindWorkerStream && e2.WorkerStream != nil && e2.WorkerStream.TaskID == taskID {
						members = append(members, order[j])
						j++
						continue
					}
					break
				}
				if len(members) >= 2 {
					allDone, selectedInside := true, false
					for _, mi := range members {
						if !m.chat.entries[mi].WorkerStream.Done {
							allDone = false
						}
						if mi == m.chat.selectedMsgIdx {
							selectedInside = true
						}
					}
					if allDone {
						if selectedInside {
							// Expand the whole task to its cards (consuming the run
							// so the remainder isn't re-collapsed) when a member is
							// selected — arrow-nav and deep-link reach every node.
							for _, mi := range members {
								block := m.renderWorkerStreamBlock(m.chat.entries[mi].WorkerStream, contentWidth, mi == m.chat.selectedMsgIdx)
								if block != "" {
									sb.WriteString(block + "\n\n")
								}
							}
						} else if block := m.renderTaskSummary(members, contentWidth); block != "" {
							sb.WriteString(block + "\n\n")
						}
						pos = j - 1
						continue
					}
				}
			}
			// Roll up a completed fan-out group (a contiguous run of branch/judge
			// cards sharing one parent) into a single summary line — unless its
			// selection is active, in which case expand it so arrow-navigation and
			// deep-link still reach each branch.
			if key, ok := fanoutGroupKey(entry); ok {
				members := []int{i}
				j := pos + 1
				for j < len(order) {
					if k2, ok2 := fanoutGroupKey(m.chat.entries[order[j]]); ok2 && k2 == key {
						members = append(members, order[j])
						j++
						continue
					}
					break
				}
				allDone, selectedInside := true, false
				for _, mi := range members {
					if !m.chat.entries[mi].WorkerStream.Done {
						allDone = false
					}
					if mi == m.chat.selectedMsgIdx {
						selectedInside = true
					}
				}
				if len(members) >= 2 && allDone && !selectedInside {
					if block := m.renderFanoutGroupSummary(members, contentWidth); block != "" {
						sb.WriteString(block + "\n\n")
					}
					pos = j - 1
					continue
				}
			}
			selected := m.chat.selectedMsgIdx == i
			block := m.renderWorkerStreamBlock(entry.WorkerStream, contentWidth, selected)
			if block != "" {
				sb.WriteString(block + "\n\n")
			}
			continue
		}

		msg := entry.Message
		// Timestamp helper.
		var ts string
		if !entry.Timestamp.IsZero() {
			ts = " · " + entry.Timestamp.Format("3:04 PM")
		}

		switch msg.Role {
		case "user":
			// Completion messages render as collapsible blocks.
			if m.chat.completionMsgIdx[i] {
				firstLine := firstLineOf(msg.Content)
				if m.chat.expandedMsgs[i] {
					hint := ""
					if i == m.chat.selectedMsgIdx {
						hint = DimStyle.Render(" [ctrl+x to collapse]")
					}
					header := DimStyle.Render("▼ "+firstLine) + hint
					sb.WriteString(header + "\n" + renderCompletionBlock(msg.Content) + "\n")
				} else {
					hint := ""
					if i == m.chat.selectedMsgIdx {
						hint = DimStyle.Render(" [ctrl+x to expand]")
					}
					sb.WriteString(DimStyle.Render("▶ "+firstLine) + hint + "\n\n")
				}
				continue
			}
			// Render user message block with optional timestamp.
			blockWidth := contentWidth - UserMsgBlockStyle.GetHorizontalFrameSize()
			if blockWidth < 1 {
				blockWidth = 1
			}
			content := wrapText(msg.Content, blockWidth)
			if ts != "" {
				content += "\n" + DimStyle.Render(ts[3:]) // strip leading " · "
			}
			block := UserMsgBlockStyle.Width(blockWidth).Render(content)
			sb.WriteString(block + "\n\n")
		case "assistant":
			aIndent := strings.Repeat(" ", AssistantMsgIndent)
			// ask-user-prompt and escalate-prompt messages render with a byline
			// identifying the asker, then the question(s) below it — no "?" glyph.
			if entry.ClaudeMeta == "ask-user-prompt" || entry.ClaudeMeta == "escalate-prompt" {
				label := "operator asks"
				if entry.ClaudeMeta == "escalate-prompt" {
					label = "needs your input"
				}
				byline := HeaderStyle.Render("◆ " + label)
				body := indentLines(msg.Content, AssistantMsgIndent)
				sb.WriteString(aIndent + byline + "\n" + body + "\n\n")
				continue
			}
			// Feed event entries render as styled single-line system events.
			if entry.ClaudeMeta == "feed-event" {
				sb.WriteString(aIndent + msg.Content + "\n\n")
				continue
			}
			// Tool-call indicator messages render as collapsible tool blocks.
			if entry.ClaudeMeta == "tool-call-indicator" {
				if m.chat.collapsedTools[i] {
					// Expanded: show full content with MCP tool names formatted.
					hint := ""
					if i == m.chat.selectedMsgIdx {
						hint = DimStyle.Render(" [ctrl+x to collapse]")
					}
					sb.WriteString(aIndent + DimStyle.Render(formatToolCallContent(msg.Content)) + hint + "\n\n")
				} else {
					// Collapsed (default): show summary line with MCP tool names formatted.
					toolName := formatToolName(extractToolName(msg.Content))
					hint := ""
					if i == m.chat.selectedMsgIdx {
						hint = DimStyle.Render(" [ctrl+x to expand]")
					}
					sb.WriteString(aIndent + DimStyle.Render("⚙ "+toolName+" ▶") + hint + "\n")
				}
				continue
			}
			// Render claude byline (if any) above the response, with timestamp.
			indent := strings.Repeat(" ", AssistantMsgIndent)
			if entry.ClaudeMeta != "" {
				byline := OperatorBylineStyle.Render("⬡ " + entry.ClaudeMeta)
				if ts != "" {
					byline += DimStyle.Render(ts)
				}
				sb.WriteString(indent + byline + "\n")
			}
			// Render reasoning trace (if any) above the response — only when expanded.
			if entry.Reasoning != "" {
				if m.chat.expandedReasoning[i] {
					sb.WriteString(indentLines(renderReasoningBlock(entry.Reasoning, contentWidth-AssistantMsgIndent), AssistantMsgIndent))
					sb.WriteString("\n")
				} else {
					sb.WriteString(indent + ReasoningStyle.Render("▶ thinking (press ctrl+t to expand)") + "\n\n")
				}
			}
			sb.WriteString(indentLines(m.renderMarkdown(msg.Content), AssistantMsgIndent) + "\n\n")
		case "tool":
			// Render tool result as a collapsible dimmed block.
			if m.chat.collapsedTools[i] {
				// Expanded: show full content.
				preview := msg.Content
				if len(preview) > 300 {
					preview = preview[:300] + "…"
				}
				hint := ""
				if i == m.chat.selectedMsgIdx {
					hint = DimStyle.Render(" [ctrl+x to collapse]")
				}
				sb.WriteString(DimStyle.Render("⚙ tool result: "+preview) + hint + "\n\n")
			} else {
				// Collapsed (default): show summary line.
				hint := ""
				if i == m.chat.selectedMsgIdx {
					hint = DimStyle.Render(" [ctrl+x to expand]")
				}
				sb.WriteString(DimStyle.Render("⚙ tool result ▶") + hint + "\n")
			}
		}
	}

	// Render activity feed entries from SQLite only when no service is wired
	// (operator events are already rendered via OperatorEventMsg as chat entries).
	if len(m.progress.feedEntries) > 0 && m.svc == nil {
		for _, entry := range m.progress.feedEntries {
			line := formatFeedEntry(entry, contentWidth)
			if line != "" {
				sb.WriteString(line + "\n")
			}
		}
		sb.WriteString("\n")
	}

	// Show streaming response in progress — re-render markdown incrementally.
	if m.stream.streaming {
		streamIndent := strings.Repeat(" ", AssistantMsgIndent)
		switch {
		case m.stream.currentReasoning != "":
			// Live reasoning trace — the model is actually thinking out loud.
			sb.WriteString(indentLines(renderReasoningBlock(m.stream.currentReasoning, contentWidth-AssistantMsgIndent), AssistantMsgIndent))
			sb.WriteString("\n")
		case m.stream.currentResponse == "" && len(m.blockers) > 0:
			// The turn is parked on an ask_user blocker waiting for a human
			// response — it's not working, it's blocked. Point the user at the
			// panel rather than implying progress.
			label := fmt.Sprintf("⛔ waiting on input · Tab → Blockers (%d)", len(m.blockers))
			sb.WriteString(streamIndent + ReasoningStyle.Render(label) + "\n\n")
		case m.stream.currentResponse == "":
			// Pre-token gap. A static label rather than an animated spinner —
			// chat content only re-renders on events, so a braille spinner here
			// just freezes into a meaningless dot.
			sb.WriteString(streamIndent + ReasoningStyle.Render("working…") + "\n\n")
		}
		// Live response content, with a static block cursor to mark the live tail.
		if m.stream.currentResponse != "" {
			sb.WriteString(indentLines(m.renderMarkdown(m.stream.currentResponse), AssistantMsgIndent))
			sb.WriteString(StreamingStyle.Render(" ▌"))
			sb.WriteString("\n\n")
		}
	}

	// Show error if present.
	if m.err != nil {
		sb.WriteString(ErrorStyle.Render("Error: "+m.err.Error()) + "\n\n")
	}

	m.chatViewport.SetContent(sb.String())
}
