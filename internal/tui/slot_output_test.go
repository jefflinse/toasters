package tui

import (
	"encoding/json"
	"testing"
)

// TestAttachFileChange_PendingItemStaysPending verifies the key ordering
// invariant: session.file_change fires mid-execution (call → file_change →
// result), so the matched item is normally still pending. Attaching the
// diff must not set endedAt or otherwise mark the item complete — a later
// completeTool for the same call ID has to still find it pending and merge
// in place, instead of finding it "already done" and synthesizing a
// duplicate.
func TestAttachFileChange_PendingItemStaysPending(t *testing.T) {
	rs := &runtimeSlot{}
	args, _ := json.Marshal(map[string]string{"path": "main.go"})
	rs.startTool("call1", "write_file", args)

	rs.attachFileChange("write_file", "main.go", "@@ -1,1 +1,1 @@\n-a\n+b", 1, 1, false, false)

	if len(rs.items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(rs.items))
	}
	it := rs.items[0]
	if it.fileDiff == "" {
		t.Fatal("diff not attached")
	}
	if !it.endedAt.IsZero() {
		t.Error("attachFileChange must not complete the item — it should stay pending until the tool_result arrives")
	}
	if _, ok := rs.toolItemIdx["call1"]; !ok {
		t.Error("toolItemIdx entry removed — completeTool would synthesize a duplicate")
	}

	// The tool_result arrives after the diff — it must merge into the same item.
	rs.completeTool("call1", "write_file", "wrote 12 bytes", false)
	if len(rs.items) != 1 {
		t.Fatalf("expected the result to merge into the same item, got %d items", len(rs.items))
	}
	if rs.items[0].fileDiff == "" {
		t.Error("diff lost after completeTool merged the result")
	}
	if rs.items[0].toolResult != "wrote 12 bytes" {
		t.Errorf("result not recorded: %+v", rs.items[0])
	}
}

// TestAttachFileChange_MatchByNamePathThenFallback verifies the three-tier
// matching: exact name+path match wins over an unrelated call of the same
// name, and a name-only fallback is used when no path matches.
func TestAttachFileChange_MatchByNamePathThenFallback(t *testing.T) {
	rs := &runtimeSlot{}
	argsA, _ := json.Marshal(map[string]string{"path": "a.go"})
	argsB, _ := json.Marshal(map[string]string{"path": "b.go"})
	rs.startTool("callA", "write_file", argsA)
	rs.startTool("callB", "write_file", argsB)

	rs.attachFileChange("write_file", "b.go", "diff-for-b", 2, 0, false, false)

	if rs.items[0].fileDiff != "" {
		t.Error("diff attached to the wrong item (path a.go) instead of b.go")
	}
	if rs.items[1].fileDiff != "diff-for-b" {
		t.Errorf("diff not attached to the matching path: %+v", rs.items[1])
	}
}

// TestAttachFileChange_FallbackByNameOnly verifies that when no tool item's
// path argument matches, the newest tool item with the same name is used.
func TestAttachFileChange_FallbackByNameOnly(t *testing.T) {
	rs := &runtimeSlot{}
	// No path arg at all (args nil) — path match can never succeed.
	rs.startTool("call1", "write_file", nil)

	rs.attachFileChange("write_file", "unknown/path.go", "diff body", 3, 0, true, false)

	if len(rs.items) != 1 {
		t.Fatalf("expected the fallback to reuse the existing item, got %d items", len(rs.items))
	}
	if rs.items[0].fileDiff != "diff body" {
		t.Errorf("diff not attached via name-only fallback: %+v", rs.items[0])
	}
}

// TestAttachFileChange_OldestPendingFirst verifies the fix for parallel tool
// calls: mycelium fires ALL tool_call events for a turn up front, then
// executes sequentially, so two calls to the same tool+path can both be
// pending before either's file_change notification arrives — and those
// notifications arrive in execution (= insertion) order. Matching must walk
// oldest-first so the first notification lands on the first item and the
// second on the second, instead of both landing on the newest (last) item.
func TestAttachFileChange_OldestPendingFirst(t *testing.T) {
	rs := &runtimeSlot{}
	args, _ := json.Marshal(map[string]string{"path": "main.go"})
	rs.startTool("call1", "write_file", args)
	rs.startTool("call2", "write_file", args)

	rs.attachFileChange("write_file", "main.go", "diff-1", 1, 0, false, false)
	rs.attachFileChange("write_file", "main.go", "diff-2", 2, 0, false, false)

	if rs.items[0].fileDiff != "diff-1" {
		t.Errorf("items[0].fileDiff = %q, want %q (first notification -> oldest pending item)", rs.items[0].fileDiff, "diff-1")
	}
	if rs.items[1].fileDiff != "diff-2" {
		t.Errorf("items[1].fileDiff = %q, want %q (second notification -> second item)", rs.items[1].fileDiff, "diff-2")
	}
}

