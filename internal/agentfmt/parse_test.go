package agentfmt_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jefflinse/toasters/internal/agentfmt"
	"gopkg.in/yaml.v3"
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
	def, err := agentfmt.ParseSkill(path)
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

func TestParseAgent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	temp := 0.7
	topP := 0.9

	content := `---
name: builder
description: Builds and tests code
mode: worker
skills:
  - code-review
  - testing
temperature: 0.7
top_p: 0.9
max_turns: 10
provider: anthropic
model: claude-sonnet-4-20250514
model_options:
  max_tokens: 4096
tools:
  - read_file
  - write_file
  - bash
disallowed_tools:
  - web_fetch
permission_mode: plan
permissions:
  bash:
    allow_all: true
mcp_servers:
  - name: github
    transport: stdio
memory: Remember to run tests after changes
color: orange
hidden: false
disabled: false
hooks:
  pre_tool_call:
    command: echo "calling tool"
background: false
isolation: container
---
You are a builder agent. Write clean, tested code.`

	path := writeTestFile(t, dir, "builder.md", content)
	def, err := agentfmt.ParseAgent(path)
	if err != nil {
		t.Fatalf("ParseAgent: %v", err)
	}

	if def.Name != "builder" {
		t.Errorf("Name = %q, want %q", def.Name, "builder")
	}
	if def.Description != "Builds and tests code" {
		t.Errorf("Description = %q, want %q", def.Description, "Builds and tests code")
	}
	if def.Mode != "worker" {
		t.Errorf("Mode = %q, want %q", def.Mode, "worker")
	}
	if !reflect.DeepEqual(def.Skills, []string{"code-review", "testing"}) {
		t.Errorf("Skills = %v, want %v", def.Skills, []string{"code-review", "testing"})
	}
	if def.Temperature == nil || *def.Temperature != temp {
		t.Errorf("Temperature = %v, want %v", def.Temperature, &temp)
	}
	if def.TopP == nil || *def.TopP != topP {
		t.Errorf("TopP = %v, want %v", def.TopP, &topP)
	}
	if def.MaxTurns != 10 {
		t.Errorf("MaxTurns = %d, want %d", def.MaxTurns, 10)
	}
	if def.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", def.Provider, "anthropic")
	}
	if def.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", def.Model, "claude-sonnet-4-20250514")
	}
	if def.ModelOptions == nil || def.ModelOptions["max_tokens"] != 4096 {
		t.Errorf("ModelOptions = %v, want max_tokens=4096", def.ModelOptions)
	}
	if !reflect.DeepEqual(def.Tools, []string{"read_file", "write_file", "bash"}) {
		t.Errorf("Tools = %v, want %v", def.Tools, []string{"read_file", "write_file", "bash"})
	}
	if !reflect.DeepEqual(def.DisallowedTools, []string{"web_fetch"}) {
		t.Errorf("DisallowedTools = %v, want %v", def.DisallowedTools, []string{"web_fetch"})
	}
	if def.PermissionMode != "plan" {
		t.Errorf("PermissionMode = %q, want %q", def.PermissionMode, "plan")
	}
	if def.Permissions == nil {
		t.Error("Permissions is nil, want non-nil")
	}
	if def.MCPServers == nil {
		t.Error("MCPServers is nil, want non-nil")
	}
	if def.Memory != "Remember to run tests after changes" {
		t.Errorf("Memory = %q, want %q", def.Memory, "Remember to run tests after changes")
	}
	// Color "orange" should be normalized to hex.
	if def.Color != "#FF8C00" {
		t.Errorf("Color = %q, want %q", def.Color, "#FF8C00")
	}
	if def.Hidden {
		t.Error("Hidden = true, want false")
	}
	if def.Disabled {
		t.Error("Disabled = true, want false")
	}
	if def.Hooks == nil {
		t.Error("Hooks is nil, want non-nil")
	}
	if def.Background {
		t.Error("Background = true, want false")
	}
	if def.Isolation != "container" {
		t.Errorf("Isolation = %q, want %q", def.Isolation, "container")
	}
	if def.Body != "You are a builder agent. Write clean, tested code." {
		t.Errorf("Body = %q, want %q", def.Body, "You are a builder agent. Write clean, tested code.")
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
	def, err := agentfmt.ParseTeam(path)
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
	defType, def, err := agentfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != agentfmt.DefSkill {
		t.Errorf("DefType = %q, want %q", defType, agentfmt.DefSkill)
	}
	skill, ok := def.(*agentfmt.SkillDef)
	if !ok {
		t.Fatalf("def is %T, want *SkillDef", def)
	}
	if skill.Name != "my-skill" {
		t.Errorf("Name = %q, want %q", skill.Name, "my-skill")
	}
}

