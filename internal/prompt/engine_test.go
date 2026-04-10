package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEngine_LoadDir_And_Compose(t *testing.T) {
	dir := t.TempDir()

	// Create directory structure.
	mkdirAll(t, filepath.Join(dir, "roles"))
	mkdirAll(t, filepath.Join(dir, "toolchains"))
	mkdirAll(t, filepath.Join(dir, "instructions"))

	// Write a toolchain.
	writeFile(t, filepath.Join(dir, "toolchains", "go.md"), `---
id: go
name: Go
description: The Go programming language toolchain.
vars:
  version:
    description: The version of Go to use.
    default: "1.26.2"
---

The current version of Go is {{ vars.version }}.
Use log/slog for structured logging.
`)

	// Write instructions.
	writeFile(t, filepath.Join(dir, "instructions", "do-exact.md"),
		"Do not make assumptions.\nDo not skip any requirements.\n")

	writeFile(t, filepath.Join(dir, "instructions", "stop-and-request.md"),
		"If you lack information, stop and ask.\n")

	// Write a role that references the toolchain and instructions.
	writeFile(t, filepath.Join(dir, "roles", "go-coder.md"), `---
name: Go Coder
description: Implements Go code.
mode: worker
---

It is {{ globals.now.month }} {{ globals.now.year }}.

{{ toolchains.go }}

{{ instructions.do-exact }}

{{ instructions.stop-and-request }}

Write clean Go code.
`)

	// Load and compose.
	engine := NewEngine()
	if err := engine.LoadDir(dir); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}

	result, err := engine.Compose("go-coder", nil)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	// Verify globals were resolved.
	now := time.Now()
	if !strings.Contains(result, now.Format("January")) {
		t.Errorf("expected current month in output, got:\n%s", result)
	}
	if !strings.Contains(result, "2026") {
		t.Errorf("expected current year in output, got:\n%s", result)
	}

	// Verify toolchain was inlined with default var.
	if !strings.Contains(result, "The current version of Go is 1.26.2.") {
		t.Errorf("expected toolchain body with default version, got:\n%s", result)
	}
	if !strings.Contains(result, "Use log/slog") {
		t.Errorf("expected toolchain content, got:\n%s", result)
	}

	// Verify instructions were inlined.
	if !strings.Contains(result, "Do not make assumptions.") {
		t.Errorf("expected do-exact instruction, got:\n%s", result)
	}
	if !strings.Contains(result, "If you lack information, stop and ask.") {
		t.Errorf("expected stop-and-request instruction, got:\n%s", result)
	}

	// Verify role's own content.
	if !strings.Contains(result, "Write clean Go code.") {
		t.Errorf("expected role body content, got:\n%s", result)
	}

	// Verify no unresolved references remain.
	if strings.Contains(result, "{{") {
		t.Errorf("unresolved template references remain in output:\n%s", result)
	}
}

func TestEngine_Compose_VarOverrides(t *testing.T) {
	dir := t.TempDir()
	mkdirAll(t, filepath.Join(dir, "roles"))
	mkdirAll(t, filepath.Join(dir, "toolchains"))

	writeFile(t, filepath.Join(dir, "toolchains", "go.md"), `---
id: go
vars:
  version:
    default: "1.26.2"
---
Go version: {{ vars.version }}
`)

	writeFile(t, filepath.Join(dir, "roles", "test.md"), `---
name: Test Role
---
{{ toolchains.go }}
`)

	engine := NewEngine()
	if err := engine.LoadDir(dir); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}

	// Override the Go version.
	result, err := engine.Compose("test", map[string]string{
		"go.version": "1.25.0",
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	if !strings.Contains(result, "Go version: 1.25.0") {
		t.Errorf("expected overridden version, got:\n%s", result)
	}
	if strings.Contains(result, "1.26.2") {
		t.Errorf("default version should not appear when overridden, got:\n%s", result)
	}
}

func TestEngine_Compose_MissingRole(t *testing.T) {
	engine := NewEngine()
	_, err := engine.Compose("nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for missing role")
	}
}

func TestEngine_Compose_MissingDir(t *testing.T) {
	engine := NewEngine()
	// Loading a nonexistent directory should not error (empty engine).
	if err := engine.LoadDir("/nonexistent/path"); err != nil {
		t.Fatalf("LoadDir should not error for missing dir: %v", err)
	}
}

func TestEngine_Compose_WithActualDefaults(t *testing.T) {
	// Test with the actual defaults/user/ directory if it exists.
	defaultsDir := filepath.Join("..", "..", "defaults", "user")
	if _, err := os.Stat(defaultsDir); os.IsNotExist(err) {
		t.Skip("defaults/user not found, skipping integration test")
	}

	engine := NewEngine()
	if err := engine.LoadDir(defaultsDir); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}

	result, err := engine.Compose("go-coder", nil)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}

	// Should have resolved everything.
	if strings.Contains(result, "{{ ") {
		// Find the unresolved references for debugging.
		for _, line := range strings.Split(result, "\n") {
			if strings.Contains(line, "{{ ") {
				t.Errorf("unresolved reference: %s", line)
			}
		}
	}

	// Should contain content from all three sources.
	if !strings.Contains(result, "Go") {
		t.Error("expected Go toolchain content")
	}
	if !strings.Contains(result, "Do not make assumptions") {
		t.Error("expected do-exact instruction content")
	}
	if !strings.Contains(result, "production-ready") {
		t.Error("expected role body content")
	}

	t.Logf("Composed prompt (%d chars):\n%s", len(result), result)
}

func TestEngine_Compose_AllRoles(t *testing.T) {
	defaultsDir := filepath.Join("..", "..", "defaults", "user")
	if _, err := os.Stat(defaultsDir); os.IsNotExist(err) {
		t.Skip("defaults/user not found, skipping integration test")
	}

	engine := NewEngine()
	if err := engine.LoadDir(defaultsDir); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}

	for _, name := range engine.Roles() {
		t.Run(name, func(t *testing.T) {
			result, err := engine.Compose(name, nil)
			if err != nil {
				t.Fatalf("Compose(%q): %v", name, err)
			}

			// No unresolved references.
			for _, line := range strings.Split(result, "\n") {
				if strings.Contains(line, "{{ ") {
					t.Errorf("unresolved reference: %s", line)
				}
			}

			if len(result) == 0 {
				t.Error("composed prompt is empty")
			}

			t.Logf("Composed %q (%d chars)", name, len(result))
		})
	}
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
