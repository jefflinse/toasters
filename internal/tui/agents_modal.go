// Agents modal: agent management UI including rendering, key handling, and CRUD operations.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/db"
)

// agentsModalState holds all state for the /agents modal overlay.
type agentsModalState struct {
	show          bool
	agents        []*db.Agent // local copy for the modal
	agentIdx      int         // selected agent in left panel
	focus         int         // 0=left panel, 1=right panel
	nameInput     string      // text being typed for new agent name
	inputMode     bool        // true when typing a new agent name
	confirmDelete bool        // true when delete confirmation is showing
}

// updateAgentsModal handles all key presses when the agents modal is open.
func (m *Model) updateAgentsModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// When typing a new agent name, only esc/enter/backspace have special meaning.
	if m.agentsModal.inputMode {
		switch msg.String() {
		case "esc":
			m.agentsModal.inputMode = false
			m.agentsModal.nameInput = ""
		case "enter":
			name := strings.TrimSpace(m.agentsModal.nameInput)
			valid := name != "" && !strings.ContainsAny(name, `/\.`)
			if valid {
				if err := createAgentFile(name); err != nil {
					slog.Error("failed to create agent file", "name", name, "error", err)
				} else {
					m.reloadAgentsForModal()
					for i, a := range m.agentsModal.agents {
						if a.Name == name {
							m.agentsModal.agentIdx = i
							break
						}
					}
				}
			}
			m.agentsModal.inputMode = false
			m.agentsModal.nameInput = ""
		case "backspace":
			if len(m.agentsModal.nameInput) > 0 {
				runes := []rune(m.agentsModal.nameInput)
				m.agentsModal.nameInput = string(runes[:len(runes)-1])
			}
		default:
			if msg.Text != "" {
				m.agentsModal.nameInput += msg.Text
			}
		}
		return m, nil
	}

	switch msg.String() {
	case "esc":
		if m.agentsModal.confirmDelete {
			m.agentsModal.confirmDelete = false
		} else {
			m.agentsModal.show = false
		}

	case "tab":
		if m.agentsModal.focus == 0 {
			m.agentsModal.focus = 1
		} else {
			m.agentsModal.focus = 0
		}

	case "up":
		if m.agentsModal.focus == 0 {
			if m.agentsModal.agentIdx > 0 {
				m.agentsModal.agentIdx--
			}
			m.agentsModal.confirmDelete = false
		}

	case "down":
		if m.agentsModal.focus == 0 {
			if m.agentsModal.agentIdx < len(m.agentsModal.agents)-1 {
				m.agentsModal.agentIdx++
			}
			m.agentsModal.confirmDelete = false
		}

	case "ctrl+n":
		m.agentsModal.inputMode = true
		m.agentsModal.nameInput = ""

	case "ctrl+d":
		if !m.agentsModal.confirmDelete && len(m.agentsModal.agents) > 0 && m.agentsModal.agentIdx < len(m.agentsModal.agents) {
			a := m.agentsModal.agents[m.agentsModal.agentIdx]
			// Only allow deleting user shared agents (not system or team-local).
			if a.Source == "user" && a.TeamID == "" {
				m.agentsModal.confirmDelete = true
			}
		}

	case "enter":
		if m.agentsModal.confirmDelete {
			if len(m.agentsModal.agents) > 0 && m.agentsModal.agentIdx < len(m.agentsModal.agents) {
				a := m.agentsModal.agents[m.agentsModal.agentIdx]
				if a.SourcePath != "" && a.Source == "user" && a.TeamID == "" {
					_ = os.Remove(a.SourcePath)
				}
			}
			m.reloadAgentsForModal()
			if m.agentsModal.agentIdx >= len(m.agentsModal.agents) && len(m.agentsModal.agents) > 0 {
				m.agentsModal.agentIdx = len(m.agentsModal.agents) - 1
			} else if len(m.agentsModal.agents) == 0 {
				m.agentsModal.agentIdx = 0
			}
			m.agentsModal.confirmDelete = false
		}

	case "e":
		if m.agentsModal.focus == 0 && len(m.agentsModal.agents) > 0 && m.agentsModal.agentIdx < len(m.agentsModal.agents) {
			a := m.agentsModal.agents[m.agentsModal.agentIdx]
			// Only allow editing user agents with a source path.
			if a.SourcePath != "" && a.Source != "system" {
				return m, openInEditor(a.SourcePath)
			}
		}
	}
	return m, nil
}

