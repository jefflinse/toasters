// Grid screen: dynamic NxM worker slot grid rendering, context bar, token bar, and reasoning block display.
package tui

import (
	"encoding/json"
	"fmt"
	"image/color"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

const (
	minGridCellInnerW = 40 // minimum inner cell content width
	minGridCellInnerH = 6  // minimum inner cell content height: headline + meta + ctx bar + 3 activity lines
	gridHotkeyBarH    = 1  // hotkey bar height
	gridCellFrameW    = 2  // per-cell horizontal frame: left bar (1) + left padding (1)

	// maxGridSlots is the maximum number of worker slots displayed in the grid.
	maxGridSlots = 16
)

// gridCellLeftBar is the left-bar border used by every grid cell, mirroring the
// worker cards in the main chat viewport so the two surfaces read as one style.
var gridCellLeftBar = lipgloss.Border{Left: "▌"}

// computeGridDimensions returns the number of columns and rows for the grid
// given the terminal dimensions. Minimum is 1×1.
func computeGridDimensions(termW, termH int) (cols, rows int) {
	minCellW := minGridCellInnerW + gridCellFrameW
	availH := termH - gridHotkeyBarH
	cols = termW / minCellW
	rows = availH / minGridCellInnerH
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	return cols, rows
}

// gridCellFrame returns the left-bar frame style for a grid cell at the given
// outer dimensions. The border color encodes status/focus; content is padded to
// fill the cell so rows tile cleanly.
func gridCellFrame(cellW, cellH int, borderColor color.Color) lipgloss.Style {
	// lipgloss Width is the total rendered width (border + padding included), so
	// the content area is cellW - gridCellFrameW; renderGrid sizes the card body
	// to match. Floor at a width that still leaves room for the frame.
	if cellW < gridCellFrameW+1 {
		cellW = gridCellFrameW + 1
	}
	if cellH < 1 {
		cellH = 1
	}
	return lipgloss.NewStyle().
		Border(gridCellLeftBar, false, false, false, true).
		BorderForeground(borderColor).
		PaddingLeft(1).
		Width(cellW).
		Height(cellH)
}

// renderEmptyCell renders a dim empty placeholder cell with the given dimensions.
// cellW and cellH are the outer cell dimensions (including the left-bar frame).
func renderEmptyCell(cellW, cellH int, focused bool) string {
	borderColor := color.Color(ColorBorder)
	if focused {
		borderColor = ColorPrimary
	}
	return gridCellFrame(cellW, cellH, borderColor).Render(DimStyle.Italic(true).Render("empty"))
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

	// Collect sorted runtime sessions for display (narrowed by any filter).
	sortedRT := m.filteredGridSessions()
	pageOffset := m.grid.gridPage * cellsPerPage

	for i := range cellsPerPage {
		absIdxPos := pageOffset + i
		focused := i == m.grid.gridFocusCell

		innerH := cellH
		innerW := cellW - gridCellFrameW
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

	totalPages := m.gridTotalPages(cellsPerPage)
	var hotkeyBar string
	switch {
	case m.grid.confirmKill:
		hotkeyBar = ModalWarningStyle.Render("  ⚠ Kill this worker?  [Enter] confirm   [Esc] cancel")
	case m.grid.filterActive:
		matches := len(m.filteredGridSessions())
		hotkeyBar = DimStyle.Render(fmt.Sprintf(
			"  filter: %s_   ·   %d match(es)   ·   enter: apply   ·   esc: clear",
			m.grid.filterQuery, matches,
		))
	default:
		bar := fmt.Sprintf(
			"  arrows: navigate   ·   enter: view output   ·   p: view prompt   ·   x: kill   ·   /: filter   ·   [/]: page %d/%d   ·   ctrl+g / esc: close",
			m.grid.gridPage+1, totalPages,
		)
		if m.grid.filterQuery != "" {
			bar += fmt.Sprintf("   ·   filter: %s", m.grid.filterQuery)
		}
		hotkeyBar = DimStyle.Render(bar)
	}
	hotkeyBar = lipgloss.NewStyle().Width(m.width).Render(hotkeyBar)

	rowStrings := make([]string, rows)
	for r := range rows {
		rowCells := cells[r*cols : (r+1)*cols]
		rowStrings[r] = lipgloss.JoinHorizontal(lipgloss.Top, rowCells...)
	}
	gridBody := lipgloss.JoinVertical(lipgloss.Left, rowStrings...)
	return lipgloss.JoinVertical(lipgloss.Left, hotkeyBar, gridBody)
}

// gridCellBorderColor picks the left-bar color for a cell: the theme accent for
// active sessions (bright primary when focused), and dim/border tones for
// finished ones — errors and cancellations read red. Uses the shared palette so
// the grid tracks the rest of the TUI's theme instead of hardcoded hues.
func gridCellBorderColor(rs *runtimeSlot, focused bool) color.Color {
	switch {
	case rs.status == "active":
		if focused {
			return ColorPrimary
		}
		return ColorAccent
	case rs.status == "failed" || rs.status == "cancelled":
		return ColorError
	case focused:
		return ColorPrimary
	default:
		return ColorBorder
	}
}

// renderRuntimeGridCell renders a single runtime session into a grid cell using
// the same ▌ left-bar card style the main chat viewport uses for worker streams,
// so the grid and the chat read as one visual language.
func (m *Model) renderRuntimeGridCell(rs *runtimeSlot, cellW, cellH, innerW, innerH int, focused bool) string {
	frame := gridCellFrame(cellW, cellH, gridCellBorderColor(rs, focused))
	ctxMax := m.modelContext[rs.model]
	return frame.Render(renderWorkerCard(rs, innerW, innerH, ctxMax, focused, m.spinnerFrame))
}

// renderWorkerCard renders the inner content of a worker card (without the
// left-bar frame). The layout, top to bottom:
//
//	🍞 build data models · a1b2c3d · 1m24s   ← headline (task, else role) + job/elapsed
//	  lmstudio/gemma-4-26b · 4.2k↑ 1.1k↓      ← model + tokens/cost (dim)
//	  ████████░░░░░░░ 34%                      ← live context-window bar
//	  ⚙ write_file (main.go)                   ← recent activity, newest first
//	  ⚙ shell (go test ./...)
//
// innerW and innerH are the available content dimensions; ctxMax is the model's
// context length (0 if unknown). Sections drop out from the bottom up as height
// shrinks so a short cell still shows the headline.
func renderWorkerCard(rs *runtimeSlot, innerW, innerH, ctxMax int, focused bool, spinnerFrame int) string {
	if innerW < 1 {
		innerW = 1
	}
	if innerH < 1 {
		innerH = 1
	}

	active := rs.status == "active"

	// --- Headline: icon + task/role, with job id · elapsed right-aligned ---
	end := time.Now()
	if !rs.endTime.IsZero() {
		end = rs.endTime
	}
	elapsed := end.Sub(rs.startTime).Round(time.Second)

	icon, iconStyle := gridCellIcon(rs.status)
	headline := rs.task
	if headline == "" {
		headline = gridWorkerLabel(rs)
	}

	shortJobID := rs.jobID
	if len(shortJobID) > 8 {
		shortJobID = shortJobID[:8]
	}
	right := shortJobID
	if right != "" {
		right += " · "
	}
	right += elapsed.String()
	rightStyled := DimStyle.Render(right)

	// Fit the headline into whatever the right-hand meta leaves, keeping at least
	// a few chars. The prefix (icon + space) is measured with lipgloss.Width so a
	// double-width glyph doesn't shove the text past the column.
	prefix := icon + " "
	headlineMax := innerW - lipgloss.Width(prefix) - lipgloss.Width(rightStyled) - 1
	if headlineMax < 6 {
		// Too tight to co-locate — drop the right-hand meta and give the
		// headline the whole line.
		rightStyled = ""
		headlineMax = innerW - lipgloss.Width(prefix)
	}
	if headlineMax < 1 {
		headlineMax = 1
	}
	headStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	if !active {
		headStyle = DimStyle
	}
	left := iconStyle.Render(icon) + " " + headStyle.Render(truncateStr(headline, headlineMax))
	headerLine := left
	if rightStyled != "" {
		gap := innerW - lipgloss.Width(left) - lipgloss.Width(rightStyled)
		if gap < 1 {
			gap = 1
		}
		headerLine = left + strings.Repeat(" ", gap) + rightStyled
	}

	lines := []string{headerLine}
	indent := "  "

	// --- Meta: model/provider · tokens · cost (dim) ---
	if innerH >= 2 {
		if meta := workerCardMeta(rs); meta != "" {
			lines = append(lines, DimStyle.Render(indent+truncateStr(meta, innerW-len(indent))))
		}
	}

	// --- Live context-window bar ---
	if innerH >= 3 {
		lines = append(lines, indent+renderMiniContextBar(int(rs.contextTokens), ctxMax, innerW-len(indent)))
	}

	// --- Activity items (newest first), styled like the chat's tool blocks ---
	activityH := innerH - len(lines)
	if activityH > 0 {
		lines = append(lines, gridActivityLines(rs, innerW, indent, activityH)...)
	}

	// Hard-clamp to innerH lines.
	if len(lines) > innerH {
		lines = lines[:innerH]
	}

	_ = spinnerFrame // reserved for future animated content in the card body
	_ = focused

	return strings.Join(lines, "\n")
}

// gridCellIcon returns the leading glyph and its style for a cell, keyed on the
// session status.
func gridCellIcon(status string) (string, lipgloss.Style) {
	switch status {
	case "active":
		return "🍞", lipgloss.NewStyle().Foreground(ColorAccent)
	case "failed", "cancelled":
		return "✗", lipgloss.NewStyle().Foreground(ColorError)
	default:
		return "✓", lipgloss.NewStyle().Foreground(ColorConnected)
	}
}

// gridWorkerLabel returns the team-scoped worker label used as a card headline
// when the session has no task text. Mirrors the loader's ID construction,
// avoiding a double "team/team/worker" prefix.
func gridWorkerLabel(rs *runtimeSlot) string {
	label := rs.workerName
	if label == "" {
		label = "runtime"
	}
	if rs.teamName != "" && !strings.HasPrefix(label, rs.teamName+"/") {
		label = rs.teamName + "/" + label
	}
	return label
}

// gridActivityLines renders up to maxLines of recent activity for a card,
// newest first, each prefixed with a dim gear so a tool call reads the same way
// it does in the chat's tool blocks. A running session with no activity yet
// shows a single dim placeholder.
func gridActivityLines(rs *runtimeSlot, innerW int, indent string, maxLines int) []string {
	if maxLines <= 0 {
		return nil
	}
	if len(rs.activities) == 0 {
		if rs.status == "active" {
			return []string{DimStyle.Italic(true).Render(indent + "waiting for activity…")}
		}
		return nil
	}
	gear := lipgloss.NewStyle().Foreground(ColorPrimary).Render("⚙")
	labelStyle := lipgloss.NewStyle().Foreground(ColorSecondary)
	if rs.status != "active" {
		labelStyle = DimStyle
	}
	maxLabelW := innerW - len(indent) - 2 // gear + space
	if maxLabelW < 1 {
		maxLabelW = 1
	}
	var out []string
	for i := len(rs.activities) - 1; i >= 0 && len(out) < maxLines; i-- {
		lbl := truncateStr(rs.activities[i].label, maxLabelW)
		out = append(out, indent+gear+" "+labelStyle.Render(lbl))
	}
	return out
}

// workerCardMeta builds the compact provider/model · tokens · cost line shown
// under a worker card header. Each segment is omitted when its value is
// zero/empty, so a freshly-started worker (no snapshot yet) renders nothing.
func workerCardMeta(rs *runtimeSlot) string {
	var segs []string
	switch {
	case rs.model != "" && rs.provider != "":
		segs = append(segs, rs.provider+"/"+rs.model)
	case rs.model != "":
		segs = append(segs, rs.model)
	case rs.provider != "":
		segs = append(segs, rs.provider)
	}
	if rs.hasTemp {
		seg := fmt.Sprintf("t%.1f", rs.temperature)
		if rs.thinking {
			seg += " 🧠"
		}
		segs = append(segs, seg)
	}
	if rs.tokensIn > 0 || rs.tokensOut > 0 {
		segs = append(segs, formatTokenCount(rs.tokensIn)+"↑ "+formatTokenCount(rs.tokensOut)+"↓")
	}
	if rs.costUSD > 0 {
		segs = append(segs, fmt.Sprintf("~$%.2f", rs.costUSD))
	}
	return strings.Join(segs, " · ")
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
// suitable for display in a runtime worker card's activity list.
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
		return "write: " + path.Base(str("path"))
	case "edit_file":
		return "edit: " + path.Base(str("path"))
	case "read_file":
		return "read: " + path.Base(str("path"))
	case "shell":
		return "shell: " + trunc(str("command"), 28)
	case "spawn_worker":
		name := str("role")
		if name == "" {
			name = "worker"
		}
		return "spawn: " + name
	case "report_progress", "report_task_progress":
		msg := str("message")
		if msg == "" {
			return "progress: (no message)"
		}
		return "progress: " + trunc(msg, 28)
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
	case "complete_task":
		summary := str("summary")
		if summary == "" {
			return "task: completed"
		}
		return "task: " + trunc(summary, 28)
	case "request_new_task":
		desc := str("description")
		if desc == "" {
			return "request: new task"
		}
		return "request: " + trunc(desc, 28)
	default:
		// MCP-namespaced tools: "server__tool_name" → "server: tool_name"
		if parts := strings.SplitN(toolName, "__", 2); len(parts) == 2 {
			return trunc(parts[0]+": "+parts[1], 35)
		}
		return trunc(toolName, 35)
	}
}