func TestParseFile_AutoDetectAgent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: worker-bot
description: Does work
mode: worker
temperature: 0.5
---
Work hard.`

	path := writeTestFile(t, dir, "worker-bot.md", content)
	defType, def, err := agentfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != agentfmt.DefAgent {
		t.Errorf("DefType = %q, want %q", defType, agentfmt.DefAgent)
	}
	agent, ok := def.(*agentfmt.AgentDef)
	if !ok {
		t.Fatalf("def is %T, want *AgentDef", def)
	}
	if agent.Name != "worker-bot" {
		t.Errorf("Name = %q, want %q", agent.Name, "worker-bot")
	}
	if agent.Temperature == nil || *agent.Temperature != 0.5 {
		t.Errorf("Temperature = %v, want 0.5", agent.Temperature)
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
	defType, def, err := agentfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != agentfmt.DefTeam {
		t.Errorf("DefType = %q, want %q", defType, agentfmt.DefTeam)
	}
	team, ok := def.(*agentfmt.TeamDef)
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
	defType, _, err := agentfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != agentfmt.DefTeam {
		t.Errorf("DefType = %q, want %q", defType, agentfmt.DefTeam)
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
	defType, def, err := agentfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != agentfmt.DefSkill {
		t.Errorf("DefType = %q, want %q", defType, agentfmt.DefSkill)
	}
	skill := def.(*agentfmt.SkillDef)
	if skill.Name != "my-cool-skill" {
		t.Errorf("Name = %q, want %q", skill.Name, "my-cool-skill")
	}
}

func TestParseFile_AgentNameDefaultsToFilenameStem(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
description: A nameless agent
mode: worker
---
Work.`

	path := writeTestFile(t, dir, "unnamed-agent.md", content)
	_, def, err := agentfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	agent := def.(*agentfmt.AgentDef)
	if agent.Name != "unnamed-agent" {
		t.Errorf("Name = %q, want %q", agent.Name, "unnamed-agent")
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
	_, def, err := agentfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	team := def.(*agentfmt.TeamDef)
	if team.Name != "unnamed-team" {
		t.Errorf("Name = %q, want %q", team.Name, "unnamed-team")
	}
}

func TestParseAgent_TemperatureAbsent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: no-temp
description: Agent without temperature
mode: worker
---
Prompt.`

	path := writeTestFile(t, dir, "no-temp.md", content)
	def, err := agentfmt.ParseAgent(path)
	if err != nil {
		t.Fatalf("ParseAgent: %v", err)
	}
	if def.Temperature != nil {
		t.Errorf("Temperature = %v, want nil", def.Temperature)
	}
	if def.TopP != nil {
		t.Errorf("TopP = %v, want nil", def.TopP)
	}
}

func TestParseAgent_TemperaturePresent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: with-temp
description: Agent with temperature
mode: worker
temperature: 0.3
---
Prompt.`

	path := writeTestFile(t, dir, "with-temp.md", content)
	def, err := agentfmt.ParseAgent(path)
	if err != nil {
		t.Fatalf("ParseAgent: %v", err)
	}
	if def.Temperature == nil {
		t.Fatal("Temperature is nil, want non-nil")
	}
	if *def.Temperature != 0.3 {
		t.Errorf("Temperature = %v, want 0.3", *def.Temperature)
	}
}

func TestParseAgent_ColorNormalization(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	tests := []struct {
		name      string
		color     string
		wantColor string
	}{
		{"named color", "red", "#FF0000"},
		{"hex passthrough", "#FF9800", "#FF9800"},
		{"invalid color", "chartreuse", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Hex colors must be quoted in YAML (# starts a comment).
			colorVal := tt.color
			if len(colorVal) > 0 && colorVal[0] == '#' {
				colorVal = `"` + colorVal + `"`
			}
			content := "---\nname: test\ndescription: test\nmode: worker\ncolor: " + colorVal + "\n---\nPrompt."
			path := writeTestFile(t, dir, "color-"+tt.name+".md", content)
			def, err := agentfmt.ParseAgent(path)
			if err != nil {
				t.Fatalf("ParseAgent: %v", err)
			}
			if def.Color != tt.wantColor {
				t.Errorf("Color = %q, want %q", def.Color, tt.wantColor)
			}
		})
	}
}

