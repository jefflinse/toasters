// Grid screen: 2x2 agent slot grid rendering, context bar, token bar, and reasoning block display.
package tui

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/gateway"
)

func (m *Model) renderGrid() string {
	cellW := m.width / 2
	cellH := (m.height - 1) / 2 // -1 to make room for the hotkey bar

	var cells [4]string
	var slots [gateway.MaxSlots]gateway.SlotSnapshot
	if m.gateway != nil {
		slots = m.gateway.Slots()
	}

	pageOffset := m.gridPage * 4

	for i := 0; i < 4; i++ {
		absIdx := pageOffset + i
		snap := slots[absIdx]
		focused := i == m.gridFocusCell

		innerH := cellH - 2 // top + bottom border
		innerW := cellW - 4 // left + right border + padding
		if innerH < 1 {
			innerH = 1
		}
		if innerW < 1 {
			innerW = 1
		}

		// Determine border color based on agent status.
		var borderColor color.Color
		switch {
		case !snap.Active:
			// Empty/inactive slot — always dim.
			if focused {
				borderColor = ColorPrimary
			} else {
				borderColor = ColorBorder
			}
		case snap.Status == gateway.SlotRunning && snap.PendingTool != "":
			if focused {
				// Bright orange for focused + pending tool.
				borderColor = lipgloss.Color("#ffaf00")
			} else {
				borderColor = ColorStreaming
			}
		case snap.Status == gateway.SlotRunning:
			if focused {
				// Bright green for focused + running.
				borderColor = lipgloss.Color("#5fff5f")
			} else {
				borderColor = ColorConnected
			}
		case snap.Status == gateway.SlotDone:
			if focused {
				borderColor = ColorPrimary
			} else {
				borderColor = ColorDim
			}
		default:
			if focused {
				borderColor = ColorPrimary
			} else {
				borderColor = ColorBorder
			}
		}
		var headerStyle lipgloss.Style
		if focused {
			headerStyle = HeaderStyle
		} else {
			headerStyle = SidebarHeaderStyle
		}

		borderType := lipgloss.RoundedBorder()
		if focused {
			borderType = lipgloss.ThickBorder()
		}

		cellStyle := lipgloss.NewStyle().
			Width(cellW).
			Height(cellH).
			Border(borderType).
			BorderForeground(borderColor).
			Padding(0, 1)

		if !snap.Active {
			emptyContent := DimStyle.Render(fmt.Sprintf("slot %d — empty", absIdx))
			emptyLines := strings.Split(emptyContent, "\n")
			if len(emptyLines) > innerH {
				emptyLines = emptyLines[:innerH]
			}
			cells[i] = cellStyle.Render(strings.Join(emptyLines, "\n"))
			continue
		}

		// 1. Header: statusMark · agent · job · elapsed
		elapsed := time.Since(snap.StartTime).Round(time.Second)
		if snap.Status == gateway.SlotDone && !snap.EndTime.IsZero() {
			elapsed = snap.EndTime.Sub(snap.StartTime).Round(time.Second)
		}
		statusMark := "▶"
		if snap.Status == gateway.SlotDone {
			statusMark = "✓"
		}
		header := fmt.Sprintf("%s %s · %s · %s", statusMark, snap.AgentName, snap.JobID, elapsed)

		// Append mini token usage bar if tokens are present.
		totalTokens := snap.InputTokens + snap.OutputTokens
		if totalTokens > 0 {
			header += " " + miniTokenBar(totalTokens)
		}

		headerLine := headerStyle.Render(truncateStr(header, innerW))

		// 2. Summary (prefer ExitSummary when done)
		summary := snap.Summary
		if snap.Status == gateway.SlotDone && snap.ExitSummary != "" {
			summary = snap.ExitSummary
		}
		if summary == "" {
			summary = snap.AgentName + " on " + snap.JobID
		}
		summaryLine := truncateStr(summary, innerW)

		// 3. Model line (with optional turn count and stop reason)
		modelStr := snap.Model
		if modelStr == "" {
			modelStr = "model: unknown"
		}
		if snap.TurnCount > 0 {
			modelStr += fmt.Sprintf(" · %d turns", snap.TurnCount)
		}
		if snap.StopReason != "" && snap.Status == gateway.SlotDone {
			modelStr += " · stop:" + snap.StopReason
		}
		modelLine := DimStyle.Render(truncateStr(modelStr, innerW))

		// 3b. Token line (only if any tokens recorded)
		var tokenLine string
		if snap.InputTokens > 0 || snap.OutputTokens > 0 {
			tokenLine = DimStyle.Render(truncateStr(
				fmt.Sprintf("↑%s ↓%s", compactNum(snap.InputTokens), compactNum(snap.OutputTokens)),
				innerW,
			))
		}

		// 3c. Version line (only if ClaudeVersion is known)
		var versionLine string
		if snap.ClaudeVersion != "" {
			versionLine = DimStyle.Render(truncateStr("claude v"+snap.ClaudeVersion, innerW))
		}

		// 3d. Session line (only if SessionID is known; truncated to 8 chars)
		var sessionLine string
		if snap.SessionID != "" {
			sid := snap.SessionID
			if len(sid) > 8 {
				sid = sid[:8]
			}
			sessionLine = DimStyle.Render(truncateStr("session: "+sid, innerW))
		}

		// 3e. Subagent line (only if any subagents have been spawned)
		var subagentLine string
		if snap.SubagentsSpawned > 0 {
			subagentStr := fmt.Sprintf("subagents: %d spawned, %d in-flight", snap.SubagentsSpawned, snap.SubagentsInFlight)
			if snap.SubagentsInFlight > 0 {
				subagentLine = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Render(truncateStr(subagentStr, innerW))
			} else {
				subagentLine = DimStyle.Render(truncateStr(subagentStr, innerW))
			}
		}

		// 4. Prompt preview: first 3 non-empty lines of the prompt
		var promptLines []string
		for _, l := range strings.Split(snap.Prompt, "\n") {
			l = strings.TrimSpace(l)
			if l != "" {
				promptLines = append(promptLines, l)
			}
			if len(promptLines) == 3 {
				break
			}
		}
		var promptPreview string
		if len(promptLines) > 0 {
			truncatedLines := make([]string, len(promptLines))
			for j, l := range promptLines {
				truncatedLines[j] = truncateStr(l, innerW)
			}
			promptPreview = DimStyle.Render(strings.Join(truncatedLines, "\n"))
		}
		// Hint for focused cell
		if focused {
			promptPreview += "\n" + DimStyle.Render(truncateStr("p: view full prompt", innerW))
		}

		// 5. Separator
		separator := DimStyle.Render(strings.Repeat("─", innerW))

		// 6. Output: tail of snap.Output to fill remaining height.
		// Budget:
		//   1 header + 1 summary + 1 model + (1 token if present) +
		//   (1 version if present) + (1 session if present) + (1 subagent if present) +
		//   3 prompt + 1 p-hint(focused) + 1 separator
		metaLines := 7 // header + summary + model + 3 prompt + separator
		if focused {
			metaLines++ // p-hint line
		}
		if tokenLine != "" {
			metaLines++ // token line
		}
		if versionLine != "" {
			metaLines++ // version line
		}
		if sessionLine != "" {
			metaLines++ // session line
		}
		if subagentLine != "" {
			metaLines++ // subagent line
		}
		outputH := innerH - metaLines
		if outputH < 0 {
			outputH = 0
		}

		// Build output body lines, prepending special indicators.
		var outputBodyLines []string

		// PendingTool indicator
		if snap.Status == gateway.SlotRunning && snap.PendingTool != "" {
			toolIndicator := lipgloss.NewStyle().Foreground(ColorStreaming).Render(
				truncateStr("⚙ "+snap.PendingTool, innerW),
			)
			outputBodyLines = append(outputBodyLines, toolIndicator)
		}

		// ThinkingOutput indicator
		if snap.ThinkingOutput != "" {
			thinkLine := DimStyle.Render(truncateStr(
				fmt.Sprintf("[thinking: %s chars]", compactNum(len(snap.ThinkingOutput))),
				innerW,
			))
			outputBodyLines = append(outputBodyLines, thinkLine)
		}

		// SubagentOutput indicator — show the last non-empty line of subagent output.
		if snap.SubagentOutput != "" {
			lastSubLine := ""
			for _, sl := range strings.Split(snap.SubagentOutput, "\n") {
				if strings.TrimSpace(sl) != "" {
					lastSubLine = sl
				}
			}
			if lastSubLine == "" {
				lastSubLine = snap.SubagentOutput
			}
			subLine := DimStyle.Render(truncateStr("↳ "+lastSubLine, innerW))
			outputBodyLines = append(outputBodyLines, subLine)
		}

		// Reserve lines for the indicators we just added.
		indicatorLines := len(outputBodyLines)
		tailH := outputH - indicatorLines
		if tailH < 0 {
			tailH = 0
		}

		if snap.Output != "" && tailH > 0 {
			outLines := strings.Split(snap.Output, "\n")
			if len(outLines) > tailH {
				outLines = outLines[len(outLines)-tailH:]
			}
			for j, l := range outLines {
				if len([]rune(l)) > innerW {
					outLines[j] = string([]rune(l)[:innerW])
				}
			}
			outputBodyLines = append(outputBodyLines, outLines...)
		}

		outputBody := strings.Join(outputBodyLines, "\n")

		// Assemble cell content parts.
		parts := []string{
			headerLine,
			summaryLine,
			modelLine,
		}
		if tokenLine != "" {
			parts = append(parts, tokenLine)
		}
		if versionLine != "" {
			parts = append(parts, versionLine)
		}
		if sessionLine != "" {
			parts = append(parts, sessionLine)
		}
		if subagentLine != "" {
			parts = append(parts, subagentLine)
		}
		parts = append(parts, promptPreview, separator, outputBody)

		inner := strings.Join(parts, "\n")

		// Hard-clamp to innerH lines so ANSI content can never overflow the cell budget.
		innerLines := strings.Split(inner, "\n")
		if len(innerLines) > innerH {
			innerLines = innerLines[:innerH]
		}
		inner = strings.Join(innerLines, "\n")

		cells[i] = cellStyle.Render(inner)
	}

	hotkeyBar := DimStyle.Render(fmt.Sprintf(
		"  arrows: navigate   ·   k/ctrl+k: kill   ·   enter: view output   ·   p: view prompt   ·   [/]: page %d/4   ·   ctrl+g / esc: close",
		m.gridPage+1,
	))
	hotkeyBar = lipgloss.NewStyle().Width(m.width).Render(hotkeyBar)

	top := lipgloss.JoinHorizontal(lipgloss.Top, cells[0], cells[1])
	bottom := lipgloss.JoinHorizontal(lipgloss.Top, cells[2], cells[3])
	return lipgloss.JoinVertical(lipgloss.Left, hotkeyBar, top, bottom)
}

