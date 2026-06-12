package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDef(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReload_PicksUpEdits(t *testing.T) {
	dir := t.TempDir()
	writeDef(t, dir, "roles/coder.md", "---\nname: Coder\n---\nOld body. {{ instructions.style }}")
	writeDef(t, dir, "instructions/style.md", "Be terse.")

	e := NewEngine()
	if err := e.LoadDir(dir, "system"); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}

	out, err := e.Compose("coder", nil, nil)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if !strings.Contains(out, "Old body.") || !strings.Contains(out, "Be terse.") {
		t.Fatalf("initial compose missing content: %q", out)
	}

	// Edit both definitions on disk — the file-watcher path.
	writeDef(t, dir, "roles/coder.md", "---\nname: Coder\n---\nNew body. {{ instructions.style }}")
	writeDef(t, dir, "instructions/style.md", "Be verbose.")

	if err := e.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	out, err = e.Compose("coder", nil, nil)
	if err != nil {
		t.Fatalf("Compose after reload: %v", err)
	}
	if !strings.Contains(out, "New body.") || !strings.Contains(out, "Be verbose.") {
		t.Errorf("reload did not take effect: %q", out)
	}
}

func TestReload_PreservesSyntheticInstructions(t *testing.T) {
	dir := t.TempDir()
	writeDef(t, dir, "roles/coder.md", "---\nname: Coder\n---\n{{ instructions.coarse-granularity }}")

	e := NewEngine()
	if err := e.LoadDir(dir, "system"); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	// Runtime-injected instruction (the granularity machinery does this).
	e.SetInstruction("coarse-granularity", "Split into 3-5 tasks.")

	if err := e.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	body, ok := e.Instruction("coarse-granularity")
	if !ok || body != "Split into 3-5 tasks." {
		t.Errorf("synthetic instruction lost on reload: %q (ok=%v)", body, ok)
	}
	out, err := e.Compose("coder", nil, nil)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if !strings.Contains(out, "Split into 3-5 tasks.") {
		t.Errorf("composed prompt missing synthetic instruction: %q", out)
	}
}

func TestReload_PreservesUserOverSystemPrecedence(t *testing.T) {
	sysDir, userDir := t.TempDir(), t.TempDir()
	writeDef(t, sysDir, "roles/coder.md", "---\nname: Coder\n---\nsystem version")
	writeDef(t, userDir, "roles/coder.md", "---\nname: Coder\n---\nuser version")

	e := NewEngine()
	if err := e.LoadDir(sysDir, "system"); err != nil {
		t.Fatal(err)
	}
	if err := e.LoadDir(userDir, "user"); err != nil {
		t.Fatal(err)
	}

	if err := e.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	role := e.Role("coder")
	if role == nil {
		t.Fatal("role missing after reload")
	}
	if role.Source != "user" || !strings.Contains(role.Body, "user version") {
		t.Errorf("user shadowing lost on reload: source=%s body=%q", role.Source, role.Body)
	}
}

// Reload must be safe concurrently with Compose and role lookups — the file
// watcher fires while graph dispatch composes prompts. Meaningful under -race.
func TestReload_ConcurrentWithCompose(t *testing.T) {
	dir := t.TempDir()
	writeDef(t, dir, "roles/coder.md", "---\nname: Coder\n---\nbody {{ instructions.style }}")
	writeDef(t, dir, "instructions/style.md", "style")
	writeDef(t, dir, "toolchains/go.md", "---\nid: go\n---\nGo {{ vars.version }}")

	e := NewEngine()
	if err := e.LoadDir(dir, "system"); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = e.Compose("coder", nil, nil)
			_ = e.Role("coder")
			_ = e.Roles()
			_ = e.Toolchains()
			_, _ = e.SchemaJSON("nope")
			e.SetGlobal("k", "v")
		}
	}()

	for range 50 {
		if err := e.Reload(); err != nil {
			t.Fatalf("Reload: %v", err)
		}
	}
	close(stop)
	<-done
}
