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

	"github.com/jefflinse/toasters/internal/provider"
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

func TestEditFileSizeLimit(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir)

	t.Run("rejects file larger than 10MB", func(t *testing.T) {
		// Create a file just over the 10 MB limit.
		const limit = 10 * 1024 * 1024
		path := filepath.Join(dir, "large.txt")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		// Write limit+1 bytes using Truncate (fast, no actual I/O).
		if err := f.Truncate(limit + 1); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		_ = f.Close()

		_, err = ct.Execute(context.Background(), "edit_file", mustJSON(t, map[string]any{
			"path":       "large.txt",
			"old_string": "foo",
			"new_string": "bar",
		}))
		assertError(t, err)
		assertContains(t, err.Error(), "file too large to edit")
		assertContains(t, err.Error(), fmt.Sprintf("max %d", limit))
	})

	t.Run("accepts file at exactly 10MB", func(t *testing.T) {
		const limit = 10 * 1024 * 1024
		content := strings.Repeat("a", limit)
		writeTestFile(t, dir, "exact10mb.txt", content)

		// The edit will fail because old_string won't be found, but it should
		// NOT fail with the size limit error — it should get past the size check.
		_, err := ct.Execute(context.Background(), "edit_file", mustJSON(t, map[string]any{
			"path":       "exact10mb.txt",
			"old_string": "NOTFOUND",
			"new_string": "bar",
		}))
		assertError(t, err)
		assertContains(t, err.Error(), "not found")
		assertNotContains(t, err.Error(), "file too large")
	})
}

func TestWriteFileSizeLimit(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir)

	t.Run("rejects content larger than 50MB", func(t *testing.T) {
		const limit = 50 * 1024 * 1024
		// Build content just over the limit. We use a byte slice to avoid
		// the overhead of strings.Repeat for 50 MB+ in the JSON marshal.
		bigContent := strings.Repeat("x", limit+1)

		_, err := ct.Execute(context.Background(), "write_file", mustJSON(t, map[string]any{
			"path":    "big.txt",
			"content": bigContent,
		}))
		assertError(t, err)
		assertContains(t, err.Error(), "content too large to write")
		assertContains(t, err.Error(), fmt.Sprintf("max %d", limit))

		// Verify the file was NOT created.
		if _, statErr := os.Stat(filepath.Join(dir, "big.txt")); statErr == nil {
			t.Error("file should not have been created when content exceeds limit")
		}
	})

	t.Run("accepts normal-sized content", func(t *testing.T) {
		result, err := ct.Execute(context.Background(), "write_file", mustJSON(t, map[string]any{
			"path":    "normal.txt",
			"content": "hello world",
		}))
		assertNoError(t, err)
		assertContains(t, result, "11 bytes")
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
	ct := NewCoreTools(dir, WithShell(true))

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

	t.Run("shell disabled by default", func(t *testing.T) {
		noShell := NewCoreTools(dir)
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
			_, _ = fmt.Fprint(w, "hello from server")
		}))
		defer srv.Close()

		dir := t.TempDir()
		ct := NewCoreTools(dir)
		ct.httpClient = srv.Client() // bypass SSRF check for local test server

		result, err := ct.Execute(context.Background(), "web_fetch", mustJSON(t, map[string]any{
			"url": srv.URL,
		}))
		assertNoError(t, err)
		assertEqual(t, "hello from server", result)
	})

	t.Run("HTTP error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, "not found")
		}))
		defer srv.Close()

		dir := t.TempDir()
		ct := NewCoreTools(dir)
		ct.httpClient = srv.Client() // bypass SSRF check for local test server

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

	t.Run("rejects private IP", func(t *testing.T) {
		dir := t.TempDir()
		ct := NewCoreTools(dir)

		_, err := ct.Execute(context.Background(), "web_fetch", mustJSON(t, map[string]any{
			"url": "http://127.0.0.1/",
		}))
		assertError(t, err)
		assertContains(t, err.Error(), "private/reserved IP")
	})

	t.Run("rejects link-local metadata IP", func(t *testing.T) {
		dir := t.TempDir()
		ct := NewCoreTools(dir)

		_, err := ct.Execute(context.Background(), "web_fetch", mustJSON(t, map[string]any{
			"url": "http://169.254.169.254/",
		}))
		assertError(t, err)
		assertContains(t, err.Error(), "private/reserved IP")
	})

	t.Run("rejects unsupported URL scheme", func(t *testing.T) {
		dir := t.TempDir()
		ct := NewCoreTools(dir)

		_, err := ct.Execute(context.Background(), "web_fetch", mustJSON(t, map[string]any{
			"url": "file:///etc/passwd",
		}))
		assertError(t, err)
		assertContains(t, err.Error(), "unsupported URL scheme")
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

func TestSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir)

	// Create a symlink inside workDir pointing to /tmp (outside sandbox).
	symlink := filepath.Join(dir, "escape")
	if err := os.Symlink("/tmp", symlink); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	// Attempting to read through the symlink should be rejected.
	_, err := ct.Execute(context.Background(), "read_file", mustJSON(t, map[string]any{
		"path": "escape/somefile.txt",
	}))
	assertError(t, err)
	assertContains(t, err.Error(), "escapes working directory")
}