// TestAttachFileChange_SynthesizesOnTotalMiss verifies that when there is no
// tool item at all for the given name, a completed item is synthesized
// (mirroring completeTool's synthesize-on-miss path) so the diff still
// surfaces instead of being silently dropped.
func TestAttachFileChange_SynthesizesOnTotalMiss(t *testing.T) {
	rs := &runtimeSlot{}

	rs.attachFileChange("edit_file", "missing.go", "diff body", 1, 1, false, true)

	if len(rs.items) != 1 {
		t.Fatalf("expected a synthesized item, got %d", len(rs.items))
	}
	it := rs.items[0]
	if it.kind != outputItemTool || it.toolName != "edit_file" {
		t.Errorf("synthesized item wrong: %+v", it)
	}
	if it.fileDiff != "diff body" || !it.diffTruncated {
		t.Errorf("diff fields not set on synthesized item: %+v", it)
	}
	if it.endedAt.IsZero() {
		t.Error("synthesized item should be marked complete (no pending call exists to merge with later)")
	}
}

// TestAttachShellExec_PendingItemStaysPending mirrors
// TestAttachFileChange_PendingItemStaysPending: shell_exec fires before the
// tool_result (session.Run emits ToolResult only after CoreTools.Execute
// returns), so attaching it must not complete the item — the later
// completeTool still needs to merge into the same item.
func TestAttachShellExec_PendingItemStaysPending(t *testing.T) {
	rs := &runtimeSlot{}
	args, _ := json.Marshal(map[string]string{"command": "go test ./..."})
	rs.startTool("call1", "shell", args)

	rs.attachShellExec(1, 500, 4096, false, false)

	if len(rs.items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(rs.items))
	}
	it := rs.items[0]
	if !it.hasShellExec {
		t.Fatal("shell exec not attached")
	}
	if !it.endedAt.IsZero() {
		t.Error("attachShellExec must not complete the item — it should stay pending until the tool_result arrives")
	}
	if _, ok := rs.toolItemIdx["call1"]; !ok {
		t.Error("toolItemIdx entry removed — completeTool would synthesize a duplicate")
	}

	rs.completeTool("call1", "shell", "FAIL\nexit status: exit status 1", false)
	if len(rs.items) != 1 {
		t.Fatalf("expected the result to merge into the same item, got %d items", len(rs.items))
	}
	if !rs.items[0].hasShellExec {
		t.Error("shell exec lost after completeTool merged the result")
	}
	if rs.items[0].shellExitCode != 1 {
		t.Errorf("shellExitCode = %d, want 1", rs.items[0].shellExitCode)
	}
}

// TestAttachShellExec_OldestPendingFirst verifies name-only matching walks
// oldest-first: two concurrent shell calls both pending at once (mycelium
// fires every tool_call event for a turn before executing any of them) must
// pair with their notifications in execution (= insertion) order, not both
// landing on the same (e.g. newest) item.
func TestAttachShellExec_OldestPendingFirst(t *testing.T) {
	rs := &runtimeSlot{}
	rs.startTool("call1", "shell", nil)
	rs.startTool("call2", "shell", nil)

	rs.attachShellExec(0, 100, 10, false, false)
	rs.attachShellExec(1, 200, 20, false, false)

	if rs.items[0].shellExitCode != 0 {
		t.Errorf("items[0].shellExitCode = %d, want 0 (first notification -> oldest pending item)", rs.items[0].shellExitCode)
	}
	if rs.items[1].shellExitCode != 1 {
		t.Errorf("items[1].shellExitCode = %d, want 1 (second notification -> second item)", rs.items[1].shellExitCode)
	}
}

