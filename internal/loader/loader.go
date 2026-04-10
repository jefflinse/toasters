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
	"sync"

	"github.com/jefflinse/toasters/internal/agentfmt"
	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"

	"gopkg.in/yaml.v3"
)

// Loader walks config directories and loads definitions into the database.
type Loader struct {
	store        db.Store
	configDir    string
	promptEngine *prompt.Engine // optional; for role-based team loading

	provMu    sync.RWMutex
	providers []provider.ProviderConfig
}

// New creates a new Loader.
func New(store db.Store, configDir string) *Loader {
	return &Loader{store: store, configDir: configDir}
}

// SetPromptEngine sets the prompt engine for role-based team loading.
func (l *Loader) SetPromptEngine(e *prompt.Engine) {
	l.promptEngine = e
}

// Providers returns the most recently loaded provider configs.
func (l *Loader) Providers() []provider.ProviderConfig {
	l.provMu.RLock()
	defer l.provMu.RUnlock()
	out := make([]provider.ProviderConfig, len(l.providers))
	copy(out, l.providers)
	return out
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
	// User skills shadow system skills with the same ID.
	userSkills := l.loadSkills(filepath.Join(l.configDir, "user", "skills"), "user")
	userSkillIDs := make(map[string]bool, len(userSkills))
	for _, sk := range userSkills {
		userSkillIDs[sk.ID] = true
	}
	filtered := skills[:0]
	for _, sk := range skills {
		if !userSkillIDs[sk.ID] {
			filtered = append(filtered, sk)
		}
	}
	skills = append(filtered, userSkills...)

	// 3. Walk user/agents/ (shared agents).
	// User agents shadow system agents with the same ID.
	userAgents := l.loadAgents(filepath.Join(l.configDir, "user", "agents"), "user", "")
	userAgentIDs := make(map[string]bool, len(userAgents))
	for _, a := range userAgents {
		userAgentIDs[a.ID] = true
	}
	filteredAgents := agents[:0]
	for _, a := range agents {
		if !userAgentIDs[a.ID] {
			filteredAgents = append(filteredAgents, a)
		} else {
			// Remove shadowed agent from systemAgents index so team
			// references resolve to the user version.
			delete(systemAgents, a.Name)
		}
	}
	agents = append(filteredAgents, userAgents...)
	for _, a := range userAgents {
		sharedAgents[a.Name] = a.ID
	}

	// 4. Walk user/teams/.
	teamsDir := filepath.Join(l.configDir, "user", "teams")
	uTeams, uAgents, uTeamAgents := l.loadUserTeams(teamsDir, sharedAgents, systemAgents)
	teams = append(teams, uTeams...)
	agents = append(agents, uAgents...)
	teamAgents = append(teamAgents, uTeamAgents...)

	// 5. Load providers from providers/*.yaml.
	provs := l.loadProviders()
	l.provMu.Lock()
	l.providers = provs
	l.provMu.Unlock()

	// 6. Rebuild database.
	if err := l.store.RebuildDefinitions(ctx, skills, agents, teams, teamAgents); err != nil {
		return fmt.Errorf("rebuilding definitions: %w", err)
	}

	return nil
}

// loadProviders reads all .yaml files in the providers/ directory.
func (l *Loader) loadProviders() []provider.ProviderConfig {
	dir := filepath.Join(l.configDir, "providers")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var configs []provider.ProviderConfig
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if info, err := e.Info(); err == nil && info.Size() > maxDefinitionFileSize {
			slog.Warn("skipping oversized provider file", "path", path, "size", info.Size())
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("skipping unreadable provider file", "path", path, "error", err)
			continue
		}
		var pc provider.ProviderConfig
		if err := yaml.Unmarshal(data, &pc); err != nil {
			slog.Warn("skipping unparseable provider file", "path", path, "error", err)
			continue
		}
		if pc.ID == "" && pc.Name == "" {
			slog.Warn("skipping provider file with no id or name", "path", path)
			continue
		}
		configs = append(configs, pc)
	}
	return configs
}

