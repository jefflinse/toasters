package tui

import (
	"testing"

	"github.com/jefflinse/toasters/internal/service"
)

// --- filterAgentsForTeam tests ---

func TestFilterAgentsForTeam_ExcludesCoordinatorAndWorkers(t *testing.T) {
	t.Parallel()
	coord := service.Agent{Name: "alpha"}
	team := service.TeamView{
		Coordinator: &coord,
		Workers:     []service.Agent{{Name: "beta"}},
	}
	available := []service.Agent{
		{Name: "alpha"},
		{Name: "beta"},
		{Name: "charlie"},
	}

	got := filterAgentsForTeam(team, available)

	if len(got) != 1 {
		t.Fatalf("got %d agents, want 1", len(got))
	}
	if got[0].Name != "charlie" {
		t.Errorf("got[0].Name = %q, want %q", got[0].Name, "charlie")
	}
}

func TestFilterAgentsForTeam_NoAgentsInTeam(t *testing.T) {
	t.Parallel()
	team := service.TeamView{
		Coordinator: nil,
		Workers:     nil,
	}
	available := []service.Agent{
		{Name: "alpha"},
		{Name: "beta"},
	}

	got := filterAgentsForTeam(team, available)

	if len(got) != 2 {
		t.Fatalf("got %d agents, want 2", len(got))
	}
}

func TestFilterAgentsForTeam_AllAgentsAlreadyInTeam(t *testing.T) {
	t.Parallel()
	coord := service.Agent{Name: "alpha"}
	team := service.TeamView{
		Coordinator: &coord,
		Workers:     []service.Agent{{Name: "beta"}},
	}
	available := []service.Agent{
		{Name: "alpha"},
		{Name: "beta"},
	}

	got := filterAgentsForTeam(team, available)

	if len(got) != 0 {
		t.Errorf("got %d agents, want 0", len(got))
	}
}

func TestFilterAgentsForTeam_EmptyAvailable(t *testing.T) {
	t.Parallel()
	coord := service.Agent{Name: "alpha"}
	team := service.TeamView{
		Coordinator: &coord,
		Workers:     []service.Agent{{Name: "beta"}},
	}

	got := filterAgentsForTeam(team, nil)

	if len(got) != 0 {
		t.Errorf("got %d agents, want 0", len(got))
	}
}

func TestFilterAgentsForTeam_OnlyCoordinator(t *testing.T) {
	t.Parallel()
	coord := service.Agent{Name: "alpha"}
	team := service.TeamView{
		Coordinator: &coord,
		Workers:     nil,
	}
	available := []service.Agent{
		{Name: "alpha"},
		{Name: "beta"},
		{Name: "charlie"},
	}

	got := filterAgentsForTeam(team, available)

	if len(got) != 2 {
		t.Fatalf("got %d agents, want 2", len(got))
	}
	names := map[string]bool{got[0].Name: true, got[1].Name: true}
	if !names["beta"] || !names["charlie"] {
		t.Errorf("got names %v, want {beta, charlie}", names)
	}
}

func TestFilterAgentsForTeam_OnlyWorkers(t *testing.T) {
	t.Parallel()
	team := service.TeamView{
		Coordinator: nil,
		Workers:     []service.Agent{{Name: "alpha"}, {Name: "beta"}},
	}
	available := []service.Agent{
		{Name: "alpha"},
		{Name: "beta"},
		{Name: "charlie"},
	}

	got := filterAgentsForTeam(team, available)

	if len(got) != 1 {
		t.Fatalf("got %d agents, want 1", len(got))
	}
	if got[0].Name != "charlie" {
		t.Errorf("got[0].Name = %q, want %q", got[0].Name, "charlie")
	}
}

func TestFilterAgentsForTeam_CaseSensitive(t *testing.T) {
	t.Parallel()
	// "Alpha" (capital A) is in the team; "alpha" (lowercase) is a different name.
	coord := service.Agent{Name: "Alpha"}
	team := service.TeamView{
		Coordinator: &coord,
		Workers:     nil,
	}
	available := []service.Agent{
		{Name: "Alpha"},
		{Name: "alpha"}, // different case — should NOT be filtered out
	}

	got := filterAgentsForTeam(team, available)

	if len(got) != 1 {
		t.Fatalf("got %d agents, want 1 (case-sensitive comparison)", len(got))
	}
	if got[0].Name != "alpha" {
		t.Errorf("got[0].Name = %q, want %q", got[0].Name, "alpha")
	}
}