func TestSpawnDepthPropagation(t *testing.T) {
	dir := t.TempDir()

	// A spawner at depth 1 with maxDepth 2 should be able to spawn.
	spawner := &mockSpawner{result: "ok"}
	ct := NewCoreTools(dir, WithSpawner(spawner, 1, 2))
	result, err := ct.Execute(context.Background(), "spawn_agent", mustJSON(t, map[string]any{
		"system_prompt": "test",
		"message":       "hello",
	}))
	assertNoError(t, err)
	assertEqual(t, "ok", result)

	// A spawner at depth 2 with maxDepth 2 should NOT be able to spawn.
	ct2 := NewCoreTools(dir, WithSpawner(spawner, 2, 2))
	_, err = ct2.Execute(context.Background(), "spawn_agent", mustJSON(t, map[string]any{
		"system_prompt": "test",
		"message":       "hello",
	}))
	assertError(t, err)
	assertContains(t, err.Error(), "max spawn depth")
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

// TestDefinitionsByName verifies that DefinitionsByName returns a map keyed by
// tool name containing all the same tools as Definitions().
func TestDefinitionsByName(t *testing.T) {
	dir := t.TempDir()

	t.Run("without spawner", func(t *testing.T) {
		ct := NewCoreTools(dir)
		byName := ct.DefinitionsByName()

		expected := []string{"read_file", "write_file", "edit_file", "glob", "grep", "shell", "web_fetch"}
		for _, name := range expected {
			if _, ok := byName[name]; !ok {
				t.Errorf("missing tool %q in DefinitionsByName()", name)
			}
		}

		if _, ok := byName["spawn_agent"]; ok {
			t.Error("spawn_agent should not be present without spawner")
		}
	})

	t.Run("with spawner", func(t *testing.T) {
		ct := NewCoreTools(dir, WithSpawner(&mockSpawner{}, 0, 3))
		byName := ct.DefinitionsByName()

		if _, ok := byName["spawn_agent"]; !ok {
			t.Error("spawn_agent should be present with spawner")
		}
	})

	t.Run("with denylist", func(t *testing.T) {
		ct := NewCoreTools(dir, WithDenylist([]string{"shell", "web_fetch"}))
		byName := ct.DefinitionsByName()

		if _, ok := byName["shell"]; ok {
			t.Error("shell should be excluded when denylisted")
		}
		if _, ok := byName["web_fetch"]; ok {
			t.Error("web_fetch should be excluded when denylisted")
		}
		if _, ok := byName["read_file"]; !ok {
			t.Error("read_file should still be present when not denylisted")
		}
	})

	t.Run("matches Definitions count", func(t *testing.T) {
		ct := NewCoreTools(dir, WithSpawner(&mockSpawner{}, 0, 3), WithStore(&noopStore{}))
		defs := ct.Definitions()
		byName := ct.DefinitionsByName()

		if len(byName) != len(defs) {
			t.Errorf("DefinitionsByName() has %d entries, Definitions() has %d", len(byName), len(defs))
		}
		for _, d := range defs {
			if _, ok := byName[d.Name]; !ok {
				t.Errorf("tool %q in Definitions() but missing from DefinitionsByName()", d.Name)
			}
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

// TestSpawnAgentDepth_TeamLeadHasSpawnAgent is a regression test for the
// off-by-one bug in WithSpawner where team leads were given depth=1 instead of
// depth=0, causing spawn_agent to be excluded from their tool set.
//
// A CoreTools at depth=0 with maxDepth=1 (the team lead configuration) MUST
// include spawn_agent in Definitions(). Before the fix, SpawnTeamLead called
// WithSpawner(r, depth+1, maxDepth) which passed depth=1, making depth < maxDepth
// false and excluding spawn_agent.
func TestSpawnAgentDepth_TeamLeadHasSpawnAgent(t *testing.T) {
	dir := t.TempDir()
	// depth=0, maxDepth=1: team lead — should have spawn_agent.
	ct := NewCoreTools(dir, WithSpawner(&mockSpawner{}, 0, 1))
	defs := ct.Definitions()

	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Name] = true
	}

	if !names["spawn_agent"] {
		t.Errorf("CoreTools at depth=0, maxDepth=1 should include spawn_agent "+
			"(team lead configuration); got tools: %v", toolNames(defs))
	}
}

// TestSpawnAgentDepth_WorkerLacksSpawnAgent verifies that a CoreTools at
// depth=1 with maxDepth=1 (the worker configuration) does NOT include
// spawn_agent. Workers must not be able to spawn further agents.
func TestSpawnAgentDepth_WorkerLacksSpawnAgent(t *testing.T) {
	dir := t.TempDir()
	// depth=1, maxDepth=1: worker — should NOT have spawn_agent.
	ct := NewCoreTools(dir, WithSpawner(&mockSpawner{}, 1, 1))
	defs := ct.Definitions()

	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Name] = true
	}

	if names["spawn_agent"] {
		t.Errorf("CoreTools at depth=1, maxDepth=1 should NOT include spawn_agent "+
			"(worker configuration); got tools: %v", toolNames(defs))
	}
}

// TestSpawnAgentDepth_ChildDepthIncrement is a regression test for the
// off-by-one bug in spawnAgent where child sessions were spawned at the same
// depth as the parent (ct.depth) instead of depth+1.
//
// When a team lead (depth=0) calls spawn_agent, the child must be spawned at
// depth=1 so that the child (a worker) cannot itself call spawn_agent.
// Before the fix, the child was spawned at depth=0, giving it spawn_agent
// access and allowing unbounded recursion.
func TestSpawnAgentDepth_ChildDepthIncrement(t *testing.T) {
	dir := t.TempDir()

	capturing := &capturingSpawner{result: "ok"}
	// Parent is at depth=0 (team lead), maxDepth=1.
	ct := NewCoreTools(dir,
		WithSpawner(capturing, 0, 1),
		WithProvider("test-provider", "test-model"),
	)

	_, err := ct.Execute(context.Background(), "spawn_agent", mustJSON(t, map[string]any{
		"system_prompt": "you are a worker",
		"message":       "do the work",
	}))
	assertNoError(t, err)

	if capturing.received == nil {
		t.Fatal("SpawnAndWait was never called")
	}

	got := *capturing.received
	// Child must be at depth=1 (parent depth 0 + 1).
	if got.Depth != 1 {
		t.Errorf("child SpawnOpts.Depth = %d, want 1 (regression: was 0 before fix, "+
			"which gave workers spawn_agent access)", got.Depth)
	}
	// MaxDepth must be propagated unchanged.
	if got.MaxDepth != 1 {
		t.Errorf("child SpawnOpts.MaxDepth = %d, want 1", got.MaxDepth)
	}
}

// TestSpawnAgentPropagatesProviderAndContext is a regression test for the bug
// where spawn_agent never passed ProviderName or Model to child SpawnOpts,
// causing every child spawn to fail with `provider "" not found`.
//
// The fix added providerName/model fields to CoreTools and wired them through
// spawnAgent. This test verifies that all four fields (ProviderName, Model,
// AgentID, JobID) are propagated from the parent CoreTools to the child
// SpawnOpts. It will FAIL if ProviderName or Model are removed from the
// SpawnOpts literal in spawnAgent.
func TestSpawnAgentPropagatesProviderAndContext(t *testing.T) {
	dir := t.TempDir()

	capturing := &capturingSpawner{result: "ok"}
	ct := NewCoreTools(dir,
		WithSpawner(capturing, 0, 3),
		WithProvider("test-provider", "test-model"),
		WithSessionContext("sess-1", "agent-1", "job-1", "task-1"),
	)

	_, err := ct.Execute(context.Background(), "spawn_agent", mustJSON(t, map[string]any{
		"system_prompt": "you are a helper",
		"message":       "do the thing",
	}))
	assertNoError(t, err)

	if capturing.received == nil {
		t.Fatal("SpawnAndWait was never called")
	}

	got := *capturing.received
	assertEqual(t, "test-provider", got.ProviderName)
	assertEqual(t, "test-model", got.Model)
	// When agent_name is omitted, AgentID falls back to "worker" (anonymous subagent).
	assertEqual(t, "worker", got.AgentID)
	assertEqual(t, "job-1", got.JobID)
	assertEqual(t, "task-1", got.TaskID)
}

// TestSpawnAgentNameParam verifies that when agent_name is provided in the tool
// call, SpawnOpts.AgentID is set to that name. When agent_name is omitted, the
// fallback to "worker" is covered by TestSpawnAgentPropagatesProviderAndContext.
func TestSpawnAgentNameParam(t *testing.T) {
	dir := t.TempDir()

	capturing := &capturingSpawner{result: "ok"}
	ct := NewCoreTools(dir,
		WithSpawner(capturing, 0, 3),
		WithProvider("test-provider", "test-model"),
		WithSessionContext("sess-1", "parent-agent", "job-1", "task-1"),
	)

	_, err := ct.Execute(context.Background(), "spawn_agent", mustJSON(t, map[string]any{
		"system_prompt": "you are a specialist",
		"message":       "do the specialized work",
		"agent_name":    "tui-engineer",
	}))
	assertNoError(t, err)

	if capturing.received == nil {
		t.Fatal("SpawnAndWait was never called")
	}

	// agent_name should override the parent's agentID.
	assertEqual(t, "tui-engineer", capturing.received.AgentID)
}

// TestSpawnAgentForwardsToolFilter is a regression test for the bug where
// spawn_agent never forwarded params.Tools to SpawnOpts.Tools, so the child
// always received a nil tool set regardless of what the caller requested.
//
// The fix resolves each requested tool name against the parent's Definitions()
// and passes the resulting []ToolDef as SpawnOpts.Tools. This test will FAIL
// if `Tools: toolDefs` is removed from the SpawnOpts literal in spawnAgent.
func TestSpawnAgentForwardsToolFilter(t *testing.T) {
	dir := t.TempDir()

	capturing := &capturingSpawner{result: "ok"}
	ct := NewCoreTools(dir,
		WithSpawner(capturing, 0, 3),
		WithProvider("test-provider", "test-model"),
		WithSessionContext("sess-1", "agent-1", "job-1", "task-1"),
	)

	_, err := ct.Execute(context.Background(), "spawn_agent", mustJSON(t, map[string]any{
		"system_prompt": "you are a helper",
		"message":       "do the thing",
		"tools":         []string{"read_file", "shell"},
	}))
	assertNoError(t, err)

	if capturing.received == nil {
		t.Fatal("SpawnAndWait was never called")
	}

	got := capturing.received.Tools
	if len(got) != 2 {
		t.Fatalf("want 2 tool defs in SpawnOpts.Tools, got %d (regression: was nil before fix)", len(got))
	}

	names := make(map[string]bool, len(got))
	for _, td := range got {
		names[td.Name] = true
	}
	if !names["read_file"] {
		t.Error("expected read_file in SpawnOpts.Tools")
	}
	if !names["shell"] {
		t.Error("expected shell in SpawnOpts.Tools")
	}
}

// TestSpawnAgentEmptyToolsPassesNil verifies that when spawn_agent is called
// with an empty (or omitted) tools list, SpawnOpts.Tools is nil so the child
// receives the full tool set rather than an empty one.
func TestSpawnAgentEmptyToolsPassesNil(t *testing.T) {
	dir := t.TempDir()

	t.Run("empty tools array", func(t *testing.T) {
		capturing := &capturingSpawner{result: "ok"}
		ct := NewCoreTools(dir,
			WithSpawner(capturing, 0, 3),
			WithProvider("test-provider", "test-model"),
		)

		_, err := ct.Execute(context.Background(), "spawn_agent", mustJSON(t, map[string]any{
			"system_prompt": "you are a helper",
			"message":       "do the thing",
			"tools":         []string{},
		}))
		assertNoError(t, err)

		if capturing.received == nil {
			t.Fatal("SpawnAndWait was never called")
		}
		if capturing.received.Tools != nil {
			t.Fatalf("want nil SpawnOpts.Tools for empty tools list, got %v", capturing.received.Tools)
		}
	})

	t.Run("omitted tools field", func(t *testing.T) {
		capturing := &capturingSpawner{result: "ok"}
		ct := NewCoreTools(dir,
			WithSpawner(capturing, 0, 3),
			WithProvider("test-provider", "test-model"),
		)

		_, err := ct.Execute(context.Background(), "spawn_agent", mustJSON(t, map[string]any{
			"system_prompt": "you are a helper",
			"message":       "do the thing",
			// no "tools" key
		}))
		assertNoError(t, err)

		if capturing.received == nil {
			t.Fatal("SpawnAndWait was never called")
		}
		if capturing.received.Tools != nil {
			t.Fatalf("want nil SpawnOpts.Tools when tools field is omitted, got %v", capturing.received.Tools)
		}
	})
}

// TestSpawnAgentUnknownToolsSkipped verifies that tool names not present in the
// parent's Definitions() are silently skipped rather than causing an error.
// Only known tool names should appear in SpawnOpts.Tools.
//
// This test will FAIL if `Tools: toolDefs` is removed from the SpawnOpts
// literal in spawnAgent (because then Tools would be nil instead of containing
// just "read_file").
func TestSpawnAgentUnknownToolsSkipped(t *testing.T) {
	dir := t.TempDir()

	capturing := &capturingSpawner{result: "ok"}
	ct := NewCoreTools(dir,
		WithSpawner(capturing, 0, 3),
		WithProvider("test-provider", "test-model"),
	)

	_, err := ct.Execute(context.Background(), "spawn_agent", mustJSON(t, map[string]any{
		"system_prompt": "you are a helper",
		"message":       "do the thing",
		"tools":         []string{"read_file", "nonexistent_tool"},
	}))
	assertNoError(t, err)

	if capturing.received == nil {
		t.Fatal("SpawnAndWait was never called")
	}

	got := capturing.received.Tools
	if len(got) != 1 {
		t.Fatalf("want 1 tool def (only read_file), got %d: %v", len(got), got)
	}
	if got[0].Name != "read_file" {
		t.Errorf("want tool name %q, got %q", "read_file", got[0].Name)
	}
}

// TestSpawnAgentAllUnknownToolsPassesNil verifies that when every requested
// tool name is unknown, toolDefs stays nil, so SpawnOpts.Tools is nil and the
// child gets the full default tool set. Unknown names are silently skipped;
// this is intentional.
func TestSpawnAgentAllUnknownToolsPassesNil(t *testing.T) {
	capturing := &capturingSpawner{result: "ok"}
	ct := NewCoreTools(t.TempDir(),
		WithSpawner(capturing, 0, 3),
		WithProvider("test-provider", "test-model"),
	)

	_, err := ct.Execute(context.Background(), "spawn_agent", mustJSON(t, map[string]any{
		"system_prompt": "you are a helper",
		"message":       "do the thing",
		"tools":         []string{"nonexistent_a", "nonexistent_b"},
	}))
	assertNoError(t, err)

	if capturing.received == nil {
		t.Fatal("SpawnAndWait was never called")
	}
	if capturing.received.Tools != nil {
		t.Fatalf("want nil SpawnOpts.Tools when all tool names are unknown, got %v", capturing.received.Tools)
	}
}

// TestSpawnAgentTaskParam verifies that when spawn_agent is called with a
// "task" field, SpawnOpts.Task is set to that value and forwarded to the spawner.
func TestSpawnAgentTaskParam(t *testing.T) {
	dir := t.TempDir()

	capturing := &capturingSpawner{result: "ok"}
	ct := NewCoreTools(dir,
		WithSpawner(capturing, 0, 3),
		WithProvider("test-provider", "test-model"),
	)

	_, err := ct.Execute(context.Background(), "spawn_agent", mustJSON(t, map[string]any{
		"system_prompt": "you are a builder",
		"message":       "build the models",
		"task":          "building core data models",
	}))
	assertNoError(t, err)

	if capturing.received == nil {
		t.Fatal("SpawnAndWait was never called")
	}

	assertEqual(t, "building core data models", capturing.received.Task)
}

// TestSpawnAgentTaskOmitted verifies that when spawn_agent is called without a
// "task" field, SpawnOpts.Task is the empty string.
func TestSpawnAgentTaskOmitted(t *testing.T) {
	dir := t.TempDir()

	capturing := &capturingSpawner{result: "ok"}
	ct := NewCoreTools(dir,
		WithSpawner(capturing, 0, 3),
		WithProvider("test-provider", "test-model"),
	)

	_, err := ct.Execute(context.Background(), "spawn_agent", mustJSON(t, map[string]any{
		"system_prompt": "you are a helper",
		"message":       "do the thing",
		// no "task" key
	}))
	assertNoError(t, err)

	if capturing.received == nil {
		t.Fatal("SpawnAndWait was never called")
	}

	assertEqual(t, "", capturing.received.Task)
}

// TestSessionTask verifies that Session.Task() returns the task description
// that was set via SpawnOpts.Task when the session was created.
func TestSessionTask(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Done"},
				{Type: provider.EventDone},
			}},
		},
	}

	opts := SpawnOpts{
		AgentID:        "test-agent",
		Model:          "test-model",
		InitialMessage: "do the work",
		MaxTurns:       10,
		Task:           "test task description",
	}

	sess := newSession("sess-task", mp, opts, &mockToolExecutor{})

	assertEqual(t, "test task description", sess.Task())
}