// TestAttachShellExec_SynthesizesOnTotalMiss mirrors
// TestAttachFileChange_SynthesizesOnTotalMiss: with no matching tool item,
// a completed item is synthesized so the status still surfaces.
func TestAttachShellExec_SynthesizesOnTotalMiss(t *testing.T) {
	rs := &runtimeSlot{}

	rs.attachShellExec(137, 5000, 0, false, true)

	if len(rs.items) != 1 {
		t.Fatalf("expected a synthesized item, got %d", len(rs.items))
	}
	it := rs.items[0]
	if it.kind != outputItemTool || it.toolName != "shell" {
		t.Errorf("synthesized item wrong: %+v", it)
	}
	if !it.hasShellExec || it.shellExitCode != 137 || !it.shellTimedOut {
		t.Errorf("shell exec fields not set on synthesized item: %+v", it)
	}
	if it.endedAt.IsZero() {
		t.Error("synthesized item should be marked complete (no pending call exists to merge with later)")
	}
}

// TestAttachWorkerSpawn_PendingItemStaysPending mirrors
// TestAttachShellExec_PendingItemStaysPending: SpawnAndWait blocks until the
// child finishes and the notifier fires from within CoreTools.Execute, so
// attaching it must not complete the item — the later tool_result still
// needs to merge into the same item.
func TestAttachWorkerSpawn_PendingItemStaysPending(t *testing.T) {
	rs := &runtimeSlot{}
	args, _ := json.Marshal(map[string]string{"role": "coder"})
	rs.startTool("call1", "spawn_worker", args)

	rs.attachWorkerSpawn("coder", "implement the thing", "job-1", 1, false, "")

	if len(rs.items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(rs.items))
	}
	it := rs.items[0]
	if !it.hasWorkerSpawn {
		t.Fatal("worker spawn not attached")
	}
	if !it.endedAt.IsZero() {
		t.Error("attachWorkerSpawn must not complete the item — it should stay pending until the tool_result arrives")
	}
	if _, ok := rs.toolItemIdx["call1"]; !ok {
		t.Error("toolItemIdx entry removed — completeTool would synthesize a duplicate")
	}

	rs.completeTool("call1", "spawn_worker", "child done", false)
	if len(rs.items) != 1 {
		t.Fatalf("expected the result to merge into the same item, got %d items", len(rs.items))
	}
	if !rs.items[0].hasWorkerSpawn {
		t.Error("worker spawn lost after completeTool merged the result")
	}
	if rs.items[0].spawnRole != "coder" {
		t.Errorf("spawnRole = %q, want %q", rs.items[0].spawnRole, "coder")
	}
}

// TestAttachWorkerSpawn_OldestPendingFirst verifies name-only matching walks
// oldest-first, mirroring TestAttachShellExec_OldestPendingFirst.
func TestAttachWorkerSpawn_OldestPendingFirst(t *testing.T) {
	rs := &runtimeSlot{}
	rs.startTool("call1", "spawn_worker", nil)
	rs.startTool("call2", "spawn_worker", nil)

	rs.attachWorkerSpawn("investigator", "", "job-1", 1, false, "")
	rs.attachWorkerSpawn("coder", "", "job-1", 1, false, "")

	if rs.items[0].spawnRole != "investigator" {
		t.Errorf("items[0].spawnRole = %q, want %q (first notification -> oldest pending item)", rs.items[0].spawnRole, "investigator")
	}
	if rs.items[1].spawnRole != "coder" {
		t.Errorf("items[1].spawnRole = %q, want %q (second notification -> second item)", rs.items[1].spawnRole, "coder")
	}
}

// TestAttachWorkerSpawn_SynthesizesOnTotalMiss mirrors
// TestAttachShellExec_SynthesizesOnTotalMiss: with no matching tool item, a
// completed item is synthesized so the status still surfaces.
func TestAttachWorkerSpawn_SynthesizesOnTotalMiss(t *testing.T) {
	rs := &runtimeSlot{}

	rs.attachWorkerSpawn("coder", "", "job-1", 1, true, "role not found")

	if len(rs.items) != 1 {
		t.Fatalf("expected a synthesized item, got %d", len(rs.items))
	}
	it := rs.items[0]
	if it.kind != outputItemTool || it.toolName != "spawn_worker" {
		t.Errorf("synthesized item wrong: %+v", it)
	}
	if !it.hasWorkerSpawn || !it.spawnFailed || it.spawnError != "role not found" {
		t.Errorf("worker spawn fields not set on synthesized item: %+v", it)
	}
	if it.endedAt.IsZero() {
		t.Error("synthesized item should be marked complete (no pending call exists to merge with later)")
	}
}
