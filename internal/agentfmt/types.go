// Package agentfmt provides YAML-based frontmatter parsing for toasters agent,
// skill, and team definition files. Each definition is a Markdown file with
// YAML frontmatter delimited by "---" lines. The markdown body is the
// prompt/content.
package agentfmt

// DefType identifies the kind of definition.
type DefType string

const (
	DefSkill DefType = "skill"
	DefAgent DefType = "agent"
	DefTeam  DefType = "team"
)

// SkillDef represents a skill definition.
type SkillDef struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tools       []string `yaml:"tools,omitempty"`
	Body        string   `yaml:"-"` // markdown body (prompt content)
}

// AgentDef represents an agent definition (superset of Claude Code + OpenCode fields).
type AgentDef struct {
	// Identity
	Name        string `yaml:"name"`
	Description string `yaml:"description"`

	// Behavior
	Mode        string   `yaml:"mode,omitempty"`        // "lead", "worker" (default: "worker")
	Skills      []string `yaml:"skills,omitempty"`      // skill references for composition
	Temperature *float64 `yaml:"temperature,omitempty"` // pointer for optional
	TopP        *float64 `yaml:"top_p,omitempty"`       // pointer for optional
	MaxTurns    int      `yaml:"max_turns,omitempty"`

	// Provider/Model
	Provider     string         `yaml:"provider,omitempty"`
	Model        string         `yaml:"model,omitempty"`
	ModelOptions map[string]any `yaml:"model_options,omitempty"`

	// Tools
	Tools           []string `yaml:"tools,omitempty"`
	DisallowedTools []string `yaml:"disallowed_tools,omitempty"`

	// Permissions
	PermissionMode string         `yaml:"permission_mode,omitempty"`
	Permissions    map[string]any `yaml:"permissions,omitempty"`

	// MCP
	MCPServers any `yaml:"mcp_servers,omitempty"` // list or map, preserve as-is

	// Memory
	Memory string `yaml:"memory,omitempty"`

	// UI/Display
	Color    string `yaml:"color,omitempty"`
	Hidden   bool   `yaml:"hidden,omitempty"`
	Disabled bool   `yaml:"disabled,omitempty"`

	// Lifecycle
	Hooks      map[string]any `yaml:"hooks,omitempty"`
	Background bool           `yaml:"background,omitempty"`
	Isolation  string         `yaml:"isolation,omitempty"`

	Body string `yaml:"-"` // markdown body (system prompt)
}

// TeamDef represents a team definition.
type TeamDef struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Lead        string   `yaml:"lead"`             // agent name reference
	Agents      []string `yaml:"agents,omitempty"` // agent name references
	Skills      []string `yaml:"skills,omitempty"` // team-wide skills
	Provider    string   `yaml:"provider,omitempty"`
	Model       string   `yaml:"model,omitempty"`
	Body        string   `yaml:"-"` // markdown body (culture document)
}
