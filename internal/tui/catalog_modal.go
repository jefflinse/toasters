// Catalog modal: models.dev provider and model browser UI.
package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// catalogModalState holds all state for the /models modal overlay.
type catalogModalState struct {
	show        bool
	providers   []service.CatalogProvider
	providerIdx int // selected provider in left panel
	modelIdx    int // selected model in right panel
	focus       int // 0=left panel (providers), 1=right panel (models)
	loading     bool
	err         error
}

// CatalogMsg is sent when the catalog data finishes loading.
type CatalogMsg struct {
	Providers []service.CatalogProvider
	Err       error
}

// fetchCatalog returns a command that fetches the models.dev catalog.
func (m Model) fetchCatalog() tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		providers, err := svc.System().ListCatalogProviders(context.Background())
		return CatalogMsg{Providers: providers, Err: err}
	}
}

// updateCatalogModal handles key presses when the catalog modal is open.
func (m *Model) updateCatalogModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.catalogModal.show = false

	case "tab":
		if m.catalogModal.focus == 0 {
			m.catalogModal.focus = 1
		} else {
			m.catalogModal.focus = 0
		}

	case "up":
		if m.catalogModal.focus == 0 {
			if m.catalogModal.providerIdx > 0 {
				m.catalogModal.providerIdx--
				m.catalogModal.modelIdx = 0
			}
		} else {
			if m.catalogModal.modelIdx > 0 {
				m.catalogModal.modelIdx--
			}
		}

	case "down":
		if m.catalogModal.focus == 0 {
			if m.catalogModal.providerIdx < len(m.catalogModal.providers)-1 {
				m.catalogModal.providerIdx++
				m.catalogModal.modelIdx = 0
			}
		} else {
			if len(m.catalogModal.providers) > 0 && m.catalogModal.providerIdx < len(m.catalogModal.providers) {
				provider := m.catalogModal.providers[m.catalogModal.providerIdx]
				if m.catalogModal.modelIdx < len(provider.Models)-1 {
					m.catalogModal.modelIdx++
				}
			}
		}
	}
	return m, nil
}

