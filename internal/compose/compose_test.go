package compose

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/db"
)

// mockStore implements Store with in-memory maps.
type mockStore struct {
	agents     map[string]*db.Agent
	teams      map[string]*db.Team
	skills     map[string]*db.Skill
	teamAgents map[string][]*db.TeamAgent // keyed by teamID
}

func newMockStore() *mockStore {
	return &mockStore{
		agents:     make(map[string]*db.Agent),
		teams:      make(map[string]*db.Team),
		skills:     make(map[string]*db.Skill),
		teamAgents: make(map[string][]*db.TeamAgent),
	}
}

func (m *mockStore) GetAgent(_ context.Context, id string) (*db.Agent, error) {
	a, ok := m.agents[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	return a, nil
}

func (m *mockStore) GetTeam(_ context.Context, id string) (*db.Team, error) {
	t, ok := m.teams[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	return t, nil
}

func (m *mockStore) GetSkill(_ context.Context, id string) (*db.Skill, error) {
	s, ok := m.skills[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	return s, nil
}

func (m *mockStore) ListTeamAgents(_ context.Context, teamID string) ([]*db.TeamAgent, error) {
	return m.teamAgents[teamID], nil
}

// toJSON marshals v to json.RawMessage for test data.
func toJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

func TestCompose_BasicAgent(t *testing.T) {
	store := newMockStore()
	store.agents["dev"] = &db.Agent{
		ID:           "dev",
		Name:         "Developer",
		SystemPrompt: "You are a developer.",
		Tools:        toJSON([]string{"read", "write", "bash"}),
	}

	c := New(store, "default-provider", "default-model")
	result, err := c.Compose(context.Background(), "dev", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.AgentID != "dev" {
		t.Errorf("AgentID = %q, want %q", result.AgentID, "dev")
	}
	if result.Name != "Developer" {
		t.Errorf("Name = %q, want %q", result.Name, "Developer")
	}
	if result.SystemPrompt != "You are a developer." {
		t.Errorf("SystemPrompt = %q, want %q", result.SystemPrompt, "You are a developer.")
	}
	if result.Provider != "default-provider" {
		t.Errorf("Provider = %q, want %q", result.Provider, "default-provider")
	}
	if result.Model != "default-model" {
		t.Errorf("Model = %q, want %q", result.Model, "default-model")
	}
	if result.TeamID != "" {
		t.Errorf("TeamID = %q, want empty", result.TeamID)
	}
	if result.Role != "" {
		t.Errorf("Role = %q, want empty", result.Role)
	}

	assertTools(t, result.Tools, []string{"read", "write", "bash"})
}

func TestCompose_WithSkills(t *testing.T) {
	store := newMockStore()
	store.agents["dev"] = &db.Agent{
		ID:           "dev",
		Name:         "Developer",
		SystemPrompt: "You are a developer.",
		Tools:        toJSON([]string{"read", "write"}),
		Skills:       toJSON([]string{"go-dev", "testing"}),
	}
	store.skills["go-dev"] = &db.Skill{
		ID:     "go-dev",
		Name:   "Go Development",
		Prompt: "Write idiomatic Go code.",
		Tools:  toJSON([]string{"go_build", "go_test"}),
	}
	store.skills["testing"] = &db.Skill{
		ID:     "testing",
		Name:   "Testing",
		Prompt: "Write comprehensive tests.",
		Tools:  toJSON([]string{"go_test", "coverage"}),
	}

	c := New(store, "default-provider", "default-model")
	result, err := c.Compose(context.Background(), "dev", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify prompt includes skills section.
	if !strings.Contains(result.SystemPrompt, "## Skills") {
		t.Error("SystemPrompt missing '## Skills' header")
	}
	if !strings.Contains(result.SystemPrompt, "### Go Development") {
		t.Error("SystemPrompt missing '### Go Development' header")
	}
	if !strings.Contains(result.SystemPrompt, "Write idiomatic Go code.") {
		t.Error("SystemPrompt missing Go Development skill prompt")
	}
	if !strings.Contains(result.SystemPrompt, "### Testing") {
		t.Error("SystemPrompt missing '### Testing' header")
	}
	if !strings.Contains(result.SystemPrompt, "Write comprehensive tests.") {
		t.Error("SystemPrompt missing Testing skill prompt")
	}

	// Verify tools are unioned (go_test appears in both skills but should be deduplicated).
	assertTools(t, result.Tools, []string{"read", "write", "go_build", "go_test", "coverage"})
}

func TestCompose_WithTeam_Lead(t *testing.T) {
	store := newMockStore()
	store.agents["lead-dev"] = &db.Agent{
		ID:           "lead-dev",
		Name:         "Lead Developer",
		SystemPrompt: "You lead the team.",
		Tools:        toJSON([]string{"read", "write"}),
	}
	store.teams["dev-team"] = &db.Team{
		ID:      "dev-team",
		Name:    "Dev Team",
		Culture: "We value clean code and thorough reviews.",
	}
	store.teamAgents["dev-team"] = []*db.TeamAgent{
		{TeamID: "dev-team", AgentID: "lead-dev", Role: "lead"},
		{TeamID: "dev-team", AgentID: "worker-1", Role: "worker"},
	}

	c := New(store, "default-provider", "default-model")
	result, err := c.Compose(context.Background(), "lead-dev", "dev-team")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Role != "lead" {
		t.Errorf("Role = %q, want %q", result.Role, "lead")
	}
	if result.TeamID != "dev-team" {
		t.Errorf("TeamID = %q, want %q", result.TeamID, "dev-team")
	}

	// Culture should be injected for lead.
	if !strings.Contains(result.SystemPrompt, "## Team Culture") {
		t.Error("SystemPrompt missing '## Team Culture' header for lead")
	}
	if !strings.Contains(result.SystemPrompt, "We value clean code and thorough reviews.") {
		t.Error("SystemPrompt missing team culture content")
	}

	// Lead tools should be present.
	assertContainsTool(t, result.Tools, "spawn_agent")
	assertContainsTool(t, result.Tools, "complete_task")
	assertContainsTool(t, result.Tools, "request_new_task")
	assertContainsTool(t, result.Tools, "report_blocker")
	assertContainsTool(t, result.Tools, "report_progress")
	assertContainsTool(t, result.Tools, "query_job_context")
	assertContainsTool(t, result.Tools, "query_team_context")
}

func TestCompose_WithTeam_Worker(t *testing.T) {
	store := newMockStore()
	store.agents["worker-1"] = &db.Agent{
		ID:           "worker-1",
		Name:         "Worker",
		SystemPrompt: "You are a worker.",
		Tools:        toJSON([]string{"read", "write"}),
	}
	store.teams["dev-team"] = &db.Team{
		ID:      "dev-team",
		Name:    "Dev Team",
		Culture: "We value clean code.",
	}
	store.teamAgents["dev-team"] = []*db.TeamAgent{
		{TeamID: "dev-team", AgentID: "lead-dev", Role: "lead"},
		{TeamID: "dev-team", AgentID: "worker-1", Role: "worker"},
	}

	c := New(store, "default-provider", "default-model")
	result, err := c.Compose(context.Background(), "worker-1", "dev-team")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Role != "worker" {
		t.Errorf("Role = %q, want %q", result.Role, "worker")
	}

	// Culture should NOT be injected for workers.
	if strings.Contains(result.SystemPrompt, "## Team Culture") {
		t.Error("SystemPrompt should not contain '## Team Culture' for worker")
	}

	// Worker tools should be present.
	assertContainsTool(t, result.Tools, "report_progress")
	assertContainsTool(t, result.Tools, "query_team_context")

	// Lead-only tools should NOT be present.
	assertNotContainsTool(t, result.Tools, "spawn_agent")
	assertNotContainsTool(t, result.Tools, "complete_task")
}

func TestCompose_TeamWideSkills(t *testing.T) {
	store := newMockStore()
	store.agents["dev"] = &db.Agent{
		ID:           "dev",
		Name:         "Developer",
		SystemPrompt: "You are a developer.",
		Tools:        toJSON([]string{"read"}),
	}
	store.teams["dev-team"] = &db.Team{
		ID:     "dev-team",
		Name:   "Dev Team",
		Skills: toJSON([]string{"code-review", "ci-cd"}),
	}
	store.teamAgents["dev-team"] = []*db.TeamAgent{
		{TeamID: "dev-team", AgentID: "dev", Role: "worker"},
	}
	store.skills["code-review"] = &db.Skill{
		ID:     "code-review",
		Name:   "Code Review",
		Prompt: "Review code carefully.",
		Tools:  toJSON([]string{"diff", "comment"}),
	}
	store.skills["ci-cd"] = &db.Skill{
		ID:     "ci-cd",
		Name:   "CI/CD",
		Prompt: "Manage CI/CD pipelines.",
		Tools:  toJSON([]string{"deploy"}),
	}

	c := New(store, "default-provider", "default-model")
	result, err := c.Compose(context.Background(), "dev", "dev-team")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify team skills section in prompt.
	if !strings.Contains(result.SystemPrompt, "## Team Skills") {
		t.Error("SystemPrompt missing '## Team Skills' header")
	}
	if !strings.Contains(result.SystemPrompt, "### Code Review") {
		t.Error("SystemPrompt missing '### Code Review' header")
	}
	if !strings.Contains(result.SystemPrompt, "### CI/CD") {
		t.Error("SystemPrompt missing '### CI/CD' header")
	}

	// Team skill tools should be merged.
	assertContainsTool(t, result.Tools, "diff")
	assertContainsTool(t, result.Tools, "comment")
	assertContainsTool(t, result.Tools, "deploy")
}

func TestCompose_Denylist(t *testing.T) {
	store := newMockStore()
	store.agents["dev"] = &db.Agent{
		ID:              "dev",
		Name:            "Developer",
		SystemPrompt:    "You are a developer.",
		Tools:           toJSON([]string{"read", "write", "bash", "dangerous_tool"}),
		DisallowedTools: toJSON([]string{"dangerous_tool", "bash"}),
	}

	c := New(store, "default-provider", "default-model")
	result, err := c.Compose(context.Background(), "dev", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertTools(t, result.Tools, []string{"read", "write"})
	assertNotContainsTool(t, result.Tools, "dangerous_tool")
	assertNotContainsTool(t, result.Tools, "bash")
}

func TestCompose_ProviderCascade(t *testing.T) {
	t.Run("agent override wins", func(t *testing.T) {
		store := newMockStore()
		store.agents["dev"] = &db.Agent{
			ID:       "dev",
			Name:     "Developer",
			Provider: "agent-provider",
			Model:    "agent-model",
		}
		store.teams["team"] = &db.Team{
			ID:       "team",
			Name:     "Team",
			Provider: "team-provider",
			Model:    "team-model",
		}
		store.teamAgents["team"] = []*db.TeamAgent{
			{TeamID: "team", AgentID: "dev", Role: "worker"},
		}

		c := New(store, "global-provider", "global-model")
		result, err := c.Compose(context.Background(), "dev", "team")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Provider != "agent-provider" {
			t.Errorf("Provider = %q, want %q", result.Provider, "agent-provider")
		}
		if result.Model != "agent-model" {
			t.Errorf("Model = %q, want %q", result.Model, "agent-model")
		}
	})

	t.Run("team default used when agent has none", func(t *testing.T) {
		store := newMockStore()
		store.agents["dev"] = &db.Agent{
			ID:   "dev",
			Name: "Developer",
		}
		store.teams["team"] = &db.Team{
			ID:       "team",
			Name:     "Team",
			Provider: "team-provider",
			Model:    "team-model",
		}
		store.teamAgents["team"] = []*db.TeamAgent{
			{TeamID: "team", AgentID: "dev", Role: "worker"},
		}

		c := New(store, "global-provider", "global-model")
		result, err := c.Compose(context.Background(), "dev", "team")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Provider != "team-provider" {
			t.Errorf("Provider = %q, want %q", result.Provider, "team-provider")
		}
		if result.Model != "team-model" {
			t.Errorf("Model = %q, want %q", result.Model, "team-model")
		}
	})

	t.Run("global default used when agent and team have none", func(t *testing.T) {
		store := newMockStore()
		store.agents["dev"] = &db.Agent{
			ID:   "dev",
			Name: "Developer",
		}

		c := New(store, "global-provider", "global-model")
		result, err := c.Compose(context.Background(), "dev", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.Provider != "global-provider" {
			t.Errorf("Provider = %q, want %q", result.Provider, "global-provider")
		}
		if result.Model != "global-model" {
			t.Errorf("Model = %q, want %q", result.Model, "global-model")
		}
	})
}

func TestCompose_SystemTeam(t *testing.T) {
	t.Run("system lead gets lead + system lead tools", func(t *testing.T) {
		store := newMockStore()
		store.agents["operator"] = &db.Agent{
			ID:           "operator",
			Name:         "Operator",
			SystemPrompt: "You are the operator.",
			Tools:        toJSON([]string{"read"}),
		}
		store.teams["system"] = &db.Team{
			ID:   "system",
			Name: "System",
		}
		store.teamAgents["system"] = []*db.TeamAgent{
			{TeamID: "system", AgentID: "operator", Role: "lead"},
		}

		c := New(store, "default-provider", "default-model")
		result, err := c.Compose(context.Background(), "operator", "system")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Standard lead tools.
		assertContainsTool(t, result.Tools, "spawn_agent")
		assertContainsTool(t, result.Tools, "complete_task")
		assertContainsTool(t, result.Tools, "report_progress")

		// System-specific lead tools.
		assertContainsTool(t, result.Tools, "consult_agent")
		assertContainsTool(t, result.Tools, "query_job")
		assertContainsTool(t, result.Tools, "query_teams")
		assertContainsTool(t, result.Tools, "surface_to_user")
	})

	t.Run("system worker gets no extra role tools", func(t *testing.T) {
		store := newMockStore()
		store.agents["helper"] = &db.Agent{
			ID:           "helper",
			Name:         "Helper",
			SystemPrompt: "You are a helper.",
			Tools:        toJSON([]string{"read", "write"}),
		}
		store.teams["system"] = &db.Team{
			ID:   "system",
			Name: "System",
		}
		store.teamAgents["system"] = []*db.TeamAgent{
			{TeamID: "system", AgentID: "operator", Role: "lead"},
			{TeamID: "system", AgentID: "helper", Role: "worker"},
		}

		c := New(store, "default-provider", "default-model")
		result, err := c.Compose(context.Background(), "helper", "system")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Only agent's own tools — no role-based tools for system workers.
		assertTools(t, result.Tools, []string{"read", "write"})
		assertNotContainsTool(t, result.Tools, "report_progress")
		assertNotContainsTool(t, result.Tools, "query_team_context")
	})
}

func TestCompose_SkillNotFound(t *testing.T) {
	store := newMockStore()
	store.agents["dev"] = &db.Agent{
		ID:           "dev",
		Name:         "Developer",
		SystemPrompt: "You are a developer.",
		Tools:        toJSON([]string{"read"}),
		Skills:       toJSON([]string{"existing-skill", "missing-skill"}),
	}
	store.skills["existing-skill"] = &db.Skill{
		ID:     "existing-skill",
		Name:   "Existing Skill",
		Prompt: "This skill exists.",
		Tools:  toJSON([]string{"skill_tool"}),
	}

	c := New(store, "default-provider", "default-model")
	result, err := c.Compose(context.Background(), "dev", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should succeed with the existing skill included.
	if !strings.Contains(result.SystemPrompt, "### Existing Skill") {
		t.Error("SystemPrompt missing existing skill")
	}
	assertContainsTool(t, result.Tools, "skill_tool")

	// Missing skill should be silently skipped (logged as warning).
	if strings.Contains(result.SystemPrompt, "missing-skill") {
		t.Error("SystemPrompt should not contain missing skill reference")
	}
}

func TestCompose_AgentNotFound(t *testing.T) {
	store := newMockStore()

	c := New(store, "default-provider", "default-model")
	_, err := c.Compose(context.Background(), "nonexistent", "")
	if err == nil {
		t.Fatal("expected error for missing agent, got nil")
	}
	if !errors.Is(err, db.ErrNotFound) {
		t.Errorf("error = %v, want wrapped db.ErrNotFound", err)
	}
}

// --- Test helpers ---

func assertTools(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("tools count = %d, want %d\n  got:  %v\n  want: %v", len(got), len(want), got, want)
		return
	}
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}
	for _, g := range got {
		if !wantSet[g] {
			t.Errorf("unexpected tool %q in result\n  got:  %v\n  want: %v", g, got, want)
		}
	}
}

func assertContainsTool(t *testing.T, tools []string, name string) {
	t.Helper()
	for _, tool := range tools {
		if tool == name {
			return
		}
	}
	t.Errorf("tools %v missing expected tool %q", tools, name)
}

func assertNotContainsTool(t *testing.T, tools []string, name string) {
	t.Helper()
	for _, tool := range tools {
		if tool == name {
			t.Errorf("tools %v should not contain %q", tools, name)
			return
		}
	}
}
