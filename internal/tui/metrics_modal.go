// Metrics modal: /metrics overlay listing per-node execution stats (runs,
// failure rate, elapsed time) and per-worker session stats (duration, token
// usage, context occupancy). Read-only, single fetch on open — no live
// updates, mirroring the /settings and /mcp modals' fetch-then-render shape.
package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// metricsModalState holds state for the /metrics modal.
type metricsModalState struct {
	show    bool
	loading bool
	err     error
	report  service.MetricsReport // snapshot taken when the fetch completes
	scroll  int                   // first visible line index
}

// MetricsLoadedMsg delivers the aggregate metrics snapshot to the modal.
type MetricsLoadedMsg struct {
	Report service.MetricsReport
	Err    error
}

// fetchMetrics loads the current aggregate metrics from the service.
func (m Model) fetchMetrics() tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		report, err := svc.System().Metrics(context.Background())
		return MetricsLoadedMsg{Report: report, Err: err}
	}
}

// updateMetricsModal handles key presses while the metrics modal is open.
func (m *Model) updateMetricsModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.metricsModal.show = false
	case "up":
		if m.metricsModal.scroll > 0 {
			m.metricsModal.scroll--
		}
	case "down":
		m.metricsModal.scroll++
	}
	return m, nil
}

// renderMetricsModal renders the /metrics overlay: a scrollable list with a
// per-node section followed by a per-worker-session section.
func (m *Model) renderMetricsModal() string {
	modalW := m.width - 4
	if modalW > 100 {
		modalW = 100
	}
	if modalW > m.width {
		modalW = m.width
	}
	if modalW < 20 {
		modalW = m.width
	}
	innerW := modalW - ModalStyle.GetHorizontalFrameSize()
	if innerW < 20 {
		innerW = 20
	}

	modalH := m.height - 4
	if modalH < 15 {
		modalH = 15
	}
	if modalH > m.height {
		modalH = m.height
	}

	var body []string
	switch {
	case m.metricsModal.loading:
		body = append(body, DimStyle.Render("Loading..."))
	case m.metricsModal.err != nil:
		body = append(body, ErrorStyle.Render("Error: "+m.metricsModal.err.Error()))
	default:
		body = append(body, metricsNodeLines(m.metricsModal.report.Nodes, innerW)...)
		body = append(body, "")
		body = append(body, metricsSessionLines(m.metricsModal.report.Sessions, innerW)...)
	}

	// Header + footer are fixed; the body scrolls within the remaining
	// height budget.
	footer := lipgloss.JoinHorizontal(lipgloss.Left,
		DimStyle.Render("[↑↓] Scroll"), "  ",
		DimStyle.Render("[Esc] Close"),
	)
	fixedLines := 3                                                      // header + divider + blank
	bodyH := modalH - ModalStyle.GetVerticalFrameSize() - fixedLines - 1 /* footer */
	if bodyH < 3 {
		bodyH = 3
	}

	scroll := m.metricsModal.scroll
	if scroll > len(body)-1 {
		scroll = len(body) - 1
	}
	if scroll < 0 {
		scroll = 0
	}
	end := scroll + bodyH
	if end > len(body) {
		end = len(body)
	}
	visible := body[scroll:end]

	var lines []string
	lines = append(lines, gradientText("Metrics", [3]uint8{100, 200, 150}, [3]uint8{50, 150, 255}))
	lines = append(lines, DimStyle.Render(strings.Repeat("─", innerW)))
	lines = append(lines, "")
	lines = append(lines, visible...)
	for len(lines) < fixedLines+bodyH {
		lines = append(lines, "")
	}
	lines = append(lines, "")
	lines = append(lines, footer)

	content := strings.Join(lines, "\n")
	modal := ModalStyle.Width(modalW).Render(content)

	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}

// metricsNodeLines renders the per-node execution stats section: one row
// per graph node with run count, failure rate, and elapsed-time stats.
func metricsNodeLines(nodes []service.NodeMetric, innerW int) []string {
	lines := []string{HeaderStyle.Render("Node Executions")}
	if len(nodes) == 0 {
		lines = append(lines, DimStyle.Render("  No node executions recorded yet."))
		return lines
	}
	header := fmt.Sprintf("  %-20s %6s %8s %10s %8s %8s", "Node", "Runs", "Fail %", "Avg ms", "Min ms", "Max ms")
	lines = append(lines, fitLine(DimStyle.Render(header), innerW))
	for _, n := range nodes {
		row := fmt.Sprintf("  %-20s %6d %7.0f%% %10.0f %8d %8d",
			truncateStr(n.Node, 20), n.Runs, n.FailureRate*100, n.AvgElapsedMS, n.MinElapsedMS, n.MaxElapsedMS)
		lines = append(lines, fitLine(row, innerW))
	}
	return lines
}

// metricsSessionLines renders the per-worker session stats section: one row
// per worker id with session/failure counts, average duration, average
// token usage (excluding usage-unavailable sessions), and average context
// occupancy.
func metricsSessionLines(sessions []service.SessionMetric, innerW int) []string {
	lines := []string{HeaderStyle.Render("Session Stats")}
	if len(sessions) == 0 {
		lines = append(lines, DimStyle.Render("  No worker sessions recorded yet."))
		return lines
	}
	header := fmt.Sprintf("  %-20s %5s %8s %8s %9s %9s %7s %6s",
		"Worker", "Runs", "Fail %", "Avg Dur", "Avg TokIn", "Avg TokOut", "Ctx %", "No Usg")
	lines = append(lines, fitLine(DimStyle.Render(header), innerW))
	for _, s := range sessions {
		ctxPct := "-"
		if s.AvgContextPercent > 0 {
			ctxPct = fmt.Sprintf("%.0f%%", s.AvgContextPercent*100)
		}
		row := fmt.Sprintf("  %-20s %5d %7.0f%% %7.0fs %9.0f %9.0f %7s %6d",
			truncateStr(s.WorkerID, 20), s.Sessions, s.FailureRate*100, s.AvgDurationSeconds,
			s.AvgTokensIn, s.AvgTokensOut, ctxPct, s.UsageUnavailable)
		lines = append(lines, fitLine(row, innerW))
	}
	return lines
}
