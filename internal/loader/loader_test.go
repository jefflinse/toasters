package loader

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jefflinse/toasters/internal/db"
)

// openTestStore creates a SQLite store in a temp directory.
func openTestStore(t *testing.T) db.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := db.Open(path)
	if err != nil {
		t.Fatalf("opening test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// writeFile creates a file with the given content, creating parent dirs as needed.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating dir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// touchFile creates an empty file.
func touchFile(t *testing.T, path string) {
	t.Helper()
	writeFile(t, path, "")
}

const systemTeamMD = `---
name: System Team
description: The core system team
lead: Operator
agents:
  - Planner
---
System team culture document.
`

const operatorAgentMD = `---
name: Operator
description: The main operator agent
mode: lead
model: claude-sonnet-4-20250514
tools:
  - assign_task
  - consult_agent
---
You are the operator.
`

const plannerAgentMD = `---
name: Planner
description: Plans work
mode: worker
model: claude-sonnet-4-20250514
tools:
  - create_plan
---
You are the planner.
`

const orchestrationSkillMD = `---
name: Orchestration
description: Skill for orchestrating work
---
Orchestration instructions here.
`

const goDevSkillMD = `---
name: Go Development
description: Go development best practices
tools:
  - go_build
  - go_test
---
Go development skill content.
`

const seniorGoDevMD = `---
name: Senior Go Dev
description: A senior Go developer
mode: worker
model: claude-sonnet-4-20250514
skills:
  - go-development
tools:
  - bash
  - write_file
---
You are a senior Go developer.
`

const devTeamMD = `---
name: Dev Team
description: Development team
lead: Frontend Specialist
agents:
  - Senior Go Dev
skills:
  - go-development
---
Dev team culture.
`

const frontendSpecialistMD = `---
name: Frontend Specialist
description: Frontend development expert
mode: lead
model: claude-sonnet-4-20250514
tools:
  - bash
---
You are a frontend specialist.
`

func TestLoad_SystemTeam(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	// Set up system directory.
	writeFile(t, filepath.Join(configDir, "system", "team.md"), systemTeamMD)
	writeFile(t, filepath.Join(configDir, "system", "agents", "operator.md"), operatorAgentMD)
	writeFile(t, filepath.Join(configDir, "system", "agents", "planner.md"), plannerAgentMD)
	writeFile(t, filepath.Join(configDir, "system", "skills", "orchestration.md"), orchestrationSkillMD)

	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Verify skill loaded.
	skills, err := store.ListSkills(ctx)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].ID != "orchestration" {
		t.Errorf("skill ID = %q, want %q", skills[0].ID, "orchestration")
	}
	if skills[0].Source != "system" {
		t.Errorf("skill source = %q, want %q", skills[0].Source, "system")
	}

	// Verify agents loaded.
	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}

	agentsByID := make(map[string]*db.Agent)
	for _, a := range agents {
		agentsByID[a.ID] = a
	}

	op, ok := agentsByID["operator"]
	if !ok {
		t.Fatal("operator agent not found")
	}
	if op.Mode != "lead" {
		t.Errorf("operator mode = %q, want %q", op.Mode, "lead")
	}
	if op.Source != "system" {
		t.Errorf("operator source = %q, want %q", op.Source, "system")
	}

	planner, ok := agentsByID["planner"]
	if !ok {
		t.Fatal("planner agent not found")
	}
	if planner.Mode != "worker" {
		t.Errorf("planner mode = %q, want %q", planner.Mode, "worker")
	}

	// Verify team loaded.
	teams, err := store.ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(teams))
	}
	if teams[0].ID != "system" {
		t.Errorf("team ID = %q, want %q", teams[0].ID, "system")
	}
	if teams[0].LeadAgent != "operator" {
		t.Errorf("team lead = %q, want %q", teams[0].LeadAgent, "operator")
	}
	if teams[0].Culture != "System team culture document." {
		t.Errorf("team culture = %q, want %q", teams[0].Culture, "System team culture document.")
	}

	// Verify team agents.
	teamAgents, err := store.ListTeamAgents(ctx, "system")
	if err != nil {
		t.Fatalf("ListTeamAgents: %v", err)
	}
	if len(teamAgents) != 2 {
		t.Fatalf("expected 2 team agents, got %d", len(teamAgents))
	}

	taByAgent := make(map[string]*db.TeamAgent)
	for _, ta := range teamAgents {
		taByAgent[ta.AgentID] = ta
	}
	if ta, ok := taByAgent["operator"]; !ok {
		t.Error("operator not in team agents")
	} else if ta.Role != "lead" {
		t.Errorf("operator role = %q, want %q", ta.Role, "lead")
	}
	if ta, ok := taByAgent["planner"]; !ok {
		t.Error("planner not in team agents")
	} else if ta.Role != "worker" {
		t.Errorf("planner role = %q, want %q", ta.Role, "worker")
	}
}

