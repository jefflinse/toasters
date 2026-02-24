package agents

import (
	"os"
	"path/filepath"
	"testing"
)

// --- ClaudePermissionArgs tests ---

func TestClaudePermissionArgs_NoToolsBlock(t *testing.T) {
	a := Agent{Name: "builder", HasToolsBlock: false}
	got := a.ClaudePermissionArgs()
	want := []string{"--dangerously-skip-permissions"}
	if !sliceEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestClaudePermissionArgs_BashDenied(t *testing.T) {
	a := Agent{
		Name:          "docs-writer",
		HasToolsBlock: true,
		Tools:         map[string]bool{"bash": false},
	}
	got := a.ClaudePermissionArgs()
	want := []string{"--permission-mode", "acceptEdits", "--allowedTools", "Read,Write,Edit,Glob,Grep,WebFetch,TodoRead,TodoWrite"}
	if !sliceEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestClaudePermissionArgs_WriteEditDenied(t *testing.T) {
	a := Agent{
		Name:          "reader",
		HasToolsBlock: true,
		Tools:         map[string]bool{"write": false, "edit": false},
	}
	got := a.ClaudePermissionArgs()
	want := []string{"--permission-mode", "acceptEdits", "--allowedTools", "Bash,Read,Glob,Grep,WebFetch,TodoRead,TodoWrite"}
	if !sliceEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestClaudePermissionArgs_AllDenied(t *testing.T) {
	a := Agent{
		Name:          "readonly",
		HasToolsBlock: true,
		Tools:         map[string]bool{"bash": false, "write": false, "edit": false},
	}
	got := a.ClaudePermissionArgs()
	want := []string{"--permission-mode", "acceptEdits", "--allowedTools", "Read,Glob,Grep,WebFetch,TodoRead,TodoWrite"}
	if !sliceEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

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

// --- BuildSystemPrompt tests ---

func TestBuildSystemPrompt_WithWorkers(t *testing.T) {
	coord := Agent{Name: "coordinator", Body: "I am the coordinator."}
	workers := []Agent{
		{Name: "builder", Description: "Builds things"},
		{Name: "tester", Description: "Tests things"},
	}

	got := BuildSystemPrompt(coord, workers)

	// Must contain the coordinator body.
	if !containsStr(got, "I am the coordinator.") {
		t.Error("missing coordinator body")
	}
	// Must contain the separator.
	if !containsStr(got, "---") {
		t.Error("missing separator between coordinator body and wrapper")
	}
	// Must contain the wrapper prompt framing.
	if !containsStr(got, "You are a coordinator agent operating inside toasters") {
		t.Error("missing wrapper prompt framing")
	}
	// Must list workers.
	if !containsStr(got, "`builder`: Builds things") {
		t.Error("missing builder in roster")
	}
	if !containsStr(got, "`tester`: Tests things") {
		t.Error("missing tester in roster")
	}
}

func TestBuildSystemPrompt_NoWorkers(t *testing.T) {
	coord := Agent{Name: "coordinator", Body: "Solo coordinator."}

	got := BuildSystemPrompt(coord, nil)

	if !containsStr(got, "No worker agents discovered.") {
		t.Error("expected 'No worker agents discovered.' when no workers provided")
	}
}

func TestBuildSystemPrompt_WorkersWithEmptyDescription(t *testing.T) {
	coord := Agent{Name: "coordinator", Body: "Coord body."}
	workers := []Agent{
		{Name: "visible", Description: "Has a description"},
		{Name: "hidden", Description: ""},
	}

	got := BuildSystemPrompt(coord, workers)

	if !containsStr(got, "`visible`: Has a description") {
		t.Error("missing visible worker in roster")
	}
	if containsStr(got, "`hidden`") {
		t.Error("worker with empty description should be omitted from roster")
	}
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
	if !containsStr(got, "I lead the team.") {
		t.Error("missing coordinator body")
	}
	// Must contain team name in instructions.
	if !containsStr(got, `"backend"`) {
		t.Error("missing team name in instructions")
	}
	// Must list workers.
	if !containsStr(got, "`coder`: Writes code") {
		t.Error("missing coder in roster")
	}
	if !containsStr(got, "`reviewer`: Reviews PRs") {
		t.Error("missing reviewer in roster")
	}
	// Must contain job directory.
	if !containsStr(got, "/jobs/job-123") {
		t.Error("missing job directory path")
	}
	// Must contain REPORT.md and BLOCKER.md instructions.
	if !containsStr(got, "REPORT.md") {
		t.Error("missing REPORT.md instructions")
	}
	if !containsStr(got, "BLOCKER.md") {
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
	if !containsStr(got, "## Toasters Team Coordinator Instructions") {
		t.Error("missing instructions header")
	}
	if !containsStr(got, "`styler`: Writes CSS") {
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

	if !containsStr(got, "(no workers configured)") {
		t.Error("expected '(no workers configured)' when no workers")
	}
}

// --- BuildOperatorPrompt tests ---

func TestBuildOperatorPrompt_WithTeams(t *testing.T) {
	coord := Agent{Name: "lead", Description: "Leads coding"}
	teams := []Team{
		{
			Name:        "coding",
			Coordinator: &coord,
			Workers:     []Agent{{Name: "builder", Description: "Builds"}},
		},
		{
			Name:        "docs",
			Coordinator: nil,
			Workers:     []Agent{{Name: "writer"}, {Name: "editor"}},
		},
	}

	got := BuildOperatorPrompt(teams, "")

	// Must contain the operator framing.
	if !containsStr(got, "You are the Operator") {
		t.Error("missing operator framing")
	}
	// Must contain the Available Teams section.
	if !containsStr(got, "## Available Teams") {
		t.Error("missing Available Teams section")
	}
	// Team with coordinator shows coordinator description.
	if !containsStr(got, "`coding`: Leads coding") {
		t.Error("missing coding team with coordinator description")
	}
	// Team without coordinator shows worker count.
	if !containsStr(got, "`docs`: 2 workers") {
		t.Error("missing docs team with worker count")
	}
}

func TestBuildOperatorPrompt_NoTeams(t *testing.T) {
	got := BuildOperatorPrompt(nil, "")

	if !containsStr(got, "No teams configured") {
		t.Error("expected 'No teams configured' message when no teams")
	}
}

func TestBuildOperatorPrompt_WithAwareness(t *testing.T) {
	teams := []Team{
		{Name: "coding", Coordinator: nil, Workers: []Agent{{Name: "w1"}}},
	}

	awareness := "Custom awareness text about teams."
	got := BuildOperatorPrompt(teams, awareness)

	// When awareness is provided, it should be used verbatim.
	if !containsStr(got, "Custom awareness text about teams.") {
		t.Error("expected awareness text to be used verbatim")
	}
	// The default team list should NOT appear.
	if containsStr(got, "`coding`") {
		t.Error("default team list should not appear when awareness is provided")
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
	if !containsStr(got, "mode: primary") {
		t.Error("expected mode: primary to be prepended")
	}
	if !containsStr(got, "Just a body with no frontmatter.") {
		t.Error("original body should be preserved")
	}
}

func TestRewriteMode_ExistingMode(t *testing.T) {
	content := "---\nmode: worker\ndescription: test\n---\nBody."
	got := rewriteMode(content, "primary")
	if !containsStr(got, "mode: primary") {
		t.Error("expected mode to be rewritten to primary")
	}
	if containsStr(got, "mode: worker") {
		t.Error("old mode: worker should be replaced")
	}
}

func TestRewriteMode_NoModeInFrontmatter(t *testing.T) {
	content := "---\ndescription: test\n---\nBody."
	got := rewriteMode(content, "primary")
	if !containsStr(got, "mode: primary") {
		t.Error("expected mode: primary to be inserted")
	}
	if !containsStr(got, "description: test") {
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

// --- helpers ---

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
