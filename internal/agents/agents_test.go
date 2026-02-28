package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- ParseFile tools block tests ---

func TestParseFile_NoToolsBlock(t *testing.T) {
	content := "---\ndescription: A builder agent\nmode: worker\n---\nDo stuff."
	a := parseContent(t, "builder", content)
	if a.HasToolsBlock {
		t.Error("expected HasToolsBlock=false for agent with no tools: block")
	}
	if a.Tools != nil {
		t.Errorf("expected Tools=nil, got %v", a.Tools)
	}
}

func TestParseFile_ToolsBlock(t *testing.T) {
	content := "---\ndescription: Docs writer\ntools:\n  bash: false\n  write: false\n---\nWrite docs."
	a := parseContent(t, "docs-writer", content)
	if !a.HasToolsBlock {
		t.Error("expected HasToolsBlock=true")
	}
	if a.Tools["bash"] != false {
		t.Errorf("expected bash=false, got %v", a.Tools["bash"])
	}
	if a.Tools["write"] != false {
		t.Errorf("expected write=false, got %v", a.Tools["write"])
	}
}

func TestParseFile_ToolsBlockWithAllowedTool(t *testing.T) {
	content := "---\ndescription: Mixed\ntools:\n  bash: false\n  write: true\n---\nBody."
	a := parseContent(t, "mixed", content)
	if !a.HasToolsBlock {
		t.Error("expected HasToolsBlock=true")
	}
	if a.Tools["bash"] != false {
		t.Errorf("expected bash=false, got %v", a.Tools["bash"])
	}
	if a.Tools["write"] != true {
		t.Errorf("expected write=true, got %v", a.Tools["write"])
	}
}

// parseContent writes content to a temp file and calls ParseFile.
func parseContent(t *testing.T, name, content string) Agent {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	a, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	return a
}

// --- SetCoordinator tests ---