func TestLoad_UserSkillsAndAgents(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	writeFile(t, filepath.Join(configDir, "user", "skills", "go-development.md"), goDevSkillMD)
	writeFile(t, filepath.Join(configDir, "user", "agents", "senior-go-dev.md"), seniorGoDevMD)

	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Verify skill.
	skills, err := store.ListSkills(ctx)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].ID != "go-development" {
		t.Errorf("skill ID = %q, want %q", skills[0].ID, "go-development")
	}
	if skills[0].Source != "user" {
		t.Errorf("skill source = %q, want %q", skills[0].Source, "user")
	}
	if skills[0].Prompt != "Go development skill content." {
		t.Errorf("skill prompt = %q, want %q", skills[0].Prompt, "Go development skill content.")
	}

	// Verify agent.
	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].ID != "senior-go-dev" {
		t.Errorf("agent ID = %q, want %q", agents[0].ID, "senior-go-dev")
	}
	if agents[0].Source != "user" {
		t.Errorf("agent source = %q, want %q", agents[0].Source, "user")
	}
	if agents[0].TeamID != "" {
		t.Errorf("shared agent should have empty team_id, got %q", agents[0].TeamID)
	}
}

func TestLoad_UserTeam(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	// Shared agent that the team references.
	writeFile(t, filepath.Join(configDir, "user", "agents", "senior-go-dev.md"), seniorGoDevMD)

	// Team with local agent.
	writeFile(t, filepath.Join(configDir, "user", "teams", "dev-team", "team.md"), devTeamMD)
	writeFile(t, filepath.Join(configDir, "user", "teams", "dev-team", "agents", "frontend-specialist.md"), frontendSpecialistMD)

	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Verify team.
	teams, err := store.ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(teams))
	}
	if teams[0].ID != "dev-team" {
		t.Errorf("team ID = %q, want %q", teams[0].ID, "dev-team")
	}
	if teams[0].LeadAgent != "dev-team/frontend-specialist" {
		t.Errorf("team lead = %q, want %q", teams[0].LeadAgent, "dev-team/frontend-specialist")
	}
	if teams[0].IsAuto {
		t.Error("team should not be auto")
	}
	if teams[0].Culture != "Dev team culture." {
		t.Errorf("team culture = %q, want %q", teams[0].Culture, "Dev team culture.")
	}

	// Verify agents: 1 shared + 1 team-local.
	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}

	agentsByID := make(map[string]*db.Agent)
	for _, a := range agents {
		agentsByID[a.ID] = a
	}

	fs, ok := agentsByID["dev-team/frontend-specialist"]
	if !ok {
		t.Fatal("dev-team/frontend-specialist agent not found")
	}
	if fs.TeamID != "dev-team" {
		t.Errorf("frontend-specialist team_id = %q, want %q", fs.TeamID, "dev-team")
	}

	sgd, ok := agentsByID["senior-go-dev"]
	if !ok {
		t.Fatal("senior-go-dev agent not found")
	}
	if sgd.TeamID != "" {
		t.Errorf("senior-go-dev should be shared (empty team_id), got %q", sgd.TeamID)
	}

	// Verify team agents.
	teamAgents, err := store.ListTeamAgents(ctx, "dev-team")
	if err != nil {
		t.Fatalf("ListTeamAgents: %v", err)
	}
	if len(teamAgents) != 2 {
		t.Fatalf("expected 2 team agents, got %d", len(teamAgents))
	}

	taByAgent := make(map[string]*db.TeamAgent)
	for _, ta := range teamAgents {
		taByAgent[ta.AgentID] = ta
	}
	if ta, ok := taByAgent["dev-team/frontend-specialist"]; !ok {
		t.Error("dev-team/frontend-specialist not in team agents")
	} else if ta.Role != "lead" {
		t.Errorf("frontend-specialist role = %q, want %q", ta.Role, "lead")
	}
	if ta, ok := taByAgent["senior-go-dev"]; !ok {
		t.Error("senior-go-dev not in team agents")
	} else if ta.Role != "worker" {
		t.Errorf("senior-go-dev role = %q, want %q", ta.Role, "worker")
	}
}

