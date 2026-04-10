// Operator modal: provider picker for the operator.
package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// operatorModalState holds state for the /operator modal.
type operatorModalState struct {
	show        bool
	providerIDs []string // configured provider IDs
	selectedIdx int
	loading     bool
	err         error
}

// OperatorConfiguredMsg is sent when the configured providers are loaded.
type OperatorConfiguredMsg struct {
	ProviderIDs []string
	Err         error
}

// OperatorProviderSetMsg is sent when the operator provider has been saved.
type OperatorProviderSetMsg struct {
	ProviderID string
	Err        error
}

// fetchConfiguredProviders loads the list of configured provider IDs.
func (m Model) fetchConfiguredProviders() tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		ids, err := svc.System().ListConfiguredProviderIDs(context.Background())
		return OperatorConfiguredMsg{ProviderIDs: ids, Err: err}
	}
}

// setOperatorProvider saves the selected provider as the operator's provider.
func (m Model) setOperatorProvider(providerID string) tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		err := svc.System().SetOperatorProvider(context.Background(), providerID)
		return OperatorProviderSetMsg{ProviderID: providerID, Err: err}
	}
}

// updateOperatorModal handles key presses when the operator modal is open.
func (m *Model) updateOperatorModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.operatorModal.show = false

	case "up":
		if m.operatorModal.selectedIdx > 0 {
			m.operatorModal.selectedIdx--
		}

	case "down":
		if m.operatorModal.selectedIdx < len(m.operatorModal.providerIDs)-1 {
			m.operatorModal.selectedIdx++
		}

	case "enter":
		if len(m.operatorModal.providerIDs) > 0 && m.operatorModal.selectedIdx < len(m.operatorModal.providerIDs) {
			id := m.operatorModal.providerIDs[m.operatorModal.selectedIdx]
			return m, m.setOperatorProvider(id)
		}
	}
	return m, nil
}

// renderOperatorModal renders the operator provider picker.
func (m *Model) renderOperatorModal() string {
	modalW := m.width - 4
	if modalW > 60 {
		modalW = 60
	}
	if modalW > m.width {
		modalW = m.width
	}

	innerW := modalW - ModalStyle.GetHorizontalFrameSize()
	if innerW < 10 {
		innerW = 10
	}

	var lines []string
	lines = append(lines, gradientText("Select Operator Provider", [3]uint8{100, 150, 255}, [3]uint8{50, 200, 255}))
	lines = append(lines, DimStyle.Render(strings.Repeat("─", innerW)))
	lines = append(lines, "")

	if m.operatorModal.loading {
		lines = append(lines, DimStyle.Render("Loading providers..."))
	} else if m.operatorModal.err != nil {
		lines = append(lines, ErrorStyle.Render("Error: "+m.operatorModal.err.Error()))
	} else if len(m.operatorModal.providerIDs) == 0 {
		lines = append(lines, DimStyle.Render("No providers configured."))
		lines = append(lines, "")
		lines = append(lines, DimStyle.Render("Use /providers to add one first."))
	} else {
		lines = append(lines, DimStyle.Render("Choose which provider the operator should use:"))
		lines = append(lines, "")
		for i, id := range m.operatorModal.providerIDs {
			line := fmt.Sprintf("  %s", id)
			if i == m.operatorModal.selectedIdx {
				line = ModalSelectedStyle.Width(innerW).Render(line)
			}
			lines = append(lines, line)
		}
	}

	lines = append(lines, "")

	var footer string
	if len(m.operatorModal.providerIDs) > 0 {
		footer = lipgloss.JoinHorizontal(lipgloss.Left,
			DimStyle.Render("[↑↓] Navigate"), "  ",
			DimStyle.Render("[Enter] Select"), "  ",
			DimStyle.Render("[Esc] Cancel"),
		)
	} else {
		footer = DimStyle.Render("[Esc] Close")
	}
	lines = append(lines, footer)

	content := strings.Join(lines, "\n")
	modal := ModalStyle.Width(modalW).Render(content)

	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}