// TestProgressToolWithStore verifies that calling a progress tool with a
// non-nil store works correctly (no nil panic). This validates the removal
// of the store nil guards — the store is always required.
func TestProgressToolWithStore(t *testing.T) {
	dir := t.TempDir()
	store := &noopStore{}
	ct := NewCoreTools(dir, WithStore(store))

	result, err := ct.Execute(context.Background(), "report_task_progress", mustJSON(t, map[string]any{
		"job_id":  "job-1",
		"status":  "in_progress",
		"message": "working on it",
	}))
	assertNoError(t, err)
	assertContains(t, result, "progress reported")
}

func TestProgressToolsFillSessionJobAndTaskIDs(t *testing.T) {
	dir := t.TempDir()
	store := &captureProgressStore{}
	ct := NewCoreTools(dir,
		WithStore(store),
		WithSessionContext("sess-1", "agent-ctx", "job-ctx", "task-ctx"),
	)

	t.Run("report_task_progress fills missing ids from session", func(t *testing.T) {
		store.lastProgress = nil
		_, err := ct.Execute(context.Background(), "report_task_progress", mustJSON(t, map[string]any{
			"status":  "in_progress",
			"message": "working",
		}))
		assertNoError(t, err)
		if store.lastProgress == nil {
			t.Fatal("expected progress write")
		}
		assertEqual(t, "job-ctx", store.lastProgress.JobID)
		assertEqual(t, "task-ctx", store.lastProgress.TaskID)
		assertEqual(t, "agent-ctx", store.lastProgress.AgentID)
	})

	t.Run("report_blocker fills missing ids from session", func(t *testing.T) {
		store.lastProgress = nil
		_, err := ct.Execute(context.Background(), "report_blocker", mustJSON(t, map[string]any{
			"description": "blocked",
			"severity":    "medium",
		}))
		assertNoError(t, err)
		if store.lastProgress == nil {
			t.Fatal("expected progress write")
		}
		assertEqual(t, "job-ctx", store.lastProgress.JobID)
		assertEqual(t, "task-ctx", store.lastProgress.TaskID)
		assertEqual(t, "agent-ctx", store.lastProgress.AgentID)
	})

	t.Run("request_review fills missing ids from session", func(t *testing.T) {
		store.lastProgress = nil
		store.lastArtifact = nil
		_, err := ct.Execute(context.Background(), "request_review", mustJSON(t, map[string]any{
			"artifact_path": "/tmp/review.txt",
			"notes":         "please review",
		}))
		assertNoError(t, err)
		if store.lastArtifact == nil {
			t.Fatal("expected artifact write")
		}
		if store.lastProgress == nil {
			t.Fatal("expected progress write")
		}
		assertEqual(t, "job-ctx", store.lastArtifact.JobID)
		assertEqual(t, "task-ctx", store.lastArtifact.TaskID)
		assertEqual(t, "job-ctx", store.lastProgress.JobID)
		assertEqual(t, "task-ctx", store.lastProgress.TaskID)
		assertEqual(t, "agent-ctx", store.lastProgress.AgentID)
	})

	t.Run("log_artifact fills missing ids from session", func(t *testing.T) {
		store.lastArtifact = nil
		_, err := ct.Execute(context.Background(), "log_artifact", mustJSON(t, map[string]any{
			"type":    "code",
			"path":    "/tmp/file.go",
			"summary": "artifact",
		}))
		assertNoError(t, err)
		if store.lastArtifact == nil {
			t.Fatal("expected artifact write")
		}
		assertEqual(t, "job-ctx", store.lastArtifact.JobID)
		assertEqual(t, "task-ctx", store.lastArtifact.TaskID)
	})

	t.Run("explicit ids are preserved", func(t *testing.T) {
		store.lastProgress = nil
		_, err := ct.Execute(context.Background(), "report_task_progress", mustJSON(t, map[string]any{
			"job_id":   "job-explicit",
			"task_id":  "task-explicit",
			"agent_id": "agent-explicit",
			"status":   "in_progress",
			"message":  "working",
		}))
		assertNoError(t, err)
		if store.lastProgress == nil {
			t.Fatal("expected progress write")
		}
		assertEqual(t, "job-explicit", store.lastProgress.JobID)
		assertEqual(t, "task-explicit", store.lastProgress.TaskID)
		assertEqual(t, "agent-explicit", store.lastProgress.AgentID)
	})
}