func TestFilterAgentsForTeam_PreservesOrder(t *testing.T) {
	t.Parallel()
	coord := service.Agent{Name: "b"}
	team := service.TeamView{
		Coordinator: &coord,
	}
	available := []service.Agent{
		{Name: "a"},
		{Name: "b"}, // filtered out
		{Name: "c"},
		{Name: "d"},
	}

	got := filterAgentsForTeam(team, available)

	if len(got) != 3 {
		t.Fatalf("got %d agents, want 3", len(got))
	}
	wantOrder := []string{"a", "c", "d"}
	for i, want := range wantOrder {
		if got[i].Name != want {
			t.Errorf("got[%d].Name = %q, want %q", i, got[i].Name, want)
		}
	}
}

// --- filterSkillsForAgent tests ---

func TestFilterSkillsForAgent_ExcludesExistingSkills(t *testing.T) {
	t.Parallel()
	a := service.Agent{Skills: []string{"skill-x"}}
	available := []service.Skill{
		{Name: "skill-x"},
		{Name: "skill-y"},
	}

	got := filterSkillsForAgent(a, available)

	if len(got) != 1 {
		t.Fatalf("got %d skills, want 1", len(got))
	}
	if got[0].Name != "skill-y" {
		t.Errorf("got[0].Name = %q, want %q", got[0].Name, "skill-y")
	}
}

func TestFilterSkillsForAgent_NoSkillsOnAgent(t *testing.T) {
	t.Parallel()
	a := service.Agent{Skills: nil}
	available := []service.Skill{
		{Name: "skill-x"},
		{Name: "skill-y"},
	}

	got := filterSkillsForAgent(a, available)

	if len(got) != 2 {
		t.Fatalf("got %d skills, want 2", len(got))
	}
}

func TestFilterSkillsForAgent_EmptySkills(t *testing.T) {
	t.Parallel()
	a := service.Agent{Skills: []string{}}
	available := []service.Skill{
		{Name: "skill-x"},
		{Name: "skill-y"},
	}

	got := filterSkillsForAgent(a, available)

	if len(got) != 2 {
		t.Fatalf("got %d skills, want 2", len(got))
	}
}

func TestFilterSkillsForAgent_AllSkillsAlreadyAssigned(t *testing.T) {
	t.Parallel()
	a := service.Agent{Skills: []string{"skill-x", "skill-y"}}
	available := []service.Skill{
		{Name: "skill-x"},
		{Name: "skill-y"},
	}

	got := filterSkillsForAgent(a, available)

	if len(got) != 0 {
		t.Errorf("got %d skills, want 0", len(got))
	}
}

func TestFilterSkillsForAgent_EmptyAvailable(t *testing.T) {
	t.Parallel()
	a := service.Agent{Skills: []string{"skill-x"}}

	got := filterSkillsForAgent(a, nil)

	if len(got) != 0 {
		t.Errorf("got %d skills, want 0", len(got))
	}
}

func TestFilterSkillsForAgent_CaseSensitive(t *testing.T) {
	t.Parallel()
	// "Skill-X" (capital S) is on the agent; "skill-x" (lowercase) is different.
	a := service.Agent{Skills: []string{"Skill-X"}}
	available := []service.Skill{
		{Name: "Skill-X"},
		{Name: "skill-x"}, // different case — should NOT be filtered out
	}

	got := filterSkillsForAgent(a, available)

	if len(got) != 1 {
		t.Fatalf("got %d skills, want 1 (case-sensitive comparison)", len(got))
	}
	if got[0].Name != "skill-x" {
		t.Errorf("got[0].Name = %q, want %q", got[0].Name, "skill-x")
	}
}

func TestFilterSkillsForAgent_PreservesOrder(t *testing.T) {
	t.Parallel()
	a := service.Agent{Skills: []string{"b"}}
	available := []service.Skill{
		{Name: "a"},
		{Name: "b"}, // filtered out
		{Name: "c"},
		{Name: "d"},
	}

	got := filterSkillsForAgent(a, available)

	if len(got) != 3 {
		t.Fatalf("got %d skills, want 3", len(got))
	}
	wantOrder := []string{"a", "c", "d"}
	for i, want := range wantOrder {
		if got[i].Name != want {
			t.Errorf("got[%d].Name = %q, want %q", i, got[i].Name, want)
		}
	}
}

func TestFilterSkillsForAgent_MultipleExistingSkills(t *testing.T) {
	t.Parallel()
	a := service.Agent{Skills: []string{"skill-a", "skill-c", "skill-e"}}
	available := []service.Skill{
		{Name: "skill-a"},
		{Name: "skill-b"},
		{Name: "skill-c"},
		{Name: "skill-d"},
		{Name: "skill-e"},
	}

	got := filterSkillsForAgent(a, available)

	if len(got) != 2 {
		t.Fatalf("got %d skills, want 2", len(got))
	}
	if got[0].Name != "skill-b" {
		t.Errorf("got[0].Name = %q, want %q", got[0].Name, "skill-b")
	}
	if got[1].Name != "skill-d" {
		t.Errorf("got[1].Name = %q, want %q", got[1].Name, "skill-d")
	}
}