// commaInt formats an integer with comma-separated thousands (e.g. 200000 → "200,000").
func commaInt(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + commaInt(-n)
	}
	if len(s) <= 3 {
		return s
	}
	// Insert commas from the right.
	var b strings.Builder
	rem := len(s) % 3
	if rem > 0 {
		b.WriteString(s[:rem])
	}
	for i := rem; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// renderContextBar renders a segmented progress bar showing context window usage.
// The bar has two segments: system prompt tokens (dimmer) and conversation tokens
// (gradient from green → yellow → red). When streaming, conversation cells pulse.
// systemTokens is the estimated token count of the system prompt.
func renderContextBar(used, systemTokens, total, width int, streaming bool, spinnerFrame int) string {
	if width < 4 {
		width = 4
	}

	var pct float64
	var summary string
	if total > 0 {
		pct = float64(used) / float64(total)
		if pct > 1 {
			pct = 1
		}
		summary = fmt.Sprintf("%s / %s (%.0f%%)", commaInt(used), commaInt(total), pct*100)
	} else {
		summary = fmt.Sprintf("%s / ?", commaInt(used))
	}

	// Calculate system vs conversation segments.
	var sysPct float64
	if total > 0 && systemTokens > 0 {
		sysPct = float64(systemTokens) / float64(total)
		if sysPct > pct {
			sysPct = pct // system can't exceed total used
		}
	}
	sysFilled := int(sysPct * float64(width))
	totalFilled := int(pct * float64(width))
	convFilled := totalFilled - sysFilled
	empty := width - totalFilled

	// Gradient anchors: green → yellow (midpoint) → red.
	type rgb struct{ r, g, b uint8 }
	green := rgb{82, 196, 26}
	yellow := rgb{250, 173, 20}
	red := rgb{245, 34, 45}

	// lerpRGB interpolates between two colors by t in [0,1].
	lerpRGB := func(a, b rgb, t float64) rgb {
		return rgb{
			r: uint8(float64(a.r)*(1-t) + float64(b.r)*t),
			g: uint8(float64(a.g)*(1-t) + float64(b.g)*t),
			b: uint8(float64(a.b)*(1-t) + float64(b.b)*t),
		}
	}

	sysStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	emptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("237"))

	var bar strings.Builder

	// System prompt segment — dim solid fill.
	bar.WriteString(sysStyle.Render(strings.Repeat("▓", sysFilled)))

	// Conversation segment — gradient fill.
	for i := range convFilled {
		// t is position across the full bar width.
		var t float64
		if width > 1 {
			t = float64(sysFilled+i) / float64(width-1)
		}
		var c rgb
		if t < 0.5 {
			c = lerpRGB(green, yellow, t*2)
		} else {
			c = lerpRGB(yellow, red, (t-0.5)*2)
		}
		cellChar := "█"
		if streaming && i%2 == spinnerFrame%2 {
			cellChar = "▓"
		}
		bar.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b))).
			Render(cellChar))
	}

	// Empty segment.
	bar.WriteString(emptyStyle.Render(strings.Repeat("░", empty)))

	// Summary line with system/conversation breakdown.
	var detail string
	if systemTokens > 0 {
		convTokens := used - systemTokens
		if convTokens < 0 {
			convTokens = 0
		}
		detail = fmt.Sprintf("sys ~%s · conv ~%s", commaInt(systemTokens), commaInt(convTokens))
	}

	lines := bar.String() + "\n" + DimStyle.Render(summary)
	if detail != "" {
		lines += "\n" + DimStyle.Render(detail)
	}
	return lines
}

