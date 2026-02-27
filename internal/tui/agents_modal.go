// Agents modal: agent management UI including rendering, key handling, and CRUD operations.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/agentfmt"
	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/loader"
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

	// Skill picker sub-modal state.
	pickerMode   bool        // true when the skill picker overlay is active
	pickerSkills []*db.Skill // skills available to add (filtered: not already on agent)
	pickerIdx    int         // currently highlighted picker item

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
				return m, generateAgentCmd(m.llmClient, prompt)
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

	case "ctrl+g":
		if !m.agentsModal.inputMode && !m.agentsModal.generating && !m.agentsModal.pickerMode {
			if m.llmClient == nil {
				return m, m.addToast("⚠ No LLM provider configured", toastWarning)
			}
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

// filterSkillsForAgent returns skills from available that are not already
// assigned to the agent. The agent's Skills field is a JSON array of skill
// names; malformed or absent JSON is treated as an empty list (all skills
// are returned). Comparison is by skill name (case-sensitive).
func filterSkillsForAgent(a *db.Agent, available []*db.Skill) []*db.Skill {
	var existing []string
	if len(a.Skills) > 0 {
		_ = json.Unmarshal(a.Skills, &existing)
	}
	existingSet := make(map[string]bool, len(existing))
	for _, s := range existing {
		existingSet[s] = true
	}
	var result []*db.Skill
	for _, sk := range available {
		if !existingSet[sk.Name] {
			result = append(result, sk)
		}
	}
	return result
}

// openSkillPicker loads all skills from the DB, filters out those already on the
// agent, and either shows a toast (if none available) or activates picker mode.
func (m *Model) openSkillPicker(a *db.Agent) tea.Cmd {
	if m.store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	allSkills, err := m.store.ListSkills(ctx)
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

// addSkillToAgent appends the given skill to the agent's .md file and reloads.
// Returns a tea.Cmd (toast) on success or failure, or nil if nothing to do.
func (m *Model) addSkillToAgent(a *db.Agent, skill *db.Skill) tea.Cmd {
	if a.SourcePath == "" {
		return m.addToast("Cannot add skill: agent source file unknown", toastWarning)
	}

	def, err := agentfmt.ParseAgent(a.SourcePath)
	if err != nil {
		slog.Error("failed to parse agent file for skill addition", "path", a.SourcePath, "error", err)
		return m.addToast("Cannot add skill: "+err.Error(), toastWarning)
	}

	def.Skills = append(def.Skills, skill.Name)

	if err := writeAgentFile(a.SourcePath, def); err != nil {
		slog.Error("failed to write agent file after skill addition", "path", a.SourcePath, "error", err)
		return m.addToast("Failed to save: "+err.Error(), toastWarning)
	}

	m.reloadAgentsForModal()
	return m.addToast("Added skill '"+skill.Name+"' to agent", toastSuccess)
}

// reloadAgentsForModal refreshes m.agentsModal.agents from the DB, ordered as:
//  1. Shared (non-team) agents, alphabetically by name
//  2. Team-local agents, alphabetically by "team/agent"
//  3. System agents, alphabetically by name
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
	var shared, teamLocal, system []*db.Agent
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
	// Each group arrives pre-sorted by name from the DB (ORDER BY name).
	// Team-local needs sorting by the composite "team/agent" key.
	slices.SortFunc(teamLocal, func(a, b *db.Agent) int {
		ka := a.TeamID + "/" + a.Name
		kb := b.TeamID + "/" + b.Name
		return strings.Compare(ka, kb)
	})
	m.agentsModal.agents = append(append(shared, teamLocal...), system...)
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

	// Sanitize name for filename using Slugify for consistent, safe filenames.
	filename := loader.Slugify(name) + ".md"
	if filename == ".md" {
		return fmt.Errorf("invalid agent name: produces empty filename")
	}
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

// writeGeneratedAgentFile writes LLM-generated agent content to the user agents
// directory. It derives the filename from the agent name in the content, and
// appends -2, -3, etc. if the file already exists. Returns the written path and
// the agent name extracted from the content.
func writeGeneratedAgentFile(content string) (string, string, error) {
	cfgDir, err := config.Dir()
	if err != nil {
		return "", "", fmt.Errorf("getting config dir: %w", err)
	}
	agentsDir := filepath.Join(cfgDir, "user", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return "", "", fmt.Errorf("creating agents dir: %w", err)
	}

	// Parse the content to extract the agent name for the filename.
	slug := "generated-agent"
	agentName := ""
	if parsed, err := agentfmt.ParseBytes([]byte(content), agentfmt.DefAgent); err == nil {
		if agentDef, ok := parsed.(*agentfmt.AgentDef); ok && agentDef.Name != "" {
			agentName = agentDef.Name
			s := loader.Slugify(agentDef.Name)
			if s != "" {
				slug = s
			}
		}
	}
	if agentName == "" {
		agentName = slug
	}

	// Find a free filename, appending -2, -3, etc. if needed.
	path := filepath.Join(agentsDir, slug+".md")
	if _, err := os.Stat(path); err == nil {
		for i := 2; ; i++ {
			candidate := filepath.Join(agentsDir, fmt.Sprintf("%s-%d.md", slug, i))
			if _, err := os.Stat(candidate); os.IsNotExist(err) {
				path = candidate
				break
			}
		}
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", "", fmt.Errorf("writing agent file: %w", err)
	}
	return path, agentName, nil
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
	if m.llmClient == nil || m.agentsModal.generating {
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
