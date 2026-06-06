package dagmap

import (
	"fmt"
	"strings"
	"testing"
)

// TestRender_FanoutExpansion verifies fan-out children expand as indented rows
// in the list view and collapse to a "+N" badge in the linear views.
func TestRender_FanoutExpansion(t *testing.T) {
	top := NewFeature()
	top.Children = map[string][]string{
		"implement": {"implement#0", "implement#1", "implement.judge"},
	}
	states := NodeStates{
		"plan":            {Phase: PhaseCompleted},
		"implement":       {Phase: PhaseRunning},
		"implement#0":     {Phase: PhaseCompleted},
		"implement#1":     {Phase: PhaseRunning},
		"implement.judge": {Phase: PhasePending},
		"test":            {Phase: PhasePending},
		"review":          {Phase: PhasePending},
	}

	list := RenderList(top, states)
	for _, want := range []string{"implement#0", "implement#1", "implement.judge"} {
		if !strings.Contains(list, want) {
			t.Errorf("list view missing fan-out child %q\n%s", want, list)
		}
	}
	if strings.Contains(list, "plan#") {
		t.Errorf("unexpected child row for childless node plan:\n%s", list)
	}

	if got := RenderBreadcrumb(top, states); !strings.Contains(got, "+3") {
		t.Errorf("breadcrumb missing fan-out badge +3:\n%s", got)
	}
	if got := Render(top, states); !strings.Contains(got, "+3") {
		t.Errorf("horizontal view missing fan-out badge +3:\n%s", got)
	}
}

// TestRender_Preview prints each template's rendered topology. Run with
// `go test -v -run TestRender_Preview ./internal/tui/dagmap/` to iterate
// on the renderer visually.
func TestRender_Preview(t *testing.T) {
	fixtures := []struct {
		name string
		t    Topology
	}{
		{"SingleWorker", SingleWorker()},
		{"Prototype", Prototype()},
		{"NewFeature", NewFeature()},
		{"BugFix", BugFix()},
	}
	for _, f := range fixtures {
		fmt.Printf("\n╔══ HORIZONTAL: %s ══╗\n\n", f.name)
		fmt.Println(Render(f.t, nil))
		fmt.Printf("\n╔══ VERTICAL: %s ══╗\n\n", f.name)
		fmt.Println(RenderVertical(f.t, nil))
		fmt.Println()
	}
}

// TestRender_LiveStates shows BugFix mid-run with implement actively running
// on its second iteration (after one tests_failed loop).
func TestRender_LiveStates(t *testing.T) {
	states := NodeStates{
		"investigate": {Phase: PhaseCompleted},
		"plan":        {Phase: PhaseCompleted},
		"implement":   {Phase: PhaseRunning, ExecCount: 2},
		"test":        {Phase: PhaseCompleted, ExecCount: 1, LastStatus: "tests_failed"},
		"review":      {Phase: PhasePending},
	}
	top := BugFix()
	fmt.Println("\n╔══ LIVE BugFix (list) ══╗")
	fmt.Println(RenderList(top, states))
	fmt.Println("\n╔══ LIVE BugFix (breadcrumb) ══╗")
	fmt.Println(RenderBreadcrumb(top, states))
	fmt.Println("\n╔══ LIVE BugFix (horizontal) ══╗")
	fmt.Println(Render(top, states))
	fmt.Println("\n╔══ LIVE BugFix (vertical) ══╗")
	fmt.Println(RenderVertical(top, states))
}
