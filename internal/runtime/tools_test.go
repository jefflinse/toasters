package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir)

	// Create a test file with numbered lines.
	var content strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&content, "line %d\n", i)
	}
	writeTestFile(t, dir, "test.txt", content.String())

	t.Run("read entire file", func(t *testing.T) {
		result, err := ct.Execute(context.Background(), "read_file", mustJSON(t, map[string]any{
			"path": "test.txt",
		}))
		assertNoError(t, err)
		assertContains(t, result, "1: line 1")
		assertContains(t, result, "20: line 20")
	})

	t.Run("read with offset", func(t *testing.T) {
		result, err := ct.Execute(context.Background(), "read_file", mustJSON(t, map[string]any{
			"path":   "test.txt",
			"offset": 5,
		}))
		assertNoError(t, err)
		assertContains(t, result, "5: line 5")
		assertNotContains(t, result, "4: line 4")
	})

	t.Run("read with limit", func(t *testing.T) {
		result, err := ct.Execute(context.Background(), "read_file", mustJSON(t, map[string]any{
			"path":  "test.txt",
			"limit": 3,
		}))
		assertNoError(t, err)
		assertContains(t, result, "1: line 1")
		assertContains(t, result, "3: line 3")
		assertNotContains(t, result, "4: line 4")
	})

	t.Run("read with offset and limit", func(t *testing.T) {
		result, err := ct.Execute(context.Background(), "read_file", mustJSON(t, map[string]any{
			"path":   "test.txt",
			"offset": 10,
			"limit":  3,
		}))
		assertNoError(t, err)
		assertContains(t, result, "10: line 10")
		assertContains(t, result, "12: line 12")
		assertNotContains(t, result, "9: line 9")
		assertNotContains(t, result, "13: line 13")
	})

	t.Run("read nonexistent file", func(t *testing.T) {
		_, err := ct.Execute(context.Background(), "read_file", mustJSON(t, map[string]any{
			"path": "nonexistent.txt",
		}))
		assertError(t, err)
	})

	t.Run("read empty file", func(t *testing.T) {
		writeTestFile(t, dir, "empty.txt", "")
		result, err := ct.Execute(context.Background(), "read_file", mustJSON(t, map[string]any{
			"path": "empty.txt",
		}))
		assertNoError(t, err)
		assertContains(t, result, "empty")
	})
}

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir)

	t.Run("write new file", func(t *testing.T) {
		result, err := ct.Execute(context.Background(), "write_file", mustJSON(t, map[string]any{
			"path":    "output.txt",
			"content": "hello world",
		}))
		assertNoError(t, err)
		assertContains(t, result, "11 bytes")

		data, err := os.ReadFile(filepath.Join(dir, "output.txt"))
		assertNoError(t, err)
		assertEqual(t, "hello world", string(data))
	})

	t.Run("write creates parent directories", func(t *testing.T) {
		result, err := ct.Execute(context.Background(), "write_file", mustJSON(t, map[string]any{
			"path":    "a/b/c/deep.txt",
			"content": "deep content",
		}))
		assertNoError(t, err)
		assertContains(t, result, "12 bytes")

		data, err := os.ReadFile(filepath.Join(dir, "a", "b", "c", "deep.txt"))
		assertNoError(t, err)
		assertEqual(t, "deep content", string(data))
	})

	t.Run("overwrite existing file", func(t *testing.T) {
		writeTestFile(t, dir, "existing.txt", "old content")
		_, err := ct.Execute(context.Background(), "write_file", mustJSON(t, map[string]any{
			"path":    "existing.txt",
			"content": "new content",
		}))
		assertNoError(t, err)

		data, err := os.ReadFile(filepath.Join(dir, "existing.txt"))
		assertNoError(t, err)
		assertEqual(t, "new content", string(data))
	})
}

