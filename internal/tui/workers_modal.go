// Workers modal: read-only worker browser with list and detail panels.
package tui

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"context"

	"github.com/jefflinse/toasters/internal/service"
)

// workersModalState holds all state for the /workers modal overlay.
type workersModalState struct {
	show      bool
	workers   []service.Worker // local copy for the modal
	workerIdx int              // selected worker in left panel
	focus     int              // 0=left panel, 1=right panel
}

// updateWorkersModal handles all key presses when the workers modal is open.
func (m *Model) updateWorkersModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.workersModal.show = false

	case "tab":
		if m.workersModal.focus == 0 {
			m.workersModal.focus = 1
		} else {
			m.workersModal.focus = 0
		}

	case "up":
		if m.workersModal.focus == 0 {
			if m.workersModal.workerIdx > 0 {
				m.workersModal.workerIdx--
			}
		}

	case "down":
		if m.workersModal.focus == 0 {
			if m.workersModal.workerIdx < len(m.workersModal.workers)-1 {
				m.workersModal.workerIdx++
			}
		}

	case "e":
		if m.workersModal.focus == 0 && len(m.workersModal.workers) > 0 && m.workersModal.workerIdx < len(m.workersModal.workers) {
			a := m.workersModal.workers[m.workersModal.workerIdx]
			if a.SourcePath != "" && a.Source != "system" && m.openInEditor != nil {
				return m, m.openInEditor(a.SourcePath)
			}
		}
	}
	return m, nil
}

// reloadWorkersForModal refreshes m.workersModal.workers from the service, ordered as:
//  1. Shared (non-team) workers, alphabetically by name
//  2. Team-local workers, alphabetically by "team/worker"
//  3. System workers, alphabetically by name
func (m *Model) reloadWorkersForModal() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	workers, err := m.svc.Definitions().ListWorkers(ctx)
	if err != nil {
		slog.Error("failed to list workers for modal", "error", err)
		return
	}
	var shared, teamLocal, system []service.Worker
	for _, a := range workers {
		switch {
		case a.Source == "system":
			system = append(system, a)
		case a.TeamID != "":
			teamLocal = append(teamLocal, a)
		default:
			shared = append(shared, a)
		}
	}
	slices.SortFunc(teamLocal, func(a, b service.Worker) int {
		ka := a.TeamID + "/" + a.Name
		kb := b.TeamID + "/" + b.Name
		return strings.Compare(ka, kb)
	})
	m.workersModal.workers = append(append(shared, teamLocal...), system...)
}