// TestProgressToolDefinitionsIncluded verifies that progress tool definitions
// are always included in Definitions() (store is required, no nil guard).
func TestProgressToolDefinitionsIncluded(t *testing.T) {
	dir := t.TempDir()
	store := &noopStore{}
	ct := NewCoreTools(dir, WithStore(store))
	defs := ct.Definitions()

	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Name] = true
	}

	progressTools := []string{
		"report_task_progress", "report_blocker", "update_task_status",
		"request_review", "query_job_context", "log_artifact",
	}
	for _, name := range progressTools {
		if !names[name] {
			t.Errorf("missing progress tool definition: %s", name)
		}
	}
}

// TestGlobTraversal verifies that glob patterns that would resolve the base
// directory outside the workspace are rejected.
func TestGlobTraversal(t *testing.T) {
	dir := t.TempDir()
	ct := NewCoreTools(dir)

	t.Run("recursive pattern escapes workspace", func(t *testing.T) {
		_, err := ct.Execute(context.Background(), "glob", mustJSON(t, map[string]any{
			"pattern": "../../**/*.conf",
		}))
		assertError(t, err)
		assertContains(t, err.Error(), "glob base directory is outside workspace")
	})

	t.Run("non-recursive pattern escapes workspace", func(t *testing.T) {
		_, err := ct.Execute(context.Background(), "glob", mustJSON(t, map[string]any{
			"pattern": "../../etc/*.conf",
		}))
		assertError(t, err)
		assertContains(t, err.Error(), "glob base directory is outside workspace")
	})

	t.Run("pattern within workspace succeeds", func(t *testing.T) {
		writeTestFile(t, dir, "sub/test.go", "package sub")
		result, err := ct.Execute(context.Background(), "glob", mustJSON(t, map[string]any{
			"pattern": "sub/*.go",
		}))
		assertNoError(t, err)
		assertContains(t, result, "test.go")
	})
}

