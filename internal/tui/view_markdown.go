package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	glamourstyles "github.com/charmbracelet/glamour/styles"
)

// isNumberedListItem returns true if line starts with a markdown numbered list
// marker: one or more digits immediately followed by ". " (e.g. "1. ", "12. ").
// This avoids false positives on sentences that merely start with a digit.
func isNumberedListItem(line string) bool {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(line) && line[i] == '.' && line[i+1] == ' '
}

// looksLikeMarkdown returns true if the content appears to contain markdown
// formatting. The check is intentionally broad — false positives (plain text
// rendered as markdown) are acceptable in the fullscreen output modal.
func looksLikeMarkdown(s string) bool {
	if strings.Contains(s, "```") {
		return true
	}
	for _, line := range strings.SplitN(s, "\n", 200) {
		switch {
		case strings.HasPrefix(line, "#"):
			return true
		case strings.HasPrefix(line, "**") || strings.HasPrefix(line, "__"):
			return true
		case strings.HasPrefix(line, "- [") || strings.HasPrefix(line, "* ["):
			return true
		case strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* "):
			return true
		case strings.HasPrefix(line, "> "): // require space after > to avoid matching git diff / log output
			return true
		case isNumberedListItem(line):
			return true
		case strings.Count(line, "|") >= 2: // markdown table needs at least two columns
			return true
		}
	}
	return false
}

// indentLines prepends each line of s with n spaces.
func indentLines(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Join(lines, "\n")
}

// renderMarkdown renders markdown content to styled terminal output.
func (m *Model) renderMarkdown(content string) string {
	if m.mdRender == nil {
		return content
	}
	rendered, err := m.mdRender.Render(content)
	if err != nil {
		return content
	}
	// glamour adds trailing newlines; trim them so we control spacing.
	return strings.TrimRight(rendered, "\n")
}

// toastersStyle returns a Glamour style config based on Dracula with
// code block colors adjusted to match the toasters dark palette.
func toastersStyle() ansi.StyleConfig {
	s := glamourstyles.DraculaStyleConfig

	// Tighten document margin — the chat area already provides padding.
	zero := uint(0)
	s.Document.Margin = &zero

	// Darken code block background to blend with the toasters dark chrome.
	bg := "#1e1e2e"
	s.CodeBlock.Chroma.Background = ansi.StylePrimitive{
		BackgroundColor: &bg,
	}

	return s
}

// ensureMarkdownRenderer creates or recreates the glamour renderer for the current width.
// It also recreates outputMdRender sized for the node detail pane.
func (m *Model) ensureMarkdownRenderer() {
	w := m.chatViewport.Width() - AssistantMsgIndent
	if w < 1 {
		w = 80
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(toastersStyle()),
		glamour.WithWordWrap(w),
	)
	if err == nil {
		m.mdRender = r
	}

	// Detail-pane renderer: sized for the nodes screen's right pane inner width
	// so markdown wraps within the pane instead of at full screen width (which
	// would then be hard-truncated, losing the right edge of every long line).
	lay := nodesLayoutFor(m.width, m.height)
	outputW := lay.detailW - 4 // rounded border (2) + padding (2)
	if outputW < 20 {
		outputW = 20
	}
	or, oerr := glamour.NewTermRenderer(
		glamour.WithStyles(toastersStyle()),
		glamour.WithWordWrap(outputW),
	)
	if oerr == nil {
		m.outputMdRender = or
	}
}
