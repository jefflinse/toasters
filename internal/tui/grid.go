// Worker node cards: the rich left-bar card rendering shared by the nodes
// screen's list rows, plus the tool-activity and reasoning helpers.
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

// nodeStatusColor picks the status color for a node: the theme accent (green)
// for active sessions, red for errors/cancellations, and a dim tone for
// finished ones. Uses the shared palette so nodes track the rest of the theme.
func nodeStatusColor(rs *runtimeSlot) color.Color {
	switch {
	case rs.status == "active":
		return ColorConnected
	case rs.status == "failed" || rs.status == "cancelled":
		return ColorError
	default:
		return ColorBorder
	}
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