// TestDenylist verifies that denylisted tools are rejected by Execute() and
// excluded from Definitions().
func TestDenylist(t *testing.T) {
	dir := t.TempDir()

	t.Run("execute rejects denylisted tool", func(t *testing.T) {
		ct := NewCoreTools(dir, WithDenylist([]string{"shell", "web_fetch"}))
		_, err := ct.Execute(context.Background(), "shell", mustJSON(t, map[string]any{
			"command": "echo hello",
		}))
		assertError(t, err)
		assertContains(t, err.Error(), `tool "shell" is not allowed for this agent`)
	})

	t.Run("execute allows non-denylisted tool", func(t *testing.T) {
		writeTestFile(t, dir, "test.txt", "hello")
		ct := NewCoreTools(dir, WithDenylist([]string{"shell"}))
		result, err := ct.Execute(context.Background(), "read_file", mustJSON(t, map[string]any{
			"path": "test.txt",
		}))
		assertNoError(t, err)
		assertContains(t, result, "hello")
	})

	t.Run("definitions excludes denylisted tools", func(t *testing.T) {
		ct := NewCoreTools(dir, WithDenylist([]string{"shell", "web_fetch"}))
		defs := ct.Definitions()

		names := make(map[string]bool, len(defs))
		for _, d := range defs {
			names[d.Name] = true
		}

		if names["shell"] {
			t.Error("shell should be excluded from definitions when denylisted")
		}
		if names["web_fetch"] {
			t.Error("web_fetch should be excluded from definitions when denylisted")
		}
		if !names["read_file"] {
			t.Error("read_file should still be present when not denylisted")
		}
	})

	t.Run("empty denylist has no effect", func(t *testing.T) {
		ct := NewCoreTools(dir, WithDenylist(nil))
		defs := ct.Definitions()

		names := make(map[string]bool, len(defs))
		for _, d := range defs {
			names[d.Name] = true
		}

		if !names["shell"] {
			t.Error("shell should be present with empty denylist")
		}
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

// capturingSpawner records the SpawnOpts it receives so tests can assert on them.
type capturingSpawner struct {
	result   string
	err      error
	received *SpawnOpts
}

func (c *capturingSpawner) SpawnAndWait(_ context.Context, opts SpawnOpts) (string, error) {
	c.received = &opts
	return c.result, c.err
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