func TestEditFile(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir)

	t.Run("successful edit", func(t *testing.T) {
		writeTestFile(t, dir, "edit.txt", "hello world\nfoo bar\nbaz qux\n")
		result, err := ct.Execute(context.Background(), "edit_file", mustJSON(t, map[string]any{
			"path":       "edit.txt",
			"old_string": "foo bar",
			"new_string": "FOO BAR",
		}))
		assertNoError(t, err)
		assertContains(t, result, "edited")

		data, err := os.ReadFile(filepath.Join(dir, "edit.txt"))
		assertNoError(t, err)
		assertContains(t, string(data), "FOO BAR")
		assertNotContains(t, string(data), "foo bar")
	})

	t.Run("old_string not found", func(t *testing.T) {
		writeTestFile(t, dir, "edit2.txt", "hello world\n")
		_, err := ct.Execute(context.Background(), "edit_file", mustJSON(t, map[string]any{
			"path":       "edit2.txt",
			"old_string": "not here",
			"new_string": "replacement",
		}))
		assertError(t, err)
		assertContains(t, err.Error(), "not found")
	})

	t.Run("old_string not unique", func(t *testing.T) {
		writeTestFile(t, dir, "edit3.txt", "aaa\naaa\naaa\n")
		_, err := ct.Execute(context.Background(), "edit_file", mustJSON(t, map[string]any{
			"path":       "edit3.txt",
			"old_string": "aaa",
			"new_string": "bbb",
		}))
		assertError(t, err)
		assertContains(t, err.Error(), "3 times")
	})

	t.Run("edit nonexistent file", func(t *testing.T) {
		_, err := ct.Execute(context.Background(), "edit_file", mustJSON(t, map[string]any{
			"path":       "nonexistent.txt",
			"old_string": "foo",
			"new_string": "bar",
		}))
		assertError(t, err)
	})
}

func TestGlob(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir)

	// Create test files.
	writeTestFile(t, dir, "a.go", "package a")
	writeTestFile(t, dir, "b.go", "package b")
	writeTestFile(t, dir, "c.txt", "text")
	mkdirAll(t, dir, "sub")
	writeTestFile(t, dir, "sub/d.go", "package sub")
	writeTestFile(t, dir, "sub/e.txt", "text")

	t.Run("simple pattern", func(t *testing.T) {
		result, err := ct.Execute(context.Background(), "glob", mustJSON(t, map[string]any{
			"pattern": "*.go",
		}))
		assertNoError(t, err)
		assertContains(t, result, "a.go")
		assertContains(t, result, "b.go")
		assertNotContains(t, result, "c.txt")
	})

	t.Run("recursive pattern", func(t *testing.T) {
		result, err := ct.Execute(context.Background(), "glob", mustJSON(t, map[string]any{
			"pattern": "**/*.go",
		}))
		assertNoError(t, err)
		assertContains(t, result, "a.go")
		assertContains(t, result, "b.go")
		assertContains(t, result, "d.go")
	})

	t.Run("no matches", func(t *testing.T) {
		result, err := ct.Execute(context.Background(), "glob", mustJSON(t, map[string]any{
			"pattern": "*.rs",
		}))
		assertNoError(t, err)
		assertContains(t, result, "no matches")
	})
}

