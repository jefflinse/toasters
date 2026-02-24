// Teams modal: team management UI including rendering, key handling, and coordinator auto-detection.
package tui

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/llm"
)

// teamsModalState holds all state for the /teams modal overlay.
type teamsModalState struct {
	show              bool
	teams             []agents.Team   // local copy for the modal; separate from m.teams
	teamIdx           int             // selected team in left panel
	agentIdx          int             // selected agent in right panel (for 'c' key)
	focus             int             // 0=left panel, 1=right panel
	nameInput         string          // text being typed for new team name
	inputMode         bool            // true when typing a new team name
	confirmDelete     bool            // true when delete confirmation is showing
	autoDetectPending map[string]bool // keyed by team.Dir; prevents re-firing
	autoDetecting     bool            // true while LLM call is in flight
}

// updateTeamsModal handles all key presses when the teams modal is open.
func (m *Model) updateTeamsModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var modalCmds []tea.Cmd

	// When typing a new team name, only esc/enter/backspace have special
	// meaning. Everything else — including named keys like "space" — feeds
	// into the name input via msg.Text (which is the actual typed character
	// for all printable input, unlike msg.String() which returns key names).
	if m.teamsModal.inputMode {
		switch msg.String() {
		case "esc":
			m.teamsModal.inputMode = false
			m.teamsModal.nameInput = ""
		case "enter":
			name := m.teamsModal.nameInput
			valid := name != "" && !strings.ContainsAny(name, `/\.`)
			if valid {
				if err := os.MkdirAll(filepath.Join(m.teamsDir, name), 0755); err != nil {
					log.Printf("teams: failed to create directory %s: %v", name, err)
				}
				m.reloadTeamsForModal()
				for i, t := range m.teamsModal.teams {
					if t.Name == name {
						m.teamsModal.teamIdx = i
						break
					}
				}
			}
			m.teamsModal.inputMode = false
			m.teamsModal.nameInput = ""
		case "backspace":
			if len(m.teamsModal.nameInput) > 0 {
				runes := []rune(m.teamsModal.nameInput)
				m.teamsModal.nameInput = string(runes[:len(runes)-1])
			}
		default:
			// msg.Text is the actual typed character(s); empty for
			// non-printable keys (arrows, function keys, etc.).
			if msg.Text != "" {
				m.teamsModal.nameInput += msg.Text
			}
		}
		return m, tea.Batch(modalCmds...)
	}

	switch msg.String() {
	case "esc":
		if m.teamsModal.confirmDelete {
			m.teamsModal.confirmDelete = false
		} else {
			m.teamsModal.show = false
		}

	case "tab":
		if !m.teamsModal.inputMode {
			if m.teamsModal.focus == 0 {
				m.teamsModal.focus = 1
			} else {
				m.teamsModal.focus = 0
			}
		}

	case "up":
		if m.teamsModal.focus == 0 {
			if m.teamsModal.teamIdx > 0 {
				m.teamsModal.teamIdx--
			}
			m.teamsModal.confirmDelete = false
			m.teamsModal.agentIdx = 0
			if len(m.teamsModal.teams) > 0 {
				modalCmds = append(modalCmds, m.maybeAutoDetectCoordinator(m.teamsModal.teams[m.teamsModal.teamIdx]))
			}
		} else {
			// Right panel: navigate agents (coordinator first, then workers).
			if len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
				if m.teamsModal.agentIdx > 0 {
					m.teamsModal.agentIdx--
				}
			}
		}

	case "down":
		if m.teamsModal.focus == 0 {
			if m.teamsModal.teamIdx < len(m.teamsModal.teams)-1 {
				m.teamsModal.teamIdx++
			}
			m.teamsModal.confirmDelete = false
			m.teamsModal.agentIdx = 0
			if len(m.teamsModal.teams) > 0 {
				modalCmds = append(modalCmds, m.maybeAutoDetectCoordinator(m.teamsModal.teams[m.teamsModal.teamIdx]))
			}
		} else {
			// Right panel: navigate agents (coordinator first, then workers).
			if len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
				team := m.teamsModal.teams[m.teamsModal.teamIdx]
				total := len(team.Workers)
				if team.Coordinator != nil {
					total++
				}
				if m.teamsModal.agentIdx < total-1 {
					m.teamsModal.agentIdx++
				}
			}
		}

	case "ctrl+n":
		if m.teamsModal.focus == 0 {
			// Creating a new team is never gated on the selected team's
			// read-only status — you can always create a new user-defined team.
			m.teamsModal.inputMode = true
			m.teamsModal.nameInput = ""
		}

	case "ctrl+d":
		if m.teamsModal.focus == 0 && !m.teamsModal.confirmDelete {
			if len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
				if !isReadOnlyTeam(m.teamsModal.teams[m.teamsModal.teamIdx]) {
					m.teamsModal.confirmDelete = true
				}
			}
		}

	case "enter":
		if m.teamsModal.confirmDelete {
			if len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
				team := m.teamsModal.teams[m.teamsModal.teamIdx]
				_ = os.RemoveAll(team.Dir)
			}
			m.reloadTeamsForModal()
			if m.teamsModal.teamIdx >= len(m.teamsModal.teams) && len(m.teamsModal.teams) > 0 {
				m.teamsModal.teamIdx = len(m.teamsModal.teams) - 1
			} else if len(m.teamsModal.teams) == 0 {
				m.teamsModal.teamIdx = 0
			}
			m.teamsModal.confirmDelete = false
		}

	case "ctrl+k":
		if m.teamsModal.focus == 1 && len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
			team := m.teamsModal.teams[m.teamsModal.teamIdx]
			if !isReadOnlyTeam(team) {
				// Build the ordered agent list: coordinator first, then workers.
				var agentList []agents.Agent
				if team.Coordinator != nil {
					agentList = append(agentList, *team.Coordinator)
				}
				agentList = append(agentList, team.Workers...)
				if m.teamsModal.agentIdx < len(agentList) {
					target := agentList[m.teamsModal.agentIdx]
					_ = agents.SetCoordinator(team.Dir, target.Name)
					m.reloadTeamsForModal()
				}
			}
		}

	}
	return m, tea.Batch(modalCmds...)
}