// maxDefinitionFileSize is the maximum size (in bytes) for agent/skill/team
// definition files. Files larger than this are skipped to prevent excessive
// memory allocation from malicious or accidentally large files.
const maxDefinitionFileSize = 1 << 20 // 1 MiB

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
		// Skip symlinks to prevent reading files outside the config directory.
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			slog.Warn("skipping symlink in skills directory", "path", path)
			continue
		}
		if info, err := e.Info(); err == nil && info.Size() > maxDefinitionFileSize {
			slog.Warn("skipping oversized definition file", "path", path, "size", info.Size())
			continue
		}
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
		// Skip symlinks — except in auto-team agents directories where
		// symlinks are expected (created by bootstrap for auto-team detection).
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			slog.Warn("skipping symlink in agents directory", "path", path)
			continue
		}
		if info, err := e.Info(); err == nil && info.Size() > maxDefinitionFileSize {
			slog.Warn("skipping oversized definition file", "path", path, "size", info.Size())
			continue
		}
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

	// Resolve member agents (skip lead — already added above).
	for _, name := range td.Agents {
		agentID, ok := resolveAgent(name, nil, systemAgents, nil)
		if ok {
			if agentID == team.LeadAgent {
				continue // already added as lead
			}
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

		// Check for new role-based team format first.
		if t, a, ta, ok := l.tryLoadRoleBasedTeam(teamPath, teamID, teamDir, source); ok {
			teams = append(teams, t)
			agents = append(agents, a...)
			teamAgents = append(teamAgents, ta...)
			continue
		}

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

				// Assign roles based on agent mode; track the first lead as team lead.
				for _, a := range localAgents {
					role := agentRole(a.Mode)
					teamAgents = append(teamAgents, &db.TeamAgent{
						TeamID:  teamID,
						AgentID: a.ID,
						Role:    role,
					})
					if role == "lead" && team.LeadAgent == "" {
						team.LeadAgent = a.ID
					}
				}
				continue
			}
			slog.Warn("skipping team without parseable team.md", "dir", teamDir, "error", err)
			continue
		}

		team := convertTeam(td, teamID, source, teamPath, isAuto)
		teams = append(teams, team)

		// Track where this team's agents start in the slice.
		teamAgentStart := len(teamAgents)

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

		// Resolve member agents (skip lead — already added above).
		for _, name := range td.Agents {
			agentID, ok := resolveAgent(name, localAgentIndex, sharedAgents, systemAgents)
			if ok {
				if agentID == team.LeadAgent {
					continue // already added as lead
				}
				teamAgents = append(teamAgents, &db.TeamAgent{
					TeamID:  teamID,
					AgentID: agentID,
					Role:    "worker",
				})
			} else {
				slog.Warn("team agent not found, skipping", "team", teamID, "agent", name)
			}
		}

		// For auto-teams, include ALL local agents — team.md supplements, not restricts.
		if isAuto {
			added := make(map[string]bool)
			for i := teamAgentStart; i < len(teamAgents); i++ {
				added[teamAgents[i].AgentID] = true
			}
			for _, a := range localAgents {
				if !added[a.ID] {
					role := agentRole(a.Mode)
					teamAgents = append(teamAgents, &db.TeamAgent{
						TeamID:  teamID,
						AgentID: a.ID,
						Role:    role,
					})
					if role == "lead" && team.LeadAgent == "" {
						team.LeadAgent = a.ID
					}
				}
			}
		}
	}

	return teams, agents, teamAgents
}

// roleTeamDef is the YAML frontmatter format for role-based teams.
type roleTeamDef struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Lead        string   `yaml:"lead"`
	Roles       []string `yaml:"roles"`
}