// renderCatalogModal renders the full-screen catalog browser modal.
func (m *Model) renderCatalogModal() string {
	providers := m.catalogModal.providers

	// Modal dimensions.
	modalW := m.width - 4
	if modalW < 60 {
		modalW = 60
	}
	if modalW > m.width {
		modalW = m.width
	}
	modalH := m.height - 4
	if modalH < 20 {
		modalH = 20
	}
	if modalH > m.height {
		modalH = m.height
	}

	innerW := modalW - ModalStyle.GetHorizontalFrameSize()
	if innerW < 10 {
		innerW = 10
	}

	// Left panel: provider list.
	leftInnerW := 32
	leftPanelW := leftInnerW + ModalPanelStyle.GetHorizontalFrameSize()
	if leftPanelW > innerW/2 {
		leftPanelW = innerW / 2
		leftInnerW = leftPanelW - ModalPanelStyle.GetHorizontalFrameSize()
	}

	// Right panel: model details.
	rightPanelW := innerW - leftPanelW - 1
	rightInnerW := rightPanelW - ModalPanelStyle.GetHorizontalFrameSize()
	if rightInnerW < 5 {
		rightInnerW = 5
	}

	footerLines := 1
	panelH := modalH - ModalStyle.GetVerticalFrameSize() - footerLines - 1
	if panelH < 5 {
		panelH = 5
	}
	panelInnerH := panelH - ModalPanelStyle.GetVerticalFrameSize()
	if panelInnerH < 3 {
		panelInnerH = 3
	}

	// --- Left panel: provider list ---
	var leftLines []string
	leftLines = append(leftLines, gradientText("Model Catalog", [3]uint8{255, 140, 50}, [3]uint8{255, 200, 80}))
	leftLines = append(leftLines, DimStyle.Render(fmt.Sprintf("%d providers", len(providers))))
	leftLines = append(leftLines, "")

	if m.catalogModal.loading {
		leftLines = append(leftLines, DimStyle.Render("Loading catalog..."))
	} else if m.catalogModal.err != nil {
		leftLines = append(leftLines, ErrorStyle.Render("Failed to load catalog"))
		leftLines = append(leftLines, DimStyle.Render(m.catalogModal.err.Error()))
	} else if len(providers) == 0 {
		leftLines = append(leftLines, DimStyle.Render("No providers available"))
	} else {
		// Compute scroll offset for providers.
		provAreaH := panelInnerH - len(leftLines)
		if provAreaH < 1 {
			provAreaH = 1
		}
		scrollOffset := 0
		if len(providers) > provAreaH {
			scrollOffset = m.catalogModal.providerIdx - provAreaH/2
			if scrollOffset < 0 {
				scrollOffset = 0
			}
			if scrollOffset > len(providers)-provAreaH {
				scrollOffset = len(providers) - provAreaH
			}
		}
		end := scrollOffset + provAreaH
		if end > len(providers) {
			end = len(providers)
		}
		for vi, p := range providers[scrollOffset:end] {
			i := vi + scrollOffset
			modelCount := len(p.Models)
			name := truncateStr(p.Name, leftInnerW-8)
			line := fmt.Sprintf(" %s (%d)", name, modelCount)
			if i == m.catalogModal.providerIdx {
				line = ModalSelectedStyle.Width(leftInnerW).Render(line)
			}
			leftLines = append(leftLines, line)
		}
	}

	for len(leftLines) < panelInnerH {
		leftLines = append(leftLines, "")
	}
	if len(leftLines) > panelInnerH {
		leftLines = leftLines[:panelInnerH]
	}

	leftContent := strings.Join(leftLines, "\n")
	var leftPanel string
	if m.catalogModal.focus == 0 {
		leftPanel = ModalFocusedPanel.Width(leftPanelW).Height(panelH).Render(leftContent)
	} else {
		leftPanel = ModalPanelStyle.Width(leftPanelW).Height(panelH).Render(leftContent)
	}

	// --- Right panel: models for selected provider ---
	var rightLines []string

	if len(providers) == 0 || m.catalogModal.providerIdx >= len(providers) {
		rightLines = append(rightLines, DimStyle.Render("Select a provider"))
	} else {
		provider := providers[m.catalogModal.providerIdx]

		// Provider header.
		rightLines = append(rightLines, HeaderStyle.Render(truncateStr(provider.Name, rightInnerW)))
		rightLines = append(rightLines, DimStyle.Render(strings.Repeat("─", rightInnerW)))

		if provider.API != "" {
			rightLines = append(rightLines, DimStyle.Render("API: "+truncateStr(provider.API, rightInnerW-5)))
		}
		if provider.Doc != "" {
			rightLines = append(rightLines, DimStyle.Render("Docs: "+truncateStr(provider.Doc, rightInnerW-6)))
		}
		if len(provider.Env) > 0 {
			rightLines = append(rightLines, DimStyle.Render("Auth: "+strings.Join(provider.Env, ", ")))
		}
		rightLines = append(rightLines, "")
		rightLines = append(rightLines, fmt.Sprintf("Models (%d)", len(provider.Models)))

		headerRows := len(rightLines)
		modelAreaH := panelInnerH - headerRows
		if modelAreaH < 1 {
			modelAreaH = 1
		}

		// Scroll offset for models.
		scrollOffset := 0
		if len(provider.Models) > modelAreaH {
			scrollOffset = m.catalogModal.modelIdx - modelAreaH/2
			if scrollOffset < 0 {
				scrollOffset = 0
			}
			if scrollOffset > len(provider.Models)-modelAreaH {
				scrollOffset = len(provider.Models) - modelAreaH
			}
		}
		end := scrollOffset + modelAreaH
		if end > len(provider.Models) {
			end = len(provider.Models)
		}
		for vi, model := range provider.Models[scrollOffset:end] {
			i := vi + scrollOffset

			// Build capability badges.
			var badges []string
			if model.ToolCall {
				badges = append(badges, "tools")
			}
			if model.Reasoning {
				badges = append(badges, "reason")
			}
			if model.StructuredOutput {
				badges = append(badges, "json")
			}
			if model.OpenWeights {
				badges = append(badges, "open")
			}

			badgeStr := ""
			if len(badges) > 0 {
				badgeStr = " [" + strings.Join(badges, ",") + "]"
			}

			// Context window.
			ctxStr := ""
			if model.ContextLimit > 0 {
				ctxStr = fmt.Sprintf(" %dk", model.ContextLimit/1000)
			}

			name := truncateStr(model.Name, rightInnerW-len(badgeStr)-len(ctxStr)-4)
			line := fmt.Sprintf("  %s%s%s", name, ctxStr, badgeStr)

			if m.catalogModal.focus == 1 && i == m.catalogModal.modelIdx {
				line = ModalSelectedStyle.Width(rightInnerW).Render(line)
			}
			rightLines = append(rightLines, line)
		}
	}

	for len(rightLines) < panelInnerH {
		rightLines = append(rightLines, "")
	}
	if len(rightLines) > panelInnerH {
		rightLines = rightLines[:panelInnerH]
	}

	rightContent := strings.Join(rightLines, "\n")
	var rightPanel string
	if m.catalogModal.focus == 1 {
		rightPanel = ModalFocusedPanel.Width(rightPanelW).Height(panelH).Render(rightContent)
	} else {
		rightPanel = ModalPanelStyle.Width(rightPanelW).Height(panelH).Render(rightContent)
	}

	panels := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, " ", rightPanel)

	footer := lipgloss.JoinHorizontal(lipgloss.Left,
		DimStyle.Render("[↑↓] Navigate"), "  ",
		DimStyle.Render("[Tab] Switch Panel"), "  ",
		DimStyle.Render("[Esc] Close"), "  ",
		DimStyle.Render("models.dev"),
	)

	inner := lipgloss.JoinVertical(lipgloss.Left, panels, footer)
	modal := ModalStyle.Width(modalW).Render(inner)

	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}
