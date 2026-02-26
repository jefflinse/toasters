// Package loader walks config directories, parses definition files with
// agentfmt, resolves cross-references, and rebuilds the database via
// db.Store.RebuildDefinitions.
package loader

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jefflinse/toasters/internal/agentfmt"
	"github.com/jefflinse/toasters/internal/db"
)

// Loader walks config directories and loads definitions into the database.
type Loader struct {
	store     db.Store
	configDir string
}

// New creates a new Loader.
func New(store db.Store, configDir string) *Loader {
	return &Loader{store: store, configDir: configDir}
}

// Load walks all directories, parses definitions, resolves references,
// and rebuilds the database. It is idempotent.
func (l *Loader) Load(ctx context.Context) error {
	var (
		skills     []*db.Skill
		agents     []*db.Agent
		teams      []*db.Team
		teamAgents []*db.TeamAgent
	)

	// Index agents by name for reference resolution.
	// Key: agent name (original case), Value: agent ID.
	systemAgents := make(map[string]string) // name → id
	sharedAgents := make(map[string]string) // name → id

	// 1. Walk system/ directory.
	systemDir := filepath.Join(l.configDir, "system")

	// System skills.
	systemSkills := l.loadSkills(filepath.Join(systemDir, "skills"), "system")
	skills = append(skills, systemSkills...)

	// System agents.
	sysAgents := l.loadAgents(filepath.Join(systemDir, "agents"), "system", "")
	agents = append(agents, sysAgents...)
	for _, a := range sysAgents {
		systemAgents[a.Name] = a.ID
	}

	// System team (single team.md at system/ root).
	sysTeam, sysTeamAgents := l.loadSystemTeam(systemDir, systemAgents)
	if sysTeam != nil {
		teams = append(teams, sysTeam)
		teamAgents = append(teamAgents, sysTeamAgents...)
	}

	// 2. Walk user/skills/.
	userSkills := l.loadSkills(filepath.Join(l.configDir, "user", "skills"), "user")
	skills = append(skills, userSkills...)

	// 3. Walk user/agents/ (shared agents).
	userAgents := l.loadAgents(filepath.Join(l.configDir, "user", "agents"), "user", "")
	agents = append(agents, userAgents...)
	for _, a := range userAgents {
		sharedAgents[a.Name] = a.ID
	}

	// 4. Walk user/teams/.
	teamsDir := filepath.Join(l.configDir, "user", "teams")
	uTeams, uAgents, uTeamAgents := l.loadUserTeams(teamsDir, sharedAgents, systemAgents)
	teams = append(teams, uTeams...)
	agents = append(agents, uAgents...)
	teamAgents = append(teamAgents, uTeamAgents...)

	// 5. Rebuild database.
	if err := l.store.RebuildDefinitions(ctx, skills, agents, teams, teamAgents); err != nil {
		return fmt.Errorf("rebuilding definitions: %w", err)
	}

	return nil
}

// loadSkills parses all .md files in dir as SkillDefs.
// Uses ParseSkill directly since directory context determines the type.
func (l *Loader) loadSkills(dir, source string) []*db.Skill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Directory doesn't exist — skip silently.
		return nil
	}

	var skills []*db.Skill
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		sd, err := agentfmt.ParseSkill(path)
		if err != nil {
			slog.Warn("skipping unparseable skill file", "path", path, "error", err)
			continue
		}
		skills = append(skills, convertSkill(sd, source, path))
	}
	return skills
}

// loadAgents parses all .md files in dir as AgentDefs.
// Uses ParseAgent directly since directory context determines the type.
func (l *Loader) loadAgents(dir, source, teamID string) []*db.Agent {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var agents []*db.Agent
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		ad, err := agentfmt.ParseAgent(path)
		if err != nil {
			slog.Warn("skipping unparseable agent file", "path", path, "error", err)
			continue
		}
		agents = append(agents, convertAgent(ad, source, path, teamID))
	}
	return agents
}

