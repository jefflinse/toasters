// TeamView: unified team representation that replaces agents.Team throughout the TUI.
// Bundles a db.Team with its resolved coordinator and worker agents, queried from the store.
package tui

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jefflinse/toasters/internal/agentfmt"
	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/db"
	"gopkg.in/yaml.v3"
)

// TeamView bundles a db.Team with its resolved coordinator and worker agents.
// It replaces agents.Team throughout the TUI, providing the same convenient
// access to coordinator/workers without depending on the agents package.
type TeamView struct {
	Team        *db.Team
	Coordinator *db.Agent   // nil if no lead agent found
	Workers     []*db.Agent // all non-coordinator agents
}

// Name returns the team name.
func (tv TeamView) Name() string {
	if tv.Team != nil {
		return tv.Team.Name
	}
	return ""
}

// Description returns the team description.
func (tv TeamView) Description() string {
	if tv.Team != nil {
		return tv.Team.Description
	}
	return ""
}

// Dir returns the team's source path (equivalent to agents.Team.Dir).
func (tv TeamView) Dir() string {
	if tv.Team != nil {
		return tv.Team.SourcePath
	}
	return ""
}

// IsAuto returns true if the team is auto-detected.
func (tv TeamView) IsAuto() bool {
	if tv.Team != nil {
		return tv.Team.IsAuto
	}
	return false
}

// BuildTeamViews queries the store to build TeamView slices for all teams.
func BuildTeamViews(ctx context.Context, store db.Store) []TeamView {
	if store == nil {
		return nil
	}
	teams, err := store.ListTeams(ctx)
	if err != nil {
		slog.Warn("failed to list teams", "error", err)
		return nil
	}
	var views []TeamView
	for _, team := range teams {
		// The system team is internal infrastructure (operator, planner, etc.)
		// and should not be visible in the TUI sidebar or modals.
		if team.Source == "system" {
			continue
		}
		view := TeamView{Team: team}
		teamAgents, err := store.ListTeamAgents(ctx, team.ID)
		if err != nil {
			slog.Warn("failed to list team agents", "team", team.Name, "error", err)
			views = append(views, view)
			continue
		}
		for _, ta := range teamAgents {
			agent, err := store.GetAgent(ctx, ta.AgentID)
			if err != nil {
				slog.Warn("failed to get agent", "agentID", ta.AgentID, "error", err)
				continue
			}
			if ta.Role == "lead" {
				view.Coordinator = agent
			} else {
				view.Workers = append(view.Workers, agent)
			}
		}
		views = append(views, view)
	}
	return views
}

// isReadOnlyTeam returns true if the team's directory is one of the well-known
// auto-detected read-only directories (~/.config/opencode/agents, ~/.claude/agents).
func isReadOnlyTeam(tv TeamView) bool {
	home := getCachedHomeDir()
	if home == "" {
		return false
	}
	readOnlyDirs := []string{
		filepath.Join(home, ".config", "opencode", "agents"),
		filepath.Join(home, ".claude", "agents"),
	}
	for _, d := range readOnlyDirs {
		if tv.Dir() == d {
			return true
		}
	}
	return false
}

// isSystemTeam returns true if the team's directory is under ~/.config/toasters/system/.
func isSystemTeam(tv TeamView) bool {
	cfgDir, err := config.Dir()
	if err != nil {
		return false
	}
	systemDir := filepath.Join(cfgDir, "system")
	return strings.HasPrefix(tv.Dir(), systemDir)
}

// isAutoTeam returns true if the team is auto-detected: either from a well-known
// read-only directory, from the db.Team.IsAuto flag, or from a directory
// containing an .auto-team marker.
func isAutoTeam(tv TeamView) bool {
	if isReadOnlyTeam(tv) {
		return true
	}
	if tv.IsAuto() {
		return true
	}
	_, err := os.Stat(filepath.Join(tv.Dir(), ".auto-team"))
	return err == nil
}