// tryLoadRoleBasedTeam attempts to parse a team.md as a role-based team.
// Returns the team, synthetic agents, and team-agent mappings if successful.
func (l *Loader) tryLoadRoleBasedTeam(teamPath, teamID, teamDir, source string) (*db.Team, []*db.Agent, []*db.TeamAgent, bool) {
	data, err := os.ReadFile(teamPath)
	if err != nil {
		return nil, nil, nil, false
	}

	// Parse frontmatter to check for the roles field.
	var def roleTeamDef
	content := string(data)
	if !strings.HasPrefix(content, "---\n") {
		return nil, nil, nil, false
	}
	rest := content[4:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return nil, nil, nil, false
	}
	if err := yaml.Unmarshal([]byte(rest[:idx]), &def); err != nil {
		return nil, nil, nil, false
	}

	// If no roles field, this is not a role-based team.
	if len(def.Roles) == 0 {
		return nil, nil, nil, false
	}

	slog.Info("loading role-based team", "team", teamID, "lead", def.Lead, "roles", len(def.Roles))

	team := &db.Team{
		ID:          teamID,
		Name:        def.Name,
		Description: def.Description,
		Source:      source,
		SourcePath:  teamDir,
	}

	var agents []*db.Agent
	var teamAgents []*db.TeamAgent

	// Create a synthetic agent for the lead role.
	if def.Lead != "" {
		leadAgentID := teamID + "/" + def.Lead
		team.LeadAgent = leadAgentID

		leadAgent := l.syntheticAgentFromRole(def.Lead, leadAgentID, teamID, source)
		agents = append(agents, leadAgent)
		teamAgents = append(teamAgents, &db.TeamAgent{
			TeamID:  teamID,
			AgentID: leadAgentID,
			Role:    "lead",
		})
	}

	// Create synthetic agents for each worker role.
	for _, roleName := range def.Roles {
		agentID := teamID + "/" + roleName
		agent := l.syntheticAgentFromRole(roleName, agentID, teamID, source)
		agents = append(agents, agent)
		teamAgents = append(teamAgents, &db.TeamAgent{
			TeamID:  teamID,
			AgentID: agentID,
			Role:    "worker",
		})
	}

	return team, agents, teamAgents, true
}

// syntheticAgentFromRole creates a db.Agent from a prompt engine role.
// The agent's system prompt is left empty — it will be composed by the
// prompt engine at spawn time.
func (l *Loader) syntheticAgentFromRole(roleName, agentID, teamID, source string) *db.Agent {
	agent := &db.Agent{
		ID:     agentID,
		Name:   roleName,
		Mode:   "worker",
		Source: source,
		TeamID: teamID,
	}

	// If the prompt engine is available, populate metadata from the role.
	if l.promptEngine != nil {
		if role := l.promptEngine.Role(roleName); role != nil {
			agent.Name = role.Name
			agent.Description = role.Description
			agent.Mode = role.Mode
			if agent.Mode == "" {
				agent.Mode = "worker"
			}
		}
	}

	return agent
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
// Recognizes both "lead" (Toasters-native) and "primary" (OpenCode vocabulary).
func agentRole(mode string) string {
	switch mode {
	case "lead", "primary":
		return "lead"
	default:
		return "worker"
	}
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

	// Scope the agent ID to its team to avoid collisions between agents with
	// the same name in different teams (e.g. system/planner vs opencode/planner).
	id := Slugify(ad.Name)
	if teamID != "" {
		id = teamID + "/" + id
	}

	return &db.Agent{
		ID:              id,
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

// Slugify converts a human-readable name to a filesystem-safe slug suitable for
// use as a filename. It lowercases the input, replaces spaces with hyphens, strips
// non-alphanumeric characters, and collapses consecutive hyphens.
//
// Examples: "Go Development" → "go-development", "Senior Go Dev" → "senior-go-dev".
//
// Slugify is exported because TUI CRUD operations in internal/tui use it to generate
// consistent filenames when creating skills, agents, and teams on disk.
func Slugify(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, " ", "-")
	s = nonAlphanumHyphen.ReplaceAllString(s, "")
	s = multipleHyphens.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}