func TestSetCoordinator(t *testing.T) {
	teamDir := t.TempDir()
	agentsDir := filepath.Join(teamDir, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("creating agents subdir: %v", err)
	}

	builderContent := "---\nmode: worker\n---\nbuilder body"
	coordContent := "---\nmode: primary\n---\ncoord body"

	writeFile := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(agentsDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	writeFile("builder.md", builderContent)
	writeFile("coordinator.md", coordContent)

	if err := SetCoordinator(teamDir, "builder"); err != nil {
		t.Fatalf("SetCoordinator: %v", err)
	}

	// builder.md should now have mode: primary
	builderAgent, err := ParseFile(filepath.Join(agentsDir, "builder.md"))
	if err != nil {
		t.Fatalf("ParseFile builder.md: %v", err)
	}
	if builderAgent.Mode != "primary" {
		t.Errorf("builder.md: got mode=%q, want %q", builderAgent.Mode, "primary")
	}

	// coordinator.md should now have mode: worker
	coordAgent, err := ParseFile(filepath.Join(agentsDir, "coordinator.md"))
	if err != nil {
		t.Fatalf("ParseFile coordinator.md: %v", err)
	}
	if coordAgent.Mode != "worker" {
		t.Errorf("coordinator.md: got mode=%q, want %q", coordAgent.Mode, "worker")
	}

	// Nonexistent agent should return an error.
	if err := SetCoordinator(teamDir, "nonexistent"); err == nil {
		t.Error("expected error for nonexistent agent, got nil")
	}
}

// --- DiscoverTeams tests ---

func TestDiscoverTeams(t *testing.T) {
	t.Run("happy path: agents subdir with md files", func(t *testing.T) {
		teamsDir := t.TempDir()
		agentsDir := filepath.Join(teamsDir, "coding", "agents")
		if err := os.MkdirAll(agentsDir, 0o755); err != nil {
			t.Fatalf("creating agents subdir: %v", err)
		}

		writeAgentFile := func(name, content string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(agentsDir, name), []byte(content), 0o644); err != nil {
				t.Fatalf("writing %s: %v", name, err)
			}
		}
		writeAgentFile("coordinator.md", "---\nmode: primary\ndescription: Coding coordinator\n---\nCoordinate coding work.")
		writeAgentFile("builder.md", "---\nmode: worker\ndescription: Builds things\n---\nBuild stuff.")

		teams, err := DiscoverTeams(teamsDir)
		if err != nil {
			t.Fatalf("DiscoverTeams: %v", err)
		}
		if len(teams) != 1 {
			t.Fatalf("got %d teams, want 1", len(teams))
		}

		team := teams[0]
		if team.Name != "coding" {
			t.Errorf("team.Name = %q, want %q", team.Name, "coding")
		}
		if team.Dir != filepath.Join(teamsDir, "coding") {
			t.Errorf("team.Dir = %q, want team root (not agents subdir)", team.Dir)
		}
		if team.Coordinator == nil {
			t.Fatal("expected a coordinator, got nil")
		}
		if team.Coordinator.Name != "coordinator" {
			t.Errorf("coordinator.Name = %q, want %q", team.Coordinator.Name, "coordinator")
		}
		if len(team.Workers) != 1 || team.Workers[0].Name != "builder" {
			t.Errorf("workers = %v, want [builder]", team.Workers)
		}
	})

	t.Run("graceful empty: no agents subdir", func(t *testing.T) {
		teamsDir := t.TempDir()
		// Create team dir but no agents/ subdir inside it.
		if err := os.MkdirAll(filepath.Join(teamsDir, "empty-team"), 0o755); err != nil {
			t.Fatalf("creating team dir: %v", err)
		}

		teams, err := DiscoverTeams(teamsDir)
		if err != nil {
			t.Fatalf("DiscoverTeams: %v", err)
		}
		if len(teams) != 1 {
			t.Fatalf("got %d teams, want 1 (empty team should still be returned)", len(teams))
		}

		team := teams[0]
		if team.Name != "empty-team" {
			t.Errorf("team.Name = %q, want %q", team.Name, "empty-team")
		}
		if team.Coordinator != nil {
			t.Errorf("expected nil coordinator for empty team, got %v", team.Coordinator)
		}
		if len(team.Workers) != 0 {
			t.Errorf("expected 0 workers for empty team, got %d", len(team.Workers))
		}
	})

	t.Run("nonexistent teams dir returns empty slice", func(t *testing.T) {
		teams, err := DiscoverTeams("/nonexistent/path/that/does/not/exist")
		if err != nil {
			t.Fatalf("DiscoverTeams: %v", err)
		}
		if len(teams) != 0 {
			t.Errorf("got %d teams, want 0", len(teams))
		}
	})
}

// --- BuildTeamCoordinatorPrompt tests ---

func TestBuildTeamCoordinatorPrompt_WithCoordinatorAndWorkers(t *testing.T) {
	coord := Agent{Name: "lead", Body: "I lead the team."}
	team := Team{
		Name:        "backend",
		Dir:         "/teams/backend",
		Coordinator: &coord,
		Workers: []Agent{
			{Name: "coder", Description: "Writes code"},
			{Name: "reviewer", Description: "Reviews PRs"},
		},
	}

	got := BuildTeamCoordinatorPrompt(team, "/jobs/job-123")

	// Must contain coordinator body.
	if !strings.Contains(got, "I lead the team.") {
		t.Error("missing coordinator body")
	}
	// Must contain team name in instructions.
	if !strings.Contains(got, `"backend"`) {
		t.Error("missing team name in instructions")
	}
	// Must list workers.
	if !strings.Contains(got, "`coder`: Writes code") {
		t.Error("missing coder in roster")
	}
	if !strings.Contains(got, "`reviewer`: Reviews PRs") {
		t.Error("missing reviewer in roster")
	}
	// Must contain job directory.
	if !strings.Contains(got, "/jobs/job-123") {
		t.Error("missing job directory path")
	}
	// Must contain REPORT.md and BLOCKER.md instructions.
	if !strings.Contains(got, "REPORT.md") {
		t.Error("missing REPORT.md instructions")
	}
	if !strings.Contains(got, "BLOCKER.md") {
		t.Error("missing BLOCKER.md instructions")
	}
}