func TestLoad_AutoTeam(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	autoTeamDir := filepath.Join(configDir, "user", "teams", "auto-claude")
	touchFile(t, filepath.Join(autoTeamDir, ".auto-team"))

	autoAgentMD := `---
name: Auto Worker
description: An auto-discovered agent
mode: worker
model: claude-sonnet-4-20250514
---
You are an auto worker.
`
	writeFile(t, filepath.Join(autoTeamDir, "agents", "auto-worker.md"), autoAgentMD)

	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Verify team.
	teams, err := store.ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(teams))
	}
	if teams[0].ID != "auto-claude" {
		t.Errorf("team ID = %q, want %q", teams[0].ID, "auto-claude")
	}
	if !teams[0].IsAuto {
		t.Error("team should be auto")
	}
	if teams[0].Source != "auto" {
		t.Errorf("team source = %q, want %q", teams[0].Source, "auto")
	}

	// Verify agent.
	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Source != "auto" {
		t.Errorf("agent source = %q, want %q", agents[0].Source, "auto")
	}
	if agents[0].TeamID != "auto-claude" {
		t.Errorf("agent team_id = %q, want %q", agents[0].TeamID, "auto-claude")
	}

	// Verify team agent.
	teamAgents, err := store.ListTeamAgents(ctx, "auto-claude")
	if err != nil {
		t.Fatalf("ListTeamAgents: %v", err)
	}
	if len(teamAgents) != 1 {
		t.Fatalf("expected 1 team agent, got %d", len(teamAgents))
	}
	if teamAgents[0].Role != "worker" {
		t.Errorf("team agent role = %q, want %q", teamAgents[0].Role, "worker")
	}
}

func TestLoad_AutoTeamWithTeamMD(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	autoTeamDir := filepath.Join(configDir, "user", "teams", "auto-claude")
	touchFile(t, filepath.Join(autoTeamDir, ".auto-team"))

	autoTeamMD := `---
name: Auto Claude Team
description: An auto-discovered team with explicit config
lead: Auto Lead
agents:
  - Auto Worker
---
Auto team culture.
`
	autoLeadMD := `---
name: Auto Lead
description: Lead of auto team
mode: lead
model: claude-sonnet-4-20250514
---
You lead the auto team.
`
	autoWorkerMD := `---
name: Auto Worker
description: Worker in auto team
mode: worker
model: claude-sonnet-4-20250514
---
You work in the auto team.
`
	writeFile(t, filepath.Join(autoTeamDir, "team.md"), autoTeamMD)
	writeFile(t, filepath.Join(autoTeamDir, "agents", "auto-lead.md"), autoLeadMD)
	writeFile(t, filepath.Join(autoTeamDir, "agents", "auto-worker.md"), autoWorkerMD)

	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	teams, err := store.ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(teams))
	}
	if !teams[0].IsAuto {
		t.Error("team should be auto")
	}
	if teams[0].LeadAgent != "auto-claude/auto-lead" {
		t.Errorf("team lead = %q, want %q", teams[0].LeadAgent, "auto-claude/auto-lead")
	}
	if teams[0].Culture != "Auto team culture." {
		t.Errorf("team culture = %q, want %q", teams[0].Culture, "Auto team culture.")
	}

	teamAgents, err := store.ListTeamAgents(ctx, "auto-claude")
	if err != nil {
		t.Fatalf("ListTeamAgents: %v", err)
	}
	if len(teamAgents) != 2 {
		t.Fatalf("expected 2 team agents, got %d", len(teamAgents))
	}
}

