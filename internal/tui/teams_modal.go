// Teams modal: team management UI including rendering, key handling, coordinator auto-detection, and auto-team promotion.
package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"gopkg.in/yaml.v3"

	"github.com/jefflinse/toasters/internal/agentfmt"
	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/loader"
	"github.com/jefflinse/toasters/internal/provider"
)

// teamsModalState holds all state for the /teams modal overlay.
type teamsModalState struct {
	show              bool
	teams             []TeamView      // local copy for the modal; separate from m.teams
	teamIdx           int             // selected team in left panel
	agentIdx          int             // selected agent in right panel (for 'c' key)
	focus             int             // 0=left panel, 1=right panel
	nameInput         string          // text being typed for new team name
	inputMode         bool            // true when typing a new team name
	confirmDelete     bool            // true when delete confirmation is showing
	autoDetectPending map[string]bool // keyed by team Dir; prevents re-firing
	autoDetecting     bool            // true while LLM call is in flight
	promoting         bool            // true while auto-team promotion is in flight

	selectedTeamDef *agentfmt.TeamDef // cached parsed team.md for the selected team

	// Picker sub-modal: select an agent to add to the current team.
	pickerMode   bool        // true when the add-agent picker overlay is active
	pickerAgents []*db.Agent // agents available to add (filtered: not already in team)
	pickerIdx    int         // currently highlighted picker item

	// LLM generation state.
	generateMode   bool        // true when user is typing a generation prompt
	generateInput  string      // the prompt text being typed
	generating     bool        // true while LLM call is in flight
	generateAgents []*db.Agent // available agents captured when generateMode was entered
}

// teamPromotedMsg is sent when the async auto-team promotion finishes.
type teamPromotedMsg struct {
	teamName string
	err      error
}

// promoteAutoTeamCmd wraps promoteAutoTeam as a tea.Cmd so it runs in a goroutine
// and does not block the Bubble Tea update loop.
func promoteAutoTeamCmd(tv TeamView) tea.Cmd {
	return func() tea.Msg {
		err := promoteAutoTeam(tv)
		return teamPromotedMsg{teamName: tv.Name(), err: err}
	}
}

