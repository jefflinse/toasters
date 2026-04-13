package tui

import (
	"testing"

	"github.com/jefflinse/toasters/internal/service"
)

// --- filterAgentsForTeam tests ---

func TestFilterAgentsForTeam_ExcludesCoordinatorAndWorkers(t *testing.T) {
	t.Parallel()
	coord := service.Worker{Name: "alpha"}
	team := service.TeamView{
		Coordinator: &coord,
		Workers:     []service.Worker{{Name: "beta"}},
	}
	available := []service.Worker{
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
	available := []service.Worker{
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
	coord := service.Worker{Name: "alpha"}
	team := service.TeamView{
		Coordinator: &coord,
		Workers:     []service.Worker{{Name: "beta"}},
	}
	available := []service.Worker{
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
	coord := service.Worker{Name: "alpha"}
	team := service.TeamView{
		Coordinator: &coord,
		Workers:     []service.Worker{{Name: "beta"}},
	}

	got := filterAgentsForTeam(team, nil)

	if len(got) != 0 {
		t.Errorf("got %d agents, want 0", len(got))
	}
}

func TestFilterAgentsForTeam_OnlyCoordinator(t *testing.T) {
	t.Parallel()
	coord := service.Worker{Name: "alpha"}
	team := service.TeamView{
		Coordinator: &coord,
		Workers:     nil,
	}
	available := []service.Worker{
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
		Workers:     []service.Worker{{Name: "alpha"}, {Name: "beta"}},
	}
	available := []service.Worker{
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
	coord := service.Worker{Name: "Alpha"}
	team := service.TeamView{
		Coordinator: &coord,
		Workers:     nil,
	}
	available := []service.Worker{
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
	coord := service.Worker{Name: "b"}
	team := service.TeamView{
		Coordinator: &coord,
	}
	available := []service.Worker{
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
