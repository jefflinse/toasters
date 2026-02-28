# Pre-Phase 4 — Wave 2: Structural Preparation

**Created:** 2026-02-27
**Status:** Pending
**Prerequisite:** Wave 1 complete (`PRE_PHASE_4_WAVE_1.md`)
**Prerequisite for:** Phase 4 feature development (especially 4.3 Server/Client Split)
**Source:** `PRE_PHASE_4_ARCH_REVIEW.md` Section 11

---

## Purpose

Wave 2 restructures the codebase architecture to prepare for the Phase 4 client/server split. The key changes are: eliminating the dual agent/team type system (so there's one source of truth), relocating the orphaned `llm/tools` package, removing the legacy TUI streaming path, and fixing several architectural issues that would complicate the split.

**Why this matters:** The client/server split (Phase 4.3) requires extracting ~1,100 lines of business logic from the TUI into a service layer. That extraction is dramatically harder if the TUI depends on `agents.Team` (which loads from files) AND `db.Team` (which loads from SQLite) simultaneously. Wave 2 consolidates onto a single type system so the extraction has one clean seam to cut along.

**Relationship to Phase 4:** Wave 2 items can be executed as the opening work of Phase 4, before any feature development begins. They are structural improvements, not features.

---

## Progress Tracking

This file is the source of truth for Wave 2 execution. Update the status checkboxes and notes as each task is completed. When all tasks are done, update the Status at the top to "✅ Complete" with the date.

---

## Context: What Wave 1 Changed

Before starting Wave 2, confirm Wave 1 is complete. After Wave 1:

- `internal/llm/client/` directory is deleted (DEAD-1)
- `internal/llm/` contains only `tools/` subdirectory (orphaned path — addressed here as Task 2.2)
- `internal/anthropic/` contains only `keychain.go` (and its tests)
- SSRF protection is consolidated in `internal/httputil/`
- `.gitignore` is expanded
- `setup_workspace` command injection is fixed
- File size limits are enforced on `editFile`/`writeFile`
- `Runtime.Shutdown()` has a timeout
- Token refresh has a mutex

---

## Tasks

### Task 2.1: Consolidate Agent/Team Type Systems (DEAD-2)

- **Status:** ⬜ Pending
- **Finding:** DEAD-2
- **Severity:** HIGH
- **Effort:** Large (this is the biggest task in Wave 2)
- **Agent:** builder (with refactorer for guidance)
- **Files:** Many — see import site inventory below

**Problem:**

The codebase has two parallel agent/team type systems:

| | Legacy (`agents.*`) | Current (`db.*` + `loader` + `compose`) |
|---|---|---|
| Types | `agents.Agent`, `agents.Team` | `db.Agent`, `db.Team` |
| Discovery | `agents.DiscoverTeams()` | `loader.Load()` → `db.Store` |
| File watching | `agents.WatchRecursive()` | `loader.NewWatcher()` |
| Consumers | TUI, `llm/tools`, `cmd/root.go` | `compose.Composer`, TUI modals |

Both systems load the same `.md` files from disk. Both have file watchers. `cmd/root.go` runs **both paths** at startup (steps 4+22 and steps 6+21 in the boot sequence — see Appendix B of the arch review).

**Impact:**
- Team data is loaded twice on startup
- Two file watchers run simultaneously on the same directories
- `agents.Agent` has fields (`Background`, `Isolation`) that `db.Agent` doesn't, and vice versa
- Code that needs team data must decide which type system to use — confusing for contributors

**Goal:**

Eliminate `agents.DiscoverTeams()`, `agents.WatchRecursive()`, and all uses of `agents.Team` / `agents.Agent` types. Everything should flow through `loader` → `db.Store`. The `internal/agents/` package should either be deleted entirely or reduced to a minimal set of utilities (if any are still needed).

**Import site inventory (all must be rewired):**

| File | What It Uses | Replacement Strategy |
|------|-------------|---------------------|
| `cmd/root.go` | `agents.DiscoverTeams()`, `agents.Team`, `agents.WatchRecursive()` | Remove calls. Teams come from `db.Store.ListTeams()`. Second watcher removed. |
| `cmd/awareness.go` | `agents.Team` (for `summarizeTeam`) | Accept `db.Team` or a simpler struct |
| `internal/llm/tools/tools.go` | `agents.Team` (stored as field, used for `assign_team` tool) | Use `db.Store.ListTeams()` at call time, or accept `db.Team` |
| `internal/llm/tools/handler_jobs.go` | `agents.Team` (for workspace dir lookup) | Use `db.Store.GetTeam()` |
| `internal/llm/tools/handler_sessions.go` | `agents.Team` (for `assign_team` dispatch) | Use `db.Store.GetTeam()` + `db.Store.ListTeamAgents()` |
| `internal/tui/model.go` | `agents.Team` (stored as `m.teams` field) | Use `db.Team` from progress poll (already polls `ListTeams`) |
| `internal/tui/panels.go` | `agents.Team` (for left panel rendering) | Accept `db.Team` |
| `internal/tui/teams_modal.go` | `agents.Team`, `agents.DiscoverTeams()`, `agents.ParseFile()` | Use `db.Store` + `agentfmt` directly |
| `internal/tui/grid.go` | `agents.Team` (for grid slot labels) | Accept `db.Team` |
| `internal/tui/helpers.go` | `agents.Team` (for session slot team lookup) | Accept `db.Team` |
| `internal/tui/streaming.go` | `agents.Team` (for legacy path team lookup) | Remove with Task 2.3 (ARCH-5 legacy path removal) |
| `internal/tui/prompt.go` | `agents.Team` (for dispatch confirmation) | Accept `db.Team` |

**Execution approach:**

1. **Audit `db.Team` vs `agents.Team` field differences.** Identify any fields on `agents.Team` that `db.Team` lacks. Add missing fields to `db.Team` / `db.Agent` if needed (may require a migration or just struct changes if the DB schema already has the columns).

2. **Update `cmd/root.go` boot sequence.** Remove `agents.DiscoverTeams()` (step 4) and `agents.WatchRecursive()` (step 22). The `loader.Load()` (step 6) and `loader.NewWatcher()` (step 21) already do this work.

3. **Update `llm/tools` package.** Replace `agents.Team` field with `db.Store` reference (or `db.Team` slice). Update `assign_team` and related handlers to query the store.

4. **Update TUI.** Replace `m.teams []agents.Team` with `m.teams []db.Team` (or `[]*db.Team`). Update all rendering code. The progress poll already queries `ListTeams()` — wire that data through.

5. **Update `cmd/awareness.go`.** Change `summarizeTeam` to accept `db.Team`.

6. **Delete or gut `internal/agents/` package.** If no code remains that needs it, delete the entire package. If some utility functions are still useful (e.g., `ParseFile` for one-off parsing), consider whether they belong in `agentfmt` instead.

7. **Verify no imports remain.**

**Acceptance criteria:**
- [ ] Zero imports of `internal/agents` anywhere in the codebase
- [ ] `agents.DiscoverTeams()` and `agents.WatchRecursive()` calls removed from `cmd/root.go`
- [ ] Only one file watcher runs at startup (the `loader.NewWatcher()`)
- [ ] All TUI code uses `db.Team` / `db.Agent` types
- [ ] All `llm/tools` code uses `db.Store` for team/agent data
- [ ] `internal/agents/` package deleted (or reduced to zero exports)
- [ ] `go build ./...` passes
- [ ] `go test ./...` passes
- [ ] `golangci-lint run` reports 0 findings

**Verification:**
```bash
grep -r "\"github.com/jefflinse/toasters/internal/agents\"" . 2>/dev/null | grep -v "_test.go" | grep -v vendor
# Must return nothing

grep -r "agents.DiscoverTeams\|agents.WatchRecursive" .
# Must return nothing

ls internal/agents/ 2>/dev/null
# Should not exist, or contain only test fixtures
```

**Risk notes:**
- This is the largest task in Waves 1-2. It touches 12+ files across 4 packages.
- The TUI `m.teams` field is used extensively in rendering. Type changes will cascade.
- `agents.Team` has a `Dir` field (filesystem path) used for workspace resolution. Ensure `db.Team` has equivalent data (it has `Dir` in the schema).
- The `agents.ParseFile()` function is used in `teams_modal.go` for one-off parsing during team promotion. Replace with `agentfmt.ParseFile()`.

---

### Task 2.2: Relocate `llm/tools` Package (DEAD-3)

- **Status:** ⬜ Pending
- **Finding:** DEAD-3
- **Severity:** MEDIUM
- **Effort:** Small
- **Agent:** builder
- **Depends on:** Task 1.3 (Wave 1 DEAD-1 — `llm/client` must be deleted first)

**Problem:**

After Wave 1 deletes `internal/llm/client/`, `internal/llm/types.go`, `internal/llm/provider.go`, and `internal/llm/doc.go`, the `internal/llm/` directory contains only the `tools/` subdirectory. The package path `internal/llm/tools` is misleading — these tools have nothing to do with `internal/llm` types. They import `provider.ToolCall`, not `llm.ToolCall`.

**Fix:**

Move `internal/llm/tools/` to a new location. Recommended options:
1. `internal/dispatch/` — "operator tool dispatch"
2. `internal/optool/` — "operator tools"
3. `internal/operator/dispatch/` — nested under operator (but may create import issues if `operator` imports it)

The best choice depends on import graph constraints. The operator package (`internal/operator/`) already exists and imports `llm/tools` — so nesting under `operator/` would create a cycle unless the dependency direction is inverted. **Recommended: `internal/dispatch/`**.

After the move, `internal/llm/` directory should be completely empty and deleted.

**Execution:**

1. Create `internal/dispatch/` (or chosen name)
2. Move all files from `internal/llm/tools/` to the new location
3. Update package declaration in all moved files
4. Update all import paths across the codebase
5. Delete `internal/llm/` directory entirely
6. Verify

**Files that import `internal/llm/tools`:**

Search for these and update:
```bash
grep -r "internal/llm/tools" . --include="*.go"
```

Expected importers: `cmd/root.go`, `internal/tui/model.go` (or similar TUI files), possibly `internal/operator/`.

**Acceptance criteria:**
- [ ] `internal/llm/tools/` moved to new location
- [ ] `internal/llm/` directory deleted entirely
- [ ] All import paths updated
- [ ] Package name updated in moved files
- [ ] `go build ./...` passes
- [ ] `go test ./...` passes

**Verification:**
```bash
ls internal/llm/ 2>/dev/null && echo "FAIL: internal/llm still exists" || echo "OK: internal/llm removed"
grep -r "internal/llm" . --include="*.go" && echo "FAIL: llm imports remain" || echo "OK: no llm imports"
```

---

### Task 2.3: Remove Legacy Dual-Path in TUI (ARCH-5)

- **Status:** ⬜ Pending
- **Finding:** ARCH-5
- **Severity:** MEDIUM
- **Effort:** Medium
- **Agent:** builder
- **Files:** `internal/tui/streaming.go`, `internal/tui/model.go`, `internal/tui/prompt.go`

**Problem:**

The TUI maintains a complete legacy direct-stream path alongside the operator path:

| Legacy Path | Operator Path |
|---|---|
| `StreamChunkMsg` | `OperatorTextMsg` |
| `StreamDoneMsg` | `OperatorDoneMsg` |
| `ToolCallMsg` → `ToolResultMsg` | Operator handles tools internally |
| `startStream()` | `operator.Send(EventUserMessage)` |
| `waitForChunk()` | Operator callbacks |

This doubles the streaming/tool-handling logic surface. The legacy path was the pre-Phase-3 interaction model where the TUI talked directly to an LLM provider. Since Phase 3, all interaction goes through the operator event loop.

**What to remove:**

1. **`startStream()` function** in `streaming.go` — the legacy direct-LLM-call path
2. **`sendAnthropicMessage()` function** in `streaming.go` — the `/anthropic` command direct path
3. **`fetchModels()` function** in `streaming.go` — sidebar model listing (evaluate: is this still used via the operator path?)
4. **`StreamChunkMsg`, `StreamDoneMsg` message types** and their handlers in `model.go` `Update()`
5. **`ToolCallMsg`, `ToolResultMsg` message types** and their handlers (the `executeToolsCmd` helper)
6. **`waitForChunk()` helper** in `streaming.go`
7. **Legacy streaming state fields** in `streamingState` that are only used by the legacy path (e.g., `streamCh`, `cancelStream` if only used by legacy)
8. **`/anthropic` slash command** handler (if it uses the legacy direct path)

**What to keep:**

- `OperatorTextMsg`, `OperatorDoneMsg`, `OperatorEventMsg` — the operator path
- Operator streaming state (`streaming`, `partialResponse`, `operatorByline`)
- Any model-listing functionality that's been rewired through the operator

**Caution:**

- Carefully audit each removal. Some message types may be shared between legacy and operator paths.
- The `executeToolsCmd` helper may be used by the operator path for tool interception (e.g., `assign_team` confirmation). Check before removing.
- `fetchModels()` may still be needed for the sidebar provider/model display. If so, keep it but note it as a TUI-direct-to-provider call that will need to move server-side in Phase 4.3.

**Acceptance criteria:**
- [ ] Legacy direct-stream path removed (`startStream`, `sendAnthropicMessage`, `waitForChunk`)
- [ ] Legacy message types removed or confirmed shared with operator path
- [ ] `StreamChunkMsg`/`StreamDoneMsg` handlers removed from `Update()`
- [ ] No dead code remains in `streaming.go`
- [ ] Operator path still works end-to-end (user message → operator → response → TUI)
- [ ] `go build ./...` passes
- [ ] `go test ./...` passes

**Verification:**
```bash
grep -n "startStream\|sendAnthropicMessage\|waitForChunk" internal/tui/streaming.go
# Should return nothing

grep -n "StreamChunkMsg\|StreamDoneMsg" internal/tui/
# Should return nothing (or only type definitions if kept for compatibility)
```

---

### Task 2.4: Fix Conversation Window Truncation (ARCH-3)

- **Status:** ⬜ Pending
- **Finding:** ARCH-3
- **Severity:** MEDIUM
- **Effort:** Small
- **Agent:** builder
- **Files:** `internal/operator/operator.go`

**Problem:**

The operator conversation uses `maxMessages=200` with naive `messages[len-200:]` truncation. This can split a tool-call/result pair, corrupting the LLM conversation. The LLM would see a tool result without the corresponding tool call, or vice versa.

**Fix:**

Replace naive truncation with boundary-aware truncation:

```go
func truncateMessages(messages []provider.Message, maxMessages int) []provider.Message {
    if len(messages) <= maxMessages {
        return messages
    }

    // Always keep the first message (system context or initial user message)
    // Find the earliest safe truncation point in the tail
    tail := messages[len(messages)-maxMessages:]

    // Walk forward from the start of the tail to find the first complete exchange.
    // A safe start is:
    // - A user message
    // - An assistant message with no tool calls
    // Skip orphaned tool results (role=tool) and assistant messages with tool calls
    // whose results might be before the window.
    startIdx := 0
    for i, msg := range tail {
        if msg.Role == "user" {
            startIdx = i
            break
        }
        if msg.Role == "assistant" && len(msg.ToolCalls) == 0 {
            startIdx = i
            break
        }
    }

    return tail[startIdx:]
}
```

The key insight: never start the window in the middle of a tool-call → tool-result exchange. Always start at a user message or a non-tool-calling assistant message.

**Acceptance criteria:**
- [ ] Truncation never splits tool-call/result pairs
- [ ] First message in truncated window is always a user message or non-tool assistant message
- [ ] Tests with synthetic conversations verify boundary safety
- [ ] `go build ./...` passes
- [ ] `go test ./...` passes

**Verification:**
```bash
grep -n "maxMessages" internal/operator/operator.go
# Should show the new truncation function, not a naive slice
```

---

### Task 2.5: Fix Self-Send Deadlock Potential (ARCH-2 / CONC-2)

- **Status:** ⬜ Pending
- **Finding:** ARCH-2, CONC-2
- **Severity:** MEDIUM
- **Effort:** Small
- **Agent:** builder
- **Files:** `internal/operator/operator.go`, `internal/operator/system_tools.go`

**Problem:**

The `assignNextTask` → `SystemTools.assignTask` → `trySendEvent(EventTaskStarted)` path sends events from the event loop goroutine back to its own channel. The 256-slot buffer makes overflow "practically impossible," but a pathological workload (256+ tasks completing in rapid succession) could deadlock the event loop.

The pattern is: event loop processes `EventTaskCompleted` → calls `assignNextTask` → which calls `trySendEvent(EventTaskStarted)` → which sends to the same channel the event loop is reading from.

**Fix:**

Handle `EventTaskStarted` inline instead of sending it through the channel. The `checkJobComplete` function already uses this pattern for `EventJobComplete` — follow the same approach.

Specifically:
1. When `assignNextTask` would send `EventTaskStarted`, instead return the event data to the caller
2. The caller (event loop) handles it inline (DB update + feed entry creation)
3. Remove the `trySendEvent(EventTaskStarted)` call

**Acceptance criteria:**
- [ ] `EventTaskStarted` is handled inline, not sent through the event channel
- [ ] No self-send pattern remains in the event loop
- [ ] Task assignment still creates DB records and feed entries
- [ ] `go build ./...` passes
- [ ] `go test ./...` passes

---

### Task 2.6: Consolidate `ToolDef` Type (STRUCT-2)

- **Status:** ⬜ Pending
- **Finding:** STRUCT-2
- **Severity:** MEDIUM
- **Effort:** Small
- **Agent:** builder
- **Files:** `internal/runtime/tools.go` (or wherever `runtime.ToolDef` is defined), `internal/progress/tools.go`, new `internal/tooldef/` package

**Problem:**

`progress.ToolDef` is an explicit copy of `runtime.ToolDef` to avoid an import cycle. The comment says "Keep in sync with runtime.ToolDef" — but there's no compiler enforcement. If someone adds a field to one and not the other, they silently diverge.

**Fix:**

Create `internal/tooldef/` package with the shared `ToolDef` type:

```go
package tooldef

// ToolDef describes a tool available to an agent.
type ToolDef struct {
    Name        string
    Description string
    InputSchema map[string]any
}
```

Then update both `runtime` and `progress` to import from `tooldef`. This breaks the import cycle because `tooldef` is a leaf package with no dependencies.

**Acceptance criteria:**
- [ ] `internal/tooldef/` package created with shared `ToolDef` type
- [ ] `runtime.ToolDef` replaced with `tooldef.ToolDef` (or type alias)
- [ ] `progress.ToolDef` replaced with `tooldef.ToolDef` (or type alias)
- [ ] "Keep in sync" comment removed
- [ ] `go build ./...` passes
- [ ] `go test ./...` passes

---

### Task 2.7: Fix Post-Shutdown TUI Sends (CONC-6)

- **Status:** ⬜ Pending
- **Finding:** CONC-6
- **Severity:** LOW
- **Effort:** Trivial
- **Agent:** builder
- **Files:** `cmd/root.go`

**Problem:**

`atomic.Pointer[tea.Program]` is never set to `nil` after `prog.Run()` returns. Session goroutines that are still running (during the shutdown grace period) may call `prog.Send()` after the TUI has exited, potentially causing a panic.

**Fix:**

After `prog.Run()` returns in `cmd/root.go`, set the atomic pointer to nil:

```go
err := prog.Run()
p.Store(nil)  // Prevent post-shutdown sends
```

Then, in all callback sites that use `p.Load().Send()`, add a nil check:

```go
if prog := p.Load(); prog != nil {
    prog.Send(msg)
}
```

Check if this nil-guard pattern already exists in some callbacks. If so, ensure it's consistent across all of them.

**Acceptance criteria:**
- [ ] `p.Store(nil)` called after `prog.Run()` returns
- [ ] All `p.Load().Send()` call sites have nil guards
- [ ] No panic possible from post-shutdown sends
- [ ] `go build ./...` passes
- [ ] `go test ./...` passes

---

## Execution Order

Tasks have the following dependencies:

```
Task 2.1 (DEAD-2: consolidate types)     — independent, start first (largest)
Task 2.2 (DEAD-3: relocate llm/tools)    — depends on Wave 1 Task 1.3
Task 2.3 (ARCH-5: remove legacy path)    — independent, but easier after 2.1
Task 2.4 (ARCH-3: truncation fix)        — independent
Task 2.5 (ARCH-2: self-send fix)         — independent
Task 2.6 (STRUCT-2: ToolDef consolidation) — independent
Task 2.7 (CONC-6: post-shutdown sends)   — independent
```

**Recommended sequence:**

```
Phase A (do first — largest item):
  Task 2.1 (consolidate agent/team types)

Phase B (parallel, after 2.1):
  Task 2.2 (relocate llm/tools)
  Task 2.3 (remove legacy TUI path)

Phase C (parallel, independent):
  Task 2.4 (truncation fix)
  Task 2.5 (self-send fix)
  Task 2.6 (ToolDef consolidation)
  Task 2.7 (post-shutdown sends)
```

Tasks 2.4–2.7 are small/trivial and can be done in any order, even in parallel with 2.1 if desired. However, doing 2.1 first reduces the number of files that 2.2 and 2.3 need to touch.

---

## Verification (All Tasks Complete)

After all Wave 2 tasks are done, run:

```bash
# Full build
go build ./...

# Full test suite
go test ./... -count=1

# Race detector
go test ./... -race -count=1

# Lint
golangci-lint run

# Verify agents package eliminated
ls internal/agents/ 2>/dev/null && echo "FAIL: agents package still exists" || echo "OK: agents package removed"
grep -r "internal/agents" . --include="*.go" && echo "FAIL: agents imports remain" || echo "OK: no agents imports"

# Verify llm directory eliminated
ls internal/llm/ 2>/dev/null && echo "FAIL: internal/llm still exists" || echo "OK: internal/llm removed"

# Verify no duplicate ToolDef
grep -rn "Keep in sync" internal/ && echo "FAIL: sync comments remain" || echo "OK: no sync comments"

# Verify legacy stream path removed
grep -n "startStream\|StreamChunkMsg\|StreamDoneMsg" internal/tui/ && echo "FAIL: legacy path remains" || echo "OK: legacy path removed"

# Verify self-send pattern fixed
grep -n "trySendEvent.*TaskStarted" internal/operator/ && echo "FAIL: self-send remains" || echo "OK: self-send fixed"
```

All commands must pass with zero errors and zero lint findings.

---

## Post-Wave 2

After Wave 2 is complete:
1. Update this file's status to "✅ Complete" with the date
2. Update `PRE_PHASE_4_ARCH_REVIEW.md` Section 10 findings registry with completion status
3. Update `CLAUDE.md`:
   - Remove references to `internal/agents/` package from Project Structure
   - Remove references to `internal/llm/` from Project Structure
   - Add new package locations (e.g., `internal/dispatch/`, `internal/tooldef/`, `internal/httputil/`)
   - Update Tech Debt section to reference Wave 2 completion
4. Proceed to Phase 4 feature development (`PHASE_4.md`)

---

## Appendix: Key Architectural Context for the Builder

### Boot Sequence After Wave 2

After Wave 2, the `cmd/root.go` boot sequence should look like:

```
1.  bootstrap.Run()           — copy embedded defaults to ~/.config/toasters/system/
2.  config.Load()             — read ~/.config/toasters/config.yaml via Viper
3.  slog.SetDefault()         — redirect logging to ~/.config/toasters/toasters.log
4.  (REMOVED — was agents.DiscoverTeams())
5.  db.Open()                 — open SQLite, run migrations, WAL mode
6.  loader.New() + Load()     — parse .md files → rebuild definition tables in DB
7.  compose.New()             — create Composer (reads from DB at runtime)
8.  provider.NewRegistry()    — register all configured LLM providers
9.  runtime.New()             — create Runtime (session manager)
10. mcp.NewManager()          — connect to MCP servers, discover tools
11. rt.SetMCPCaller()         — wire MCP into runtime (TruncatingCaller wrapper)
12. dispatch.NewToolExecutor() — create operator tool executor (was llm/tools)
13. Create operator provider  — Anthropic or OpenAI based on config
14. compose.Compose("operator", "system") — build operator system prompt
15. operator.New()            — create Operator with 3 callbacks
16. tui.NewModel()            — create TUI Model with all dependencies
17. tea.NewProgram()          — create Bubble Tea program
18. p.Store(prog)             — store atomic pointer
19. op.Start(opCtx)           — launch operator event loop goroutine
20. Background goroutine      — generate team awareness, send greeting
21. loader.NewWatcher()       — start fsnotify watcher for .md file changes
22. (REMOVED — was agents.WatchRecursive())
23. prog.Run()                — enter Bubble Tea event loop
24. p.Store(nil)              — (NEW) prevent post-shutdown sends
25. Deferred cleanup          — rt.Shutdown(), mcpManager.Close(), sqliteStore.Close()
```

### TUI Type Changes

The TUI `Model` struct's `teams` field changes from `[]agents.Team` to `[]*db.Team`. All rendering functions that accept `agents.Team` must be updated. The `db.Team` type has these relevant fields:

```go
type Team struct {
    ID          string
    Name        string
    Description string
    Lead        string    // lead agent name
    Dir         string    // filesystem path
    Culture     string    // team culture document
    IsAuto      bool      // auto-detected team
    IsSystem    bool      // system team
    Source      string    // source format
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

Compare with `agents.Team` to identify any missing fields that need to be added to `db.Team`.