// updateTeamsModal handles all key presses when the teams modal is open.
func (m *Model) updateTeamsModal(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	var modalCmds []tea.Cmd

	// When typing a generation prompt, intercept all keys.
	if m.teamsModal.generateMode {
		switch msg.String() {
		case "esc":
			m.teamsModal.generateMode = false
			m.teamsModal.generateInput = ""
		case "enter":
			if strings.TrimSpace(m.teamsModal.generateInput) != "" {
				m.teamsModal.generating = true
				m.teamsModal.generateMode = false
				prompt := m.teamsModal.generateInput
				m.teamsModal.generateInput = ""
				return m, generateTeamCmd(m.llmClient, prompt, m.teamsModal.generateAgents)
			}
		case "backspace":
			if len(m.teamsModal.generateInput) > 0 {
				runes := []rune(m.teamsModal.generateInput)
				m.teamsModal.generateInput = string(runes[:len(runes)-1])
			}
		default:
			if msg.Text != "" {
				m.teamsModal.generateInput += msg.Text
			}
		}
		return m, tea.Batch(modalCmds...)
	}

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
			name := strings.TrimSpace(m.teamsModal.nameInput)
			valid := name != "" && !strings.ContainsAny(name, "/\\.\n\r:")
			if valid {
				if err := os.MkdirAll(filepath.Join(m.teamsDir, name), 0755); err != nil {
					slog.Error("failed to create team directory", "name", name, "error", err)
				}
				m.reloadTeamsForModal()
				for i, t := range m.teamsModal.teams {
					if t.Name() == name {
						m.teamsModal.teamIdx = i
						m.refreshSelectedTeamDef()
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

	// Picker mode: intercept navigation and selection keys.
	if m.teamsModal.pickerMode {
		switch msg.String() {
		case "esc":
			m.teamsModal.pickerMode = false
			m.teamsModal.pickerAgents = nil
			m.teamsModal.pickerIdx = 0
		case "up":
			if m.teamsModal.pickerIdx > 0 {
				m.teamsModal.pickerIdx--
			}
		case "down":
			if m.teamsModal.pickerIdx < len(m.teamsModal.pickerAgents)-1 {
				m.teamsModal.pickerIdx++
			}
		case "enter":
			if len(m.teamsModal.pickerAgents) > 0 && m.teamsModal.pickerIdx < len(m.teamsModal.pickerAgents) {
				agent := m.teamsModal.pickerAgents[m.teamsModal.pickerIdx]
				if agent.SourcePath == "" {
					modalCmds = append(modalCmds, m.addToast("Cannot add agent: source file unknown", toastWarning))
				} else if len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
					tv := m.teamsModal.teams[m.teamsModal.teamIdx]
					if err := addAgentToTeam(tv, agent); err != nil {
						modalCmds = append(modalCmds, m.addToast("⚠ Add failed: "+err.Error(), toastWarning))
					} else {
						m.teamsModal.pickerMode = false
						m.teamsModal.pickerAgents = nil
						m.teamsModal.pickerIdx = 0
						m.reloadTeamsForModal()
						modalCmds = append(modalCmds, m.addToast("✓ Added '"+agent.Name+"' to team", toastSuccess))
					}
				}
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
				m.refreshSelectedTeamDef()
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
				m.refreshSelectedTeamDef()
			}
			m.teamsModal.confirmDelete = false
			m.teamsModal.agentIdx = 0
			if len(m.teamsModal.teams) > 0 {
				modalCmds = append(modalCmds, m.maybeAutoDetectCoordinator(m.teamsModal.teams[m.teamsModal.teamIdx]))
			}
		} else {
			// Right panel: navigate agents (coordinator first, then workers).
			if len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
				tv := m.teamsModal.teams[m.teamsModal.teamIdx]
				total := len(tv.Workers)
				if tv.Coordinator != nil {
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
				tv := m.teamsModal.teams[m.teamsModal.teamIdx]
				// Validate that team dir is under the expected teams directory
				// before performing recursive deletion.
				realTeamDir, err := filepath.EvalSymlinks(tv.Dir())
				realTeamsDir, err2 := filepath.EvalSymlinks(m.teamsDir)
				if err == nil && err2 == nil && strings.HasPrefix(realTeamDir, realTeamsDir+string(filepath.Separator)) {
					_ = os.RemoveAll(tv.Dir())
				} else {
					slog.Error("refusing to delete team outside teams directory", "dir", tv.Dir(), "teamsDir", m.teamsDir)
				}
			}
			m.reloadTeamsForModal()
			if m.teamsModal.teamIdx >= len(m.teamsModal.teams) && len(m.teamsModal.teams) > 0 {
				m.teamsModal.teamIdx = len(m.teamsModal.teams) - 1
				m.refreshSelectedTeamDef()
			} else if len(m.teamsModal.teams) == 0 {
				m.teamsModal.teamIdx = 0
				m.refreshSelectedTeamDef()
			}
			m.teamsModal.confirmDelete = false
		}

	case "ctrl+k":
		if m.teamsModal.focus == 1 && len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
			tv := m.teamsModal.teams[m.teamsModal.teamIdx]
			if !isReadOnlyTeam(tv) {
				// Build the ordered agent list: coordinator first, then workers.
				var agentList []*db.Agent
				if tv.Coordinator != nil {
					agentList = append(agentList, tv.Coordinator)
				}
				agentList = append(agentList, tv.Workers...)
				if m.teamsModal.agentIdx < len(agentList) {
					target := agentList[m.teamsModal.agentIdx]
					if err := SetCoordinator(tv.Dir(), target.Name); err != nil {
						slog.Error("failed to set coordinator", "team", tv.Name(), "agent", target.Name, "error", err)
						modalCmds = append(modalCmds, m.addToast("⚠ Set coordinator failed: "+err.Error(), toastWarning))
					} else {
						m.reloadTeamsForModal()
						modalCmds = append(modalCmds, m.addToast("✓ Coordinator set to '"+target.Name+"'", toastSuccess))
					}
				}
			}
		}

	case "ctrl+p":
		if m.teamsModal.focus == 0 && len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
			tv := m.teamsModal.teams[m.teamsModal.teamIdx]
			if isAutoTeam(tv) && !isSystemTeam(tv) && !m.teamsModal.promoting {
				m.teamsModal.promoting = true
				modalCmds = append(modalCmds, promoteAutoTeamCmd(tv))
			}
		}

	case "ctrl+a":
		// Open the add-agent picker when a non-system, non-read-only team is selected.
		if !m.teamsModal.inputMode && len(m.teamsModal.teams) > 0 && m.teamsModal.teamIdx < len(m.teamsModal.teams) {
			tv := m.teamsModal.teams[m.teamsModal.teamIdx]
			if !isReadOnlyTeam(tv) && !isSystemTeam(tv) && m.store != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				allAgents, err := m.store.ListAgents(ctx)
				cancel()
				if err != nil {
					slog.Error("failed to list agents for picker", "error", err)
					modalCmds = append(modalCmds, m.addToast("⚠ Failed to load agents", toastWarning))
				} else {
					available := filterAgentsForTeam(tv, allAgents)
					if len(available) == 0 {
						modalCmds = append(modalCmds, m.addToast("No additional agents available", toastInfo))
					} else {
						m.teamsModal.pickerMode = true
						m.teamsModal.pickerAgents = available
						m.teamsModal.pickerIdx = 0
					}
				}
			}
		}

	case "ctrl+g":
		// Enter LLM generation mode (only when idle and not in any sub-mode).
		if !m.teamsModal.inputMode && !m.teamsModal.generating && !m.teamsModal.pickerMode {
			if m.llmClient == nil {
				modalCmds = append(modalCmds, m.addToast("⚠ No LLM provider configured", toastWarning))
			} else if m.store != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				allAgents, err := m.store.ListAgents(ctx)
				cancel()
				if err != nil {
					slog.Error("failed to list agents for team generation", "error", err)
					modalCmds = append(modalCmds, m.addToast("⚠ Failed to load agents", toastWarning))
				} else {
					m.teamsModal.generateMode = true
					m.teamsModal.generateInput = ""
					m.teamsModal.generateAgents = allAgents
				}
			}
		}

	}
	return m, tea.Batch(modalCmds...)
}