func TestParseFile_EmptyFrontmatter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
---
Just a body with empty frontmatter.`

	path := writeTestFile(t, dir, "empty-fm.md", content)
	defType, def, err := agentfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != agentfmt.DefSkill {
		t.Errorf("DefType = %q, want %q", defType, agentfmt.DefSkill)
	}
	skill := def.(*agentfmt.SkillDef)
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
	_, _, err := agentfmt.ParseFile(path)
	if err == nil {
		t.Fatal("expected error for missing frontmatter, got nil")
	}
}

func TestParseFile_NonexistentFile(t *testing.T) {
	t.Parallel()
	_, _, err := agentfmt.ParseFile("/nonexistent/path/agent.md")
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
	_, _, err := agentfmt.ParseFile(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestParseAgent_NestedMapsPreserved(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: nested
description: Agent with nested maps
mode: worker
permissions:
  bash:
    allow_all: true
    deny_patterns:
      - "rm -rf /"
hooks:
  pre_tool_call:
    command: echo "pre"
    timeout: 30
model_options:
  max_tokens: 8192
  stop_sequences:
    - "END"
---
Prompt.`

	path := writeTestFile(t, dir, "nested.md", content)
	def, err := agentfmt.ParseAgent(path)
	if err != nil {
		t.Fatalf("ParseAgent: %v", err)
	}

	// Verify permissions nested map.
	if def.Permissions == nil {
		t.Fatal("Permissions is nil")
	}
	bashPerms, ok := def.Permissions["bash"]
	if !ok {
		t.Fatal("Permissions missing 'bash' key")
	}
	bashMap, ok := bashPerms.(map[string]any)
	if !ok {
		t.Fatalf("Permissions['bash'] is %T, want map[string]any", bashPerms)
	}
	if bashMap["allow_all"] != true {
		t.Errorf("Permissions['bash']['allow_all'] = %v, want true", bashMap["allow_all"])
	}

	// Verify hooks nested map.
	if def.Hooks == nil {
		t.Fatal("Hooks is nil")
	}
	preHook, ok := def.Hooks["pre_tool_call"]
	if !ok {
		t.Fatal("Hooks missing 'pre_tool_call' key")
	}
	preMap, ok := preHook.(map[string]any)
	if !ok {
		t.Fatalf("Hooks['pre_tool_call'] is %T, want map[string]any", preHook)
	}
	if preMap["command"] != "echo \"pre\"" {
		t.Errorf("Hooks['pre_tool_call']['command'] = %v, want %q", preMap["command"], `echo "pre"`)
	}

	// Verify model_options nested map.
	if def.ModelOptions == nil {
		t.Fatal("ModelOptions is nil")
	}
	if def.ModelOptions["max_tokens"] != 8192 {
		t.Errorf("ModelOptions['max_tokens'] = %v, want 8192", def.ModelOptions["max_tokens"])
	}
}

func TestParseBytes(t *testing.T) {
	t.Parallel()

	t.Run("skill", func(t *testing.T) {
		t.Parallel()
		data := []byte("---\nname: test-skill\ndescription: A skill\n---\nSkill body.")
		result, err := agentfmt.ParseBytes(data, agentfmt.DefSkill)
		if err != nil {
			t.Fatalf("ParseBytes: %v", err)
		}
		skill, ok := result.(*agentfmt.SkillDef)
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

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		data := []byte("---\nname: test-agent\ndescription: An agent\nmode: lead\n---\nAgent body.")
		result, err := agentfmt.ParseBytes(data, agentfmt.DefAgent)
		if err != nil {
			t.Fatalf("ParseBytes: %v", err)
		}
		agent, ok := result.(*agentfmt.AgentDef)
		if !ok {
			t.Fatalf("result is %T, want *AgentDef", result)
		}
		if agent.Mode != "lead" {
			t.Errorf("Mode = %q, want %q", agent.Mode, "lead")
		}
	})

	t.Run("team", func(t *testing.T) {
		t.Parallel()
		data := []byte("---\nname: test-team\ndescription: A team\nlead: boss\n---\nTeam body.")
		result, err := agentfmt.ParseBytes(data, agentfmt.DefTeam)
		if err != nil {
			t.Fatalf("ParseBytes: %v", err)
		}
		team, ok := result.(*agentfmt.TeamDef)
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
		_, err := agentfmt.ParseBytes(data, "unknown")
		if err == nil {
			t.Fatal("expected error for unknown type, got nil")
		}
	})

	t.Run("missing frontmatter", func(t *testing.T) {
		t.Parallel()
		data := []byte("No frontmatter here.")
		_, err := agentfmt.ParseBytes(data, agentfmt.DefSkill)
		if err == nil {
			t.Fatal("expected error for missing frontmatter, got nil")
		}
	})

	t.Run("name defaults to empty for bytes", func(t *testing.T) {
		t.Parallel()
		data := []byte("---\ndescription: No name\n---\nBody.")
		result, err := agentfmt.ParseBytes(data, agentfmt.DefSkill)
		if err != nil {
			t.Fatalf("ParseBytes: %v", err)
		}
		skill := result.(*agentfmt.SkillDef)
		// No filename to derive from, so name should be empty.
		if skill.Name != "" {
			t.Errorf("Name = %q, want empty", skill.Name)
		}
	})
}