func TestGrep(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir)

	writeTestFile(t, dir, "a.go", "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n")
	writeTestFile(t, dir, "b.txt", "some text\nhello world\nmore text\n")
	mkdirAll(t, dir, "sub")
	writeTestFile(t, dir, "sub/c.go", "package sub\n\n// hello from sub\n")

	t.Run("basic search", func(t *testing.T) {
		result, err := ct.Execute(context.Background(), "grep", mustJSON(t, map[string]any{
			"pattern": "hello",
		}))
		assertNoError(t, err)
		assertContains(t, result, "hello")
	})

	t.Run("with include filter", func(t *testing.T) {
		result, err := ct.Execute(context.Background(), "grep", mustJSON(t, map[string]any{
			"pattern": "hello",
			"include": "*.go",
		}))
		assertNoError(t, err)
		assertContains(t, result, ".go")
		assertNotContains(t, result, "b.txt")
	})

	t.Run("regex pattern", func(t *testing.T) {
		result, err := ct.Execute(context.Background(), "grep", mustJSON(t, map[string]any{
			"pattern": "func\\s+main",
		}))
		assertNoError(t, err)
		assertContains(t, result, "func main")
	})

	t.Run("no matches", func(t *testing.T) {
		result, err := ct.Execute(context.Background(), "grep", mustJSON(t, map[string]any{
			"pattern": "zzzznotfound",
		}))
		assertNoError(t, err)
		assertContains(t, result, "no matches")
	})

	t.Run("invalid regex", func(t *testing.T) {
		_, err := ct.Execute(context.Background(), "grep", mustJSON(t, map[string]any{
			"pattern": "[invalid",
		}))
		assertError(t, err)
	})
}

func TestShell(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir)

	t.Run("simple command", func(t *testing.T) {
		result, err := ct.Execute(context.Background(), "shell", mustJSON(t, map[string]any{
			"command": "echo hello",
		}))
		assertNoError(t, err)
		assertContains(t, result, "hello")
	})

	t.Run("command with exit code", func(t *testing.T) {
		result, err := ct.Execute(context.Background(), "shell", mustJSON(t, map[string]any{
			"command": "echo output && exit 1",
		}))
		// Non-zero exit is not an error — the LLM should see the output.
		assertNoError(t, err)
		assertContains(t, result, "output")
		assertContains(t, result, "exit status")
	})

	t.Run("command timeout", func(t *testing.T) {
		_, err := ct.Execute(context.Background(), "shell", mustJSON(t, map[string]any{
			"command": "sleep 10",
			"timeout": 1,
		}))
		assertError(t, err)
		assertContains(t, err.Error(), "timed out")
	})

	t.Run("shell disabled", func(t *testing.T) {
		noShell := NewCoreTools(dir, WithShell(false))
		_, err := noShell.Execute(context.Background(), "shell", mustJSON(t, map[string]any{
			"command": "echo hello",
		}))
		assertError(t, err)
		assertContains(t, err.Error(), "disabled")
	})
}

func TestWebFetch(t *testing.T) {
	t.Run("successful fetch", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "hello from server")
		}))
		defer srv.Close()

		dir := t.TempDir()
		ct := NewCoreTools(dir)

		result, err := ct.Execute(context.Background(), "web_fetch", mustJSON(t, map[string]any{
			"url": srv.URL,
		}))
		assertNoError(t, err)
		assertEqual(t, "hello from server", result)
	})

	t.Run("HTTP error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, "not found")
		}))
		defer srv.Close()

		dir := t.TempDir()
		ct := NewCoreTools(dir)

		_, err := ct.Execute(context.Background(), "web_fetch", mustJSON(t, map[string]any{
			"url": srv.URL,
		}))
		assertError(t, err)
		assertContains(t, err.Error(), "404")
	})

	t.Run("empty URL", func(t *testing.T) {
		dir := t.TempDir()
		ct := NewCoreTools(dir)

		_, err := ct.Execute(context.Background(), "web_fetch", mustJSON(t, map[string]any{
			"url": "",
		}))
		assertError(t, err)
	})
}

func TestPathTraversal(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir)

	tests := []struct {
		name string
		tool string
		args map[string]any
	}{
		{
			name: "read_file traversal",
			tool: "read_file",
			args: map[string]any{"path": "../../../etc/passwd"},
		},
		{
			name: "write_file traversal",
			tool: "write_file",
			args: map[string]any{"path": "../../../tmp/evil.txt", "content": "evil"},
		},
		{
			name: "edit_file traversal",
			tool: "edit_file",
			args: map[string]any{"path": "../../../etc/hosts", "old_string": "a", "new_string": "b"},
		},
		{
			name: "read_file absolute path outside workdir",
			tool: "read_file",
			args: map[string]any{"path": "/etc/passwd"},
		},
		{
			name: "grep path traversal",
			tool: "grep",
			args: map[string]any{"pattern": "root", "path": "../../../etc"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ct.Execute(context.Background(), tt.tool, mustJSON(t, tt.args))
			assertError(t, err)
			assertContains(t, err.Error(), "escapes working directory")
		})
	}
}

