// Log view: fullscreen tail of ~/.config/toasters/toasters.log.
// Toggle with ctrl+\; dismiss with ctrl+\ or esc.
// Scrollable via viewport (mouse wheel + keyboard); copy-paste via terminal selection.
package tui

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/config"
)

// logViewState holds all state for the fullscreen log tail view.
type logViewState struct {
	show     bool
	viewport logViewport // thin wrapper so we can size it independently
	logPath  string      // absolute path to toasters.log
	atBottom bool        // true when viewport is scrolled to the bottom (auto-tail)
}

// logViewport is a minimal scroll-state tracker for the log view.
// We use a raw string + scroll offset rather than bubbles/viewport so that
// the terminal can select and copy text freely (viewport.Model wraps content
// in a clipped region that can interfere with selection in some terminals).
// Scrolling is line-based; the viewport renders a window of lines.
type logViewport struct {
	lines      []string // all lines of the log file
	scrollTop  int      // index of the first visible line
	viewHeight int      // number of visible lines (set by resizeComponents)
	viewWidth  int      // width of the view area
}

// logTailTickMsg fires every 500ms while the log view is open to re-read the file.
type logTailTickMsg struct{}

// scheduleLogTail returns a command that fires logTailTickMsg after 500ms.
func scheduleLogTail() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
		return logTailTickMsg{}
	})
}

// logReadCmd reads the log file and returns a logContentMsg.
type logContentMsg struct {
	lines []string
}

// readLogCmd reads the log file at path and returns its lines as a logContentMsg.
func readLogCmd(path string) tea.Cmd {
	return func() tea.Msg {
		data, err := os.ReadFile(path)
		if err != nil {
			// File may not exist yet — return empty.
			return logContentMsg{lines: nil}
		}
		raw := string(data)
		// Split on newlines; drop trailing empty line from final newline.
		lines := strings.Split(raw, "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		return logContentMsg{lines: lines}
	}
}

// logPath returns the absolute path to the toasters log file.
func logFilePath() string {
	dir, err := config.Dir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "toasters.log")
}

// openLogView opens the log view, reads the current log content, and starts the tail poll.
func (m *Model) openLogView() tea.Cmd {
	if m.logView.logPath == "" {
		m.logView.logPath = logFilePath()
	}
	m.logView.show = true
	m.logView.atBottom = true
	m.resizeLogView()
	return tea.Batch(
		readLogCmd(m.logView.logPath),
		scheduleLogTail(),
	)
}

// closeLogView hides the log view.
func (m *Model) closeLogView() {
	m.logView.show = false
}

// resizeLogView updates the viewport dimensions from the current terminal size.
func (m *Model) resizeLogView() {
	const hotkeyBarH = 1
	m.logView.viewport.viewHeight = m.height - hotkeyBarH
	if m.logView.viewport.viewHeight < 1 {
		m.logView.viewport.viewHeight = 1
	}
	m.logView.viewport.viewWidth = m.width
	// Re-clamp scroll after resize.
	m.clampLogScroll()
}

// rewrapLogLines re-wraps the stored lines to the current viewport width.
// Called after a terminal resize so the line-count-based scroll stays correct.
func (m *Model) rewrapLogLines() {
	// We don't have the original raw lines after wrapping, so we join and re-split.
	// This is a best-effort re-wrap; minor artifacts at wrap boundaries are acceptable.
	if len(m.logView.viewport.lines) == 0 {
		return
	}
	raw := strings.Join(m.logView.viewport.lines, "\n")
	rawLines := strings.Split(raw, "\n")
	m.applyLogContent(rawLines)
}

// clampLogScroll ensures scrollTop is within valid bounds.
func (m *Model) clampLogScroll() {
	vp := &m.logView.viewport
	maxTop := len(vp.lines) - vp.viewHeight
	if maxTop < 0 {
		maxTop = 0
	}
	if vp.scrollTop > maxTop {
		vp.scrollTop = maxTop
	}
	if vp.scrollTop < 0 {
		vp.scrollTop = 0
	}
}

// scrollLogToBottom scrolls the log view to the last line.
func (m *Model) scrollLogToBottom() {
	vp := &m.logView.viewport
	maxTop := len(vp.lines) - vp.viewHeight
	if maxTop < 0 {
		maxTop = 0
	}
	vp.scrollTop = maxTop
	m.logView.atBottom = true
}