// isReadOnlyTeam returns true if the team's directory is one of the well-known
// auto-detected read-only directories (~/.opencode/agents, ~/.claude/agents).
func isReadOnlyTeam(team agents.Team) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	readOnlyDirs := []string{
		filepath.Join(home, ".opencode", "agents"),
		filepath.Join(home, ".claude", "agents"),
	}
	for _, d := range readOnlyDirs {
		if team.Dir == d {
			return true
		}
	}
	return false
}

// reloadTeamsForModal refreshes m.teamsModal.teams from disk.
func (m *Model) reloadTeamsForModal() {
	discovered, _ := agents.DiscoverTeams(m.teamsDir)
	auto := agents.AutoDetectTeams()
	m.teamsModal.teams = append(discovered, auto...)
}

// maybeAutoDetectCoordinator fires an LLM call to pick a coordinator for team
// if the team has no coordinator, is not read-only, and hasn't been attempted yet.
func (m *Model) maybeAutoDetectCoordinator(team agents.Team) tea.Cmd {
	if isReadOnlyTeam(team) {
		return nil
	}
	if team.Coordinator != nil {
		return nil
	}
	allAgents := team.Workers // no coordinator, so all agents are workers
	if len(allAgents) == 0 {
		return nil
	}
	if m.teamsModal.autoDetectPending == nil {
		m.teamsModal.autoDetectPending = make(map[string]bool)
	}
	if m.teamsModal.autoDetectPending[team.Dir] {
		return nil
	}
	m.teamsModal.autoDetectPending[team.Dir] = true
	m.teamsModal.autoDetecting = true

	// Capture values for the goroutine.
	client := m.llmClient
	teamDir := team.Dir
	agentsCopy := make([]agents.Agent, len(allAgents))
	copy(agentsCopy, allAgents)

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var sb strings.Builder
		sb.WriteString("Given these agents, which one is best suited to be the team coordinator? Respond with just the agent name, nothing else.\n\nAgents:\n")
		for _, a := range agentsCopy {
			desc := a.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Fprintf(&sb, "- %s: %s\n", a.Name, desc)
		}

		msgs := []llm.Message{{Role: "user", Content: sb.String()}}
		result, err := client.ChatCompletion(ctx, msgs)
		if err != nil {
			return TeamsAutoDetectDoneMsg{teamDir: teamDir, err: err}
		}

		// Match result to an agent name (case-insensitive, trimmed).
		result = strings.TrimSpace(result)
		for _, a := range agentsCopy {
			if strings.EqualFold(result, a.Name) {
				return TeamsAutoDetectDoneMsg{teamDir: teamDir, agentName: a.Name}
			}
		}
		// No match.
		return TeamsAutoDetectDoneMsg{teamDir: teamDir}
	}
}