func TestBuildTeamCoordinatorPrompt_NilCoordinator(t *testing.T) {
	team := Team{
		Name:        "frontend",
		Dir:         "/teams/frontend",
		Coordinator: nil,
		Workers: []Agent{
			{Name: "styler", Description: "Writes CSS"},
		},
	}

	got := BuildTeamCoordinatorPrompt(team, "/jobs/job-456")

	// Should NOT contain the separator that comes after coordinator body.
	// The prompt should start directly with the instructions.
	if !strings.Contains(got, "## Toasters Team Coordinator Instructions") {
		t.Error("missing instructions header")
	}
	if !strings.Contains(got, "`styler`: Writes CSS") {
		t.Error("missing worker in roster")
	}
}

func TestBuildTeamCoordinatorPrompt_NoWorkers(t *testing.T) {
	team := Team{
		Name:        "solo",
		Dir:         "/teams/solo",
		Coordinator: nil,
		Workers:     nil,
	}

	got := BuildTeamCoordinatorPrompt(team, "/jobs/job-789")

	if !strings.Contains(got, "(no workers configured)") {
		t.Error("expected '(no workers configured)' when no workers")
	}
}

// --- Discover tests ---

func TestDiscover_NonexistentDir(t *testing.T) {
	agents, err := Discover("/nonexistent/path/that/does/not/exist")
	if err != nil {
		t.Fatalf("Discover: expected nil error for nonexistent dir, got: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected empty slice, got %d agents", len(agents))
	}
}

func TestDiscover_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	agents, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected empty slice for dir with no .md files, got %d agents", len(agents))
	}
}

func TestDiscover_LoadsAgents(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha.md"), []byte("---\ndescription: Alpha agent\nmode: worker\n---\nAlpha body."), 0o644); err != nil {
		t.Fatalf("writing alpha.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beta.md"), []byte("---\ndescription: Beta agent\nmode: primary\n---\nBeta body."), 0o644); err != nil {
		t.Fatalf("writing beta.md: %v", err)
	}
	// Non-.md file should be ignored.
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not an agent"), 0o644); err != nil {
		t.Fatalf("writing readme.txt: %v", err)
	}

	agents, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}
}

// --- BuildRegistry tests ---

func TestBuildRegistry_Empty(t *testing.T) {
	reg := BuildRegistry(nil, "")
	if reg.Coordinator != nil {
		t.Error("expected nil coordinator for empty input")
	}
	if len(reg.Workers) != 0 {
		t.Errorf("expected 0 workers, got %d", len(reg.Workers))
	}
}

func TestBuildRegistry_ByName(t *testing.T) {
	agents := []Agent{
		{Name: "alpha", Mode: "worker"},
		{Name: "beta", Mode: "worker"},
		{Name: "gamma", Mode: "worker"},
	}

	reg := BuildRegistry(agents, "beta")
	if reg.Coordinator == nil {
		t.Fatal("expected coordinator, got nil")
	}
	if reg.Coordinator.Name != "beta" {
		t.Errorf("coordinator.Name = %q, want %q", reg.Coordinator.Name, "beta")
	}
	if len(reg.Workers) != 2 {
		t.Fatalf("expected 2 workers, got %d", len(reg.Workers))
	}
}

func TestBuildRegistry_ByMode(t *testing.T) {
	agents := []Agent{
		{Name: "alpha", Mode: "worker"},
		{Name: "beta", Mode: "primary"},
		{Name: "gamma", Mode: "worker"},
	}

	reg := BuildRegistry(agents, "")
	if reg.Coordinator == nil {
		t.Fatal("expected coordinator, got nil")
	}
	if reg.Coordinator.Name != "beta" {
		t.Errorf("coordinator.Name = %q, want %q", reg.Coordinator.Name, "beta")
	}
	if len(reg.Workers) != 2 {
		t.Fatalf("expected 2 workers, got %d", len(reg.Workers))
	}
}

func TestBuildRegistry_NoPrimary(t *testing.T) {
	agents := []Agent{
		{Name: "alpha", Mode: "worker"},
		{Name: "beta", Mode: "worker"},
	}

	reg := BuildRegistry(agents, "")
	if reg.Coordinator != nil {
		t.Errorf("expected nil coordinator when no primary, got %v", reg.Coordinator)
	}
	if len(reg.Workers) != 2 {
		t.Errorf("expected 2 workers, got %d", len(reg.Workers))
	}
}