// updateLogView handles key events when the log view is visible.
func (m *Model) updateLogView(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	vp := &m.logView.viewport
	switch msg.String() {
	case `ctrl+\`, "esc":
		m.closeLogView()
		return m, nil

	case "up", "k":
		if vp.scrollTop > 0 {
			vp.scrollTop--
		}
		m.logView.atBottom = false

	case "down", "j":
		maxTop := len(vp.lines) - vp.viewHeight
		if maxTop < 0 {
			maxTop = 0
		}
		if vp.scrollTop < maxTop {
			vp.scrollTop++
		}
		m.logView.atBottom = (vp.scrollTop >= maxTop)

	case "pgup", "ctrl+u":
		half := vp.viewHeight / 2
		vp.scrollTop -= half
		if vp.scrollTop < 0 {
			vp.scrollTop = 0
		}
		m.logView.atBottom = false

	case "pgdown", "ctrl+d":
		half := vp.viewHeight / 2
		maxTop := len(vp.lines) - vp.viewHeight
		if maxTop < 0 {
			maxTop = 0
		}
		vp.scrollTop += half
		if vp.scrollTop > maxTop {
			vp.scrollTop = maxTop
		}
		m.logView.atBottom = (vp.scrollTop >= maxTop)

	case "home", "g":
		vp.scrollTop = 0
		m.logView.atBottom = false

	case "end", "G":
		m.scrollLogToBottom()
	}
	return m, nil
}

// handleLogTailTick re-reads the log file and reschedules the next tick.
// Only fires when the log view is open.
func (m *Model) handleLogTailTick() (tea.Model, tea.Cmd) {
	if !m.logView.show {
		// View was closed between ticks — don't reschedule.
		return m, nil
	}
	return m, tea.Batch(
		readLogCmd(m.logView.logPath),
		scheduleLogTail(),
	)
}

// applyLogContent updates the viewport lines and optionally auto-tails.
// Raw file lines are word-wrapped to wrapWidth so the scroll arithmetic
// (which is line-count-based) stays correct and no content is lost.
func (m *Model) applyLogContent(lines []string) {
	wrapWidth := m.logView.viewport.viewWidth
	if wrapWidth < 20 {
		wrapWidth = 80
	}
	wrapped := wrapLogLines(lines, wrapWidth)
	m.logView.viewport.lines = wrapped
	m.clampLogScroll()
	if m.logView.atBottom {
		m.scrollLogToBottom()
	}
}

// wrapLogLines expands each raw log line into one or more screen lines of at
// most width runes. This keeps the line-count-based scroll arithmetic correct
// while ensuring no content is truncated.
func wrapLogLines(lines []string, width int) []string {
	if width <= 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		runes := []rune(line)
		if len(runes) <= width {
			out = append(out, line)
			continue
		}
		// Break into chunks of width runes.
		for len(runes) > 0 {
			chunk := width
			if chunk > len(runes) {
				chunk = len(runes)
			}
			out = append(out, string(runes[:chunk]))
			runes = runes[chunk:]
		}
	}
	return out
}

// renderLogView renders the fullscreen log tail view.
func (m *Model) renderLogView() string {
	const hotkeyBarH = 1

	vp := &m.logView.viewport
	viewH := m.height - hotkeyBarH
	if viewH < 1 {
		viewH = 1
	}

	// Hotkey bar — same pattern as the grid view.
	scrollInfo := ""
	if len(vp.lines) > 0 {
		pct := 0
		if len(vp.lines) > vp.viewHeight {
			pct = vp.scrollTop * 100 / (len(vp.lines) - vp.viewHeight)
		} else {
			pct = 100
		}
		scrollInfo = DimStyle.Render(" · ") + DimStyle.Render(strings.Join([]string{
			"line ", itoa(vp.scrollTop + 1), "/", itoa(len(vp.lines)), " (", itoa(pct), "%)",
		}, ""))
	}

	hotkeyBar := lipgloss.NewStyle().Width(m.width).Render(
		DimStyle.Render("ctrl+\\: close") +
			DimStyle.Render(" · ") +
			DimStyle.Render("↑↓/jk: scroll") +
			DimStyle.Render(" · ") +
			DimStyle.Render("ctrl+u/d: half page") +
			DimStyle.Render(" · ") +
			DimStyle.Render("g/G: top/bottom") +
			scrollInfo,
	)

	// Title bar.
	title := HeaderStyle.Render("logs") + DimStyle.Render("  "+m.logView.logPath)
	titleBar := lipgloss.NewStyle().Width(m.width).Render(title)

	// Content area: slice the visible window of lines.
	contentH := viewH - 1 // subtract title bar
	if contentH < 1 {
		contentH = 1
	}

	var visibleLines []string
	if len(vp.lines) == 0 {
		visibleLines = []string{DimStyle.Render("(log file is empty or does not exist yet)")}
	} else {
		end := vp.scrollTop + contentH
		if end > len(vp.lines) {
			end = len(vp.lines)
		}
		visibleLines = vp.lines[vp.scrollTop:end]
	}

	// Pad to fill the content area so the layout is stable.
	for len(visibleLines) < contentH {
		visibleLines = append(visibleLines, "")
	}

	// Lines are already wrapped to terminal width by applyLogContent — render directly.
	content := strings.Join(visibleLines, "\n")

	return lipgloss.JoinVertical(lipgloss.Left,
		hotkeyBar,
		titleBar,
		content,
	)
}

// itoa is a tiny int-to-string helper to avoid importing strconv in this file.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