// loadSystemTeam loads the system team from system/team.md.
func (l *Loader) loadSystemTeam(systemDir string, systemAgents map[string]string) (*db.Team, []*db.TeamAgent) {
	teamPath := filepath.Join(systemDir, "team.md")
	td, err := agentfmt.ParseTeam(teamPath)
	if err != nil {
		// No system team file — skip silently.
		return nil, nil
	}
	team := convertTeam(td, "system", "system", teamPath, false)

	// Resolve lead.
	var teamAgents []*db.TeamAgent
	if td.Lead != "" {
		leadID, ok := resolveAgent(td.Lead, nil, systemAgents, nil)
		if ok {
			team.LeadAgent = leadID
			teamAgents = append(teamAgents, &db.TeamAgent{
				TeamID:  team.ID,
				AgentID: leadID,
				Role:    "lead",
			})
		} else {
			slog.Error("system team lead agent not found", "agent", td.Lead)
		}
	}

	// Resolve member agents.
	for _, name := range td.Agents {
		agentID, ok := resolveAgent(name, nil, systemAgents, nil)
		if ok {
			teamAgents = append(teamAgents, &db.TeamAgent{
				TeamID:  team.ID,
				AgentID: agentID,
				Role:    "worker",
			})
		} else {
			slog.Warn("system team agent not found, skipping", "agent", name)
		}
	}

	return team, teamAgents
}

// loadUserTeams walks user/teams/ and loads each team directory.
func (l *Loader) loadUserTeams(teamsDir string, sharedAgents, systemAgents map[string]string) ([]*db.Team, []*db.Agent, []*db.TeamAgent) {
	entries, err := os.ReadDir(teamsDir)
	if err != nil {
		return nil, nil, nil
	}

	var (
		teams      []*db.Team
		agents     []*db.Agent
		teamAgents []*db.TeamAgent
	)

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		teamDir := filepath.Join(teamsDir, e.Name())
		teamID := e.Name()

		// Check for .auto-team marker.
		isAuto := false
		if _, err := os.Stat(filepath.Join(teamDir, ".auto-team")); err == nil {
			isAuto = true
		}

		source := "user"
		if isAuto {
			source = "auto"
		}

		// Load team-local agents first so we can resolve references.
		localAgents := l.loadAgents(filepath.Join(teamDir, "agents"), source, teamID)
		agents = append(agents, localAgents...)

		localAgentIndex := make(map[string]string) // name → id
		for _, a := range localAgents {
			localAgentIndex[a.Name] = a.ID
		}

		// Parse team.md if it exists.
		teamPath := filepath.Join(teamDir, "team.md")
		td, err := agentfmt.ParseTeam(teamPath)
		if err != nil {
			if isAuto {
				// Auto-teams without team.md get a synthetic TeamDef.
				team := &db.Team{
					ID:         teamID,
					Name:       teamID,
					Source:     source,
					SourcePath: teamDir,
					IsAuto:     true,
				}
				teams = append(teams, team)

				// All local agents become workers.
				for _, a := range localAgents {
					teamAgents = append(teamAgents, &db.TeamAgent{
						TeamID:  teamID,
						AgentID: a.ID,
						Role:    agentRole(a.Mode),
					})
				}
				continue
			}
			slog.Warn("skipping team without parseable team.md", "dir", teamDir, "error", err)
			continue
		}

		team := convertTeam(td, teamID, source, teamPath, isAuto)
		teams = append(teams, team)

		// Resolve lead agent.
		if td.Lead != "" {
			leadID, ok := resolveAgent(td.Lead, localAgentIndex, sharedAgents, systemAgents)
			if ok {
				team.LeadAgent = leadID
				teamAgents = append(teamAgents, &db.TeamAgent{
					TeamID:  teamID,
					AgentID: leadID,
					Role:    "lead",
				})
			} else {
				slog.Error("team lead agent not found", "team", teamID, "agent", td.Lead)
			}
		}

		// Resolve member agents.
		for _, name := range td.Agents {
			agentID, ok := resolveAgent(name, localAgentIndex, sharedAgents, systemAgents)
			if ok {
				teamAgents = append(teamAgents, &db.TeamAgent{
					TeamID:  teamID,
					AgentID: agentID,
					Role:    "worker",
				})
			} else {
				slog.Warn("team agent not found, skipping", "team", teamID, "agent", name)
			}
		}
	}

	return teams, agents, teamAgents
}

