package tui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"
	xansi "github.com/charmbracelet/x/ansi"
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
	return renderMarkdownWith(m.jobsPaneMdRender, content)
}

// renderMarkdownWith renders content through the given glamour renderer,
// trimming glamour's trailing newlines, and falls back to the raw input when
// the renderer is nil or errors so the caller always sees something. Callers
// pass their own correctly-sized renderer (jobs pane, cockpit, chat) rather
// than sharing one, which would thrash its word-wrap width.
func renderMarkdownWith(r *glamour.TermRenderer, content string) string {
	if r == nil {
		return content
	}
	out, err := r.Render(content)
	if err != nil {
		return content
	}
	return strings.TrimRight(out, "\n")
}

// renderOutputItems renders typed output items (streamed text runs + tool calls)
// into styled lines: text goes through the given glamour renderer, tool calls
// through renderToolBlock. Extracted so both the graph pane (jobs-pane renderer)
// and the cockpit (its own modal-width renderer) reuse the styling while each
// keeps a renderer sized for its own surface.
func renderOutputItems(items []outputItem, width int, r *glamour.TermRenderer) string {
	var sb strings.Builder
	for i := range items {
		it := &items[i]
		switch it.kind {
		case outputItemText:
			if it.text == "" {
				continue
			}
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(renderMarkdownWith(r, it.text))
		case outputItemTool:
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(renderToolBlock(it, width))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
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

	rendered := renderOutputItems(slot.items, width, m.jobsPaneMdRender)
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
	// Tool names use a distinct hue (ColorPrimary) rather than the bold cyan
	// ColorAccent of the card's task headline, so a tool-call line reads as its
	// own kind of element instead of blending into the header.
	nameStyle := lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	gear := lipgloss.NewStyle().Foreground(ColorPrimary).Render("⚙")

	header := gear + " " + nameStyle.Render(it.toolName)
	if argSummary := summarizeToolArgs(it.toolName, it.toolArgs); argSummary != "" {
		header += " " + DimStyle.Render("("+truncateMiddle(argSummary, width-len(it.toolName)-6)+")")
	}

	if it.endedAt.IsZero() {
		spinner := DimStyle.Italic(true).Render("running…")
		out := header + "\n  " + spinner
		if it.fileDiff != "" {
			out += renderFileDiffSection(it, width)
		}
		return out
	}

	var status string
	switch {
	case it.hasShellExec:
		// CoreTools.shell folds a nonzero exit into the result text with a
		// nil error (the LLM needs to see the output either way), so
		// it.toolError is always false for a failed command — the generic
		// ok/error line below would misreport it as "✓ ok". Only the
		// shell_exec side-channel knows the real outcome.
		status = renderShellExecStatusLine(it)
	case it.hasWorkerSpawn:
		status = renderWorkerSpawnStatusLine(it)
	case it.hasKBNote:
		status = renderKBNoteStatusLine(it)
	default:
		dur := it.endedAt.Sub(it.startedAt).Round(time.Millisecond)
		statusMark := "✓"
		statusColor := ColorConnected
		statusWord := "ok"
		if it.toolError {
			statusMark = "✗"
			statusColor = ColorError
			statusWord = "error"
		}
		status = lipgloss.NewStyle().Foreground(statusColor).Render(statusMark+" "+statusWord) +
			DimStyle.Render(" · "+dur.String())
	}

	out := header + "\n  " + status
	switch {
	case it.fileDiff != "":
		// A diff supersedes the raw result preview below — "wrote N bytes" is
		// redundant once the actual change is shown.
		out += renderFileDiffSection(it, width)
	case it.hasWorkerSpawn && it.spawnTask != "":
		// Second line: the task label the child was spawned for, dimmed —
		// same "supersedes the raw result preview" reasoning as the diff
		// case above, plus the model-visible result here is either the
		// child's own final text or a failure message, neither of which is
		// a useful preview of what this spawn_worker call was for.
		out += "\n  " + DimStyle.Render(truncateMiddle(it.spawnTask, max(width-2, 4)))
	case it.toolResult != "":
		preview := strings.SplitN(it.toolResult, "\n", 2)[0]
		preview = truncateMiddle(preview, width-4)
		arrowColor := ColorError
		if !it.toolError {
			arrowColor = ColorDim
		}
		out += "\n  " + lipgloss.NewStyle().Foreground(arrowColor).Render("→ "+preview)
	}
	return out
}

// renderFileDiffSection formats the diff summary line ("Added N lines,
// removed M lines" / "Created file (N lines)") plus the colorized diff body
// for a tool item carrying a file change. Returns a string starting with
// "\n" so callers can append it directly.
func renderFileDiffSection(it *outputItem, width int) string {
	var summary string
	switch {
	case it.diffCreated:
		summary = fmt.Sprintf("Created file (%d lines)", it.diffAdded)
	default:
		addWord, remWord := "lines", "lines"
		if it.diffAdded == 1 {
			addWord = "line"
		}
		if it.diffRemoved == 1 {
			remWord = "line"
		}
		summary = fmt.Sprintf("Added %d %s, removed %d %s", it.diffAdded, addWord, it.diffRemoved, remWord)
	}
	out := "\n  " + DimStyle.Render(summary)
	if body := renderDiffLines(it.fileDiff, width-2); body != "" {
		out += "\n" + indentLines(body, 2)
	}
	if it.diffTruncated {
		out += "\n  " + DimStyle.Render("… diff truncated")
	}
	return out
}

// renderShellExecStatusLine builds a shell tool item's status line from its
// exec metadata (exit code, duration, output size) instead of the generic
// toolError flag — see the caller's comment for why toolError can't be
// trusted here. Unlike renderFileDiffSection, this replaces the status
// line rather than appending a section: the spec calls for a single compact
// line, and stacking it under a misleading "✓ ok" would read as
// self-contradictory for a failed command.
func renderShellExecStatusLine(it *outputItem) string {
	if it.shellTimedOut {
		return lipgloss.NewStyle().Foreground(ColorError).Render("⏱ timed out")
	}
	mark, color := "✓", ColorConnected
	if it.shellExitCode != 0 {
		mark, color = "✗", ColorError
	}
	head := lipgloss.NewStyle().Foreground(color).Render(fmt.Sprintf("%s exit %d", mark, it.shellExitCode))
	rest := fmt.Sprintf(" · %s · %s", formatShellDuration(it.shellDurationMs), formatBytes(it.shellOutputBytes))
	if it.shellTruncated {
		rest += " · truncated"
	}
	return head + DimStyle.Render(rest)
}

// formatShellDuration renders a shell command's wall-clock time compactly:
// exact milliseconds under a second (small enough that a decimal would be
// noise), rounded to 100ms above it (matches the precision a human actually
// cares about for multi-second commands).
func formatShellDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d < time.Second {
		return d.String()
	}
	return d.Round(100 * time.Millisecond).String()
}

// formatBytes renders a byte count as a short human string (B/KB/MB) for the
// shell status line's output-size segment.
func formatBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// renderWorkerSpawnStatusLine builds a spawn_worker tool item's status line
// from its WorkerSpawn side-channel metadata. Like renderShellExecStatusLine,
// this replaces the generic ok/error line rather than appending a section:
// spawn_worker's model-visible result is the child's own final text
// (success) or an error string (failure), neither of which is the compact
// "what got spawned, where" summary the card needs.
func renderWorkerSpawnStatusLine(it *outputItem) string {
	if it.spawnFailed {
		msg := it.spawnError
		if msg == "" {
			msg = "unknown error"
		}
		return lipgloss.NewStyle().Foreground(ColorError).Render("✗ spawn failed") +
			DimStyle.Render(" · "+msg)
	}
	head := lipgloss.NewStyle().Foreground(ColorConnected).Render("⚡ spawned " + it.spawnRole)
	if id := shortID(it.spawnJobID); id != "" {
		head += DimStyle.Render(" · " + id)
	}
	return head
}

// renderKBNoteStatusLine builds a job_note_write/job_notes_search tool
// item's status line from its KBNote side-channel metadata, mirroring
// renderShellExecStatusLine. Unlike shell, CoreTools' job-note tools already
// return an accurate result/error via the generic toolError/toolResult
// path — the notifier only ever fires on success (see jobNoteWrite/
// jobNotesSearch) — so this isn't correcting a misreported outcome, just
// giving a more compact, icon-led summary than the raw tool result preview
// below it.
func renderKBNoteStatusLine(it *outputItem) string {
	icon := "📝"
	if it.kbOp == "search" {
		icon = "🔎"
	}
	return lipgloss.NewStyle().Foreground(ColorConnected).Render(icon + " " + it.kbPreview)
}

// shortID shortens a UUID-style identifier to its first 8 characters for
// compact inline display (e.g. the spawn card's job id), matching the
// truncation convention used elsewhere in the TUI for job/session ids.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// diffHunkHeaderRe matches a unified-diff hunk header, e.g. "@@ -5,3 +5,4 @@".
// The count after the comma is optional — "@@ -5 +5 @@" means a 1-line hunk.
var diffHunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// diffRenderLine is one parsed row of a unified diff body, ready to render:
// marker is one of ' ' (context), '+', '-', or '@' (hunk boundary — num/code
// unused). num is the line number in the file the marker's column belongs to
// (new-file number for context/added lines, old-file number for removed
// lines).
type diffRenderLine struct {
	marker byte
	num    int
	code   string
}

// parseDiffHunks turns a capped unified-diff body (hunk headers + ' '/'+'/'-'
// prefixed lines, no ---/+++ file headers — see SessionFileChangePayload) into
// renderable rows, tracking old/new line counters across hunks.
func parseDiffHunks(diff string) []diffRenderLine {
	var out []diffRenderLine
	var oldLine, newLine int
	for _, raw := range strings.Split(diff, "\n") {
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, "@@") {
			m := diffHunkHeaderRe.FindStringSubmatch(raw)
			if m == nil {
				continue
			}
			oldLine, _ = strconv.Atoi(m[1])
			newLine, _ = strconv.Atoi(m[3])
			out = append(out, diffRenderLine{marker: '@'})
			continue
		}
		if strings.HasPrefix(raw, `\`) {
			// go-udiff's "\ No newline at end of file" marker — not a real
			// hunk line. Must not render and must not advance either line
			// counter, or every line after it in the hunk gets a skewed
			// gutter number.
			continue
		}
		marker, code := raw[0], raw[1:]
		switch marker {
		case '+':
			out = append(out, diffRenderLine{marker: '+', num: newLine, code: code})
			newLine++
		case '-':
			out = append(out, diffRenderLine{marker: '-', num: oldLine, code: code})
			oldLine++
		default:
			// ' ' context, or an unrecognized prefix — treat as context so a
			// malformed line still renders instead of vanishing.
			out = append(out, diffRenderLine{marker: ' ', num: newLine, code: code})
			oldLine++
			newLine++
		}
	}
	return out
}

// renderDiffLines renders a capped unified-diff body as a Claude Code-style
// colorized diff: a right-aligned line-number gutter, a -/+/space marker,
// then the code — removed lines red-tinted, added lines green-tinted,
// context lines dim. Hunk headers become a dim "···" separator rather than
// printing the raw "@@ …@@" line. Each row is truncated to width (gutter +
// marker + code) so the caller's frame never has to re-wrap it.
func renderDiffLines(diff string, width int) string {
	if diff == "" || width < 6 {
		return ""
	}
	rows := parseDiffHunks(diff)
	if len(rows) == 0 {
		return ""
	}

	maxNum := 0
	for _, r := range rows {
		if r.marker != '@' && r.num > maxNum {
			maxNum = r.num
		}
	}
	gutterWidth := len(strconv.Itoa(maxNum))
	if gutterWidth < 3 {
		gutterWidth = 3
	}
	codeWidth := width - gutterWidth - 2 // gutter + space + marker
	if codeWidth < 4 {
		codeWidth = 4
	}

	addStyle := lipgloss.NewStyle().Background(ColorDiffAddBg).Foreground(ColorDiffAddFg)
	delStyle := lipgloss.NewStyle().Background(ColorDiffDelBg).Foreground(ColorDiffDelFg)

	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.marker == '@' {
			lines = append(lines, DimStyle.Render("···"))
			continue
		}
		gutter := DimStyle.Render(fmt.Sprintf("%*d", gutterWidth, r.num))
		code := truncate(sanitizeDiffCode(r.code), codeWidth)
		// Pad added/removed lines to the full code width so the background
		// tint fills the row to EOL instead of stopping at the last character.
		if pad := codeWidth - lipgloss.Width(code); pad > 0 {
			code += strings.Repeat(" ", pad)
		}
		switch r.marker {
		case '+':
			lines = append(lines, gutter+" "+addStyle.Render("+"+code))
		case '-':
			lines = append(lines, gutter+" "+delStyle.Render("-"+code))
		default:
			lines = append(lines, gutter+" "+DimStyle.Render(" "+code))
		}
	}
	return strings.Join(lines, "\n")
}

// sanitizeDiffCode strips ANSI/CSI escapes and C0 control characters from a
// diff code line before it's styled and rendered. The content originates
// from worker-written file bytes, not our own formatting, so a code line can
// carry embedded escape sequences (which would bleed into the terminal's
// styling state) or a bare "\r" left over from a CRLF file — go-udiff splits
// hunk lines on "\n" only, so the "\r" stays attached to the line. xansi.Strip
// removes recognized ANSI/CSI/OSC sequences but does not touch a bare "\r",
// so the C0 pass below is still required. Tabs expand to 4 spaces (rather
// than being dropped) so gutter alignment and width truncation downstream
// reflect what a tab-honoring editor would show.
func sanitizeDiffCode(s string) string {
	s = xansi.Strip(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("    ")
		case r < 0x20:
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
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
	case "spawn_worker":
		if name := str("role"); name != "" {
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

// truncateMiddle shortens s to at most max runes by eliding the middle with an
// ellipsis, preserving both ends. For paths this keeps the leading directories
// and the filename (and any trailing suffix like ", 3 lines") instead of
// dropping the tail, which is usually the most informative part.
func truncateMiddle(s string, max int) string {
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
	keep := max - 1 // one rune for the ellipsis
	head := keep / 2
	tail := keep - head // tail gets the extra rune when keep is odd
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}
