// Package mdfmt provides YAML-based frontmatter parsing for toasters skill
// definition files. Each definition is a Markdown file with YAML frontmatter
// delimited by "---" lines. The markdown body is the prompt/content.
package mdfmt

// SkillDef represents a skill definition.
type SkillDef struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tools       []string `yaml:"tools,omitempty"`
	Body        string   `yaml:"-"` // markdown body (prompt content)
}