// filterAgentsForTeam returns agents from available that are not already in the
// team (neither coordinator nor workers). Comparison is by agent name (case-sensitive).
func filterAgentsForTeam(tv TeamView, available []*db.Agent) []*db.Agent {
	inTeam := make(map[string]bool)
	if tv.Coordinator != nil {
		inTeam[tv.Coordinator.Name] = true
	}
	for _, w := range tv.Workers {
		inTeam[w.Name] = true
	}
	var result []*db.Agent
	for _, a := range available {
		if !inTeam[a.Name] {
			result = append(result, a)
		}
	}
	return result
}

var (
	cachedHomeDir     string
	cachedHomeDirOnce sync.Once
)

func getCachedHomeDir() string {
	cachedHomeDirOnce.Do(func() {
		cachedHomeDir, _ = os.UserHomeDir()
	})
	return cachedHomeDir
}

// promoteAutoTeam promotes an auto-detected team to a fully managed team.
// It branches on whether the team is a legacy read-only auto-team (lives at a
// well-known read-only directory like ~/.claude/agents) or a bootstrap
// auto-team (already lives inside user/teams/ with a .auto-team marker and an
// agents/ symlink).
func promoteAutoTeam(tv TeamView) error {
	if isReadOnlyTeam(tv) {
		return promoteReadOnlyAutoTeam(tv)
	}
	return promoteMarkerAutoTeam(tv)
}