// --- rewriteMode tests ---

func TestRewriteMode_NoFrontmatter(t *testing.T) {
	content := "Just a body with no frontmatter."
	got := rewriteMode(content, "primary")
	if !strings.Contains(got, "mode: primary") {
		t.Error("expected mode: primary to be prepended")
	}
	if !strings.Contains(got, "Just a body with no frontmatter.") {
		t.Error("original body should be preserved")
	}
}

func TestRewriteMode_ExistingMode(t *testing.T) {
	content := "---\nmode: worker\ndescription: test\n---\nBody."
	got := rewriteMode(content, "primary")
	if !strings.Contains(got, "mode: primary") {
		t.Error("expected mode to be rewritten to primary")
	}
	if strings.Contains(got, "mode: worker") {
		t.Error("old mode: worker should be replaced")
	}
}

func TestRewriteMode_NoModeInFrontmatter(t *testing.T) {
	content := "---\ndescription: test\n---\nBody."
	got := rewriteMode(content, "primary")
	if !strings.Contains(got, "mode: primary") {
		t.Error("expected mode: primary to be inserted")
	}
	if !strings.Contains(got, "description: test") {
		t.Error("existing frontmatter fields should be preserved")
	}
}

// --- SetCoordinator edge case tests ---

func TestSetCoordinator_EmptyAgentsDir(t *testing.T) {
	teamDir := t.TempDir()
	agentsDir := filepath.Join(teamDir, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("creating agents subdir: %v", err)
	}

	err := SetCoordinator(teamDir, "anyone")
	if err == nil {
		t.Error("expected error for empty agents dir, got nil")
	}
}

// --- DiscoverTeams: hidden dirs are skipped ---

