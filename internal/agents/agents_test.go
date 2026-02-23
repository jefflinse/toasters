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
	dir := t.TempDir()

	builderContent := "---\nmode: worker\n---\nbuilder body"
	coordContent := "---\nmode: primary\n---\ncoord body"

	writeFile := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	writeFile("builder.md", builderContent)
	writeFile("coordinator.md", coordContent)

	if err := SetCoordinator(dir, "builder"); err != nil {
		t.Fatalf("SetCoordinator: %v", err)
	}

	// builder.md should now have mode: primary
	builderAgent, err := ParseFile(filepath.Join(dir, "builder.md"))
	if err != nil {
		t.Fatalf("ParseFile builder.md: %v", err)
	}
	if builderAgent.Mode != "primary" {
		t.Errorf("builder.md: got mode=%q, want %q", builderAgent.Mode, "primary")
	}

	// coordinator.md should now have mode: worker
	coordAgent, err := ParseFile(filepath.Join(dir, "coordinator.md"))
	if err != nil {
		t.Fatalf("ParseFile coordinator.md: %v", err)
	}
	if coordAgent.Mode != "worker" {
		t.Errorf("coordinator.md: got mode=%q, want %q", coordAgent.Mode, "worker")
	}

	// Nonexistent agent should return an error.
	if err := SetCoordinator(dir, "nonexistent"); err == nil {
		t.Error("expected error for nonexistent agent, got nil")
	}
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
