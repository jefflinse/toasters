package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jefflinse/toasters/internal/prompt"
)

// captureWorkerSpawnNotifier records WorkerSpawn notifications for
// assertion. Safe for concurrent use even though these tests are
// single-goroutine.
type captureWorkerSpawnNotifier struct {
	mu    sync.Mutex
	calls []WorkerSpawn
}

func (c *captureWorkerSpawnNotifier) notify(_ context.Context, ws WorkerSpawn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, ws)
}

func (c *captureWorkerSpawnNotifier) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func (c *captureWorkerSpawnNotifier) last() WorkerSpawn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[len(c.calls)-1]
}

// enginedWithRole builds a prompt.Engine with a single loaded role named
// "coder", cheap enough to construct per test (no toolchains/instructions —
// LoadDir tolerates missing subdirectories).
func enginedWithRole(t *testing.T) *prompt.Engine {
	t.Helper()
	dir := t.TempDir()
	writeTestFile(t, dir, filepath.Join("roles", "coder.md"), `---
name: Coder
description: Writes code.
mode: worker
---

Write clean code.
`)
	e := prompt.NewEngine()
	if err := e.LoadDir(dir, "test"); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	return e
}

func TestSpawnWorker_NotifierFiresOnSuccess(t *testing.T) {
	cn := &captureWorkerSpawnNotifier{}
	spawner := &mockSpawner{result: "child done"}
	ct := NewCoreTools(t.TempDir(),
		WithSpawner(spawner, 0, 3),
		WithPromptEngine(enginedWithRole(t)),
		WithWorkerSpawnNotifier(cn.notify),
		WithSessionContext("s-1", "parent", "job-1", "task-1"),
	)

	result, err := ct.Execute(context.Background(), "spawn_worker", mustJSON(t, map[string]any{
		"role":    "coder",
		"message": "go do it",
		"task":    "implement the thing",
	}))
	assertNoError(t, err)
	assertEqual(t, "child done", result)

	if cn.count() != 1 {
		t.Fatalf("notifier called %d times, want 1", cn.count())
	}
	ws := cn.last()
	if ws.Failed {
		t.Errorf("Failed = true, want false")
	}
	if ws.Role != "coder" {
		t.Errorf("Role = %q, want %q", ws.Role, "coder")
	}
	if ws.Task != "implement the thing" {
		t.Errorf("Task = %q, want %q", ws.Task, "implement the thing")
	}
	if ws.JobID != "job-1" {
		t.Errorf("JobID = %q, want %q", ws.JobID, "job-1")
	}
	if ws.Depth != 1 {
		t.Errorf("Depth = %d, want 1 (parent depth 0 + 1)", ws.Depth)
	}
	if ws.Error != "" {
		t.Errorf("Error = %q, want empty on success", ws.Error)
	}
}

// TestSpawnWorker_NotifierFiresOnDepthLimit verifies the notifier fires for
// a pre-attempt validation failure, not just a failed SpawnAndWait — spawn
// intentionally diverges from shell here (see WorkerSpawn's doc comment):
// a rejected spawn_worker call is itself meaningful for the display card,
// and the depth limit is the cheapest failure to trigger without an actual
// spawn.
func TestSpawnWorker_NotifierFiresOnDepthLimit(t *testing.T) {
	cn := &captureWorkerSpawnNotifier{}
	spawner := &mockSpawner{result: "unused"}
	ct := NewCoreTools(t.TempDir(),
		WithSpawner(spawner, 2, 2), // depth == maxDepth: exceeded
		WithPromptEngine(enginedWithRole(t)),
		WithWorkerSpawnNotifier(cn.notify),
	)

	_, err := ct.Execute(context.Background(), "spawn_worker", mustJSON(t, map[string]any{
		"role": "coder",
	}))
	assertError(t, err)
	assertContains(t, err.Error(), "max spawn depth")

	if cn.count() != 1 {
		t.Fatalf("notifier called %d times, want 1", cn.count())
	}
	ws := cn.last()
	if !ws.Failed {
		t.Errorf("Failed = false, want true")
	}
	if !strings.Contains(ws.Error, "max spawn depth") {
		t.Errorf("Error = %q, want it to mention the depth limit", ws.Error)
	}
	// Depth still reflects what the child *would* run at (ct.depth+1), even
	// though no child was ever created — informative context for the card.
	if ws.Depth != 3 {
		t.Errorf("Depth = %d, want 3 (parent depth 2 + 1)", ws.Depth)
	}
}