func TestDiscoverTeams_HiddenDirsSkipped(t *testing.T) {
	teamsDir := t.TempDir()
	// Create a hidden directory — should be skipped.
	if err := os.MkdirAll(filepath.Join(teamsDir, ".hidden", "agents"), 0o755); err != nil {
		t.Fatalf("creating hidden dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(teamsDir, ".hidden", "agents", "agent.md"), []byte("---\nmode: worker\n---\nbody"), 0o644); err != nil {
		t.Fatalf("writing agent file: %v", err)
	}

	teams, err := DiscoverTeams(teamsDir)
	if err != nil {
		t.Fatalf("DiscoverTeams: %v", err)
	}
	if len(teams) != 0 {
		t.Errorf("expected 0 teams (hidden dir should be skipped), got %d", len(teams))
	}
}

// --- ParseFile edge cases ---

func TestParseFile_NoFrontmatter(t *testing.T) {
	a := parseContent(t, "plain", "Just a plain body with no frontmatter delimiters.")
	if a.Name != "plain" {
		t.Errorf("Name: got %q, want %q", a.Name, "plain")
	}
	if a.Body != "Just a plain body with no frontmatter delimiters." {
		t.Errorf("Body: got %q", a.Body)
	}
}

func TestParseFile_AllFrontmatterFields(t *testing.T) {
	content := "---\ndescription: \"A test agent\"\nmode: primary\ncolor: \"#FF9800\"\ntemperature: 0.7\n---\nThe body."
	a := parseContent(t, "full", content)
	if a.Description != "A test agent" {
		t.Errorf("Description: got %q, want %q", a.Description, "A test agent")
	}
	if a.Mode != "primary" {
		t.Errorf("Mode: got %q, want %q", a.Mode, "primary")
	}
	if a.Color != "#FF9800" {
		t.Errorf("Color: got %q, want %q", a.Color, "#FF9800")
	}
	if a.Temperature != 0.7 {
		t.Errorf("Temperature: got %f, want 0.7", a.Temperature)
	}
	if a.Body != "The body." {
		t.Errorf("Body: got %q, want %q", a.Body, "The body.")
	}
}

func TestParseFile_NonexistentFile(t *testing.T) {
	_, err := ParseFile("/nonexistent/path/agent.md")
	if err == nil {
		t.Error("expected error for nonexistent file, got nil")
	}
}

// --- Superset field tests (agentfmt migration) ---

func TestParseFile_SupersetFields(t *testing.T) {
	content := `---
name: full-agent
description: Agent with all superset fields
mode: worker
temperature: 0.7
top_p: 0.9
max_turns: 10
provider: anthropic
model: claude-sonnet-4-20250514
model_options:
  max_tokens: 4096
skills:
  - code-review
  - testing
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
memory: Remember to run tests
color: "#FF9800"
hidden: false
disabled: false
background: false
isolation: container
---
You are a full-featured agent.`

	a := parseContent(t, "full-agent", content)

	// Existing fields.
	if a.Name != "full-agent" {
		t.Errorf("Name: got %q, want %q", a.Name, "full-agent")
	}
	if a.Description != "Agent with all superset fields" {
		t.Errorf("Description: got %q", a.Description)
	}
	if a.Mode != "worker" {
		t.Errorf("Mode: got %q, want %q", a.Mode, "worker")
	}
	if a.Temperature != 0.7 {
		t.Errorf("Temperature: got %f, want 0.7", a.Temperature)
	}
	if a.Color != "#FF9800" {
		t.Errorf("Color: got %q, want %q", a.Color, "#FF9800")
	}
	if a.Body != "You are a full-featured agent." {
		t.Errorf("Body: got %q", a.Body)
	}

	// Superset fields.
	if a.Provider != "anthropic" {
		t.Errorf("Provider: got %q, want %q", a.Provider, "anthropic")
	}
	if a.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model: got %q, want %q", a.Model, "claude-sonnet-4-20250514")
	}
	if a.MaxTurns != 10 {
		t.Errorf("MaxTurns: got %d, want 10", a.MaxTurns)
	}
	if a.TopP == nil || *a.TopP != 0.9 {
		t.Errorf("TopP: got %v, want 0.9", a.TopP)
	}
	if len(a.Skills) != 2 || a.Skills[0] != "code-review" || a.Skills[1] != "testing" {
		t.Errorf("Skills: got %v, want [code-review testing]", a.Skills)
	}
	if len(a.AllowedTools) != 3 {
		t.Errorf("AllowedTools: got %v, want 3 items", a.AllowedTools)
	}
	if len(a.DisallowedTools) != 1 || a.DisallowedTools[0] != "web_fetch" {
		t.Errorf("DisallowedTools: got %v, want [web_fetch]", a.DisallowedTools)
	}
	if a.PermissionMode != "plan" {
		t.Errorf("PermissionMode: got %q, want %q", a.PermissionMode, "plan")
	}
	if a.Permissions == nil {
		t.Error("Permissions: got nil, want non-nil")
	}
	if a.Memory != "Remember to run tests" {
		t.Errorf("Memory: got %q, want %q", a.Memory, "Remember to run tests")
	}
	if a.Isolation != "container" {
		t.Errorf("Isolation: got %q, want %q", a.Isolation, "container")
	}
	if a.ModelOptions == nil || a.ModelOptions["max_tokens"] != 4096 {
		t.Errorf("ModelOptions: got %v, want max_tokens=4096", a.ModelOptions)
	}

	// Legacy compat: HasToolsBlock should be true, Tools map should have entries.
	if !a.HasToolsBlock {
		t.Error("HasToolsBlock: got false, want true")
	}
	if a.Tools == nil {
		t.Fatal("Tools: got nil, want non-nil")
	}
	if !a.Tools["read_file"] {
		t.Error("Tools[read_file]: got false, want true")
	}
	if a.Tools["web_fetch"] != false {
		t.Error("Tools[web_fetch]: got true, want false")
	}
}

