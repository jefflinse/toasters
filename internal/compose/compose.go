// Package compose assembles agent definitions from the database into fully
// resolved agents ready for session creation. It implements the composition
// algorithm: loading the agent, resolving skills, merging team context,
// assembling the system prompt, unioning tool sets, and cascading
// provider/model configuration.
package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jefflinse/toasters/internal/db"
)

// Tool name constants — shared between compose and tool executors to ensure
// compile-time consistency.
const (
	ToolSpawnAgent       = "spawn_agent"
	ToolCompleteTask     = "complete_task"
	ToolRequestNewTask   = "request_new_task"
	ToolReportBlocker    = "report_blocker"
	ToolReportProgress   = "report_progress"
	ToolQueryJobContext  = "query_job_context"
	ToolQueryTeamContext = "query_team_context"
	ToolConsultAgent     = "consult_agent"
	ToolQueryJob         = "query_job"
	ToolQueryTeams       = "query_teams"
	ToolSurfaceToUser    = "surface_to_user"
)

// Role-based tool sets injected during composition.
func leadToolNames() []string {
	return []string{
		ToolSpawnAgent,
		ToolCompleteTask,
		ToolRequestNewTask,
		ToolReportBlocker,
		ToolReportProgress,
		ToolQueryJobContext,
		ToolQueryTeamContext,
	}
}

func workerToolNames() []string {
	return []string{
		ToolReportProgress,
		ToolQueryTeamContext,
	}
}

func systemLeadToolNames() []string {
	return []string{
		ToolConsultAgent,
		ToolQueryJob,
		ToolQueryTeams,
		ToolSurfaceToUser,
	}
}

// ComposedAgent is the fully resolved agent ready for session creation.
type ComposedAgent struct {
	AgentID      string
	Name         string
	SystemPrompt string   // fully assembled prompt
	Tools        []string // merged tool names (after denylist)
	Provider     string   // resolved provider name
	Model        string   // resolved model name
	Temperature  *float64
	MaxTurns     *int
	TeamID       string // empty if not on a team
	Role         string // "lead" or "worker"
	WorkDir      string // inherited from job/config
}

// Store is the subset of db.Store needed by the composer.
type Store interface {
	GetAgent(ctx context.Context, id string) (*db.Agent, error)
	GetTeam(ctx context.Context, id string) (*db.Team, error)
	GetSkill(ctx context.Context, id string) (*db.Skill, error)
	ListTeamAgents(ctx context.Context, teamID string) ([]*db.TeamAgent, error)
}

// Composer assembles agent definitions into fully composed agents.
type Composer struct {
	store           Store
	defaultProvider string
	defaultModel    string
}

// New creates a new Composer.
func New(store Store, defaultProvider, defaultModel string) *Composer {
	return &Composer{
		store:           store,
		defaultProvider: defaultProvider,
		defaultModel:    defaultModel,
	}
}

// Compose assembles a fully composed agent from its DB definition.
// If teamID is non-empty, team context (culture, team skills) is included.
func (c *Composer) Compose(ctx context.Context, agentID string, teamID string) (*ComposedAgent, error) {
	// 1. Load agent definition.
	agent, err := c.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("loading agent %q: %w", agentID, err)
	}

	// 2. Load agent-level skills.
	agentSkillNames := parseStringArray(agent.Skills)
	agentSkills := c.loadSkills(ctx, agentSkillNames)

	// 3. Load team context if applicable.
	var team *db.Team
	var teamSkills []*db.Skill
	var role string

	if teamID != "" {
		team, err = c.store.GetTeam(ctx, teamID)
		if err != nil {
			slog.Warn("team not found, composing without team context",
				"team_id", teamID, "error", err)
		} else {
			// Load team-wide skills.
			teamSkillNames := parseStringArray(team.Skills)
			teamSkills = c.loadSkills(ctx, teamSkillNames)
		}

		// Determine role from team membership.
		role = c.resolveRole(ctx, agentID, teamID)
	}

	// 4. Compose the system prompt.
	systemPrompt := c.assemblePrompt(agent, agentSkills, teamSkills, team, role)

	// 5. Merge tool sets.
	tools := c.mergeTools(agent, agentSkills, teamSkills, teamID, role)

	// 6. Resolve provider/model.
	provider := c.resolveProvider(agent, team)
	model := c.resolveModel(agent, team)

	return &ComposedAgent{
		AgentID:      agent.ID,
		Name:         agent.Name,
		SystemPrompt: systemPrompt,
		Tools:        tools,
		Provider:     provider,
		Model:        model,
		Temperature:  agent.Temperature,
		MaxTurns:     agent.MaxTurns,
		TeamID:       teamID,
		Role:         role,
	}, nil
}