// TestSpawnWorker_NotifierFiresOnUnknownRole verifies the notifier reports
// the requested role even when it doesn't exist — the failure is still
// worth surfacing, and params.Role is known by the time this check runs.
func TestSpawnWorker_NotifierFiresOnUnknownRole(t *testing.T) {
	cn := &captureWorkerSpawnNotifier{}
	spawner := &mockSpawner{result: "unused"}
	ct := NewCoreTools(t.TempDir(),
		WithSpawner(spawner, 0, 3),
		WithPromptEngine(enginedWithRole(t)),
		WithWorkerSpawnNotifier(cn.notify),
	)

	_, err := ct.Execute(context.Background(), "spawn_worker", mustJSON(t, map[string]any{
		"role": "nonexistent-role",
	}))
	assertError(t, err)

	if cn.count() != 1 {
		t.Fatalf("notifier called %d times, want 1", cn.count())
	}
	ws := cn.last()
	if !ws.Failed {
		t.Errorf("Failed = false, want true")
	}
	if ws.Role != "nonexistent-role" {
		t.Errorf("Role = %q, want %q", ws.Role, "nonexistent-role")
	}
	if ws.Error == "" {
		t.Error("Error = \"\", want a message naming the missing role")
	}
}

// TestSpawnWorker_NotifierFiresOnChildFailure verifies the failure path when
// the spawn attempt itself runs but the spawner reports an error (e.g. the
// child session failed or was cancelled) — as opposed to a pre-attempt
// validation failure.
func TestSpawnWorker_NotifierFiresOnChildFailure(t *testing.T) {
	cn := &captureWorkerSpawnNotifier{}
	spawner := &mockSpawner{err: context.DeadlineExceeded}
	ct := NewCoreTools(t.TempDir(),
		WithSpawner(spawner, 0, 3),
		WithPromptEngine(enginedWithRole(t)),
		WithWorkerSpawnNotifier(cn.notify),
	)

	_, err := ct.Execute(context.Background(), "spawn_worker", mustJSON(t, map[string]any{
		"role": "coder",
	}))
	assertError(t, err)

	if cn.count() != 1 {
		t.Fatalf("notifier called %d times, want 1", cn.count())
	}
	ws := cn.last()
	if !ws.Failed {
		t.Errorf("Failed = false, want true")
	}
	if ws.Role != "coder" {
		t.Errorf("Role = %q, want %q", ws.Role, "coder")
	}
	if ws.Error == "" {
		t.Error("Error = \"\", want the spawn failure message")
	}
}

func TestSpawnWorker_NotifierNotCalledWhenUnset(t *testing.T) {
	spawner := &mockSpawner{result: "ok"}
	ct := NewCoreTools(t.TempDir(),
		WithSpawner(spawner, 0, 3),
		WithPromptEngine(enginedWithRole(t)),
	)

	// Must not panic with no notifier attached.
	_, err := ct.Execute(context.Background(), "spawn_worker", mustJSON(t, map[string]any{
		"role": "coder",
	}))
	assertNoError(t, err)
}

func TestSetWorkerSpawnNotifier_PostConstruction(t *testing.T) {
	spawner := &mockSpawner{result: "ok"}
	ct := NewCoreTools(t.TempDir(),
		WithSpawner(spawner, 0, 3),
		WithPromptEngine(enginedWithRole(t)),
	)

	cn := &captureWorkerSpawnNotifier{}
	ct.SetWorkerSpawnNotifier(cn.notify)

	_, err := ct.Execute(context.Background(), "spawn_worker", mustJSON(t, map[string]any{
		"role": "coder",
	}))
	assertNoError(t, err)

	if cn.count() != 1 {
		t.Fatalf("notifier set post-construction called %d times, want 1", cn.count())
	}
}

func TestDisplaySpawnTask_CapsLongTask(t *testing.T) {
	short := "implement the thing"
	if got := displaySpawnTask(short); got != short {
		t.Errorf("displaySpawnTask(%q) = %q, want unchanged", short, got)
	}

	long := strings.Repeat("x", maxWorkerSpawnTaskBytes+50)
	got := displaySpawnTask(long)
	if len(got) >= len(long) {
		t.Errorf("displaySpawnTask did not shorten a %d-byte task: got %d bytes", len(long), len(got))
	}
	if !strings.HasSuffix(got, "… (truncated)") {
		t.Errorf("displaySpawnTask(long) = %q, want a truncation marker suffix", got)
	}
}