func TestParseFile_ClaudeCodeFormat(t *testing.T) {
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

	a := parseContent(t, "cc-agent", content)

	if a.Name != "cc-agent" {
		t.Errorf("Name: got %q, want %q", a.Name, "cc-agent")
	}
	// Model alias should be expanded.
	if a.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model: got %q, want %q", a.Model, "claude-sonnet-4-20250514")
	}
	if a.Provider != "anthropic" {
		t.Errorf("Provider: got %q, want %q", a.Provider, "anthropic")
	}
	if a.MaxTurns != 10 {
		t.Errorf("MaxTurns: got %d, want 10", a.MaxTurns)
	}
	if len(a.DisallowedTools) != 1 || a.DisallowedTools[0] != "web_fetch" {
		t.Errorf("DisallowedTools: got %v, want [web_fetch]", a.DisallowedTools)
	}
	// Color "red" should be normalized to hex.
	if a.Color != "#FF0000" {
		t.Errorf("Color: got %q, want %q", a.Color, "#FF0000")
	}
	if a.Body != "You are a Claude Code agent." {
		t.Errorf("Body: got %q", a.Body)
	}
	// Legacy compat: disallowed tools should set HasToolsBlock.
	if !a.HasToolsBlock {
		t.Error("HasToolsBlock: got false, want true")
	}
	if a.Tools["web_fetch"] != false {
		t.Error("Tools[web_fetch]: should be false (denied)")
	}
}

func TestParseFile_OpenCodeFormat(t *testing.T) {
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

	a := parseContent(t, "oc-agent", content)

	if a.Name != "oc-agent" {
		t.Errorf("Name: got %q, want %q", a.Name, "oc-agent")
	}
	if a.Provider != "anthropic" {
		t.Errorf("Provider: got %q, want %q", a.Provider, "anthropic")
	}
	if a.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model: got %q, want %q", a.Model, "claude-sonnet-4-20250514")
	}
	if a.MaxTurns != 25 {
		t.Errorf("MaxTurns: got %d, want 25", a.MaxTurns)
	}
	if !a.Disabled {
		t.Error("Disabled: got false, want true")
	}
	if a.Permissions == nil {
		t.Fatal("Permissions: got nil, want non-nil")
	}
	if a.Permissions["_mode"] != "auto" {
		t.Errorf("Permissions[_mode]: got %v, want %q", a.Permissions["_mode"], "auto")
	}
	// Color "cyan" should be normalized to hex.
	if a.Color != "#00FFFF" {
		t.Errorf("Color: got %q, want %q", a.Color, "#00FFFF")
	}
	if a.Body != "You are an OpenCode agent." {
		t.Errorf("Body: got %q", a.Body)
	}
}

func TestDiscoverTeams_WithTeamMD(t *testing.T) {
	teamsDir := t.TempDir()
	teamDir := filepath.Join(teamsDir, "coding")
	agentsDir := filepath.Join(teamDir, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("creating agents subdir: %v", err)
	}

	// Write agent files — both are workers by mode.
	writeFile := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(agentsDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	writeFile("builder.md", "---\nmode: worker\ndescription: Builds things\n---\nBuild stuff.")
	writeFile("reviewer.md", "---\nmode: worker\ndescription: Reviews code\n---\nReview stuff.")

	// Write team.md with description and lead override.
	teamMD := `---
name: coding
description: Full-stack coding team
lead: builder
---
We value clean code.`
	if err := os.WriteFile(filepath.Join(teamDir, "team.md"), []byte(teamMD), 0o644); err != nil {
		t.Fatalf("writing team.md: %v", err)
	}

	teams, err := DiscoverTeams(teamsDir)
	if err != nil {
		t.Fatalf("DiscoverTeams: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("got %d teams, want 1", len(teams))
	}

	team := teams[0]
	if team.Description != "Full-stack coding team" {
		t.Errorf("team.Description = %q, want %q", team.Description, "Full-stack coding team")
	}
	// Lead override: builder should be coordinator (even though mode is worker).
	if team.Coordinator == nil {
		t.Fatal("expected coordinator, got nil")
	}
	if team.Coordinator.Name != "builder" {
		t.Errorf("coordinator.Name = %q, want %q", team.Coordinator.Name, "builder")
	}
	if len(team.Workers) != 1 || team.Workers[0].Name != "reviewer" {
		t.Errorf("workers = %v, want [reviewer]", team.Workers)
	}
}

// --- helpers ---