// renderWorkersModal renders the full-screen workers browser modal.
func (m *Model) renderWorkersModal() string {
	workers := m.workersModal.workers

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

	innerW := modalW - ModalStyle.GetHorizontalFrameSize()
	if innerW < 10 {
		innerW = 10
	}

	leftInnerW := 20
	for _, a := range workers {
		displayName := a.Name
		if a.TeamID != "" {
			displayName = a.TeamID + "/" + a.Name
		}
		if w := len([]rune(displayName)) + 3; w > leftInnerW {
			leftInnerW = w
		}
	}
	maxLeftInnerW := innerW * 2 / 5
	if leftInnerW > maxLeftInnerW {
		leftInnerW = maxLeftInnerW
	}
	leftPanelW := leftInnerW + ModalPanelStyle.GetHorizontalFrameSize()

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

	// --- Left panel: worker list ---
	var leftLines []string
	leftLines = append(leftLines, gradientText("Workers", [3]uint8{50, 200, 100}, [3]uint8{0, 150, 200}))
	leftLines = append(leftLines, "")

	if len(workers) == 0 {
		leftLines = append(leftLines, DimStyle.Render("No workers configured"))
	} else {
		for i, a := range workers {
			icon := "■"
			if a.Mode == "lead" {
				icon = "◆"
			}
			displayName := a.Name
			if a.TeamID != "" {
				displayName = a.TeamID + "/" + a.Name
			}
			name := truncateStr(displayName, leftInnerW-3)
			line := fmt.Sprintf(" %s %s", icon, name)
			if a.Source == "system" {
				line += " ⚙"
			}
			if a.Source == "system" {
				line += " 🔒"
			}
			if i == m.workersModal.workerIdx {
				line = ModalSelectedStyle.Width(leftInnerW).Render(line)
			} else if a.Source == "system" {
				line = ModalReadOnlyStyle.Render(line)
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
	if m.workersModal.focus == 0 {
		leftPanel = ModalFocusedPanel.Width(leftPanelW).Height(panelH).Render(leftContent)
	} else {
		leftPanel = ModalPanelStyle.Width(leftPanelW).Height(panelH).Render(leftContent)
	}

	// --- Right panel: worker detail ---
	var rightLines []string
	if len(workers) == 0 {
		rightLines = append(rightLines, DimStyle.Render("No workers configured."))
	} else if m.workersModal.workerIdx < len(workers) {
		a := workers[m.workersModal.workerIdx]

		headerText := truncateStr(a.Name, rightInnerW-12)
		if a.Source == "system" {
			headerText += " " + DimStyle.Render("⚙ system")
		}
		rightLines = append(rightLines, HeaderStyle.Render(headerText))
		rightLines = append(rightLines, DimStyle.Render(strings.Repeat("─", rightInnerW)))

		if a.Description != "" {
			rightLines = append(rightLines, DimStyle.Render(truncateStr(a.Description, rightInnerW)))
		}

		rightLines = append(rightLines, "")

		if a.Mode != "" {
			rightLines = append(rightLines, "Mode: "+a.Mode)
		}

		rightLines = append(rightLines, DimStyle.Render("Source: "+a.Source))
		if a.TeamID != "" {
			rightLines = append(rightLines, DimStyle.Render("Team: "+a.TeamID))
		}
		if a.SourcePath != "" {
			rightLines = append(rightLines, DimStyle.Render("Path: "+truncateStr(a.SourcePath, rightInnerW-6)))
		}

		if a.Provider != "" || a.Model != "" {
			pmLine := ""
			if a.Provider != "" {
				pmLine = a.Provider
			}
			if a.Model != "" {
				if pmLine != "" {
					pmLine += "/"
				}
				pmLine += a.Model
			}
			rightLines = append(rightLines, DimStyle.Render("Provider: "+truncateStr(pmLine, rightInnerW-10)))
		}

		if len(a.Skills) > 0 {
			rightLines = append(rightLines, "")
			rightLines = append(rightLines, fmt.Sprintf("Skills (%d)", len(a.Skills)))
			for _, s := range a.Skills {
				rightLines = append(rightLines, "  · "+truncateStr(s, rightInnerW-4))
			}
		}

		if len(a.Tools) > 0 {
			rightLines = append(rightLines, "")
			rightLines = append(rightLines, fmt.Sprintf("Tools (%d)", len(a.Tools)))
			maxTools := 8
			for i, t := range a.Tools {
				if i >= maxTools {
					rightLines = append(rightLines, DimStyle.Render(fmt.Sprintf("  ... and %d more", len(a.Tools)-maxTools)))
					break
				}
				rightLines = append(rightLines, "  · "+truncateStr(t, rightInnerW-4))
			}
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
	if m.workersModal.focus == 1 {
		rightPanel = ModalFocusedPanel.Width(rightPanelW).Height(panelH).Render(rightContent)
	} else {
		rightPanel = ModalPanelStyle.Width(rightPanelW).Height(panelH).Render(rightContent)
	}

	panels := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, " ", rightPanel)

	eHint := "[e] Edit"
	canEdit := len(workers) > 0 && m.workersModal.workerIdx < len(workers) && workers[m.workersModal.workerIdx].Source != "system"
	if !canEdit {
		eHint = DimStyle.Render(eHint)
	}
	footer := lipgloss.JoinHorizontal(lipgloss.Left,
		eHint, "  ",
		DimStyle.Render("[Tab] Switch"), "  ",
		DimStyle.Render("[Esc] Close"),
	)

	inner := lipgloss.JoinVertical(lipgloss.Left, panels, footer)
	modal := ModalStyle.Width(modalW).Render(inner)

	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}
