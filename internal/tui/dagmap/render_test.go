package dagmap

import (
	"fmt"
	"testing"
)

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