// renderTeamsModal renders the full-screen teams management modal.
func (m *Model) renderTeamsModal() string {
	teams := m.teamsModal.teams

	// Modal dimensions: use most of the terminal.
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

	// Inner width after modal border + padding (border=2, padding=2 each side).
	innerW := modalW - TeamsModalStyle.GetHorizontalFrameSize()
	if innerW < 10 {
		innerW = 10
	}

	// Left panel: ~32 chars inner content.
	leftInnerW := 30
	leftPanelW := leftInnerW + TeamsPanelStyle.GetHorizontalFrameSize()
	if leftPanelW > innerW/2 {
		leftPanelW = innerW / 2
		leftInnerW = leftPanelW - TeamsPanelStyle.GetHorizontalFrameSize()
	}

	// Right panel: remaining width.
	rightPanelW := innerW - leftPanelW - 1 // -1 for spacing
	rightInnerW := rightPanelW - TeamsPanelStyle.GetHorizontalFrameSize()
	if rightInnerW < 5 {
		rightInnerW = 5
	}

	// Panel inner height (subtract border + footer line).
	footerLines := 1
	panelH := modalH - TeamsModalStyle.GetVerticalFrameSize() - footerLines - 1
	if panelH < 5 {
		panelH = 5
	}
	panelInnerH := panelH - TeamsPanelStyle.GetVerticalFrameSize()
	if panelInnerH < 3 {
		panelInnerH = 3
	}

	// --- Left panel: team list ---
	var leftLines []string
	for i, t := range teams {
		var icon string
		if t.Coordinator != nil {
			icon = "◆"
		} else {
			icon = "■"
		}
		name := truncateStr(t.Name, leftInnerW-4)
		line := fmt.Sprintf(" %s %s", icon, name)
		if isReadOnlyTeam(t) {
			line += " 🔒"
		}
		if i == m.teamsModal.teamIdx {
			line = TeamsSelectedStyle.Width(leftInnerW).Render(line)
		} else if isReadOnlyTeam(t) {
			line = TeamsReadOnlyStyle.Render(line)
		}
		leftLines = append(leftLines, line)
	}

	// Input mode: show name-entry prompt at the bottom.
	if m.teamsModal.inputMode {
		leftLines = append(leftLines, "")
		leftLines = append(leftLines, DimStyle.Render("> New team name:"))
		cursor := m.teamsModal.nameInput + "█"
		leftLines = append(leftLines, "  "+cursor)
	}

	// Pad left panel to fill height.
	for len(leftLines) < panelInnerH {
		leftLines = append(leftLines, "")
	}
	if len(leftLines) > panelInnerH {
		leftLines = leftLines[:panelInnerH]
	}

	leftContent := strings.Join(leftLines, "\n")
	var leftPanel string
	if m.teamsModal.focus == 0 {
		leftPanel = TeamsFocusedPanel.Width(leftPanelW).Height(panelH).Render(leftContent)
	} else {
		leftPanel = TeamsPanelStyle.Width(leftPanelW).Height(panelH).Render(leftContent)
	}

	// --- Right panel: team detail ---
	var rightLines []string
	if len(teams) == 0 {
		rightLines = append(rightLines, DimStyle.Render("No teams configured."))
		rightLines = append(rightLines, DimStyle.Render("Press [Ctrl+N] to create one."))
	} else if m.teamsModal.teamIdx < len(teams) {
		team := teams[m.teamsModal.teamIdx]

		// Header.
		rightLines = append(rightLines, HeaderStyle.Render(truncateStr(team.Name, rightInnerW)))
		rightLines = append(rightLines, DimStyle.Render(strings.Repeat("─", rightInnerW)))

		// Coordinator line.
		coordName := "(none)"
		if team.Coordinator != nil {
			coordName = team.Coordinator.Name
		}
		coordLine := "Coordinator: " + coordName
		if m.teamsModal.autoDetecting {
			coordLine += DimStyle.Render(" [detecting...]")
		}
		rightLines = append(rightLines, coordLine)
		rightLines = append(rightLines, "")

		// Build ordered agent list for right panel: coordinator first, then workers.
		var agentList []agents.Agent
		if team.Coordinator != nil {
			agentList = append(agentList, *team.Coordinator)
		}
		agentList = append(agentList, team.Workers...)

		// Workers section — scroll a window around the selected agent so long
		// lists don't get clipped by the panel height.
		rightLines = append(rightLines, fmt.Sprintf("Workers (%d)", len(team.Workers)))
		// How many lines are left for agents after header rows (name, divider,
		// coordinator, blank, workers-header = 5 lines) and optional confirm (2).
		confirmExtra := 0
		if m.teamsModal.confirmDelete {
			confirmExtra = 2
		}
		agentAreaH := panelInnerH - 5 - confirmExtra
		if agentAreaH < 1 {
			agentAreaH = 1
		}
		// Compute scroll offset so selected agent is always visible.
		scrollOffset := 0
		if len(agentList) > agentAreaH {
			scrollOffset = m.teamsModal.agentIdx - agentAreaH/2
			if scrollOffset < 0 {
				scrollOffset = 0
			}
			if scrollOffset > len(agentList)-agentAreaH {
				scrollOffset = len(agentList) - agentAreaH
			}
		}
		visibleAgents := agentList
		if scrollOffset > 0 || len(agentList) > agentAreaH {
			end := scrollOffset + agentAreaH
			if end > len(agentList) {
				end = len(agentList)
			}
			visibleAgents = agentList[scrollOffset:end]
		}
		for vi, a := range visibleAgents {
			i := vi + scrollOffset
			prefix := "  ■ "
			if team.Coordinator != nil && i == 0 {
				prefix = "  ◆ " // coordinator marker
			}
			line := prefix + truncateStr(a.Name, rightInnerW-4)
			if m.teamsModal.focus == 1 && i == m.teamsModal.agentIdx {
				line = TeamsSelectedStyle.Width(rightInnerW).Render(line)
			}
			rightLines = append(rightLines, line)
		}

		// Delete confirmation.
		if m.teamsModal.confirmDelete {
			rightLines = append(rightLines, "")
			rightLines = append(rightLines, TeamsWarningStyle.Render(
				fmt.Sprintf("⚠ Delete '%s'? [Enter] confirm  [Esc] cancel", truncateStr(team.Name, rightInnerW-30)),
			))
		}
	}

	// Pad right panel to fill height.
	for len(rightLines) < panelInnerH {
		rightLines = append(rightLines, "")
	}
	if len(rightLines) > panelInnerH {
		rightLines = rightLines[:panelInnerH]
	}

	rightContent := strings.Join(rightLines, "\n")
	var rightPanel string
	if m.teamsModal.focus == 1 {
		rightPanel = TeamsFocusedPanel.Width(rightPanelW).Height(panelH).Render(rightContent)
	} else {
		rightPanel = TeamsPanelStyle.Width(rightPanelW).Height(panelH).Render(rightContent)
	}

	// Join panels horizontally.
	panels := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, " ", rightPanel)

	// Footer with key hints — dim read-only-gated keys when team is read-only.
	readOnly := len(teams) > 0 && m.teamsModal.teamIdx < len(teams) && isReadOnlyTeam(teams[m.teamsModal.teamIdx])
	nHint := "[Ctrl+N] New"
	dHint := "[Ctrl+D] Delete"
	cHint := "[Ctrl+K] Set Coordinator"
	if readOnly {
		nHint = DimStyle.Render(nHint)
		dHint = DimStyle.Render(dHint)
		cHint = DimStyle.Render(cHint)
	}
	footer := lipgloss.JoinHorizontal(lipgloss.Left,
		nHint, "  ", dHint, "  ", cHint, "  ",
		DimStyle.Render("[Tab] Switch"), "  ",
		DimStyle.Render("[Esc] Close"),
	)

	inner := lipgloss.JoinVertical(lipgloss.Left, panels, footer)

	modal := TeamsModalStyle.Width(modalW).Render(inner)

	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}
