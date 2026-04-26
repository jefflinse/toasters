package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"
)

// glamourDebounce caps how often the graph pane re-renders a streaming
// slot through glamour. Markdown rendering parses the whole accumulated
// text on every call; without a cap the per-token redraw cost would
// dominate during fast streaming. The visible effect is staircase
// updates roughly every 250ms, which reads as smooth typing in
// practice.
const glamourDebounce = 250 * time.Millisecond

// ensureJobsPaneMarkdownRenderer (re)creates m.jobsPaneMdRender if its
// current word-wrap width doesn't match the panel. The pane width
// changes with the layout, so the renderer is reissued lazily on the
// first render after a resize.
func (m *Model) ensureJobsPaneMarkdownRenderer(width int) {
	if width < 1 {
		width = 80
	}
	if m.jobsPaneMdRender != nil && m.jobsPaneMdRenderWidth == width {
		return
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(toastersStyle()),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return
	}
	m.jobsPaneMdRender = r
	m.jobsPaneMdRenderWidth = width
}

// renderJobsPaneMarkdown formats a string with the jobs-pane glamour
// renderer, falling back to the raw input on any error so the user
// always sees something.
func (m *Model) renderJobsPaneMarkdown(content string) string {
	if m.jobsPaneMdRender == nil {
		return content
	}
	out, err := m.jobsPaneMdRender.Render(content)
	if err != nil {
		return content
	}
	return strings.TrimRight(out, "\n")
}

// renderSlotOutputContent returns the styled, glamour-rendered content
// for a runtime slot's typed output items. Caches per (slot, width,
// contentVersion) and debounces re-renders during streaming so fast
// token deltas don't burn CPU.
func (m *Model) renderSlotOutputContent(slot *runtimeSlot, width int) string {
	if slot == nil || width <= 0 {
		return ""
	}

	// Cache hit when content version + width match.
	if slot.cachedRender != "" &&
		slot.cachedRenderWidth == width &&
		slot.cachedRenderVersion == slot.contentVersion {
		return slot.cachedRender
	}

	// While the slot is still streaming, debounce: if a render landed
	// recently, return the (slightly stale) cache instead of re-rendering.
	// Once the slot finishes, render once at the final state and pin it.
	terminal := slot.status != "active" && !slot.endTime.IsZero()
	if !terminal && slot.cachedRender != "" &&
		time.Since(slot.cachedRenderAt) < glamourDebounce {
		return slot.cachedRender
	}

	m.ensureJobsPaneMarkdownRenderer(width)

	var sb strings.Builder
	for i := range slot.items {
		it := &slot.items[i]
		switch it.kind {
		case outputItemText:
			text := it.text.String()
			if text == "" {
				continue
			}
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(m.renderJobsPaneMarkdown(text))
		case outputItemTool:
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(renderToolBlock(it, width))
		}
	}

	rendered := strings.TrimRight(sb.String(), "\n")
	slot.cachedRender = rendered
	slot.cachedRenderWidth = width
	slot.cachedRenderVersion = slot.contentVersion
	slot.cachedRenderAt = time.Now()
	slot.cachedRenderTerminal = terminal
	return rendered
}

// renderToolBlock returns a styled, two-or-three-line block describing
// a single tool invocation: header with the tool name and arg summary,
// status line with duration and ok/error, and an optional truncated
// preview of the result. Width is the available column count for
// truncation.
func renderToolBlock(it *outputItem, width int) string {
	nameStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	gear := lipgloss.NewStyle().Foreground(ColorAccent).Render("⚙")

	header := gear + " " + nameStyle.Render(it.toolName)
	if argSummary := summarizeToolArgs(it.toolName, it.toolArgs); argSummary != "" {
		header += " " + DimStyle.Render("("+truncate(argSummary, width-len(it.toolName)-6)+")")
	}

	if it.endedAt.IsZero() {
		spinner := DimStyle.Italic(true).Render("running…")
		return header + "\n  " + spinner
	}

	dur := it.endedAt.Sub(it.startedAt).Round(time.Millisecond)
	statusMark := "✓"
	statusColor := ColorConnected
	statusWord := "ok"
	if it.toolError {
		statusMark = "✗"
		statusColor = ColorError
		statusWord = "error"
	}
	status := lipgloss.NewStyle().Foreground(statusColor).Render(statusMark+" "+statusWord) +
		DimStyle.Render(" · "+dur.String())

	out := header + "\n  " + status
	if it.toolResult != "" {
		preview := strings.SplitN(it.toolResult, "\n", 2)[0]
		preview = truncate(preview, width-4)
		arrowColor := ColorError
		if !it.toolError {
			arrowColor = ColorDim
		}
		out += "\n  " + lipgloss.NewStyle().Foreground(arrowColor).Render("→ "+preview)
	}
	return out
}

// summarizeToolArgs returns the parenthesized arg portion shown next
// to the tool name. Common tools get a hand-rolled summary; unknown
// tools fall back to a sorted, truncated list of their JSON keys so
// the user at least sees what arguments were passed.
func summarizeToolArgs(toolName string, args json.RawMessage) string {
	var a map[string]any
	if len(args) > 0 {
		_ = json.Unmarshal(args, &a)
	}
	str := func(key string) string {
		v, _ := a[key].(string)
		return v
	}

	switch toolName {
	case "write_file":
		p := str("path")
		if content := str("content"); content != "" {
			lines := strings.Count(content, "\n") + 1
			return fmt.Sprintf("%s, %d lines", p, lines)
		}
		return p
	case "edit_file", "read_file":
		return str("path")
	case "shell":
		return str("command")
	case "spawn_agent":
		if name := str("agent_name"); name != "" {
			return name
		}
		return ""
	case "report_progress", "report_task_progress":
		return str("message")
	case "web_fetch":
		return str("url")
	case "glob", "grep":
		return str("pattern")
	case "log_artifact":
		return str("name")
	case "update_task_status":
		return str("status")
	case "complete_task":
		return str("summary")
	case "request_new_task":
		return str("description")
	}

	if parts := strings.SplitN(toolName, "__", 2); len(parts) == 2 && len(a) > 0 {
		return summarizeKeys(a)
	}
	if len(a) > 0 {
		// Single-arg tools commonly have one obvious value; surface it.
		if len(a) == 1 {
			for _, v := range a {
				if s, ok := v.(string); ok {
					return s
				}
			}
		}
		return summarizeKeys(a)
	}
	return ""
}

func summarizeKeys(a map[string]any) string {
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 3 {
		keys = append(keys[:3], "…")
	}
	return strings.Join(keys, ", ")
}

func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}
