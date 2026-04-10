// Operator modal: two-step provider + model picker for the operator.
package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// operatorModalState holds state for the /operator modal.
type operatorModalState struct {
	show        bool
	providerIDs []string // configured provider IDs
	selectedIdx int
	loading     bool
	err         error

	// Step 2: model selection.
	pickingModel   bool
	providerID     string // selected provider ID
	models         []service.ModelInfo
	modelIdx       int
	modelsLoading  bool
	modelsErr      error
}

// OperatorConfiguredMsg is sent when the configured providers are loaded.
type OperatorConfiguredMsg struct {
	ProviderIDs []string
	Err         error
}

// ProviderModelsMsg is sent when models for a provider have been fetched.
type ProviderModelsMsg struct {
	Models []service.ModelInfo
	Err    error
}

// OperatorProviderSetMsg is sent when the operator provider has been saved.
type OperatorProviderSetMsg struct {
	ProviderID string
	Model      string
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

// fetchProviderModels fetches available models from a specific provider.
func (m Model) fetchProviderModels(providerID string) tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		models, err := svc.System().ListProviderModels(context.Background(), providerID)
		return ProviderModelsMsg{Models: models, Err: err}
	}
}

// setOperatorProvider saves the selected provider and model as the operator's config.
func (m Model) setOperatorProvider(providerID, model string) tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		err := svc.System().SetOperatorProvider(context.Background(), providerID, model)
		return OperatorProviderSetMsg{ProviderID: providerID, Model: model, Err: err}
	}
}

// updateOperatorModal handles key presses when the operator modal is open.
func (m *Model) updateOperatorModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.operatorModal.pickingModel {
		return m.updateOperatorModelPicker(msg)
	}

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
			m.operatorModal.pickingModel = true
			m.operatorModal.providerID = id
			m.operatorModal.modelsLoading = true
			m.operatorModal.modelsErr = nil
			m.operatorModal.models = nil
			m.operatorModal.modelIdx = 0
			return m, m.fetchProviderModels(id)
		}
	}
	return m, nil
}

// updateOperatorModelPicker handles keys in the model selection sub-state.
func (m *Model) updateOperatorModelPicker(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// Back to provider list.
		m.operatorModal.pickingModel = false

	case "up":
		if m.operatorModal.modelIdx > 0 {
			m.operatorModal.modelIdx--
		}

	case "down":
		if m.operatorModal.modelIdx < len(m.operatorModal.models)-1 {
			m.operatorModal.modelIdx++
		}

	case "enter":
		if len(m.operatorModal.models) > 0 && m.operatorModal.modelIdx < len(m.operatorModal.models) {
			model := m.operatorModal.models[m.operatorModal.modelIdx]
			return m, m.setOperatorProvider(m.operatorModal.providerID, model.ID)
		}
	}
	return m, nil
}

// renderOperatorModal renders the operator provider/model picker.
func (m *Model) renderOperatorModal() string {
	if m.operatorModal.pickingModel {
		return m.renderOperatorModelPicker()
	}

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

// renderOperatorModelPicker renders the model selection step.
func (m *Model) renderOperatorModelPicker() string {
	modalW := m.width - 4
	if modalW > 70 {
		modalW = 70
	}
	if modalW > m.width {
		modalW = m.width
	}

	innerW := modalW - ModalStyle.GetHorizontalFrameSize()
	if innerW < 10 {
		innerW = 10
	}

	var lines []string
	lines = append(lines, gradientText("Select Model", [3]uint8{100, 150, 255}, [3]uint8{50, 200, 255}))
	lines = append(lines, DimStyle.Render("Provider: "+m.operatorModal.providerID))
	lines = append(lines, DimStyle.Render(strings.Repeat("─", innerW)))
	lines = append(lines, "")

	if m.operatorModal.modelsLoading {
		lines = append(lines, DimStyle.Render("Loading models..."))
	} else if m.operatorModal.modelsErr != nil {
		lines = append(lines, ErrorStyle.Render("Error: "+m.operatorModal.modelsErr.Error()))
		lines = append(lines, "")
		lines = append(lines, DimStyle.Render("The provider may not be reachable."))
	} else if len(m.operatorModal.models) == 0 {
		lines = append(lines, DimStyle.Render("No models available from this provider."))
	} else {
		// Scrollable model list.
		maxVisible := 15
		scrollOffset := 0
		if len(m.operatorModal.models) > maxVisible {
			scrollOffset = m.operatorModal.modelIdx - maxVisible/2
			if scrollOffset < 0 {
				scrollOffset = 0
			}
			if scrollOffset > len(m.operatorModal.models)-maxVisible {
				scrollOffset = len(m.operatorModal.models) - maxVisible
			}
		}
		end := scrollOffset + maxVisible
		if end > len(m.operatorModal.models) {
			end = len(m.operatorModal.models)
		}
		for vi, model := range m.operatorModal.models[scrollOffset:end] {
			i := vi + scrollOffset
			// Show model ID, state, and context length.
			ctxStr := ""
			cl := model.ContextLength()
			if cl > 0 {
				ctxStr = fmt.Sprintf(" %dk", cl/1000)
			}
			stateStr := ""
			if model.State == "loaded" {
				stateStr = " " + ConnectedStyle.Render("(loaded)")
			}

			line := fmt.Sprintf("  %s%s%s", truncateStr(model.ID, innerW-20), ctxStr, stateStr)
			if i == m.operatorModal.modelIdx {
				line = ModalSelectedStyle.Width(innerW).Render(line)
			}
			lines = append(lines, line)
		}
	}

	lines = append(lines, "")

	footer := lipgloss.JoinHorizontal(lipgloss.Left,
		DimStyle.Render("[↑↓] Navigate"), "  ",
		DimStyle.Render("[Enter] Confirm"), "  ",
		DimStyle.Render("[Esc] Back"),
	)
	lines = append(lines, footer)

	content := strings.Join(lines, "\n")
	modal := ModalStyle.Width(modalW).Render(content)

	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}