func TestUnknownTool(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir)

	_, err := ct.Execute(context.Background(), "nonexistent_tool", json.RawMessage(`{}`))
	assertError(t, err)
	assertContains(t, err.Error(), "unknown tool")
}

func TestDefinitions(t *testing.T) {
	dir := t.TempDir()

	t.Run("without spawner", func(t *testing.T) {
		ct := NewCoreTools(dir)
		defs := ct.Definitions()

		names := make(map[string]bool)
		for _, d := range defs {
			names[d.Name] = true
		}

		expected := []string{"read_file", "write_file", "edit_file", "glob", "grep", "shell", "web_fetch"}
		for _, name := range expected {
			if !names[name] {
				t.Errorf("missing tool definition: %s", name)
			}
		}

		if names["spawn_agent"] {
			t.Error("spawn_agent should not be present without spawner")
		}
	})

	t.Run("with spawner", func(t *testing.T) {
		ct := NewCoreTools(dir, WithSpawner(&mockSpawner{}, 0, 3))
		defs := ct.Definitions()

		names := make(map[string]bool)
		for _, d := range defs {
			names[d.Name] = true
		}

		if !names["spawn_agent"] {
			t.Error("spawn_agent should be present with spawner")
		}
	})

	t.Run("spawn_agent excluded at max depth", func(t *testing.T) {
		ct := NewCoreTools(dir, WithSpawner(&mockSpawner{}, 3, 3))
		defs := ct.Definitions()

		names := make(map[string]bool)
		for _, d := range defs {
			names[d.Name] = true
		}

		if names["spawn_agent"] {
			t.Error("spawn_agent should not be present at max depth")
		}
	})
}

func TestSpawnAgent(t *testing.T) {
	dir := t.TempDir()

	t.Run("no spawner", func(t *testing.T) {
		ct := NewCoreTools(dir)
		_, err := ct.Execute(context.Background(), "spawn_agent", mustJSON(t, map[string]any{
			"system_prompt": "test",
			"message":       "hello",
		}))
		assertError(t, err)
		assertContains(t, err.Error(), "not available")
	})

	t.Run("max depth exceeded", func(t *testing.T) {
		ct := NewCoreTools(dir, WithSpawner(&mockSpawner{}, 3, 3))
		_, err := ct.Execute(context.Background(), "spawn_agent", mustJSON(t, map[string]any{
			"system_prompt": "test",
			"message":       "hello",
		}))
		assertError(t, err)
		assertContains(t, err.Error(), "max spawn depth")
	})

	t.Run("successful spawn", func(t *testing.T) {
		spawner := &mockSpawner{result: "child result"}
		ct := NewCoreTools(dir, WithSpawner(spawner, 0, 3))
		result, err := ct.Execute(context.Background(), "spawn_agent", mustJSON(t, map[string]any{
			"system_prompt": "test",
			"message":       "hello",
		}))
		assertNoError(t, err)
		assertEqual(t, "child result", result)
	})
}

// --- Test helpers ---

type mockSpawner struct {
	result string
	err    error
}

func (m *mockSpawner) SpawnAndWait(_ context.Context, _ SpawnOpts) (string, error) {
	return m.result, m.err
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdirAll(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func assertError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func assertEqual(t *testing.T, want, got string) {
	t.Helper()
	if want != got {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Fatalf("expected %q to contain %q", s, substr)
	}
}

func assertNotContains(t *testing.T, s, substr string) {
	t.Helper()
	if strings.Contains(s, substr) {
		t.Fatalf("expected %q to NOT contain %q", s, substr)
	}
}