// SetCoordinator updates a team so that exactly one agent — the one whose
// frontmatter name field matches agentName (case-insensitive) — is the
// coordinator. It does two things:
//
//  1. Rewrites team.md's lead: field to agentName so that the loader picks
//     up the change immediately (lead: takes precedence over mode: in agent files).
//  2. Rewrites all agent .md files in teamDir/agents/ so that the target agent
//     has mode: primary and all others have mode: worker.
//
// Partial updates are acceptable on write failure (prototype behaviour).
func SetCoordinator(teamDir, agentName string) error {
	agentsDir := filepath.Join(teamDir, "agents")
	matches, err := filepath.Glob(filepath.Join(agentsDir, "*.md"))
	if err != nil {
		return fmt.Errorf("globbing agent files in %s: %w", agentsDir, err)
	}
	if len(matches) == 0 {
		return fmt.Errorf("no agent files found in %s", agentsDir)
	}

	// Parse each agent file to get its frontmatter name, then match
	// case-insensitively against agentName.
	needle := strings.ToLower(agentName)
	type agentFile struct {
		path string
		name string // frontmatter name (falls back to filename stem)
	}
	var agentFiles []agentFile
	for _, p := range matches {
		stem := strings.TrimSuffix(filepath.Base(p), ".md")
		name := stem // default: filename stem
		if defType, def, parseErr := agentfmt.ParseFile(p); parseErr == nil && defType == agentfmt.DefAgent {
			if agentDef, ok := def.(*agentfmt.AgentDef); ok && agentDef.Name != "" {
				name = agentDef.Name
			}
		}
		agentFiles = append(agentFiles, agentFile{path: p, name: name})
	}

	// Verify the target agent exists.
	found := false
	for _, af := range agentFiles {
		if strings.ToLower(af.name) == needle {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("agent %q not found in %s", agentName, agentsDir)
	}

	// Update team.md's lead: field.
	teamMDPath := filepath.Join(teamDir, "team.md")
	if teamDef, parseErr := agentfmt.ParseTeam(teamMDPath); parseErr == nil {
		teamDef.Lead = agentName
		if writeErr := writeTeamFileTo(teamMDPath, teamDef); writeErr != nil {
			return fmt.Errorf("updating team.md lead: %w", writeErr)
		}
	}

	// Rewrite mode: in each agent file.
	for _, af := range agentFiles {
		targetMode := "worker"
		if strings.ToLower(af.name) == needle {
			targetMode = "primary"
		}

		data, err := os.ReadFile(af.path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", af.path, err)
		}

		newContent := rewriteMode(string(data), targetMode)

		tmp, err := os.CreateTemp(agentsDir, "agent-*.md.tmp")
		if err != nil {
			return fmt.Errorf("creating temp file in %s: %w", agentsDir, err)
		}
		tmpName := tmp.Name()

		if _, err := tmp.WriteString(newContent); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
			return fmt.Errorf("writing temp file %s: %w", tmpName, err)
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpName)
			return fmt.Errorf("closing temp file %s: %w", tmpName, err)
		}
		if err := os.Rename(tmpName, af.path); err != nil {
			_ = os.Remove(tmpName)
			return fmt.Errorf("renaming %s to %s: %w", tmpName, af.path, err)
		}
	}

	return nil
}

// writeTeamFileTo writes a TeamDef as a toasters-format .md file.
func writeTeamFileTo(path string, def *agentfmt.TeamDef) error {
	data, err := yaml.Marshal(def)
	if err != nil {
		return fmt.Errorf("marshaling team frontmatter: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(strings.TrimRight(string(data), "\n"))
	sb.WriteString("\n---\n")
	if def.Body != "" {
		sb.WriteString(def.Body)
		sb.WriteString("\n")
	}

	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// rewriteMode returns content with the frontmatter mode: field set to mode.
func rewriteMode(content, mode string) string {
	const delim = "---"
	modeLine := "mode: " + mode

	if !strings.HasPrefix(content, delim+"\n") {
		return delim + "\n" + modeLine + "\n" + delim + "\n" + content
	}

	rest := content[len(delim)+1:]
	closingIdx := strings.Index(rest, "\n"+delim)
	if closingIdx < 0 {
		return delim + "\n" + modeLine + "\n" + delim + "\n" + content
	}

	fmBlock := rest[:closingIdx]
	afterClose := rest[closingIdx+1+len(delim):]

	lines := strings.Split(fmBlock, "\n")
	modeFound := false
	for i, line := range lines {
		if strings.HasPrefix(line, "mode:") {
			lines[i] = modeLine
			modeFound = true
			break
		}
	}
	if !modeFound {
		lines = append(lines, modeLine)
	}

	var sb strings.Builder
	sb.WriteString(delim + "\n")
	sb.WriteString(strings.Join(lines, "\n"))
	sb.WriteString("\n" + delim)
	sb.WriteString(afterClose)
	return sb.String()
}

// reloadTeamsFromStore rebuilds the teams list from the database store.
func reloadTeamsFromStore(store db.Store) []TeamView {
	if store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return BuildTeamViews(ctx, store)
}
