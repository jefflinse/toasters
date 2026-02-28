// Grid screen: dynamic NxM agent slot grid rendering, context bar, token bar, and reasoning block display.
package tui

import (
	"encoding/json"
	"fmt"
	"image/color"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

const (
	minGridCellInnerW = 40 // minimum inner cell width
	minGridCellInnerH = 8  // minimum inner cell height
	gridHotkeyBarH    = 1  // hotkey bar height
	gridCellBorderW   = 4  // total horizontal border+padding per cell
	gridCellBorderH   = 2  // total vertical border+padding per cell

	// maxGridSlots is the maximum number of agent slots displayed in the grid.
	maxGridSlots = 16
)

// computeGridDimensions returns the number of columns and rows for the grid
// given the terminal dimensions. Minimum is 1×1.
func computeGridDimensions(termW, termH int) (cols, rows int) {
	minCellW := minGridCellInnerW + gridCellBorderW
	minCellH := minGridCellInnerH + gridCellBorderH
	availH := termH - gridHotkeyBarH
	cols = termW / minCellW
	rows = availH / minCellH
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	return cols, rows
}

// renderEmptyCell renders a dim empty placeholder cell with the given dimensions.
// cellW and cellH are the outer cell dimensions (including border); the safety
// floor of 1 is enforced by computeGridDimensions and the renderGrid safety floor.
func renderEmptyCell(cellW, cellH int, focused bool) string {
	var borderColor color.Color
	if focused {
		borderColor = ColorPrimary
	} else {
		borderColor = ColorBorder
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
	emptyContent := DimStyle.Render("empty")
	return cellStyle.Render(emptyContent)
}

func (m *Model) renderGrid() string {
	// Safety floor: ensure cols/rows are at least 1.
	cols := m.grid.gridCols
	rows := m.grid.gridRows
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}

	cellsPerPage := cols * rows
	cellW := m.width / cols
	cellH := (m.height - gridHotkeyBarH) / rows

	cells := make([]string, cellsPerPage)

	// Collect sorted runtime sessions for display.
	sortedRT := m.sortedRuntimeSessions()
	pageOffset := m.grid.gridPage * cellsPerPage

	for i := range cellsPerPage {
		absIdxPos := pageOffset + i
		focused := i == m.grid.gridFocusCell

		innerH := cellH - gridCellBorderH
		innerW := cellW - gridCellBorderW
		if innerH < 1 {
			innerH = 1
		}
		if innerW < 1 {
			innerW = 1
		}

		if absIdxPos < len(sortedRT) {
			rs := sortedRT[absIdxPos]
			cells[i] = m.renderRuntimeGridCell(rs, cellW, cellH, innerW, innerH, focused)
		} else {
			cells[i] = renderEmptyCell(cellW, cellH, focused)
		}
	}

	totalPages := (maxGridSlots + cellsPerPage - 1) / cellsPerPage
	hotkeyBar := DimStyle.Render(fmt.Sprintf(
		"  arrows: navigate   ·   enter: view output   ·   p: view prompt   ·   [/]: page %d/%d   ·   ctrl+g / esc: close",
		m.grid.gridPage+1, totalPages,
	))
	hotkeyBar = lipgloss.NewStyle().Width(m.width).Render(hotkeyBar)

	rowStrings := make([]string, rows)
	for r := range rows {
		rowCells := cells[r*cols : (r+1)*cols]
		rowStrings[r] = lipgloss.JoinHorizontal(lipgloss.Top, rowCells...)
	}
	gridBody := lipgloss.JoinVertical(lipgloss.Left, rowStrings...)
	return lipgloss.JoinVertical(lipgloss.Left, hotkeyBar, gridBody)
}

// renderRuntimeGridCell renders a single runtime session into a grid cell as a
// structured smart card:
//
//	⚡ team/agent-name · <uuid-short> · 1m24s   ← header
//	──────────────────────────────────────────   ← dim separator
//	building core data models                    ← task description (word-wrapped, ≤2 lines)
//	──────────────────────────────────────────   ← dim separator (only if task non-empty)
//	• write: main.go                             ← activity items, newest first
//	• shell: go test ./...
func (m *Model) renderRuntimeGridCell(rs *runtimeSlot, cellW, cellH, innerW, innerH int, focused bool) string {
	// Green border for active runtime sessions.
	var borderColor color.Color
	if rs.status == "active" {
		if focused {
			borderColor = lipgloss.Color("#5fff5f") // bright green when focused
		} else {
			borderColor = ColorConnected // bright green for active sessions
		}
	} else {
		if focused {
			borderColor = ColorPrimary
		} else {
			borderColor = ColorDim
		}
	}

	var hdrStyle lipgloss.Style
	if focused {
		hdrStyle = HeaderStyle
	} else {
		hdrStyle = SidebarHeaderStyle
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

	// Graceful degrade: too narrow to show a useful card.
	if innerH < 4 {
		jobID := rs.jobID
		if len(jobID) > 8 {
			jobID = jobID[:8]
		}
		mini := DimStyle.Render(truncateStr(jobID, innerW))
		miniLines := strings.Split(mini, "\n")
		if len(miniLines) > innerH {
			miniLines = miniLines[:innerH]
		}
		return cellStyle.Render(strings.Join(miniLines, "\n"))
	}

	// --- Header line ---
	elapsed := time.Since(rs.startTime).Round(time.Second)
	statusMark := "⚡"
	if rs.status != "active" {
		statusMark = "✓"
	}
	agentLabel := rs.agentName
	if agentLabel == "" {
		agentLabel = "runtime"
	}
	// The agentName may already be team-scoped (e.g. "auto-opencode/orchestrator")
	// from the loader's ID construction. Only prepend teamName if it's not already
	// a prefix to avoid double-prefixing like "auto-opencode/auto-opencode/orchestrator".
	if rs.teamName != "" && !strings.HasPrefix(agentLabel, rs.teamName+"/") {
		agentLabel = rs.teamName + "/" + agentLabel
	}
	// Short job ID (first 8 chars).
	shortJobID := rs.jobID
	if len(shortJobID) > 8 {
		shortJobID = shortJobID[:8]
	}
	header := fmt.Sprintf("%s %s · %s · %s", statusMark, agentLabel, shortJobID, elapsed)
	headerLine := hdrStyle.Render(truncateStr(header, innerW))

	// --- Separator after header ---
	separator := DimStyle.Render(strings.Repeat("─", innerW))

	// --- Task description section ---
	// Word-wrap the task to innerW, cap at 2 lines.
	var taskLines []string
	if rs.task != "" {
		wrapped := wrapText(rs.task, innerW)
		all := strings.Split(wrapped, "\n")
		if len(all) > 2 {
			all = all[:2]
		}
		if rs.status == "active" {
			// Slowly cycle colors through the task text to signal in-progress work.
			for _, l := range all {
				taskLines = append(taskLines, rainbowText(l, m.spinnerFrame))
			}
		} else {
			// Dim for completed/killed.
			for _, l := range all {
				taskLines = append(taskLines, DimStyle.Render(l))
			}
		}
	}
	hasTask := len(taskLines) > 0

	// --- Line budget ---
	// 1 header + 1 separator = 2 fixed lines.
	// Task section: len(taskLines) + 1 separator (if non-empty).
	taskSectionLines := 0
	if hasTask {
		taskSectionLines = len(taskLines) + 1 // task lines + task separator
	}
	// activityH may be 0 when the task section fills the available height;
	// the hard-clamp below ensures we never overflow innerH.
	activityH := innerH - 2 - taskSectionLines
	if activityH < 0 {
		activityH = 0
	}

	// --- Activity items (newest first) ---
	bulletStyle := DimStyle
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	maxLabelW := innerW - 2 // "• " prefix
	if maxLabelW < 1 {
		maxLabelW = 1
	}

	var activityLines []string
	if len(rs.activities) == 0 && rs.status == "active" {
		// Waiting state — show a single dim placeholder.
		if activityH > 0 {
			activityLines = append(activityLines, DimStyle.Render("waiting for activity…"))
		}
	} else {
		// Iterate newest-first (activities are oldest-first).
		for i := len(rs.activities) - 1; i >= 0 && len(activityLines) < activityH; i-- {
			lbl := rs.activities[i].label
			if len([]rune(lbl)) > maxLabelW {
				lbl = string([]rune(lbl)[:maxLabelW])
			}
			line := bulletStyle.Render("• ") + labelStyle.Render(lbl)
			activityLines = append(activityLines, line)
		}
	}

	// --- Assemble lines slice ---
	var lines []string
	lines = append(lines, headerLine)
	lines = append(lines, separator)
	if hasTask {
		lines = append(lines, taskLines...)
		lines = append(lines, separator)
	}
	lines = append(lines, activityLines...)

	// Hard-clamp to innerH lines.
	if len(lines) > innerH {
		lines = lines[:innerH]
	}

	return cellStyle.Render(strings.Join(lines, "\n"))
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

// activityLabel returns a short human-readable label for a tool call,
// suitable for display in a runtime agent card's activity list.
func activityLabel(toolName string, args json.RawMessage) string {
	var a map[string]any
	_ = json.Unmarshal(args, &a)

	str := func(key string) string {
		v, _ := a[key].(string)
		return v
	}
	trunc := func(s string, n int) string {
		r := []rune(s)
		if len(r) > n {
			return string(r[:n]) + "…"
		}
		return s
	}

	switch toolName {
	case "write_file":
		return "write: " + filepath.Base(str("path"))
	case "edit_file":
		return "edit: " + filepath.Base(str("path"))
	case "read_file":
		return "read: " + filepath.Base(str("path"))
	case "shell":
		return "shell: " + trunc(str("command"), 28)
	case "spawn_agent":
		name := str("agent_name")
		if name == "" {
			name = "worker"
		}
		return "spawn: " + name
	case "report_progress":
		msg := str("message")
		if msg == "" {
			return "progress: (no message)"
		}
		return "progress: " + trunc(msg, 28)
	case "report_blocker":
		desc := str("description")
		if desc == "" {
			return "blocker: (no description)"
		}
		return "blocker: " + trunc(desc, 28)
	case "web_fetch":
		u := str("url")
		if parsed, err := url.Parse(u); err == nil && parsed.Host != "" {
			return "fetch: " + parsed.Host
		}
		return "fetch: " + trunc(u, 28)
	case "glob":
		return "glob: " + trunc(str("pattern"), 28)
	case "grep":
		return "grep: " + trunc(str("pattern"), 28)
	case "log_artifact":
		return "artifact: " + trunc(str("name"), 28)
	case "update_task_status":
		return "task: " + str("status")
	case "request_review":
		return "review: requested"
	case "query_job_context":
		return "query: job context"
	default:
		// MCP-namespaced tools: "server__tool_name" → "server: tool_name"
		if parts := strings.SplitN(toolName, "__", 2); len(parts) == 2 {
			return trunc(parts[0]+": "+parts[1], 35)
		}
		return trunc(toolName, 35)
	}
}