func TestLoad_AgentResolution(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	// Shared agent.
	writeFile(t, filepath.Join(configDir, "user", "agents", "senior-go-dev.md"), seniorGoDevMD)

	// Team that references the shared agent as lead (no local agents).
	teamMD := `---
name: Small Team
description: A small team
lead: Senior Go Dev
---
Small team culture.
`
	writeFile(t, filepath.Join(configDir, "user", "teams", "small-team", "team.md"), teamMD)

	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	teams, err := store.ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(teams))
	}
	if teams[0].LeadAgent != "senior-go-dev" {
		t.Errorf("team lead = %q, want %q", teams[0].LeadAgent, "senior-go-dev")
	}

	teamAgents, err := store.ListTeamAgents(ctx, "small-team")
	if err != nil {
		t.Fatalf("ListTeamAgents: %v", err)
	}
	if len(teamAgents) != 1 {
		t.Fatalf("expected 1 team agent, got %d", len(teamAgents))
	}
	if teamAgents[0].AgentID != "senior-go-dev" {
		t.Errorf("team agent ID = %q, want %q", teamAgents[0].AgentID, "senior-go-dev")
	}
	if teamAgents[0].Role != "lead" {
		t.Errorf("team agent role = %q, want %q", teamAgents[0].Role, "lead")
	}
}

func TestLoad_UnresolvedAgent(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	// Team referencing a non-existent agent.
	teamMD := `---
name: Broken Team
description: Team with missing agent
lead: Ghost Agent
agents:
  - Also Missing
---
`
	localAgentMD := `---
name: Local Worker
description: A local worker
mode: worker
model: claude-sonnet-4-20250514
---
You are a local worker.
`
	writeFile(t, filepath.Join(configDir, "user", "teams", "broken-team", "team.md"), teamMD)
	writeFile(t, filepath.Join(configDir, "user", "teams", "broken-team", "agents", "local-worker.md"), localAgentMD)

	l := New(store, configDir)
	// Should not return an error — just log warnings.
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Team should exist but with empty lead (unresolved).
	teams, err := store.ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("expected 1 team, got %d", len(teams))
	}
	if teams[0].LeadAgent != "" {
		t.Errorf("expected empty lead (unresolved), got %q", teams[0].LeadAgent)
	}

	// Only the local agent should be a team agent (the missing ones are skipped).
	teamAgents, err := store.ListTeamAgents(ctx, "broken-team")
	if err != nil {
		t.Fatalf("ListTeamAgents: %v", err)
	}
	if len(teamAgents) != 0 {
		t.Errorf("expected 0 team agents (all unresolved), got %d", len(teamAgents))
	}
}

func TestLoad_Idempotent(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	writeFile(t, filepath.Join(configDir, "user", "skills", "go-development.md"), goDevSkillMD)
	writeFile(t, filepath.Join(configDir, "user", "agents", "senior-go-dev.md"), seniorGoDevMD)

	l := New(store, configDir)

	// Load twice.
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load (1st): %v", err)
	}
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load (2nd): %v", err)
	}

	// Verify same data after second load.
	skills, err := store.ListSkills(ctx)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill after idempotent load, got %d", len(skills))
	}

	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent after idempotent load, got %d", len(agents))
	}
}

