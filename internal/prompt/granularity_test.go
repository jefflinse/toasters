package prompt

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestSetInstruction_OverridesLoadedBody verifies that SetInstruction
// replaces the body of an instruction that was previously loaded from disk,
// and that Compose picks up the new value on the next call.
func TestSetInstruction_OverridesLoadedBody(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "roles"))
	mkdirAll(t, filepath.Join(dir, "instructions"))

	writeFile(t, filepath.Join(dir, "instructions", "vibe.md"), "original vibe")
	writeFile(t, filepath.Join(dir, "roles", "worker.md"), `---
name: Worker
---
{{ instructions.vibe }}
`)

	e := NewEngine()
	if err := e.LoadDir(dir, "test"); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}

	got, err := e.Compose("worker", nil, nil)
	if err != nil {
		t.Fatalf("Compose (first): %v", err)
	}
	if !strings.Contains(got, "original vibe") {
		t.Errorf("first compose missing original instruction: %q", got)
	}

	e.SetInstruction("vibe", "replaced vibe")

	got, err = e.Compose("worker", nil, nil)
	if err != nil {
		t.Fatalf("Compose (second): %v", err)
	}
	if !strings.Contains(got, "replaced vibe") {
		t.Errorf("second compose should reflect SetInstruction, got: %q", got)
	}
	if strings.Contains(got, "original vibe") {
		t.Errorf("second compose should not retain original body, got: %q", got)
	}
}

// TestApplyGranularity_RoutesBodyThroughSyntheticName verifies both kinds
// of granularity lever (coarse and fine): a role referencing
// {{ instructions.<kind>-granularity }} picks up the body of the selected
// level's source instruction.
func TestApplyGranularity_RoutesBodyThroughSyntheticName(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "roles"))
	mkdirAll(t, filepath.Join(dir, "instructions"))

	writeFile(t, filepath.Join(dir, "instructions", "fine-granularity-xcoarse.md"), "FINE_XCOARSE")
	writeFile(t, filepath.Join(dir, "instructions", "fine-granularity-xfine.md"), "FINE_XFINE")
	writeFile(t, filepath.Join(dir, "instructions", "coarse-granularity-xcoarse.md"), "COARSE_XCOARSE")
	writeFile(t, filepath.Join(dir, "instructions", "coarse-granularity-xfine.md"), "COARSE_XFINE")

	writeFile(t, filepath.Join(dir, "roles", "w.md"), `---
name: W
---
fine={{ globals.fine.granularity }} / coarse={{ globals.coarse.granularity }}
FINE: {{ instructions.fine-granularity }}
COARSE: {{ instructions.coarse-granularity }}
`)

	e := NewEngine()
	if err := e.LoadDir(dir, "test"); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}

	if err := ApplyGranularity(e, "fine", "xcoarse"); err != nil {
		t.Fatalf("ApplyGranularity(fine, xcoarse): %v", err)
	}
	if err := ApplyGranularity(e, "coarse", "xfine"); err != nil {
		t.Fatalf("ApplyGranularity(coarse, xfine): %v", err)
	}

	got, err := e.Compose("w", nil, nil)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	for _, want := range []string{"fine=xcoarse", "coarse=xfine", "FINE_XCOARSE", "COARSE_XFINE"} {
		if !strings.Contains(got, want) {
			t.Errorf("compose missing %q, got:\n%s", want, got)
		}
	}
	// Cross-leaking would be very bad: fine's body showing up via coarse or vice versa.
	if strings.Contains(got, "FINE_XFINE") {
		t.Errorf("fine unselected level leaked into compose")
	}
	if strings.Contains(got, "COARSE_XCOARSE") {
		t.Errorf("coarse unselected level leaked into compose")
	}

	// Change fine, coarse stays put.
	if err := ApplyGranularity(e, "fine", "xfine"); err != nil {
		t.Fatalf("ApplyGranularity(fine, xfine): %v", err)
	}
	got, err = e.Compose("w", nil, nil)
	if err != nil {
		t.Fatalf("Compose (2nd): %v", err)
	}
	if !strings.Contains(got, "FINE_XFINE") {
		t.Errorf("fine update not reflected, got:\n%s", got)
	}
	if !strings.Contains(got, "COARSE_XFINE") {
		t.Errorf("coarse value should be unchanged by fine update, got:\n%s", got)
	}
}

// TestApplyGranularity_MissingLevel_ReturnsError verifies that the helper
// surfaces a missing-file condition so startup can warn rather than
// silently composing with an unset instruction.
func TestApplyGranularity_MissingLevel_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "instructions"))

	e := NewEngine()
	if err := e.LoadDir(dir, "test"); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if err := ApplyGranularity(e, "fine", "medium"); err == nil {
		t.Errorf("expected error when fine-granularity-medium is missing")
	}
	if err := ApplyGranularity(e, "coarse", "medium"); err == nil {
		t.Errorf("expected error when coarse-granularity-medium is missing")
	}
	if err := ApplyGranularity(e, "", "medium"); err == nil {
		t.Errorf("expected error when kind is empty")
	}
}