func TestParseFile_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Parse an agent, marshal its frontmatter back to YAML, write a new file,
	// and parse again — the result should be equivalent.
	content := `---
name: roundtrip
description: Round-trip test agent
mode: lead
temperature: 0.8
max_turns: 5
provider: anthropic
model: claude-sonnet-4-20250514
tools:
  - read_file
  - write_file
color: "#FF9800"
---
Round-trip prompt body.`

	path := writeTestFile(t, dir, "roundtrip.md", content)
	def, err := agentfmt.ParseAgent(path)
	if err != nil {
		t.Fatalf("ParseAgent (first pass): %v", err)
	}

	// Marshal the struct back to YAML (excluding Body).
	fmBytes, err := yaml.Marshal(def)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}

	// Reconstruct the file.
	reconstructed := "---\n" + string(fmBytes) + "---\n" + def.Body

	path2 := writeTestFile(t, dir, "roundtrip2.md", reconstructed)
	def2, err := agentfmt.ParseAgent(path2)
	if err != nil {
		t.Fatalf("ParseAgent (second pass): %v", err)
	}

	// Compare key fields.
	if def.Name != def2.Name {
		t.Errorf("Name mismatch: %q vs %q", def.Name, def2.Name)
	}
	if def.Description != def2.Description {
		t.Errorf("Description mismatch: %q vs %q", def.Description, def2.Description)
	}
	if def.Mode != def2.Mode {
		t.Errorf("Mode mismatch: %q vs %q", def.Mode, def2.Mode)
	}
	if (def.Temperature == nil) != (def2.Temperature == nil) {
		t.Errorf("Temperature nil mismatch: %v vs %v", def.Temperature, def2.Temperature)
	} else if def.Temperature != nil && *def.Temperature != *def2.Temperature {
		t.Errorf("Temperature mismatch: %v vs %v", *def.Temperature, *def2.Temperature)
	}
	if def.MaxTurns != def2.MaxTurns {
		t.Errorf("MaxTurns mismatch: %d vs %d", def.MaxTurns, def2.MaxTurns)
	}
	if def.Provider != def2.Provider {
		t.Errorf("Provider mismatch: %q vs %q", def.Provider, def2.Provider)
	}
	if def.Model != def2.Model {
		t.Errorf("Model mismatch: %q vs %q", def.Model, def2.Model)
	}
	if !reflect.DeepEqual(def.Tools, def2.Tools) {
		t.Errorf("Tools mismatch: %v vs %v", def.Tools, def2.Tools)
	}
	if def.Color != def2.Color {
		t.Errorf("Color mismatch: %q vs %q", def.Color, def2.Color)
	}
	if def.Body != def2.Body {
		t.Errorf("Body mismatch: %q vs %q", def.Body, def2.Body)
	}
}

func TestDetectFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		fm   map[string]any
		want agentfmt.Format
	}{
		{
			name: "toasters format",
			fm:   map[string]any{"name": "test", "mode": "worker", "temperature": 0.5},
			want: agentfmt.FormatToasters,
		},
		{
			name: "claude code camelCase maxTurns",
			fm:   map[string]any{"name": "test", "maxTurns": 10},
			want: agentfmt.FormatClaudeCode,
		},
		{
			name: "claude code camelCase disallowedTools",
			fm:   map[string]any{"name": "test", "disallowedTools": []string{"bash"}},
			want: agentfmt.FormatClaudeCode,
		},
		{
			name: "claude code camelCase mcpServers",
			fm:   map[string]any{"name": "test", "mcpServers": []any{}},
			want: agentfmt.FormatClaudeCode,
		},
		{
			name: "opencode steps field",
			fm:   map[string]any{"name": "test", "steps": []any{}},
			want: agentfmt.FormatOpenCode,
		},
		{
			name: "opencode disable field",
			fm:   map[string]any{"name": "test", "disable": true},
			want: agentfmt.FormatOpenCode,
		},
		{
			name: "opencode permission singular",
			fm:   map[string]any{"name": "test", "permission": "ask"},
			want: agentfmt.FormatOpenCode,
		},
		{
			name: "empty frontmatter",
			fm:   map[string]any{},
			want: agentfmt.FormatToasters,
		},
		{
			name: "nil frontmatter",
			fm:   nil,
			want: agentfmt.FormatToasters,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := agentfmt.DetectFormat(tt.fm)
			if got != tt.want {
				t.Errorf("DetectFormat(%v) = %q, want %q", tt.fm, got, tt.want)
			}
		})
	}
}

func TestParseFile_AgentDetectedByProviderField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: provider-agent
description: Agent detected by provider field
provider: openai
---
Prompt.`

	path := writeTestFile(t, dir, "provider-agent.md", content)
	defType, _, err := agentfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != agentfmt.DefAgent {
		t.Errorf("DefType = %q, want %q", defType, agentfmt.DefAgent)
	}
}

func TestParseFile_AgentDetectedByToolsField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: tools-agent
description: Agent detected by tools field
tools:
  - bash
---
Prompt.`

	path := writeTestFile(t, dir, "tools-agent.md", content)
	defType, _, err := agentfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != agentfmt.DefAgent {
		t.Errorf("DefType = %q, want %q", defType, agentfmt.DefAgent)
	}
}

func TestParseFile_AgentDetectedByColorField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: color-agent
description: Agent detected by color field
color: blue
---
Prompt.`

	path := writeTestFile(t, dir, "color-agent.md", content)
	defType, _, err := agentfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != agentfmt.DefAgent {
		t.Errorf("DefType = %q, want %q", defType, agentfmt.DefAgent)
	}
}

func TestParseAgent_EmptyBody(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: no-body
description: Agent with no body
mode: worker
---`

	path := writeTestFile(t, dir, "no-body.md", content)
	def, err := agentfmt.ParseAgent(path)
	if err != nil {
		t.Fatalf("ParseAgent: %v", err)
	}
	if def.Body != "" {
		t.Errorf("Body = %q, want empty", def.Body)
	}
}

func TestParseAgent_MultilineBody(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: multi
description: Agent with multiline body
mode: worker
---
Line one.

Line two.

Line three.`

	path := writeTestFile(t, dir, "multi.md", content)
	def, err := agentfmt.ParseAgent(path)
	if err != nil {
		t.Fatalf("ParseAgent: %v", err)
	}
	want := "Line one.\n\nLine two.\n\nLine three."
	if def.Body != want {
		t.Errorf("Body = %q, want %q", def.Body, want)
	}
}

func TestParseAgent_MCPServersAsList(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: mcp-list
description: Agent with MCP servers as list
mode: worker
mcp_servers:
  - name: github
    transport: stdio
  - name: jira
    transport: sse
---
Prompt.`

	path := writeTestFile(t, dir, "mcp-list.md", content)
	def, err := agentfmt.ParseAgent(path)
	if err != nil {
		t.Fatalf("ParseAgent: %v", err)
	}
	if def.MCPServers == nil {
		t.Fatal("MCPServers is nil, want non-nil")
	}
	// Should be a list.
	servers, ok := def.MCPServers.([]any)
	if !ok {
		t.Fatalf("MCPServers is %T, want []any", def.MCPServers)
	}
	if len(servers) != 2 {
		t.Errorf("MCPServers length = %d, want 2", len(servers))
	}
}

func TestParseAgent_MCPServersAsMap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: mcp-map
description: Agent with MCP servers as map
mode: worker
mcp_servers:
  github:
    transport: stdio
  jira:
    transport: sse
