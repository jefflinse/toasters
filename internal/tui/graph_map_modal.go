// Graph map modal: proof-of-concept viewer for dagmap renderers with fake
// data. Scaffolding — real data arrives once the GraphBuilder/topology-event
// plumbing lands.
package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/tui/dagmap"
)

type graphMapModalState struct {
	show bool
	view int // 0=list, 1=breadcrumb, 2=horizontal, 3=vertical
}

const (
	gmViewList = iota
	gmViewBreadcrumb
	gmViewHorizontal
	gmViewVertical
	gmViewCount
)

var gmViewNames = []string{"List", "Breadcrumb", "Horizontal", "Vertical"}

// fakeGraphState returns a mid-run BugFix fixture used when there is no
// live graph task to show.
func fakeGraphState() (dagmap.Topology, dagmap.NodeStates) {
	return dagmap.BugFix(), dagmap.NodeStates{
		"investigate": {Phase: dagmap.PhaseCompleted},
		"plan":        {Phase: dagmap.PhaseCompleted},
		"implement":   {Phase: dagmap.PhaseRunning, ExecCount: 2},
		"test":        {Phase: dagmap.PhaseCompleted, ExecCount: 1, LastStatus: "tests_failed"},
		"review":      {Phase: dagmap.PhasePending},
	}
}

// liveGraphState returns the currently-tracked graph task if one exists.
func (m *Model) liveGraphState() (dagmap.Topology, dagmap.NodeStates, string, bool) {
	gts := m.activeGraphTaskState()
	if gts == nil {
		return dagmap.Topology{}, nil, "", false
	}
	label := gts.jobType
	if label == "" {
		label = "bug_fix"
	}
	label = label + " · task " + truncateStr(gts.taskID, 8)
	return gts.topology, gts.nodes, label, true
}

func (m *Model) updateGraphMapModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.graphMapModal.show = false
	case "tab", "right", "l":
		m.graphMapModal.view = (m.graphMapModal.view + 1) % gmViewCount
	case "shift+tab", "left", "h":
		m.graphMapModal.view = (m.graphMapModal.view + gmViewCount - 1) % gmViewCount
	case "1":
		m.graphMapModal.view = gmViewList
	case "2":
		m.graphMapModal.view = gmViewBreadcrumb
	case "3":
		m.graphMapModal.view = gmViewHorizontal
	case "4":
		m.graphMapModal.view = gmViewVertical
	}
	return m, nil
}

func (m *Model) renderGraphMapModal() string {
	modalW := m.width - 4
	if modalW < 60 {
		modalW = 60
	}
	if modalW > m.width {
		modalW = m.width
	}
	modalH := m.height - 4
	if modalH < 14 {
		modalH = 14
	}
	innerW := modalW - ModalStyle.GetHorizontalFrameSize()
	if innerW < 20 {
		innerW = 20
	}

	topo, states, label, live := m.liveGraphState()
	if !live {
		topo, states = fakeGraphState()
		label = "BugFix (fake data)"
	}

	var body string
	switch m.graphMapModal.view {
	case gmViewList:
		body = dagmap.RenderList(topo, states)
	case gmViewBreadcrumb:
		body = dagmap.RenderBreadcrumb(topo, states)
	case gmViewHorizontal:
		body = dagmap.Render(topo, states)
	case gmViewVertical:
		body = dagmap.RenderVertical(topo, states)
	}

	header := gradientText("Graph Map · "+label,
		[3]uint8{0, 200, 200}, [3]uint8{175, 50, 200})

	var tabs []string
	for i, name := range gmViewNames {
		style := DimStyle
		if i == m.graphMapModal.view {
			style = lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
		}
		tabs = append(tabs, style.Render(name))
	}
	tabLine := strings.Join(tabs, "  ·  ")

	footer := DimStyle.Render("[Tab/←→] Switch view   [1-4] Jump   [Esc] Close")

	sep := DimStyle.Render(strings.Repeat("─", innerW))

	inner := lipgloss.JoinVertical(lipgloss.Left,
		header,
		tabLine,
		sep,
		"",
		body,
		"",
		sep,
		footer,
	)

	modal := ModalStyle.Width(modalW).Render(inner)
	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}
