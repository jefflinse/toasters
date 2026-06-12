package mdfmt

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

// ParseSkill reads and parses a .md file as a SkillDef.
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

// ParseBytes parses raw skill definition content (frontmatter + body) into a SkillDef.
func ParseBytes(data []byte) (*SkillDef, error) {
	fmYAML, body, err := SplitFrontmatter(string(data))
	if err != nil {
		return nil, fmt.Errorf("parsing definition: %w", err)
	}
	return unmarshalSkill(fmYAML, body, "")
}

// SplitFrontmatter extracts the YAML block and body from content delimited by
// "---" lines. Returns the raw YAML string and the trimmed body. Delimiter
// lines are matched after trimming trailing whitespace (including \r for
// Windows line endings), so "--- " and "---\r" are both accepted.
//
// The opening delimiter must be the FIRST non-blank line. Accepting "---"
// anywhere meant text before the frontmatter was silently dropped, and a
// horizontal rule in a frontmatter-less skill misparsed as a frontmatter
// opener.
func SplitFrontmatter(content string) (string, string, error) {
	lines := strings.Split(content, "\n")

	// Find opening "---" — only blank lines may precede it.
	start := -1
	for i, l := range lines {
		trimmed := strings.TrimRight(l, " \t\r")
		if trimmed == "---" {
			start = i
			break
		}
		if trimmed != "" {
			return "", "", errors.New("frontmatter delimiter must be the first non-blank line")
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