// reloadAgentsForModal refreshes m.agentsModal.agents from the DB.
func (m *Model) reloadAgentsForModal() {
	if m.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	agents, err := m.store.ListAgents(ctx)
	if err != nil {
		slog.Error("failed to list agents for modal", "error", err)
		return
	}
	m.agentsModal.agents = agents
}

// createAgentFile writes a template .md file for a new shared agent.
func createAgentFile(name string) error {
	cfgDir, err := config.Dir()
	if err != nil {
		return fmt.Errorf("getting config dir: %w", err)
	}
	agentsDir := filepath.Join(cfgDir, "user", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return fmt.Errorf("creating agents dir: %w", err)
	}

	// Sanitize name for filename: lowercase, replace spaces with hyphens.
	filename := strings.ToLower(strings.ReplaceAll(name, " ", "-")) + ".md"
	path := filepath.Join(agentsDir, filename)

	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("agent file %q already exists", filename)
	}

	template := fmt.Sprintf(`---
name: %s
description: A brief description of what this agent does
mode: worker
skills: []
---

Your agent system prompt goes here.
`, name)

	return os.WriteFile(path, []byte(template), 0o644)
}

// renderAgentsModal renders the full-screen agents management modal.
func (m *Model) renderAgentsModal() string {
	agents := m.agentsModal.agents

	// Modal dimensions: use most of the terminal (same pattern as teams modal).
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

	leftInnerW := 30
	leftPanelW := leftInnerW + ModalPanelStyle.GetHorizontalFrameSize()
	if leftPanelW > innerW/2 {
		leftPanelW = innerW / 2
		leftInnerW = leftPanelW - ModalPanelStyle.GetHorizontalFrameSize()
	}

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

	// --- Left panel: agent list ---
	var leftLines []string
	leftLines = append(leftLines, gradientText("Agents", [3]uint8{50, 200, 100}, [3]uint8{0, 150, 200}))
	leftLines = append(leftLines, "")

	if len(agents) == 0 {
		leftLines = append(leftLines, DimStyle.Render("No agents configured"))
		leftLines = append(leftLines, DimStyle.Render("Press [Ctrl+N] to create one"))
	} else {
		for i, a := range agents {
			icon := "■"
			if a.Mode == "lead" {
				icon = "◆"
			}
			name := truncateStr(a.Name, leftInnerW-8)
			line := fmt.Sprintf(" %s %s", icon, name)
			if a.Source == "system" {
				line += " ⚙"
			}
			if a.TeamID != "" {
				line += " ↻" // team-local indicator
			}
			if a.Source == "system" {
				line += " 🔒"
			}
			if i == m.agentsModal.agentIdx {
				line = ModalSelectedStyle.Width(leftInnerW).Render(line)
			} else if a.Source == "system" {
				line = ModalReadOnlyStyle.Render(line)
			}
			leftLines = append(leftLines, line)
		}
	}

	// Input mode: show name-entry prompt at the bottom.
	if m.agentsModal.inputMode {
		leftLines = append(leftLines, "")
		leftLines = append(leftLines, DimStyle.Render("> New agent name:"))
		cursor := m.agentsModal.nameInput + "█"
		leftLines = append(leftLines, "  "+cursor)
	}

	for len(leftLines) < panelInnerH {
		leftLines = append(leftLines, "")
	}
	if len(leftLines) > panelInnerH {
		leftLines = leftLines[:panelInnerH]
	}

	leftContent := strings.Join(leftLines, "\n")
	var leftPanel string
	if m.agentsModal.focus == 0 {
		leftPanel = ModalFocusedPanel.Width(leftPanelW).Height(panelH).Render(leftContent)
	} else {
		leftPanel = ModalPanelStyle.Width(leftPanelW).Height(panelH).Render(leftContent)
	}

	// --- Right panel: agent detail ---
	var rightLines []string
	if len(agents) == 0 {
		rightLines = append(rightLines, DimStyle.Render("No agents configured."))
		rightLines = append(rightLines, DimStyle.Render("Press [Ctrl+N] to create one."))
	} else if m.agentsModal.agentIdx < len(agents) {
		a := agents[m.agentsModal.agentIdx]

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

		// Mode.
		if a.Mode != "" {
			rightLines = append(rightLines, "Mode: "+a.Mode)
		}

		// Source info.
		rightLines = append(rightLines, DimStyle.Render("Source: "+a.Source))
		if a.TeamID != "" {
			rightLines = append(rightLines, DimStyle.Render("Team: "+a.TeamID))
		}
		if a.SourcePath != "" {
			rightLines = append(rightLines, DimStyle.Render("Path: "+truncateStr(a.SourcePath, rightInnerW-6)))
		}

		// Provider/Model.
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

		// Skills.
		if len(a.Skills) > 0 {
			var skillNames []string
			_ = json.Unmarshal(a.Skills, &skillNames)
			if len(skillNames) > 0 {
				rightLines = append(rightLines, "")
				rightLines = append(rightLines, fmt.Sprintf("Skills (%d)", len(skillNames)))
				for _, s := range skillNames {
					rightLines = append(rightLines, "  · "+truncateStr(s, rightInnerW-4))
				}
			}
		}

		// Tools.
		if len(a.Tools) > 0 {
			var toolNames []string
			_ = json.Unmarshal(a.Tools, &toolNames)
			if len(toolNames) > 0 {
				rightLines = append(rightLines, "")
				rightLines = append(rightLines, fmt.Sprintf("Tools (%d)", len(toolNames)))
				maxTools := 8
				for i, t := range toolNames {
					if i >= maxTools {
						rightLines = append(rightLines, DimStyle.Render(fmt.Sprintf("  ... and %d more", len(toolNames)-maxTools)))
						break
					}
					rightLines = append(rightLines, "  · "+truncateStr(t, rightInnerW-4))
				}
			}
		}

		// Delete confirmation.
		if m.agentsModal.confirmDelete {
			rightLines = append(rightLines, "")
			rightLines = append(rightLines, ModalWarningStyle.Render(
				fmt.Sprintf("⚠ Delete '%s'? [Enter] confirm  [Esc] cancel", truncateStr(a.Name, rightInnerW-30)),
			))
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
	if m.agentsModal.focus == 1 {
		rightPanel = ModalFocusedPanel.Width(rightPanelW).Height(panelH).Render(rightContent)
	} else {
		rightPanel = ModalPanelStyle.Width(rightPanelW).Height(panelH).Render(rightContent)
	}

	panels := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, " ", rightPanel)

	// Footer with key hints — dim edit/delete keys when agent is read-only.
	canEdit := len(agents) > 0 && m.agentsModal.agentIdx < len(agents) && agents[m.agentsModal.agentIdx].Source != "system"
	canDelete := canEdit && agents[m.agentsModal.agentIdx].TeamID == ""
	nHint := "[Ctrl+N] New"
	dHint := "[Ctrl+D] Delete"
	eHint := "[e] Edit"
	if !canEdit {
		eHint = DimStyle.Render(eHint)
	}
	if !canDelete {
		dHint = DimStyle.Render(dHint)
	}
	footer := lipgloss.JoinHorizontal(lipgloss.Left,
		nHint, "  ", eHint, "  ", dHint, "  ",
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
