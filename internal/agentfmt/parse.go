package agentfmt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jefflinse/toasters/internal/frontmatter"
	"gopkg.in/yaml.v3"
)

// agentOnlyFields are frontmatter keys that indicate an agent definition
// (as opposed to a skill). If any of these are present, the definition is
// classified as an agent.
var agentOnlyFields = map[string]bool{
	"skills":           true,
	"mode":             true,
	"provider":         true,
	"temperature":      true,
	"model":            true,
	"tools":            true,
	"disallowed_tools": true,
	"permission_mode":  true,
	"permissions":      true,
	"mcp_servers":      true,
	"memory":           true,
	"hooks":            true,
	"background":       true,
	"isolation":        true,
	"hidden":           true,
	"disabled":         true,
	"top_p":            true,
	"max_turns":        true,
	"model_options":    true,
	"color":            true,
}

// teamFields are frontmatter keys that indicate a team definition.
var teamFields = map[string]bool{
	"lead":   true,
	"agents": true,
}

// detectType examines raw frontmatter fields and returns the definition type.
//
// Heuristic:
//   - Has "lead" or "agents" → team
//   - Has any agent-only field → agent
//   - Otherwise → skill
func detectType(raw map[string]any) DefType {
	for key := range raw {
		if teamFields[key] {
			return DefTeam
		}
	}
	for key := range raw {
		if agentOnlyFields[key] {
			return DefAgent
		}
	}
	return DefSkill
}

// ParseFile reads a .md file and returns the parsed definition.
// It auto-detects the definition type based on frontmatter fields:
//   - Has "lead" or "agents" field → TeamDef
//   - Has agent-specific fields → AgentDef
//   - Otherwise → SkillDef
//
// Returns (DefType, any, error) where any is *SkillDef, *AgentDef, or *TeamDef.
func ParseFile(path string) (DefType, any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, fmt.Errorf("reading definition file %s: %w", path, err)
	}

	fmYAML, body, err := splitFrontmatter(string(data))
	if err != nil {
		return "", nil, fmt.Errorf("parsing definition file %s: %w", path, err)
	}

	// Unmarshal into a generic map for type detection.
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &raw); err != nil {
		return "", nil, fmt.Errorf("parsing frontmatter YAML in %s: %w", path, err)
	}
	if raw == nil {
		raw = make(map[string]any)
	}

	defType := detectType(raw)
	stem := filenameStem(path)

	switch defType {
	case DefTeam:
		def, err := unmarshalTeam(fmYAML, body, stem)
		if err != nil {
			return "", nil, fmt.Errorf("parsing team definition %s: %w", path, err)
		}
		return DefTeam, def, nil

	case DefAgent:
		def, err := unmarshalAgent(fmYAML, body, stem)
		if err != nil {
			return "", nil, fmt.Errorf("parsing agent definition %s: %w", path, err)
		}
		return DefAgent, def, nil

	default:
		def, err := unmarshalSkill(fmYAML, body, stem)
		if err != nil {
			return "", nil, fmt.Errorf("parsing skill definition %s: %w", path, err)
		}
		return DefSkill, def, nil
	}
}

// ParseSkill parses a .md file as a SkillDef.
func ParseSkill(path string) (*SkillDef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading skill file %s: %w", path, err)
	}

	fmYAML, body, err := splitFrontmatter(string(data))
	if err != nil {
		return nil, fmt.Errorf("parsing skill file %s: %w", path, err)
	}

	return unmarshalSkill(fmYAML, body, filenameStem(path))
}

// ParseAgent parses a .md file as an AgentDef.
func ParseAgent(path string) (*AgentDef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading agent file %s: %w", path, err)
	}

	fmYAML, body, err := splitFrontmatter(string(data))
	if err != nil {
		return nil, fmt.Errorf("parsing agent file %s: %w", path, err)
	}

	return unmarshalAgent(fmYAML, body, filenameStem(path))
}

// ParseTeam parses a .md file as a TeamDef.
func ParseTeam(path string) (*TeamDef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading team file %s: %w", path, err)
	}

	fmYAML, body, err := splitFrontmatter(string(data))
	if err != nil {
		return nil, fmt.Errorf("parsing team file %s: %w", path, err)
	}

	return unmarshalTeam(fmYAML, body, filenameStem(path))
}

// ParseBytes parses raw content (frontmatter + body) into the specified type.
// Returns *SkillDef, *AgentDef, or *TeamDef depending on defType.
func ParseBytes(data []byte, defType DefType) (any, error) {
	fmYAML, body, err := splitFrontmatter(string(data))
	if err != nil {
		return nil, fmt.Errorf("parsing definition: %w", err)
	}

	switch defType {
	case DefSkill:
		return unmarshalSkill(fmYAML, body, "")
	case DefAgent:
		return unmarshalAgent(fmYAML, body, "")
	case DefTeam:
		return unmarshalTeam(fmYAML, body, "")
	default:
		return nil, fmt.Errorf("unknown definition type: %q", defType)
	}
}

// splitFrontmatter uses the existing frontmatter.Split to extract the YAML
// block and body from content. Returns the raw YAML string (joined frontmatter
// lines) and the body.
func splitFrontmatter(content string) (string, string, error) {
	fmLines, body, err := frontmatter.Split(content)
	if err != nil {
		return "", "", err
	}
	return strings.Join(fmLines, "\n"), strings.TrimSpace(body), nil
}

// filenameStem returns the filename without extension.
func filenameStem(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// unmarshalSkill parses YAML frontmatter into a SkillDef.
func unmarshalSkill(fmYAML, body, defaultName string) (*SkillDef, error) {
	var def SkillDef
	if fmYAML != "" {
		if err := yaml.Unmarshal([]byte(fmYAML), &def); err != nil {
			return nil, fmt.Errorf("unmarshaling skill frontmatter: %w", err)
		}
	}
	if def.Name == "" {
		def.Name = defaultName
	}
	def.Body = body
	return &def, nil
}

// unmarshalAgent parses YAML frontmatter into an AgentDef.
func unmarshalAgent(fmYAML, body, defaultName string) (*AgentDef, error) {
	var def AgentDef
	if fmYAML != "" {
		if err := yaml.Unmarshal([]byte(fmYAML), &def); err != nil {
			return nil, fmt.Errorf("unmarshaling agent frontmatter: %w", err)
		}
	}
	if def.Name == "" {
		def.Name = defaultName
	}
	def.Color = NormalizeColor(def.Color)
	def.Body = body
	return &def, nil
}

// unmarshalTeam parses YAML frontmatter into a TeamDef.
func unmarshalTeam(fmYAML, body, defaultName string) (*TeamDef, error) {
	var def TeamDef
	if fmYAML != "" {
		if err := yaml.Unmarshal([]byte(fmYAML), &def); err != nil {
			return nil, fmt.Errorf("unmarshaling team frontmatter: %w", err)
		}
	}
	if def.Name == "" {
		def.Name = defaultName
	}
	def.Body = body
	return &def, nil
}