---
Prompt.`

	path := writeTestFile(t, dir, "mcp-map.md", content)
	def, err := agentfmt.ParseAgent(path)
	if err != nil {
		t.Fatalf("ParseAgent: %v", err)
	}
	if def.MCPServers == nil {
		t.Fatal("MCPServers is nil, want non-nil")
	}
	// Should be a map.
	servers, ok := def.MCPServers.(map[string]any)
	if !ok {
		t.Fatalf("MCPServers is %T, want map[string]any", def.MCPServers)
	}
	if len(servers) != 2 {
		t.Errorf("MCPServers length = %d, want 2", len(servers))
	}
}

func TestParseSkill_FileReadError(t *testing.T) {
	t.Parallel()
	_, err := agentfmt.ParseSkill("/nonexistent/skill.md")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestParseAgent_FileReadError(t *testing.T) {
	t.Parallel()
	_, err := agentfmt.ParseAgent("/nonexistent/agent.md")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestParseTeam_FileReadError(t *testing.T) {
	t.Parallel()
	_, err := agentfmt.ParseTeam("/nonexistent/team.md")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestParseFile_ClaudeCodeFormat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: cc-agent
description: A Claude Code agent
model: sonnet
maxTurns: 10
disallowedTools:
  - web_fetch
color: red
---
You are a Claude Code agent.`

	path := writeTestFile(t, dir, "cc-agent.md", content)
	defType, def, err := agentfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != agentfmt.DefAgent {
		t.Errorf("DefType = %q, want %q", defType, agentfmt.DefAgent)
	}
	agent, ok := def.(*agentfmt.AgentDef)
	if !ok {
		t.Fatalf("def is %T, want *AgentDef", def)
	}
	if agent.Name != "cc-agent" {
		t.Errorf("Name = %q, want %q", agent.Name, "cc-agent")
	}
	// Model alias should be expanded.
	if agent.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", agent.Model, "claude-sonnet-4-20250514")
	}
	if agent.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", agent.Provider, "anthropic")
	}
	if agent.MaxTurns != 10 {
		t.Errorf("MaxTurns = %d, want %d", agent.MaxTurns, 10)
	}
	if !reflect.DeepEqual(agent.DisallowedTools, []string{"web_fetch"}) {
		t.Errorf("DisallowedTools = %v, want [web_fetch]", agent.DisallowedTools)
	}
	if agent.Color != "#FF0000" {
		t.Errorf("Color = %q, want %q", agent.Color, "#FF0000")
	}
	if agent.Body != "You are a Claude Code agent." {
		t.Errorf("Body = %q, want %q", agent.Body, "You are a Claude Code agent.")
	}
}

func TestParseFile_OpenCodeFormat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	content := `---
name: oc-agent
description: An OpenCode agent
provider: anthropic/claude-sonnet-4-20250514
steps: 25
disable: true
permission: auto
color: cyan
---
You are an OpenCode agent.`

	path := writeTestFile(t, dir, "oc-agent.md", content)
	defType, def, err := agentfmt.ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if defType != agentfmt.DefAgent {
		t.Errorf("DefType = %q, want %q", defType, agentfmt.DefAgent)
	}
	agent, ok := def.(*agentfmt.AgentDef)
	if !ok {
		t.Fatalf("def is %T, want *AgentDef", def)
	}
	if agent.Name != "oc-agent" {
		t.Errorf("Name = %q, want %q", agent.Name, "oc-agent")
	}
	if agent.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", agent.Provider, "anthropic")
	}
	if agent.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", agent.Model, "claude-sonnet-4-20250514")
	}
	if agent.MaxTurns != 25 {
		t.Errorf("MaxTurns = %d, want %d", agent.MaxTurns, 25)
	}
	if !agent.Disabled {
		t.Error("Disabled = false, want true")
	}
	wantPerms := map[string]any{"_mode": "auto"}
	if !reflect.DeepEqual(agent.Permissions, wantPerms) {
		t.Errorf("Permissions = %v, want %v", agent.Permissions, wantPerms)
	}
	if agent.Color != "#00FFFF" {
		t.Errorf("Color = %q, want %q", agent.Color, "#00FFFF")
	}
	if agent.Body != "You are an OpenCode agent." {
		t.Errorf("Body = %q, want %q", agent.Body, "You are an OpenCode agent.")
	}
}