// miniTokenBar returns a compact 8-char token usage bar with gradient coloring
// and a compact token count suffix, e.g. "[████░░░░] 45k".
// maxTokens is the reference ceiling (200k).
func miniTokenBar(totalTokens int) string {
	const barWidth = 8
	const maxTokens = 200_000

	pct := float64(totalTokens) / float64(maxTokens)
	if pct > 1 {
		pct = 1
	}
	filled := int(pct * barWidth)
	if filled < 0 {
		filled = 0
	}
	empty := barWidth - filled

	// Gradient anchors: green → yellow (midpoint) → red.
	type rgb struct{ r, g, b uint8 }
	green := rgb{82, 196, 26}
	yellow := rgb{250, 173, 20}
	red := rgb{245, 34, 45}
	lerpRGB := func(a, b rgb, t float64) rgb {
		return rgb{
			r: uint8(float64(a.r)*(1-t) + float64(b.r)*t),
			g: uint8(float64(a.g)*(1-t) + float64(b.g)*t),
			b: uint8(float64(a.b)*(1-t) + float64(b.b)*t),
		}
	}

	emptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("237"))

	var bar strings.Builder
	bar.WriteString("[")
	for i := range filled {
		var t float64
		if barWidth > 1 {
			t = float64(i) / float64(barWidth-1)
		}
		var c rgb
		if t < 0.5 {
			c = lerpRGB(green, yellow, t*2)
		} else {
			c = lerpRGB(yellow, red, (t-0.5)*2)
		}
		bar.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b))).
			Render("█"))
	}
	bar.WriteString(emptyStyle.Render(strings.Repeat("░", empty)))
	bar.WriteString("] ")
	bar.WriteString(compactNum(totalTokens))

	return bar.String()
}

// renderReasoningBlock renders a chain-of-thought reasoning trace as a dimmed,
// left-bordered block with a "thinking" header.
func renderReasoningBlock(reasoning string, contentWidth int) string {
	blockWidth := contentWidth - ReasoningBlockStyle.GetHorizontalFrameSize()
	if blockWidth < 1 {
		blockWidth = 1
	}
	header := ReasoningHeaderStyle.Render("⟳ thinking")
	body := ReasoningBlockStyle.Width(blockWidth).Render(wrapText(reasoning, blockWidth))
	return header + "\n" + body
}