// resolveAgent looks up an agent name across scopes in priority order:
// team-local → shared (user) → system. Returns the agent ID and whether found.
func resolveAgent(name string, local, shared, system map[string]string) (string, bool) {
	slug := Slugify(name)

	// Try each scope in order.
	for _, index := range []map[string]string{local, shared, system} {
		if index == nil {
			continue
		}
		// Try by name first.
		if id, ok := index[name]; ok {
			return id, true
		}
		// Try by slug (in case the index key is a name but the slug matches an ID).
		for _, id := range index {
			if id == slug {
				return id, true
			}
		}
	}
	return "", false
}

// agentRole returns the role string for a db.TeamAgent based on agent mode.
func agentRole(mode string) string {
	if mode == "lead" {
		return "lead"
	}
	return "worker"
}

// --- Conversion helpers ---

func convertSkill(sd *agentfmt.SkillDef, source, path string) *db.Skill {
	return &db.Skill{
		ID:          Slugify(sd.Name),
		Name:        sd.Name,
		Description: sd.Description,
		Tools:       marshalJSON(sd.Tools),
		Prompt:      sd.Body,
		Source:      source,
		SourcePath:  path,
	}
}

func convertAgent(ad *agentfmt.AgentDef, source, path, teamID string) *db.Agent {
	var maxTurns *int
	if ad.MaxTurns > 0 {
		v := ad.MaxTurns
		maxTurns = &v
	}

	return &db.Agent{
		ID:              Slugify(ad.Name),
		Name:            ad.Name,
		Description:     ad.Description,
		Mode:            ad.Mode,
		Model:           ad.Model,
		Provider:        ad.Provider,
		Temperature:     ad.Temperature,
		SystemPrompt:    ad.Body,
		Tools:           marshalJSON(ad.Tools),
		DisallowedTools: marshalJSON(ad.DisallowedTools),
		Skills:          marshalJSON(ad.Skills),
		PermissionMode:  ad.PermissionMode,
		Permissions:     marshalJSON(ad.Permissions),
		MCPServers:      marshalJSON(ad.MCPServers),
		MaxTurns:        maxTurns,
		Color:           ad.Color,
		Hidden:          ad.Hidden,
		Disabled:        ad.Disabled,
		Source:          source,
		SourcePath:      path,
		TeamID:          teamID,
	}
}

func convertTeam(td *agentfmt.TeamDef, teamID, source, path string, isAuto bool) *db.Team {
	return &db.Team{
		ID:          teamID,
		Name:        td.Name,
		Description: td.Description,
		Skills:      marshalJSON(td.Skills),
		Provider:    td.Provider,
		Model:       td.Model,
		Culture:     td.Body,
		Source:      source,
		SourcePath:  path,
		IsAuto:      isAuto,
	}
}

// marshalJSON marshals v to json.RawMessage. Returns nil for nil/empty values.
func marshalJSON(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	// Don't store empty arrays/objects/null.
	s := string(data)
	if s == "null" || s == "[]" || s == "{}" {
		return nil
	}
	return data
}

// --- Slugify ---

var (
	nonAlphanumHyphen = regexp.MustCompile(`[^a-z0-9-]`)
	multipleHyphens   = regexp.MustCompile(`-{2,}`)
)

// Slugify converts a name to a URL-safe identifier.
// "Go Development" → "go-development", "Senior Go Dev" → "senior-go-dev".
func Slugify(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, " ", "-")
	s = nonAlphanumHyphen.ReplaceAllString(s, "")
	s = multipleHyphens.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}