// promoteReadOnlyAutoTeam handles promotion of legacy read-only auto-teams
// (e.g. ~/.claude/agents). It copies agent files into a new managed team
// directory under ~/.config/toasters/user/teams/{team-name}/.
func promoteReadOnlyAutoTeam(tv TeamView) error {
	configDir, err := config.Dir()
	if err != nil {
		return fmt.Errorf("getting config directory: %w", err)
	}
	userTeamsDir := filepath.Join(configDir, "user", "teams")

	targetDir := filepath.Join(userTeamsDir, tv.Name())
	targetAgentsDir := filepath.Join(targetDir, "agents")

	// Fail if the target already exists to avoid overwriting.
	if _, err := os.Stat(targetDir); err == nil {
		return fmt.Errorf("team directory %q already exists", targetDir)
	}

	// For read-only teams, Dir IS the agents directory.
	agentsSourceDir := tv.Dir()

	// Discover agent .md files in the source directory.
	matches, err := filepath.Glob(filepath.Join(agentsSourceDir, "*.md"))
	if err != nil {
		return fmt.Errorf("globbing agent files in %s: %w", agentsSourceDir, err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("no agent files found in %s", agentsSourceDir)
	}

	// Parse all agent files before creating any directories — fail fast on errors.
	type parsedAgent struct {
		stem string
		def  *agentfmt.AgentDef
	}
	var parsed []parsedAgent
	for _, path := range matches {
		defType, def, err := agentfmt.ParseFile(path)
		if err != nil {
			slog.Warn("skipping unparseable agent during promotion", "path", path, "error", err)
			continue
		}
		if defType != agentfmt.DefAgent {
			slog.Warn("skipping non-agent file during promotion", "path", path, "type", defType)
			continue
		}
		agentDef, ok := def.(*agentfmt.AgentDef)
		if !ok {
			slog.Warn("unexpected type for agent definition", "path", path)
			continue
		}
		stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		parsed = append(parsed, parsedAgent{stem: stem, def: agentDef})
	}
	if len(parsed) == 0 {
		return fmt.Errorf("no valid agent definitions found in %s", agentsSourceDir)
	}

	// Create the target directory structure.
	if err := os.MkdirAll(targetAgentsDir, 0o755); err != nil {
		return fmt.Errorf("creating target directory %s: %w", targetAgentsDir, err)
	}

	// Write each agent file in toasters format.
	var agentNames []string
	for _, pa := range parsed {
		agentPath := filepath.Join(targetAgentsDir, pa.stem+".md")
		if err := writeAgentFile(agentPath, pa.def); err != nil {
			// Clean up on failure.
			_ = os.RemoveAll(targetDir)
			return fmt.Errorf("writing agent file %s: %w", agentPath, err)
		}
		agentNames = append(agentNames, pa.def.Name)
	}

	// Determine the lead agent.
	lead := ""
	if tv.Coordinator != nil {
		lead = tv.Coordinator.Name
	}

	// Use the parent/dir label for clarity (e.g. ".claude/agents").
	source := filepath.Base(filepath.Dir(tv.Dir())) + "/" + filepath.Base(tv.Dir())

	// Generate team.md.
	teamDef := &agentfmt.TeamDef{
		Name:        tv.Name(),
		Description: fmt.Sprintf("Promoted from %s", source),
		Lead:        lead,
		Agents:      agentNames,
	}
	teamMDPath := filepath.Join(targetDir, "team.md")
	if err := writeTeamFile(teamMDPath, teamDef); err != nil {
		_ = os.RemoveAll(targetDir)
		return fmt.Errorf("writing team.md: %w", err)
	}

	slog.Info("promoted read-only auto-team to managed team", "team", tv.Name(), "target", targetDir, "agents", len(parsed))
	return nil
}

// promoteMarkerAutoTeam handles in-place promotion of bootstrap auto-teams.
// These teams already live inside user/teams/{name}/ with a .auto-team marker
// and an agents/ symlink pointing to the real source directory. Promotion
// replaces the symlink with a real directory containing the parsed agent files,
// writes team.md, and removes the .auto-team marker.
func promoteMarkerAutoTeam(tv TeamView) error {
	agentsSymlink := filepath.Join(tv.Dir(), "agents")

	// Discover agent .md files via the symlink (which points to the real source).
	matches, err := filepath.Glob(filepath.Join(agentsSymlink, "*.md"))
	if err != nil {
		return fmt.Errorf("globbing agent files in %s: %w", agentsSymlink, err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("no agent files found in %s", agentsSymlink)
	}

	// Parse all agent files first — fail fast before any writes.
	type parsedAgent struct {
		stem string
		def  *agentfmt.AgentDef
	}
	var parsed []parsedAgent
	for _, path := range matches {
		defType, def, err := agentfmt.ParseFile(path)
		if err != nil {
			slog.Warn("skipping unparseable agent during promotion", "path", path, "error", err)
			continue
		}
		if defType != agentfmt.DefAgent {
			slog.Warn("skipping non-agent file during promotion", "path", path, "type", defType)
			continue
		}
		agentDef, ok := def.(*agentfmt.AgentDef)
		if !ok {
			slog.Warn("unexpected type for agent definition", "path", path)
			continue
		}
		stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		parsed = append(parsed, parsedAgent{stem: stem, def: agentDef})
	}
	if len(parsed) == 0 {
		return fmt.Errorf("no valid agent definitions found in %s", agentsSymlink)
	}

	// Remove the agents/ symlink and replace it with a real directory.
	if err := os.Remove(agentsSymlink); err != nil {
		return fmt.Errorf("removing agents symlink %s: %w", agentsSymlink, err)
	}
	if err := os.MkdirAll(agentsSymlink, 0o755); err != nil {
		return fmt.Errorf("creating agents directory %s: %w", agentsSymlink, err)
	}

	// Write each agent file in toasters format. On failure, log and continue —
	// partial state is acceptable; we do not attempt to restore the symlink.
	var agentNames []string
	for _, pa := range parsed {
		agentPath := filepath.Join(agentsSymlink, pa.stem+".md")
		if err := writeAgentFile(agentPath, pa.def); err != nil {
			slog.Error("failed to write agent file during promotion", "path", agentPath, "error", err)
			continue
		}
		agentNames = append(agentNames, pa.def.Name)
	}

	// Determine the lead agent.
	lead := ""
	if tv.Coordinator != nil {
		lead = tv.Coordinator.Name
	}

	// Write team.md into the team directory.
	teamDef := &agentfmt.TeamDef{
		Name:        tv.Name(),
		Description: fmt.Sprintf("Promoted from %s", tv.Name()),
		Lead:        lead,
		Agents:      agentNames,
	}
	teamMDPath := filepath.Join(tv.Dir(), "team.md")
	if err := writeTeamFile(teamMDPath, teamDef); err != nil {
		return fmt.Errorf("writing team.md: %w", err)
	}

	// Remove the .auto-team marker to complete the promotion.
	if err := os.Remove(filepath.Join(tv.Dir(), ".auto-team")); err != nil {
		slog.Warn("failed to remove .auto-team marker", "dir", tv.Dir(), "error", err)
	}

	slog.Info("promoted bootstrap auto-team in-place", "team", tv.Name(), "dir", tv.Dir(), "agents", len(parsed))
	return nil
}

// writeAgentFile writes an AgentDef as a toasters-format .md file with YAML
// frontmatter. Only non-zero fields are included in the frontmatter.
func writeAgentFile(path string, def *agentfmt.AgentDef) error {
	fm, err := yaml.Marshal(def)
	if err != nil {
		return fmt.Errorf("marshaling agent frontmatter: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.Write(bytes.TrimRight(fm, "\n"))
	sb.WriteString("\n---\n")
	if def.Body != "" {
		sb.WriteString(def.Body)
		sb.WriteString("\n")
	}

	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// writeTeamFile writes a TeamDef as a toasters-format .md file with YAML
// frontmatter and an optional body (culture document).
func writeTeamFile(path string, def *agentfmt.TeamDef) error {
	fm, err := yaml.Marshal(def)
	if err != nil {
		return fmt.Errorf("marshaling team frontmatter: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.Write(bytes.TrimRight(fm, "\n"))
	sb.WriteString("\n---\n")
	if def.Body != "" {
		sb.WriteString(def.Body)
		sb.WriteString("\n")
	}

	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// reloadTeamsForModal refreshes m.teamsModal.teams from the store.
func (m *Model) reloadTeamsForModal() {
	m.teamsModal.teams = reloadTeamsFromStore(m.store)
	m.refreshSelectedTeamDef()
}

// refreshSelectedTeamDef parses team.md for the currently selected team and
// caches the result in m.teamsModal.selectedTeamDef. Called whenever teamIdx
// changes or the team list is reloaded, so renderTeamsModal never reads files.
func (m *Model) refreshSelectedTeamDef() {
	if m.teamsModal.teamIdx < 0 || m.teamsModal.teamIdx >= len(m.teamsModal.teams) {
		m.teamsModal.selectedTeamDef = nil
		return
	}
	tv := m.teamsModal.teams[m.teamsModal.teamIdx]
	teamMDPath := filepath.Join(tv.Dir(), "team.md")
	if def, err := agentfmt.ParseTeam(teamMDPath); err == nil {
		m.teamsModal.selectedTeamDef = def
	} else {
		m.teamsModal.selectedTeamDef = nil
	}
}

// addAgentToTeam adds the given agent to the team by:
//  1. Appending the agent name to the team's team.md agents list.
//  2. Copying the agent's source .md file into <team.Dir>/agents/<slug>.md.
func addAgentToTeam(tv TeamView, agent *db.Agent) error {
	teamMDPath := filepath.Join(tv.Dir(), "team.md")

	// Parse the existing team.md (or create a minimal one if absent).
	teamDef, err := agentfmt.ParseTeam(teamMDPath)
	if err != nil {
		// If team.md doesn't exist yet, start with a minimal definition.
		teamDef = &agentfmt.TeamDef{Name: tv.Name()}
	}

	// Append the agent name if not already present.
	alreadyListed := false
	for _, n := range teamDef.Agents {
		if n == agent.Name {
			alreadyListed = true
			break
		}
	}
	if !alreadyListed {
		teamDef.Agents = append(teamDef.Agents, agent.Name)
	}

	// Write the updated team.md back to disk.
	if err := writeTeamFile(teamMDPath, teamDef); err != nil {
		return fmt.Errorf("writing team.md: %w", err)
	}

	// Copy the agent's source file into the team's agents directory.
	agentsDir := filepath.Join(tv.Dir(), "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return fmt.Errorf("creating agents directory: %w", err)
	}

	slug := loader.Slugify(agent.Name)
	if slug == "" {
		slug = loader.Slugify(agent.ID)
	}
	destPath := filepath.Join(agentsDir, slug+".md")

	if err := copyFile(agent.SourcePath, destPath); err != nil {
		return fmt.Errorf("copying agent file: %w", err)
	}

	return nil
}

// copyFile copies the file at src to dst, creating dst if it does not exist.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source file: %w", err)
	}

	out, err := os.Create(dst)
	if err != nil {
		_ = in.Close()
		return fmt.Errorf("creating destination file: %w", err)
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = in.Close()
		_ = out.Close()
		return fmt.Errorf("copying file contents: %w", err)
	}

	if err := in.Close(); err != nil {
		_ = out.Close()
		return fmt.Errorf("closing source file: %w", err)
	}

	return out.Close()
}

// maybeAutoDetectCoordinator fires an LLM call to pick a coordinator for team
// if the team has no coordinator, is not read-only, and hasn't been attempted yet.
func (m *Model) maybeAutoDetectCoordinator(tv TeamView) tea.Cmd {
	if isReadOnlyTeam(tv) {
		return nil
	}
	if tv.Coordinator != nil {
		return nil
	}
	allAgents := tv.Workers // no coordinator, so all agents are workers
	if len(allAgents) == 0 {
		return nil
	}
	if m.teamsModal.autoDetectPending == nil {
		m.teamsModal.autoDetectPending = make(map[string]bool)
	}
	if m.teamsModal.autoDetectPending[tv.Dir()] {
		return nil
	}
	m.teamsModal.autoDetectPending[tv.Dir()] = true
	m.teamsModal.autoDetecting = true

	// Capture values for the goroutine.
	client := m.llmClient
	teamDir := tv.Dir()
	agentsCopy := make([]*db.Agent, len(allAgents))
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

		msgs := []provider.Message{{Role: "user", Content: sb.String()}}
		result, err := provider.ChatCompletion(ctx, client, msgs)
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
		name := truncateStr(t.Name(), leftInnerW-4)
		line := fmt.Sprintf(" %s %s", icon, name)
		// Append badges for system, auto, and read-only teams.
		if isSystemTeam(t) {
			line += " ⚙"
		} else if isAutoTeam(t) {
			line += " ↻"
		}
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

	// Generate mode: show generation prompt input at the bottom.
	if m.teamsModal.generateMode {
		leftLines = append(leftLines, "")
		leftLines = append(leftLines, DimStyle.Render("> Describe the team:"))
		cursor := m.teamsModal.generateInput + "█"
		leftLines = append(leftLines, "  "+cursor)
	}

	// Generating: show status indicator at the bottom.
	if m.teamsModal.generating {
		leftLines = append(leftLines, "")
		leftLines = append(leftLines, DimStyle.Render("⟳ Generating team..."))
	}

	// Promoting: show status indicator at the bottom.
	if m.teamsModal.promoting {
		leftLines = append(leftLines, "")
		leftLines = append(leftLines, DimStyle.Render("⟳ Promoting team..."))
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

	// --- Right panel: team detail or picker overlay ---
	var rightLines []string
	if m.teamsModal.pickerMode {
		// Picker overlay: show selectable list of agents to add.
		rightLines = append(rightLines, HeaderStyle.Render("Select an agent to add:"))
		rightLines = append(rightLines, DimStyle.Render(strings.Repeat("─", rightInnerW)))
		rightLines = append(rightLines, "")

		if len(m.teamsModal.pickerAgents) == 0 {
			rightLines = append(rightLines, DimStyle.Render("No agents available."))
		} else {
			// Compute scroll window so selected item stays visible.
			agentAreaH := panelInnerH - 3 // 3 lines for header + separator + blank
			if agentAreaH < 1 {
				agentAreaH = 1
			}
			scrollOffset := 0
			if len(m.teamsModal.pickerAgents) > agentAreaH {
				scrollOffset = m.teamsModal.pickerIdx - agentAreaH/2
				if scrollOffset < 0 {
					scrollOffset = 0
				}
				if scrollOffset > len(m.teamsModal.pickerAgents)-agentAreaH {
					scrollOffset = len(m.teamsModal.pickerAgents) - agentAreaH
				}
			}
			end := scrollOffset + agentAreaH
			if end > len(m.teamsModal.pickerAgents) {
				end = len(m.teamsModal.pickerAgents)
			}
			for vi, a := range m.teamsModal.pickerAgents[scrollOffset:end] {
				i := vi + scrollOffset
				icon := "■"
				if a.Mode == "lead" {
					icon = "◆"
				}
				line := fmt.Sprintf(" %s %s", icon, truncateStr(a.Name, rightInnerW-4))
				if i == m.teamsModal.pickerIdx {
					line = TeamsSelectedStyle.Width(rightInnerW).Render(line)
				}
				rightLines = append(rightLines, line)
			}
		}
	} else if len(teams) == 0 {
		rightLines = append(rightLines, DimStyle.Render("No teams configured."))
		rightLines = append(rightLines, DimStyle.Render("Press [Ctrl+N] to create one."))
	} else if m.teamsModal.teamIdx < len(teams) {
		tv := teams[m.teamsModal.teamIdx]

		// Header with badges.
		headerText := truncateStr(tv.Name(), rightInnerW-12)
		if isSystemTeam(tv) {
			headerText += " " + DimStyle.Render("⚙ system")
		} else if isAutoTeam(tv) {
			headerText += " " + DimStyle.Render("↻ auto")
		}
		rightLines = append(rightLines, HeaderStyle.Render(headerText))
		rightLines = append(rightLines, DimStyle.Render(strings.Repeat("─", rightInnerW)))

		// Description line (if available).
		if tv.Description() != "" {
			rightLines = append(rightLines, DimStyle.Render(truncateStr(tv.Description(), rightInnerW)))
		}

		// Promote hint for auto-teams.
		if isAutoTeam(tv) && !isSystemTeam(tv) {
			rightLines = append(rightLines, DimStyle.Render("⇧ Ctrl+P to promote to managed team"))
		}

		// Coordinator line.
		coordName := "(none)"
		if tv.Coordinator != nil {
			coordName = tv.Coordinator.Name
		}
		coordLine := "Coordinator: " + coordName
		if m.teamsModal.autoDetecting {
			coordLine += DimStyle.Render(" [detecting...]")
		}
		rightLines = append(rightLines, coordLine)

		// Composition info from team.md (skills, provider/model, culture preview).
		if teamDef := m.teamsModal.selectedTeamDef; teamDef != nil {
			if len(teamDef.Skills) > 0 {
				rightLines = append(rightLines, DimStyle.Render("Skills: "+strings.Join(teamDef.Skills, ", ")))
			}
			if teamDef.Provider != "" || teamDef.Model != "" {
				pmLine := ""
				if teamDef.Provider != "" {
					pmLine = teamDef.Provider
				}
				if teamDef.Model != "" {
					if pmLine != "" {
						pmLine += "/"
					}
					pmLine += teamDef.Model
				}
				rightLines = append(rightLines, DimStyle.Render("Provider: "+truncateStr(pmLine, rightInnerW-10)))
			}
			if teamDef.Body != "" {
				rightLines = append(rightLines, DimStyle.Render("Culture:"))
				cultureLines := strings.SplitN(teamDef.Body, "\n", 4)
				for i, cl := range cultureLines {
					if i >= 3 {
						break
					}
					cl = strings.TrimSpace(cl)
					if cl != "" {
						rightLines = append(rightLines, DimStyle.Render("  "+truncateStr(cl, rightInnerW-2)))
					}
				}
			}
		}
		rightLines = append(rightLines, "")

		// Build ordered agent list for right panel: coordinator first, then workers.
		var agentList []*db.Agent
		if tv.Coordinator != nil {
			agentList = append(agentList, tv.Coordinator)
		}
		agentList = append(agentList, tv.Workers...)

		// Workers section — scroll a window around the selected agent so long
		// lists don't get clipped by the panel height.
		rightLines = append(rightLines, fmt.Sprintf("Workers (%d)", len(tv.Workers)))
		// How many lines are left for agents after the header rows we've already
		// rendered (rightLines so far) and optional confirm (2).
		confirmExtra := 0
		if m.teamsModal.confirmDelete {
			confirmExtra = 2
		}
		agentAreaH := panelInnerH - len(rightLines) - confirmExtra
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
			if tv.Coordinator != nil && i == 0 {
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
				fmt.Sprintf("⚠ Delete '%s'? [Enter] confirm  [Esc] cancel", truncateStr(tv.Name(), rightInnerW-30)),
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
	autoTeam := len(teams) > 0 && m.teamsModal.teamIdx < len(teams) && isAutoTeam(teams[m.teamsModal.teamIdx]) && !isSystemTeam(teams[m.teamsModal.teamIdx])
	systemTeam := len(teams) > 0 && m.teamsModal.teamIdx < len(teams) && isSystemTeam(teams[m.teamsModal.teamIdx])
	noTeamSelected := len(teams) == 0
	nHint := "[Ctrl+N] New"
	dHint := "[Ctrl+D] Delete"
	cHint := "[Ctrl+K] Set Coordinator"
	pHint := "[Ctrl+P] Promote"
	aHint := "[Ctrl+A] Add Agent"
	gHint := "[Ctrl+G] Generate"
	if readOnly {
		dHint = DimStyle.Render(dHint)
		cHint = DimStyle.Render(cHint)
	}
	if !autoTeam {
		pHint = DimStyle.Render(pHint)
	}
	// Dim the add-agent hint when: no team selected, team is system/read-only, or picker is already active.
	if noTeamSelected || readOnly || systemTeam || m.teamsModal.pickerMode {
		aHint = DimStyle.Render(aHint)
	}
	// Dim the generate hint when: no LLM client configured, or generation is in progress.
	if m.llmClient == nil || m.teamsModal.generating {
		gHint = DimStyle.Render(gHint)
	}
	var footer string
	if m.teamsModal.pickerMode {
		footer = lipgloss.JoinHorizontal(lipgloss.Left,
			"[Enter] Add", "  ",
			"[Esc] Cancel",
		)
	} else {
		footer = lipgloss.JoinHorizontal(lipgloss.Left,
			nHint, "  ", dHint, "  ", cHint, "  ", pHint, "  ", aHint, "  ", gHint, "  ",
			DimStyle.Render("[Tab] Switch"), "  ",
			DimStyle.Render("[Esc] Close"),
		)
	}

	inner := lipgloss.JoinVertical(lipgloss.Left, panels, footer)

	modal := TeamsModalStyle.Width(modalW).Render(inner)

	return lipgloss.Place(m.width, m.height,
		lipgloss.Center, lipgloss.Center,
		modal,
		lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(lipgloss.Color("235"))),
	)
}