func TestLoad_EmptyDirs(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	// Create empty directories.
	for _, dir := range []string{
		filepath.Join(configDir, "system", "skills"),
		filepath.Join(configDir, "system", "agents"),
		filepath.Join(configDir, "user", "skills"),
		filepath.Join(configDir, "user", "agents"),
		filepath.Join(configDir, "user", "teams"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("creating dir %s: %v", dir, err)
		}
	}

	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Verify nothing loaded.
	skills, _ := store.ListSkills(ctx)
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
	agents, _ := store.ListAgents(ctx)
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
	teams, _ := store.ListTeams(ctx)
	if len(teams) != 0 {
		t.Errorf("expected 0 teams, got %d", len(teams))
	}
}

func TestLoad_NoDirs(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	// Don't create any directories — configDir exists but is empty.
	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load with no dirs: %v", err)
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Go Development", "go-development"},
		{"Senior Go Dev", "senior-go-dev"},
		{"Blocker Handler", "blocker-handler"},
		{"simple", "simple"},
		{"UPPER CASE", "upper-case"},
		{"with---multiple---hyphens", "with-multiple-hyphens"},
		{"  leading trailing  ", "leading-trailing"},
		{"special!@#$chars", "specialchars"},
		{"mixed Special-Chars_123", "mixed-special-chars123"},
		{"", ""},
		{"already-slugified", "already-slugified"},
		{"-leading-hyphen", "leading-hyphen"},
		{"trailing-hyphen-", "trailing-hyphen"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := Slugify(tt.input)
			if got != tt.want {
				t.Errorf("Slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestLoad_FullIntegration(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	// Set up complete directory structure.
	// System.
	writeFile(t, filepath.Join(configDir, "system", "team.md"), systemTeamMD)
	writeFile(t, filepath.Join(configDir, "system", "agents", "operator.md"), operatorAgentMD)
	writeFile(t, filepath.Join(configDir, "system", "agents", "planner.md"), plannerAgentMD)
	writeFile(t, filepath.Join(configDir, "system", "skills", "orchestration.md"), orchestrationSkillMD)

	// User shared.
	writeFile(t, filepath.Join(configDir, "user", "skills", "go-development.md"), goDevSkillMD)
	writeFile(t, filepath.Join(configDir, "user", "agents", "senior-go-dev.md"), seniorGoDevMD)

	// User team.
	writeFile(t, filepath.Join(configDir, "user", "teams", "dev-team", "team.md"), devTeamMD)
	writeFile(t, filepath.Join(configDir, "user", "teams", "dev-team", "agents", "frontend-specialist.md"), frontendSpecialistMD)

	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Verify totals.
	skills, _ := store.ListSkills(ctx)
	if len(skills) != 2 {
		t.Errorf("expected 2 skills, got %d", len(skills))
	}

	agents, _ := store.ListAgents(ctx)
	if len(agents) != 4 { // operator, planner, senior-go-dev, frontend-specialist
		t.Errorf("expected 4 agents, got %d", len(agents))
	}

	teams, _ := store.ListTeams(ctx)
	if len(teams) != 2 { // system, dev-team
		t.Errorf("expected 2 teams, got %d", len(teams))
	}
}

func TestLoad_UnparseableFileSkipped(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	// Write a valid skill and an invalid one.
	writeFile(t, filepath.Join(configDir, "user", "skills", "good.md"), goDevSkillMD)
	writeFile(t, filepath.Join(configDir, "user", "skills", "bad.md"), "this is not valid frontmatter at all")

	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Only the good skill should be loaded.
	skills, _ := store.ListSkills(ctx)
	if len(skills) != 1 {
		t.Errorf("expected 1 skill (bad skipped), got %d", len(skills))
	}
}
