package agentfmt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// maxDefinitionFileSize is the maximum size (in bytes) for definition files.
// Files larger than this are rejected to prevent excessive memory allocation
// from malicious or accidentally large files.
const maxDefinitionFileSize = 1 << 20 // 1 MiB

// readDefinitionFile reads a definition file after verifying it does not exceed
// maxDefinitionFileSize. Returns the file contents or an error.
func readDefinitionFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("reading definition file %s: %w", path, err)
	}
	if info.Size() > maxDefinitionFileSize {
		return nil, fmt.Errorf("definition file %s is too large (%d bytes, max %d)", path, info.Size(), maxDefinitionFileSize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading definition file %s: %w", path, err)
	}
	return data, nil
}

// teamFields are frontmatter keys that indicate a team definition.
var teamFields = map[string]bool{
	"lead":   true,
	"agents": true,
	"roles":  true,
}

// detectType examines raw frontmatter fields and returns the definition type.
func detectType(raw map[string]any) DefType {
	for key := range raw {
		if teamFields[key] {
			return DefTeam
		}
	}
	return DefSkill
}

// ParseFile reads a .md file and returns the parsed definition.
// It auto-detects the definition type based on frontmatter fields:
//   - Has "lead", "agents", or "roles" field → TeamDef
//   - Otherwise → SkillDef
//
// Returns (DefType, any, error) where any is *SkillDef or *TeamDef.
func ParseFile(path string) (DefType, any, error) {
	data, err := readDefinitionFile(path)
	if err != nil {
		return "", nil, err
	}

	fmYAML, body, err := SplitFrontmatter(string(data))
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
	data, err := readDefinitionFile(path)
	if err != nil {
		return nil, err
	}

	fmYAML, body, err := SplitFrontmatter(string(data))
	if err != nil {
		return nil, fmt.Errorf("parsing skill file %s: %w", path, err)
	}

	return unmarshalSkill(fmYAML, body, filenameStem(path))
}

// ParseTeam parses a .md file as a TeamDef.
func ParseTeam(path string) (*TeamDef, error) {
	data, err := readDefinitionFile(path)
	if err != nil {
		return nil, err
	}

	fmYAML, body, err := SplitFrontmatter(string(data))
	if err != nil {
		return nil, fmt.Errorf("parsing team file %s: %w", path, err)
	}

	return unmarshalTeam(fmYAML, body, filenameStem(path))
}

// ParseBytes parses raw content (frontmatter + body) into the specified type.
// Returns *SkillDef or *TeamDef depending on defType.
func ParseBytes(data []byte, defType DefType) (any, error) {
	fmYAML, body, err := SplitFrontmatter(string(data))
	if err != nil {
		return nil, fmt.Errorf("parsing definition: %w", err)
	}

	switch defType {
	case DefSkill:
		return unmarshalSkill(fmYAML, body, "")
	case DefTeam:
		return unmarshalTeam(fmYAML, body, "")
	default:
		return nil, fmt.Errorf("unknown definition type: %q", defType)
	}
}

// SplitFrontmatter extracts the YAML block and body from content delimited by
// "---" lines. Returns the raw YAML string and the trimmed body. Delimiter
// lines are matched after trimming trailing whitespace (including \r for
// Windows line endings), so "--- " and "---\r" are both accepted.
func SplitFrontmatter(content string) (string, string, error) {
	lines := strings.Split(content, "\n")

	// Find opening "---".
	start := -1
	for i, l := range lines {
		if strings.TrimRight(l, " \t\r") == "---" {
			start = i
			break
		}
	}
	if start == -1 {
		return "", "", errors.New("no frontmatter delimiter found")
	}

	// Find closing "---".
	end := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t\r") == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return "", "", errors.New("frontmatter closing delimiter not found")
	}

	fmYAML := strings.Join(lines[start+1:end], "\n")
	body := strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	return fmYAML, body, nil
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