// loadSkills loads skills by name, logging warnings for missing ones.
func (c *Composer) loadSkills(ctx context.Context, names []string) []*db.Skill {
	skills := make([]*db.Skill, 0, len(names))
	for _, name := range names {
		skill, err := c.store.GetSkill(ctx, name)
		if err != nil {
			slog.Warn("skill not found, skipping", "skill", name, "error", err)
			continue
		}
		skills = append(skills, skill)
	}
	return skills
}

// resolveRole determines the agent's role within a team.
func (c *Composer) resolveRole(ctx context.Context, agentID, teamID string) string {
	members, err := c.store.ListTeamAgents(ctx, teamID)
	if err != nil {
		slog.Warn("failed to list team agents, defaulting to worker",
			"team_id", teamID, "error", err)
		return "worker"
	}
	for _, m := range members {
		if m.AgentID == agentID {
			return m.Role
		}
	}
	// Agent not found in team membership — default to worker.
	return "worker"
}

// assemblePrompt builds the full system prompt from agent, skills, and team context.
func (c *Composer) assemblePrompt(
	agent *db.Agent,
	agentSkills []*db.Skill,
	teamSkills []*db.Skill,
	team *db.Team,
	role string,
) string {
	var b strings.Builder

	// Agent's own system prompt.
	if agent.SystemPrompt != "" {
		b.WriteString(agent.SystemPrompt)
	}

	// Agent-level skills.
	if len(agentSkills) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("## Skills")
		for _, s := range agentSkills {
			b.WriteString("\n\n### ")
			b.WriteString(s.Name)
			if s.Prompt != "" {
				b.WriteString("\n")
				b.WriteString(s.Prompt)
			}
		}
	}

	// Team-wide skills.
	if len(teamSkills) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("## Team Skills")
		for _, s := range teamSkills {
			b.WriteString("\n\n### ")
			b.WriteString(s.Name)
			if s.Prompt != "" {
				b.WriteString("\n")
				b.WriteString(s.Prompt)
			}
		}
	}

	// Team culture — only for lead agents.
	if team != nil && team.Culture != "" && role == "lead" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("## Team Culture\n\n")
		b.WriteString(team.Culture)
	}

	return b.String()
}

// mergeTools unions all tool sources and applies the denylist.
func (c *Composer) mergeTools(
	agent *db.Agent,
	agentSkills []*db.Skill,
	teamSkills []*db.Skill,
	teamID string,
	role string,
) []string {
	seen := make(map[string]bool)
	var tools []string

	addTool := func(name string) {
		if !seen[name] {
			seen[name] = true
			tools = append(tools, name)
		}
	}

	// Agent's own tools.
	for _, t := range parseStringArray(agent.Tools) {
		addTool(t)
	}

	// Skill tools (agent-level).
	for _, s := range agentSkills {
		for _, t := range parseStringArray(s.Tools) {
			addTool(t)
		}
	}

	// Team-wide skill tools.
	for _, s := range teamSkills {
		for _, t := range parseStringArray(s.Tools) {
			addTool(t)
		}
	}

	// Role-based tools.
	if teamID != "" {
		isSystem := teamID == "system"

		switch {
		case role == "lead" && isSystem:
			for _, t := range leadToolNames() {
				addTool(t)
			}
			for _, t := range systemLeadToolNames() {
				addTool(t)
			}
		case role == "lead":
			for _, t := range leadToolNames() {
				addTool(t)
			}
		case role == "worker" && !isSystem:
			for _, t := range workerToolNames() {
				addTool(t)
			}
			// System workers: no extra role-based tools.
		}
	}

	// Apply denylist.
	denied := make(map[string]bool)
	for _, t := range parseStringArray(agent.DisallowedTools) {
		denied[t] = true
	}

	if len(denied) == 0 {
		return tools
	}

	filtered := make([]string, 0, len(tools))
	for _, t := range tools {
		if !denied[t] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// resolveProvider cascades: agent → team → global default.
func (c *Composer) resolveProvider(agent *db.Agent, team *db.Team) string {
	if agent.Provider != "" {
		return agent.Provider
	}
	if team != nil && team.Provider != "" {
		return team.Provider
	}
	return c.defaultProvider
}

// resolveModel cascades: agent → team → global default.
func (c *Composer) resolveModel(agent *db.Agent, team *db.Team) string {
	if agent.Model != "" {
		return agent.Model
	}
	if team != nil && team.Model != "" {
		return team.Model
	}
	return c.defaultModel
}

// parseStringArray unmarshals a JSON array of strings from a json.RawMessage.
// Returns nil for nil/empty/invalid input.
func parseStringArray(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	return arr
}
