package graphexec

import (
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/db"
)

func TestIsDecompositionGraph(t *testing.T) {
	cases := map[string]bool{
		"coarse-decompose": true,
		"fine-decompose":   true,
		"bug-fix":          false,
		"":                 false,
		"new-feature":      false,
	}
	for id, want := range cases {
		if got := IsDecompositionGraph(id); got != want {
			t.Errorf("IsDecompositionGraph(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestSiblingTitles_FiltersSelfAndBootstrap(t *testing.T) {
	tasks := []*db.Task{
		{ID: "a", Title: "Implement Go backend", GraphID: "new-feature"},
		{ID: "b", Title: "Implement React frontend", GraphID: "new-feature"},
		{ID: "c", Title: "Pick graph: Implement Go backend", GraphID: GraphFineDecompose},
		{ID: "d", Title: "Decompose: My Job", GraphID: GraphCoarseDecompose},
		nil,
	}
	got := SiblingTitles(tasks, "a")
	want := []string{"Implement React frontend"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("SiblingTitles(_, %q) = %v, want %v", "a", got, want)
	}
}

func TestSiblingTitles_ExcludeUnknownTask(t *testing.T) {
	tasks := []*db.Task{
		{ID: "a", Title: "T1", GraphID: "new-feature"},
		{ID: "b", Title: "T2", GraphID: "new-feature"},
	}
	got := SiblingTitles(tasks, "missing")
	if len(got) != 2 {
		t.Errorf("SiblingTitles with unknown exclude id should keep all real tasks; got %v", got)
	}
}

func TestFormatSiblingTitles(t *testing.T) {
	cases := []struct {
		name   string
		in     []string
		want   string
	}{
		{"empty", nil, ""},
		{"single", []string{"Build the API"}, "- Build the API"},
		{"multiple", []string{"A", "B", "C"}, "- A\n- B\n- C"},
		{"trims_blank", []string{"  ", "Real"}, "- Real"},
		{"trims_whitespace", []string{"  Spaced  "}, "- Spaced"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FormatSiblingTitles(c.in)
			if got != c.want {
				t.Errorf("FormatSiblingTitles(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSiblingsArtifact(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty becomes placeholder", "", noSiblingsPlaceholder},
		{"whitespace becomes placeholder", "   \n\t", noSiblingsPlaceholder},
		{"populated passes through", "- A\n- B", "- A\n- B"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := siblingsArtifact(c.in); got != c.want {
				t.Errorf("siblingsArtifact(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}

	if !strings.Contains(noSiblingsPlaceholder, "only task") {
		t.Errorf("placeholder %q lost the 'only task' signal", noSiblingsPlaceholder)
	}
}
