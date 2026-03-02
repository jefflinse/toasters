// Agents modal: agent management UI including rendering, key handling, and CRUD operations.
package tui

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// agentsModalState holds all state for the /agents modal overlay.
type agentsModalState struct {
	show          bool
	agents        []service.Agent // local copy for the modal
	agentIdx      int             // selected agent in left panel
	focus         int             // 0=left panel, 1=right panel
	nameInput     string          // text being typed for new agent name
	inputMode     bool            // true when typing a new agent name
	confirmDelete bool            // true when delete confirmation is showing

	// Skill picker sub-modal state.
	pickerMode   bool            // true when the skill picker overlay is active
	pickerSkills []service.Skill // skills available to add (filtered: not already on agent)
	pickerIdx    int             // currently highlighted picker item

	// LLM generation state.
	generateMode  bool   // true when user is typing a generation prompt
	generateInput string // the prompt text being typed
	generating    bool   // true while LLM call is in flight
}

// updateAgentsModal handles all key presses when the agents modal is open.
func (m *Model) updateAgentsModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// When the skill picker is active, intercept all keys for picker navigation.
	if m.agentsModal.pickerMode {
		switch msg.String() {
		case "esc":
			m.agentsModal.pickerMode = false
			m.agentsModal.pickerSkills = nil
			m.agentsModal.pickerIdx = 0
		case "up":
			if m.agentsModal.pickerIdx > 0 {
				m.agentsModal.pickerIdx--
			}
		case "down":
			if m.agentsModal.pickerIdx < len(m.agentsModal.pickerSkills)-1 {
				m.agentsModal.pickerIdx++
			}
		case "enter":
			if len(m.agentsModal.pickerSkills) > 0 && m.agentsModal.pickerIdx < len(m.agentsModal.pickerSkills) {
				skill := m.agentsModal.pickerSkills[m.agentsModal.pickerIdx]
				a := m.agentsModal.agents[m.agentsModal.agentIdx]
				if cmd := m.addSkillToAgent(a, skill); cmd != nil {
					m.agentsModal.pickerMode = false
					m.agentsModal.pickerSkills = nil
					m.agentsModal.pickerIdx = 0
					return m, cmd
				}
				m.agentsModal.pickerMode = false
				m.agentsModal.pickerSkills = nil
				m.agentsModal.pickerIdx = 0
			}
		}
		return m, nil
	}

	// When typing a generation prompt, intercept all keys.
	if m.agentsModal.generateMode {
		switch msg.String() {
		case "esc":
			m.agentsModal.generateMode = false
			m.agentsModal.generateInput = ""
		case "enter":
			if strings.TrimSpace(m.agentsModal.generateInput) != "" {
				m.agentsModal.generating = true
				m.agentsModal.generateMode = false
				prompt := m.agentsModal.generateInput
				m.agentsModal.generateInput = ""
				return m, m.generateAgentAsync(prompt)
			}
		case "backspace":
			if len(m.agentsModal.generateInput) > 0 {
				runes := []rune(m.agentsModal.generateInput)
				m.agentsModal.generateInput = string(runes[:len(runes)-1])
			}
		default:
			if msg.Text != "" {
				m.agentsModal.generateInput += msg.Text
			}
		}
		return m, nil
	}

	// When typing a new agent name, only esc/enter/backspace have special meaning.
	if m.agentsModal.inputMode {
		switch msg.String() {
		case "esc":
			m.agentsModal.inputMode = false
			m.agentsModal.nameInput = ""
		case "enter":
			name := strings.TrimSpace(m.agentsModal.nameInput)
			valid := name != "" && !strings.ContainsAny(name, "/\\.\n\r:")
			if valid {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, err := m.svc.Definitions().CreateAgent(ctx, name)
				cancel()
				if err != nil {
					slog.Error("failed to create agent", "name", name, "error", err)
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

	case "ctrl+g":
		if !m.agentsModal.inputMode && !m.agentsModal.generating && !m.agentsModal.pickerMode {
			m.agentsModal.generateMode = true
			m.agentsModal.generateInput = ""
		}

	case "ctrl+a":
		// Open the skill picker for the selected agent (non-system agents only).
		if len(m.agentsModal.agents) > 0 && m.agentsModal.agentIdx < len(m.agentsModal.agents) {
			a := m.agentsModal.agents[m.agentsModal.agentIdx]
			if a.Source != "system" {
				return m, m.openSkillPicker(a)
			}
		}

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
				if a.Source == "user" && a.TeamID == "" {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					err := m.svc.Definitions().DeleteAgent(ctx, a.ID)
					cancel()
					if err != nil {
						slog.Error("failed to delete agent", "id", a.ID, "error", err)
					}
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

// generateAgentAsync calls the service to generate an agent asynchronously.
// The service pushes an operation.completed event when done; the event consumer
// handles it and sets generating = false.
func (m *Model) generateAgentAsync(prompt string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, err := m.svc.Definitions().GenerateAgent(ctx, prompt)
		if err != nil {
			return agentGeneratedMsg{err: err}
		}
		return nil
	}
}

// filterSkillsForAgent returns skills from available that are not already
// assigned to the agent. Comparison is by skill name (case-sensitive).
func filterSkillsForAgent(a service.Agent, available []service.Skill) []service.Skill {
	existingSet := make(map[string]bool, len(a.Skills))
	for _, s := range a.Skills {
		existingSet[s] = true
	}
	var result []service.Skill
	for _, sk := range available {
		if !existingSet[sk.Name] {
			result = append(result, sk)
		}
	}
	return result
}

// openSkillPicker loads all skills from the service, filters out those already on the
// agent, and either shows a toast (if none available) or activates picker mode.
func (m *Model) openSkillPicker(a service.Agent) tea.Cmd {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	allSkills, err := m.svc.Definitions().ListSkills(ctx)
	if err != nil {
		slog.Error("failed to list skills for picker", "error", err)
		return nil
	}

	available := filterSkillsForAgent(a, allSkills)
	if len(available) == 0 {
		return m.addToast("No additional skills available", toastInfo)
	}

	m.agentsModal.pickerMode = true
	m.agentsModal.pickerSkills = available
	m.agentsModal.pickerIdx = 0
	return nil
}

// addSkillToAgent appends the given skill to the agent via the service and reloads.
// Returns a tea.Cmd (toast) on success or failure, or nil if nothing to do.
func (m *Model) addSkillToAgent(a service.Agent, skill service.Skill) tea.Cmd {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := m.svc.Definitions().AddSkillToAgent(ctx, a.ID, skill.Name); err != nil {
		slog.Error("failed to add skill to agent", "agent", a.Name, "skill", skill.Name, "error", err)
		return m.addToast("Cannot add skill: "+err.Error(), toastWarning)
	}

	m.reloadAgentsForModal()
	return m.addToast("Added skill '"+skill.Name+"' to agent", toastSuccess)
}

// reloadAgentsForModal refreshes m.agentsModal.agents from the service, ordered as:
//  1. Shared (non-team) agents, alphabetically by name
//  2. Team-local agents, alphabetically by "team/agent"
//  3. System agents, alphabetically by name
func (m *Model) reloadAgentsForModal() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	agents, err := m.svc.Definitions().ListAgents(ctx)
	if err != nil {
		slog.Error("failed to list agents for modal", "error", err)
		return
	}
	var shared, teamLocal, system []service.Agent
	for _, a := range agents {
		switch {
		case a.Source == "system":
			system = append(system, a)
		case a.TeamID != "":
			teamLocal = append(teamLocal, a)
		default:
			shared = append(shared, a)
		}
	}
	// Each group arrives pre-sorted by name from the service (ORDER BY name).
	// Team-local needs sorting by the composite "team/agent" key.
	slices.SortFunc(teamLocal, func(a, b service.Agent) int {
		ka := a.TeamID + "/" + a.Name
		kb := b.TeamID + "/" + b.Name
		return strings.Compare(ka, kb)
	})
	m.agentsModal.agents = append(append(shared, teamLocal...), system...)
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

	// Size the left column to fit the longest display name, with a min of 20
	// and a max of 40% of the available width so the detail panel always gets
	// meaningful space.
	leftInnerW := 20
	for _, a := range agents {
		displayName := a.Name
		if a.TeamID != "" {
			displayName = a.TeamID + "/" + a.Name
		}
		// +3 for icon + space + trailing badge room
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

	// Generate mode: show generation prompt input at the bottom.
	if m.agentsModal.generateMode {
		leftLines = append(leftLines, "")
		leftLines = append(leftLines, DimStyle.Render("> Describe the agent:"))
		cursor := m.agentsModal.generateInput + "█"
		leftLines = append(leftLines, "  "+cursor)
	}

	// Generating: show status indicator at the bottom.
	if m.agentsModal.generating {
		leftLines = append(leftLines, "")
		leftLines = append(leftLines, DimStyle.Render("⟳ Generating agent..."))
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

	// --- Right panel: agent detail or skill picker ---
	var rightLines []string
	if m.agentsModal.pickerMode {
		// Skill picker overlay: replace right panel content with the picker list.
		rightLines = append(rightLines, HeaderStyle.Render("Select a skill to add:"))
		rightLines = append(rightLines, DimStyle.Render(strings.Repeat("─", rightInnerW)))
		rightLines = append(rightLines, "")

		for i, sk := range m.agentsModal.pickerSkills {
			icon := "◇"
			if sk.Source == "system" {
				icon = "⚙"
			}
			line := fmt.Sprintf(" %s %s", icon, truncateStr(sk.Name, rightInnerW-4))
			if sk.Description != "" {
				line += DimStyle.Render("  " + truncateStr(sk.Description, rightInnerW-len(sk.Name)-6))
			}
			if i == m.agentsModal.pickerIdx {
				line = ModalSelectedStyle.Width(rightInnerW).Render(line)
			}
			rightLines = append(rightLines, line)
		}
	} else if len(agents) == 0 {
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
			rightLines = append(rightLines, "")
			rightLines = append(rightLines, fmt.Sprintf("Skills (%d)", len(a.Skills)))
			for _, s := range a.Skills {
				rightLines = append(rightLines, "  · "+truncateStr(s, rightInnerW-4))
			}
		}

		// Tools.
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

	// Footer with key hints — dim edit/delete/add-skill keys when not applicable.
	canEdit := len(agents) > 0 && m.agentsModal.agentIdx < len(agents) && agents[m.agentsModal.agentIdx].Source != "system"
	canDelete := canEdit && agents[m.agentsModal.agentIdx].TeamID == ""
	canAddSkill := canEdit && !m.agentsModal.pickerMode
	nHint := "[Ctrl+N] New"
	dHint := "[Ctrl+D] Delete"
	eHint := "[e] Edit"
	aHint := "[Ctrl+A] Add Skill"
	gHint := "[Ctrl+G] Generate"
	if !canEdit {
		eHint = DimStyle.Render(eHint)
	}
	if !canDelete {
		dHint = DimStyle.Render(dHint)
	}
	if !canAddSkill {
		aHint = DimStyle.Render(aHint)
	}
	if m.agentsModal.generating {
		gHint = DimStyle.Render(gHint)
	}
	var footer string
	if m.agentsModal.pickerMode {
		footer = lipgloss.JoinHorizontal(lipgloss.Left,
			"[Enter] Add", "  ",
			DimStyle.Render("[Esc] Cancel"),
		)
	} else {
		footer = lipgloss.JoinHorizontal(lipgloss.Left,
			nHint, "  ", eHint, "  ", dHint, "  ", aHint, "  ", gHint, "  ",
			DimStyle.Render("[Tab] Switch"), "  ",
			DimStyle.Render("[Esc] Close"),
		)
	}

	inner := lipgloss.JoinVertical(lipgloss.Left, panels, footer)
	modal := ModalStyle.Width(modalW).Render(inner)

	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}
