package loader

import (
	"context"
	"path/filepath"
	"testing"
)

const userBugFixYAML = `id: bug-fix
name: Bug Fix
entry: investigate
nodes:
  - id: investigate
    role: investigator
  - id: plan
    role: planner
edges:
  - from: investigate
    to: plan
  - from: plan
    to: end
`

const systemBugFixYAML = `id: bug-fix
name: System Bug Fix
entry: investigate
nodes:
  - id: investigate
    role: investigator
edges:
  - from: investigate
    to: end
`

const otherGraphYAML = `id: recon
entry: investigate
nodes:
  - id: investigate
    role: investigator
edges:
  - from: investigate
    to: end
`

const invalidYAML = `id: busted
# no nodes → validation fails
`

func TestLoad_UserGraph(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	writeFile(t, filepath.Join(configDir, "user", "graphs", "bug-fix.yaml"), userBugFixYAML)

	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	graphs := l.Graphs()
	if len(graphs) != 1 {
		t.Fatalf("Graphs: got %d, want 1", len(graphs))
	}
	if graphs[0].ID != "bug-fix" {
		t.Errorf("ID = %q, want %q", graphs[0].ID, "bug-fix")
	}
	if graphs[0].Name != "Bug Fix" {
		t.Errorf("Name = %q, want %q", graphs[0].Name, "Bug Fix")
	}
}

func TestLoad_UserGraphShadowsSystem(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	writeFile(t, filepath.Join(configDir, "system", "graphs", "bug-fix.yaml"), systemBugFixYAML)
	writeFile(t, filepath.Join(configDir, "system", "graphs", "recon.yaml"), otherGraphYAML)
	writeFile(t, filepath.Join(configDir, "user", "graphs", "bug-fix.yaml"), userBugFixYAML)

	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	graphs := l.Graphs()
	if len(graphs) != 2 {
		t.Fatalf("Graphs: got %d, want 2 (recon from system + bug-fix from user)", len(graphs))
	}

	byID := map[string]string{}
	for _, g := range graphs {
		byID[g.ID] = g.Name
	}
	if byID["bug-fix"] != "Bug Fix" {
		t.Errorf("bug-fix name = %q, want user version %q", byID["bug-fix"], "Bug Fix")
	}
	if _, ok := byID["recon"]; !ok {
		t.Errorf("recon graph (system-only) missing")
	}
}

func TestLoad_InvalidGraphIsSkipped(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	writeFile(t, filepath.Join(configDir, "user", "graphs", "good.yaml"), userBugFixYAML)
	writeFile(t, filepath.Join(configDir, "user", "graphs", "busted.yaml"), invalidYAML)

	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	graphs := l.Graphs()
	if len(graphs) != 1 {
		t.Fatalf("Graphs: got %d, want 1 (invalid graph skipped)", len(graphs))
	}
	if graphs[0].ID != "bug-fix" {
		t.Errorf("expected surviving graph to be bug-fix, got %q", graphs[0].ID)
	}
}

func TestLoad_NoGraphsDirIsHarmless(t *testing.T) {
	store := openTestStore(t)
	configDir := t.TempDir()
	ctx := context.Background()

	// No graphs/ dir at all.
	l := New(store, configDir)
	if err := l.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if graphs := l.Graphs(); len(graphs) != 0 {
		t.Errorf("Graphs: got %d entries, want 0", len(graphs))
	}
}
