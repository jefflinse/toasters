// Package mdfmt provides YAML-based frontmatter parsing for toasters
// skill and team definition files. Each definition is a Markdown file with
// YAML frontmatter delimited by "---" lines. The markdown body is the
// prompt/content.
package mdfmt

// DefType identifies the kind of definition.
type DefType string

const (
	DefSkill DefType = "skill"
	DefTeam  DefType = "team"
)

// SkillDef represents a skill definition.
type SkillDef struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tools       []string `yaml:"tools,omitempty"`
	Body        string   `yaml:"-"` // markdown body (prompt content)
}

// TeamDef represents a team definition.
type TeamDef struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Lead        string   `yaml:"lead"`             // role name reference
	Roles       []string `yaml:"roles,omitempty"`  // role name references (prompt engine)
	Agents      []string `yaml:"agents,omitempty"` // legacy agent name references
	Skills      []string `yaml:"skills,omitempty"` // team-wide skills
	Provider    string   `yaml:"provider,omitempty"`
	Model       string   `yaml:"model,omitempty"`
	Body        string   `yaml:"-"` // markdown body (culture document)
}
