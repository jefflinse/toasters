package mdfmt_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jefflinse/toasters/internal/mdfmt"
)

// writeTestFile creates a temporary .md file with the given content and returns its path.
func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing test file %s: %v", path, err)
	}
	return path
}

func TestParseSkill(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: code-review
description: Reviews code for quality and correctness
tools:
  - read_file
  - grep
---
You are a code review specialist. Review the provided code carefully.`

	path := writeTestFile(t, dir, "code-review.md", content)
	def, err := mdfmt.ParseSkill(path)
	if err != nil {
		t.Fatalf("ParseSkill: %v", err)
	}

	if def.Name != "code-review" {
		t.Errorf("Name = %q, want %q", def.Name, "code-review")
	}
	if def.Description != "Reviews code for quality and correctness" {
		t.Errorf("Description = %q, want %q", def.Description, "Reviews code for quality and correctness")
	}
	if !reflect.DeepEqual(def.Tools, []string{"read_file", "grep"}) {
		t.Errorf("Tools = %v, want %v", def.Tools, []string{"read_file", "grep"})
	}
	if def.Body != "You are a code review specialist. Review the provided code carefully." {
		t.Errorf("Body = %q, want %q", def.Body, "You are a code review specialist. Review the provided code carefully.")
	}
}

func TestParseTeam(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: coding
description: Full-stack coding team
lead: coordinator
agents:
  - builder
  - reviewer
  - tester
skills:
  - code-review
provider: anthropic
model: claude-sonnet-4-20250514
---
We value clean code, thorough testing, and clear communication.`

	path := writeTestFile(t, dir, "coding.md", content)
	def, err := mdfmt.ParseTeam(path)
	if err != nil {
		t.Fatalf("ParseTeam: %v", err)
	}

	if def.Name != "coding" {
		t.Errorf("Name = %q, want %q", def.Name, "coding")
	}
	if def.Description != "Full-stack coding team" {
		t.Errorf("Description = %q, want %q", def.Description, "Full-stack coding team")
	}
	if def.Lead != "coordinator" {
		t.Errorf("Lead = %q, want %q", def.Lead, "coordinator")
	}
	if !reflect.DeepEqual(def.Agents, []string{"builder", "reviewer", "tester"}) {
		t.Errorf("Agents = %v, want %v", def.Agents, []string{"builder", "reviewer", "tester"})
	}
	if !reflect.DeepEqual(def.Skills, []string{"code-review"}) {
		t.Errorf("Skills = %v, want %v", def.Skills, []string{"code-review"})
	}
	if def.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", def.Provider, "anthropic")
	}
	if def.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", def.Model, "claude-sonnet-4-20250514")
	}
	if def.Body != "We value clean code, thorough testing, and clear communication." {
		t.Errorf("Body = %q, want %q", def.Body, "We value clean code, thorough testing, and clear communication.")
	}
}

func TestParseFile_AutoDetectSkill(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: my-skill
description: A simple skill
---
Do the thing.`

	path := writeTestFile(t, dir, "my-skill.md", content)
	defType, def, err := mdfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != mdfmt.DefSkill {
		t.Errorf("DefType = %q, want %q", defType, mdfmt.DefSkill)
	}
	skill, ok := def.(*mdfmt.SkillDef)
	if !ok {
		t.Fatalf("def is %T, want *SkillDef", def)
	}
	if skill.Name != "my-skill" {
		t.Errorf("Name = %q, want %q", skill.Name, "my-skill")
	}
}

func TestParseFile_AutoDetectTeam(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: my-team
description: A team
lead: boss
agents:
  - worker1
  - worker2
---
Team culture doc.`

	path := writeTestFile(t, dir, "my-team.md", content)
	defType, def, err := mdfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != mdfmt.DefTeam {
		t.Errorf("DefType = %q, want %q", defType, mdfmt.DefTeam)
	}
	team, ok := def.(*mdfmt.TeamDef)
	if !ok {
		t.Fatalf("def is %T, want *TeamDef", def)
	}
	if team.Lead != "boss" {
		t.Errorf("Lead = %q, want %q", team.Lead, "boss")
	}
}

func TestParseFile_AutoDetectTeamByAgentsField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Has "agents" but no "lead" — should still detect as team.
	content := `---
name: headless-team
description: A team without explicit lead
agents:
  - worker1
---
Body.`

	path := writeTestFile(t, dir, "headless-team.md", content)
	defType, _, err := mdfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != mdfmt.DefTeam {
		t.Errorf("DefType = %q, want %q", defType, mdfmt.DefTeam)
	}
}

func TestParseFile_NameDefaultsToFilenameStem(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// No name in frontmatter — should default to filename stem.
	content := `---
description: A nameless skill
---
Do stuff.`

	path := writeTestFile(t, dir, "my-cool-skill.md", content)
	defType, def, err := mdfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != mdfmt.DefSkill {
		t.Errorf("DefType = %q, want %q", defType, mdfmt.DefSkill)
	}
	skill := def.(*mdfmt.SkillDef)
	if skill.Name != "my-cool-skill" {
		t.Errorf("Name = %q, want %q", skill.Name, "my-cool-skill")
	}
}

func TestParseFile_TeamNameDefaultsToFilenameStem(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
description: A nameless team
lead: boss
---
Culture.`

	path := writeTestFile(t, dir, "unnamed-team.md", content)
	_, def, err := mdfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	team := def.(*mdfmt.TeamDef)
	if team.Name != "unnamed-team" {
		t.Errorf("Name = %q, want %q", team.Name, "unnamed-team")
	}
}

func TestParseFile_EmptyFrontmatter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
---
Just a body with empty frontmatter.`

	path := writeTestFile(t, dir, "empty-fm.md", content)
	defType, def, err := mdfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != mdfmt.DefSkill {
		t.Errorf("DefType = %q, want %q", defType, mdfmt.DefSkill)
	}
	skill := def.(*mdfmt.SkillDef)
	if skill.Name != "empty-fm" {
		t.Errorf("Name = %q, want %q", skill.Name, "empty-fm")
	}
	if skill.Body != "Just a body with empty frontmatter." {
		t.Errorf("Body = %q, want %q", skill.Body, "Just a body with empty frontmatter.")
	}
}

func TestParseFile_MissingFrontmatter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `No frontmatter here, just text.`

	path := writeTestFile(t, dir, "no-fm.md", content)
	_, _, err := mdfmt.ParseFile(path)
	if err == nil {
		t.Fatal("expected error for missing frontmatter, got nil")
	}
}

func TestParseFile_NonexistentFile(t *testing.T) {
	t.Parallel()
	_, _, err := mdfmt.ParseFile("/nonexistent/path/agent.md")
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
}

func TestParseFile_InvalidYAML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: [invalid yaml
  this is broken: {
---
Body.`

	path := writeTestFile(t, dir, "bad-yaml.md", content)
	_, _, err := mdfmt.ParseFile(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestParseBytes(t *testing.T) {
	t.Parallel()

	t.Run("skill", func(t *testing.T) {
		t.Parallel()
		data := []byte("---\nname: test-skill\ndescription: A skill\n---\nSkill body.")
		result, err := mdfmt.ParseBytes(data, mdfmt.DefSkill)
		if err != nil {
			t.Fatalf("ParseBytes: %v", err)
		}
		skill, ok := result.(*mdfmt.SkillDef)
		if !ok {
			t.Fatalf("result is %T, want *SkillDef", result)
		}
		if skill.Name != "test-skill" {
			t.Errorf("Name = %q, want %q", skill.Name, "test-skill")
		}
		if skill.Body != "Skill body." {
			t.Errorf("Body = %q, want %q", skill.Body, "Skill body.")
		}
	})

	t.Run("team", func(t *testing.T) {
		t.Parallel()
		data := []byte("---\nname: test-team\ndescription: A team\nlead: boss\n---\nTeam body.")
		result, err := mdfmt.ParseBytes(data, mdfmt.DefTeam)
		if err != nil {
			t.Fatalf("ParseBytes: %v", err)
		}
		team, ok := result.(*mdfmt.TeamDef)
		if !ok {
			t.Fatalf("result is %T, want *TeamDef", result)
		}
		if team.Lead != "boss" {
			t.Errorf("Lead = %q, want %q", team.Lead, "boss")
		}
	})

	t.Run("unknown type", func(t *testing.T) {
		t.Parallel()
		data := []byte("---\nname: test\n---\nBody.")
		_, err := mdfmt.ParseBytes(data, "unknown")
		if err == nil {
			t.Fatal("expected error for unknown type, got nil")
		}
	})

	t.Run("missing frontmatter", func(t *testing.T) {
		t.Parallel()
		data := []byte("No frontmatter here.")
		_, err := mdfmt.ParseBytes(data, mdfmt.DefSkill)
		if err == nil {
			t.Fatal("expected error for missing frontmatter, got nil")
		}
	})

	t.Run("name defaults to empty for bytes", func(t *testing.T) {
		t.Parallel()
		data := []byte("---\ndescription: No name\n---\nBody.")
		result, err := mdfmt.ParseBytes(data, mdfmt.DefSkill)
		if err != nil {
			t.Fatalf("ParseBytes: %v", err)
		}
		skill := result.(*mdfmt.SkillDef)
		// No filename to derive from, so name should be empty.
		if skill.Name != "" {
			t.Errorf("Name = %q, want empty", skill.Name)
		}
	})
}

func TestParseFile_ToolsOnlyClassifiedAsSkill(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// A definition with only "tools" (no other agent-only fields) should be
	// classified as a skill, since skills can also declare tools.
	content := `---
name: tools-only
description: Definition with only tools field
tools:
  - bash
---
Prompt.`

	path := writeTestFile(t, dir, "tools-only.md", content)
	defType, _, err := mdfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != mdfmt.DefSkill {
		t.Errorf("DefType = %q, want %q", defType, mdfmt.DefSkill)
	}
}

func TestParseFile_SkillWithToolsField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// A definition with name, description, and tools but no agent-only fields
	// should be classified as a skill.
	content := `---
name: code-review
description: Reviews code for quality
tools:
  - read_file
  - grep
---
Review the code carefully.`

	path := writeTestFile(t, dir, "code-review.md", content)
	defType, def, err := mdfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != mdfmt.DefSkill {
		t.Errorf("DefType = %q, want %q", defType, mdfmt.DefSkill)
	}
	skill, ok := def.(*mdfmt.SkillDef)
	if !ok {
		t.Fatalf("def is %T, want *SkillDef", def)
	}
	if !reflect.DeepEqual(skill.Tools, []string{"read_file", "grep"}) {
		t.Errorf("Tools = %v, want %v", skill.Tools, []string{"read_file", "grep"})
	}
}

func TestParseFile_SkillWithEmptyToolsList(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// A definition with tools: [] (empty list) and no agent-only fields
	// should be classified as a skill.
	content := `---
name: empty-tools
description: Skill with empty tools list
tools: []
---
A skill with no tools.`

	path := writeTestFile(t, dir, "empty-tools.md", content)
	defType, _, err := mdfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != mdfmt.DefSkill {
		t.Errorf("DefType = %q, want %q", defType, mdfmt.DefSkill)
	}
}
