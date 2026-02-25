# Phase 2: Connect to the World — Implementation Plan

**Created:** 2026-02-24
**Status:** Complete
**Branch:** `phase-2`

---

## Objective

Give Toasters agents access to external tools via MCP (GitHub, Jira, Linear, etc.), enable structured progress reporting from agents back to the orchestrator via SQLite, and display real-time progress in the TUI. Delivered as 3 PRs corresponding to deliverables 2.1, 2.2, and 2.3.

---

## Assumptions

- `mcp-go` (`github.com/mark3labs/mcp-go`) needs to be added as a dependency in the first PR that lands
- The existing `llm.Provider` interface (TUI operator) and `provider.Provider` interface (agent runtime) remain as-is — MCP tools are adapted to both formats
- The `db.Store` interface already has all the CRUD methods needed for progress reporting (`ReportProgress`, `UpdateTaskStatus`, `LogArtifact`, etc.) — no schema migration is needed
- The `progress_reports` table already exists with the right columns (`job_id`, `task_id`, `agent_id`, `status`, `message`, `created_at`)
- Claude CLI subprocess integration (Part B2 of the MCP plan) is included but is lower priority than in-process integration — if it proves complex, it can be deferred
- OpenAPI bridges (Part C) are explicitly Phase 3 scope and excluded
- Environment variable expansion (`${ENV_VAR}`) for MCP server config values follows the same pattern already used in `provider.NewFromConfig` (`os.Expand`)

---

## PR Overview

| PR | Deliverable | Branch | Effort | Dependencies | Status |
|----|------------|--------|--------|-------------|--------|
| PR 1 | 2.1 — MCP Client (Consume External Servers) | `feat/mcp-client` | 2–3 days | None (Phase 1 complete) | ✅ Merged |
| PR 2 | 2.2 — Toasters MCP Server (Progress Reporting) | `feat/mcp-server` | 2–3 days | None (Phase 1 complete) | ✅ Merged |
| PR 3 | 2.3 — Real-Time TUI Progress Display | `feat/tui-progress` | 1–2 days | PR 2 must be merged first | ✅ Merged |

**Merge order:** PR 1 and PR 2 can merge in either order (they touch different packages). PR 3 merges last.

---

## PR 1: MCP Client — Consume External Servers

**Branch:** `feat/mcp-client`
**Depends on:** Nothing (Phase 1 complete)

### Steps

| # | Step | Agent | Status |
|---|------|-------|--------|
| 2.1.1 | Add `mcp-go` dependency | builder | ✅ Done |
| 2.1.2 | Add MCP config schema | builder | ✅ Done |
| 2.1.3 | Create `internal/mcp/` package — Manager, connect, discover, dispatch | builder | ✅ Done |
| 2.1.4 | Implement tool conversion and namespacing | builder | ✅ Done |
| 2.1.5 | Wire MCP tools into operator tool set | builder | ✅ Done |
| 2.1.6 | Wire MCP tools into agent runtime tool set | builder | ✅ Done |
| 2.1.7 | Write comprehensive tests (≥75% coverage) | test-writer | ✅ Done |
| 2.1.8 | Security audit | security-auditor | ✅ Done |
| 2.1.9 | Code review (+ concurrency review) | code-reviewer | ✅ Done |

### Details

**2.1.1 — Add `mcp-go` dependency**
- Run `go get github.com/mark3labs/mcp-go`
- Verify `go build ./...` and `go test ./...` still pass
- Risk: Transitive dependency conflicts — check go.sum for version clashes

**2.1.2 — Add MCP config schema**
- Modify: `internal/config/config.go`
- Add types:
  ```go
  MCPServerConfig — Name, Transport ("stdio"/"http"/"sse"), Command, Args, Env, URL, Headers, Enabled (default true), EnabledTools []string
  MCPConfig — Servers []MCPServerConfig
  ```
- Add `MCP MCPConfig` field to the top-level `Config` struct with `mapstructure:"mcp"`
- Support `${ENV_VAR}` expansion in `Env`, `Headers`, `URL` fields (same pattern as `provider.NewFromConfig`)
- Add tests: `internal/config/config_test.go` — verify empty/absent `mcp` section produces zero-value, verify full config unmarshals correctly, verify env var expansion
- Acceptance criteria: `config.Load()` unmarshals an `mcp.servers` list without error; existing configs without `mcp` key continue to load correctly

**2.1.3 — Create `internal/mcp/` package**
- Files: `internal/mcp/doc.go`, `internal/mcp/manager.go`, `internal/mcp/server_entry.go`
- Key type: `Manager` struct with:
  - `mu sync.RWMutex` — protects `servers` and `toolIndex`
  - `servers []serverEntry` — each entry holds an `mcp-go` client, server name, and the server's tool list
  - `toolIndex map[string]int` — maps namespaced tool name → server index for O(1) dispatch
- `serverEntry` struct: `name string`, `client` (mcp-go client interface), `tools []ToolInfo`, `config config.MCPServerConfig`
- `ToolInfo` struct: `NamespacedName string`, `OriginalName string`, `Description string`, `InputSchema json.RawMessage`
- Public API:
  - `NewManager() *Manager`
  - `Connect(ctx context.Context, servers []config.MCPServerConfig) error` — iterates servers, creates mcp-go client per transport type (stdio: `mcpclient.NewStdioMCPClient`, http: `mcpclient.NewStreamableHTTPMCPClient` or `mcpclient.NewSSEMCPClient`), calls `Initialize()`, then `ListTools()`, builds tool index. Failed servers are logged and skipped (not fatal).
  - `Call(ctx context.Context, namespacedName string, argsJSON json.RawMessage) (string, error)` — looks up server by tool name in `toolIndex`, calls `CallTool()` on the mcp-go client, returns result as string
  - `Tools() []ToolInfo` — returns all discovered tools (thread-safe read)
  - `Close() error` — closes all mcp-go clients
- Namespacing: tool name format is `{server_name}__{tool_name}`. The `__` separator is chosen because it's unlikely to appear in either server names or tool names.
- Risk: mcp-go API surface — need to verify the exact client constructors and method signatures. The builder should read mcp-go's godoc/source to confirm.
- Risk: stdio server process lifecycle — mcp-go's stdio client manages the subprocess, but we need to ensure `Close()` properly terminates it.

**2.1.4 — Implement tool conversion and namespacing**
- File: `internal/mcp/convert.go`
- Functions:
  - `ToLLMTools(tools []ToolInfo) []llm.Tool` — converts MCP tool definitions to `llm.Tool` format (the operator's format). Each tool gets `Type: "function"`, `Function.Name` = namespaced name, `Function.Description` from MCP, `Function.Parameters` from MCP's `inputSchema`
  - `ToProviderTools(tools []ToolInfo) []provider.Tool` — converts MCP tool definitions to `provider.Tool` format (the agent runtime's format). `Name` = namespaced name, `Description` from MCP, `Parameters` = `inputSchema` as `json.RawMessage`
  - `ToRuntimeToolDefs(tools []ToolInfo) []runtime.ToolDef` — converts to `runtime.ToolDef` format
- Tool filtering: `FilterTools(tools []ToolInfo, whitelist []string) []ToolInfo` — if whitelist is non-empty, only include tools whose original (non-namespaced) name is in the whitelist. Applied during `Connect()` per-server.
- Acceptance criteria: round-trip conversion preserves tool name, description, and parameter schema

**2.1.5 — Wire MCP tools into operator tool set**
- Modify: `cmd/root.go`, `internal/llm/tools/tools.go`
- In `cmd/root.go`:
  - After config load, create `mcp.NewManager()` and call `manager.Connect(ctx, cfg.MCP.Servers)`
  - Convert MCP tools to `llm.Tool` format via `mcp.ToLLMTools()`
  - Append to `toolExec.Tools` (the operator's static tools list)
  - Store the manager reference on `toolExec` so it can dispatch MCP tool calls
  - Add `defer manager.Close()` for graceful shutdown
- In `internal/llm/tools/tools.go`:
  - Add `MCPManager` field to `ToolExecutor` struct (type: interface with `Call(ctx, name, args) (string, error)` method — avoid importing `internal/mcp` directly to keep the dependency clean)
  - Define `MCPCaller` interface: `Call(ctx context.Context, name string, args json.RawMessage) (string, error)`
  - In `ExecuteTool()`, add a check before the `default` case: if the tool name contains `__` and `te.MCPManager != nil`, parse the arguments and dispatch to `te.MCPManager.Call()`
  - This means MCP tools are dispatched through the existing async tool execution path (`executeToolsCmd` in the TUI) — no TUI changes needed
- Acceptance criteria: with a GitHub MCP server configured, the operator sees `github__create_issue` etc. in its tool list; calling one routes to the MCP server and returns the result

**2.1.6 — Wire MCP tools into agent runtime tool set**
- Modify: `internal/runtime/tools.go`, `internal/runtime/runtime.go`
- Approach: Create a `CompositeTools` executor that wraps `CoreTools` and adds MCP tool dispatch
- New file: `internal/runtime/composite_tools.go`
  - `CompositeTools` struct: wraps a `CoreTools` and an optional `MCPCaller` (same interface as 2.1.5)
  - `Execute()`: tries `CoreTools.Execute()` first; if it returns "unknown tool" and the name contains `__`, dispatch to `MCPCaller.Call()`
  - `Definitions()`: returns `CoreTools.Definitions()` + MCP tool definitions (converted to `runtime.ToolDef`)
- In `runtime.Runtime`: add `mcpCaller` field (optional), set via `SetMCPCaller(caller MCPCaller)` method
- In `runtime.SpawnAgent()`: when creating the tool executor, if `r.mcpCaller != nil`, wrap `CoreTools` in `CompositeTools`
- In `cmd/root.go`: after creating the MCP manager, call `rt.SetMCPCaller(manager)` to wire it in
- Acceptance criteria: runtime agents see MCP tools in their tool set and can call them

**2.1.7 — Write comprehensive tests**
- Files: `internal/mcp/manager_test.go`, `internal/mcp/convert_test.go`, `internal/runtime/composite_tools_test.go`
- Test strategy:
  - **Manager tests**: Use a mock MCP server (mcp-go provides test helpers, or create a simple stdio server that responds to Initialize and ListTools). Test: connect succeeds, tool discovery works, dispatch routes correctly, failed server is skipped, unknown tool returns error, Close() is clean.
  - **Convert tests**: Round-trip conversion for various tool schemas (simple params, nested objects, arrays, no params). Verify namespacing. Verify filtering.
  - **CompositeTools tests**: Core tool takes priority, MCP tool dispatched for unknown names with `__`, error when no MCP caller and unknown tool.
- Target: ≥75% coverage on `internal/mcp/`, ≥85% on modified runtime code
- All tests must pass with `-race`

**2.1.8 — Security audit**
- Focus areas:
  - **Credential handling**: Env var expansion in MCP server config — ensure secrets aren't logged. Verify `Env` map values are expanded but not echoed in error messages.
  - **Tool name injection**: Verify that namespaced tool names can't be crafted to collide with static tools (static tools always win — verify this is enforced)
  - **SSRF via MCP**: MCP servers configured by the user are trusted (they chose to configure them), but verify that tool call arguments from the LLM can't be used to redirect calls to unintended servers
  - **Subprocess lifecycle**: For stdio transport, verify that `Close()` terminates the subprocess and doesn't leave orphans
  - **Input validation**: Verify that `Call()` validates the tool name exists before dispatching

**2.1.9 — Code review (+ concurrency review)**
- Concurrency focus:
  - `Manager.mu` usage — verify all reads of `servers` and `toolIndex` are under `RLock`, all writes under `Lock`
  - `Connect()` should hold the write lock for the entire duration of server setup (or use a pattern where it builds the new state, then swaps atomically)
  - `Call()` should hold only a read lock to look up the server, then release before making the (potentially slow) MCP call
  - Verify no goroutine leaks from mcp-go client connections
- General review: error wrapping, interface design, naming conventions, test adequacy

---

## PR 2: Toasters MCP Server — Progress Reporting

**Branch:** `feat/mcp-server`
**Depends on:** Nothing (Phase 1 complete)

### Steps

| # | Step | Agent | Status |
|---|------|-------|--------|
| 2.2.1 | Add `mcp-go` dependency (if not already added by PR 1) | builder | ✅ Done |
| 2.2.2 | Create progress tool handlers package | builder | ✅ Done |
| 2.2.3 | Wire progress tools into agent runtime (in-process) | builder | ✅ Done |
| 2.2.4 | Create MCP server for external agents (Claude CLI) | builder | ✅ Done |
| 2.2.5 | Wire MCP server into gateway spawn path | builder | ✅ Done |
| 2.2.6 | Write comprehensive tests (≥80% coverage) | test-writer | ✅ Done |
| 2.2.7 | Code review | code-reviewer | ✅ Done |

### Details

**2.2.1 — Add `mcp-go` dependency**
- If PR 1 hasn't landed yet, run `go get github.com/mark3labs/mcp-go`
- If PR 1 has landed, this step is a no-op
- Verify `go build ./...` and `go test ./...` still pass

**2.2.2 — Create progress tool handlers package**
- New files: `internal/progress/doc.go`, `internal/progress/handlers.go`, `internal/progress/tools.go`
- Why a new package (`internal/progress/`) rather than putting this in `internal/mcp/`: These handlers are used by both in-process agents (no MCP protocol) and external agents (via MCP). They are fundamentally SQLite write operations, not MCP-specific. The MCP server wraps them, but the handlers themselves are protocol-agnostic.
- `handlers.go` — Handler functions, each taking a `db.Store` and typed parameters:
  - `ReportProgress(ctx, store, params ReportProgressParams) (string, error)` — Inserts a `db.ProgressReport` with `job_id`, `task_id`, `agent_id` (from session context), `status`, `message`. Returns confirmation string.
  - `ReportBlocker(ctx, store, params ReportBlockerParams) (string, error)` — Inserts a `db.ProgressReport` with status="blocked" and the blocker description as message. Severity is stored in the message or metadata. Returns confirmation.
  - `UpdateTaskStatus(ctx, store, params UpdateTaskStatusParams) (string, error)` — Calls `store.UpdateTaskStatus()` with the new status and optional summary. Returns confirmation.
  - `RequestReview(ctx, store, params RequestReviewParams) (string, error)` — Logs an artifact of type "review_request" via `store.LogArtifact()` and inserts a progress report with status="review_requested". Returns confirmation.
  - `QueryJobContext(ctx, store, params QueryJobContextParams) (string, error)` — Reads job via `store.GetJob()`, tasks via `store.ListTasksForJob()`, recent progress via `store.GetRecentProgress()`, artifacts via `store.ListArtifactsForJob()`. Assembles a structured JSON response with job overview, task statuses, recent progress, and active blockers.
  - `LogArtifact(ctx, store, params LogArtifactParams) (string, error)` — Calls `store.LogArtifact()`. Returns confirmation.
- `tools.go` — Tool definitions as `runtime.ToolDef` slices:
  - `ProgressToolDefs() []runtime.ToolDef` — Returns the 6 tool definitions with JSON Schema parameters matching the MCP integration plan spec
  - Tool names: `report_progress`, `report_blocker`, `update_task_status`, `request_review`, `query_job_context`, `log_artifact`
  - Note: These are NOT namespaced with `toasters__` prefix for in-process agents — they're native tools. The MCP server (step 2.2.4) will expose them with the `toasters__` prefix for external agents.
- Param structs: `ReportProgressParams`, `ReportBlockerParams`, `UpdateTaskStatusParams`, `RequestReviewParams`, `QueryJobContextParams`, `LogArtifactParams` — each with JSON tags matching the tool parameter names
- Acceptance criteria: Each handler writes to SQLite correctly; `QueryJobContext` returns a useful structured response

**2.2.3 — Wire progress tools into agent runtime (in-process)**
- Modify: `internal/runtime/tools.go`, `internal/runtime/session.go`, `internal/runtime/runtime.go`
- Approach: Extend `CoreTools` to include progress tools when a `db.Store` and session context are available
- Add to `CoreTools`:
  - `store db.Store` field (may be nil)
  - `sessionID string` field
  - `agentID string` field
  - `jobID string` field
  - New options: `WithStore(store db.Store)` and `WithSessionContext(sessionID, agentID, jobID string)`
- In `CoreTools.Execute()`: add cases for `report_progress`, `report_blocker`, `update_task_status`, `request_review`, `query_job_context`, `log_artifact` — each parses args, injects `agentID` from session context, delegates to the corresponding `progress.Handler` function
- In `CoreTools.Definitions()`: if `store != nil`, append the progress tool definitions from `progress.ProgressToolDefs()`
- In `runtime.SpawnAgent()`: pass `WithStore(r.store)` and `WithSessionContext(id, opts.AgentID, opts.JobID)` when creating `CoreTools`
- Acceptance criteria: In-process agents see the 6 progress tools in their tool set; calling `report_progress` inserts a row in `progress_reports`; calling `query_job_context` returns job data from SQLite

**2.2.4 — Create MCP server for external agents (Claude CLI)**
- New files: `internal/progress/server.go`
- Use `mcp-go`'s server package to create an MCP server that exposes the 6 progress tools
- `NewServer(store db.Store) *mcpserver.MCPServer` — creates an mcp-go server with tool handlers registered
- Each tool handler: parses the MCP `CallToolRequest`, extracts parameters, delegates to the corresponding handler function from `handlers.go`, returns the result as MCP `CallToolResult`
- Transport: Start as stdio server (simplest for Claude CLI integration — Claude launches it as a subprocess)
- `StartStdioServer(ctx context.Context, store db.Store) error` — creates the server and runs it on stdin/stdout. This will be called as a separate subprocess command.
- Approach: Add a `toasters mcp-server` subcommand to the Cobra CLI that starts the MCP server in stdio mode. This is cleaner than a separate binary.
- New file: `cmd/mcp_server.go` — implements the `toasters mcp-server` subcommand that opens the SQLite database and runs the MCP server on stdio
- Acceptance criteria: `toasters mcp-server` starts and responds to MCP Initialize and ListTools requests; tool calls write to SQLite

**2.2.5 — Wire MCP server into gateway spawn path**
- Modify: `internal/gateway/gateway.go`
- When spawning a Claude CLI subprocess, generate a temporary MCP config JSON file that points to `toasters mcp-server` as a stdio server
- Pass `--mcp-config <path>` to the Claude CLI command
- The MCP config JSON format (Claude's expected format):
  ```json
  {
    "mcpServers": {
      "toasters": {
        "command": "<path-to-toasters-binary>",
        "args": ["mcp-server", "--db", "<database-path>"],
        "env": {}
      }
    }
  }
  ```
- Need to resolve the path to the current toasters binary (`os.Executable()`) and the database path from config
- Clean up the temporary config file when the slot completes
- Risk: This is the most complex step — Claude CLI's MCP config format needs to be verified. If the format is wrong or Claude doesn't support `--mcp-config`, this step may need revision.
- Risk: The toasters binary must be accessible from the subprocess. If running in a container or unusual environment, this could fail.
- Acceptance criteria: Claude CLI subprocesses can discover and call the progress tools; data appears in SQLite

**2.2.6 — Write comprehensive tests**
- Files: `internal/progress/handlers_test.go`, `internal/progress/server_test.go`, `internal/runtime/tools_test.go` (additions)
- Test strategy:
  - **Handler tests**: Use a real SQLite store (via `db.Open(t.TempDir() + "/test.db")`). Test each handler: correct data written, error cases (missing job, invalid status), `QueryJobContext` returns complete data.
  - **Server tests**: Start the MCP server in-process, send Initialize and ListTools requests, verify tool list. Send CallTool requests, verify SQLite writes.
  - **Runtime integration tests**: Spawn a session with a mock provider that calls `report_progress`, verify the progress report appears in SQLite.
- Target: ≥80% coverage on `internal/progress/`
- All tests must pass with `-race`

**2.2.7 — Code review**
- Focus areas:
  - Handler correctness — verify all SQLite writes use the correct fields
  - Error handling — verify errors are wrapped with context
  - `QueryJobContext` response format — verify it's useful for agents
  - MCP server tool registration — verify parameter schemas match handler expectations
  - Gateway integration — verify temp file cleanup, binary path resolution
  - Thread safety — handlers may be called concurrently from multiple agent sessions

---

## PR 3: Real-Time TUI Progress Display

**Branch:** `feat/tui-progress`
**Depends on:** PR 2 (progress tools writing to SQLite)

### Steps

| # | Step | Agent | Status |
|---|------|-------|--------|
| 2.3.1 | Define progress polling types and messages | builder | ✅ Done |
| 2.3.2 | Implement SQLite polling command | builder | ✅ Done |
| 2.3.3 | Add progress state to TUI Model | builder | ✅ Done |
| 2.3.4 | Render task status in the left panel | builder | ✅ Done |
| 2.3.5 | Render blocker alerts | builder | ✅ Done |
| 2.3.6 | Render token usage and cost per session | builder | ✅ Done |
| 2.3.7 | Write tests | test-writer | ✅ Done |
| 2.3.8 | Code review | code-reviewer | ✅ Done |

### Details

**2.3.1 — Define progress polling types and messages**
- Modify: `internal/tui/messages.go`
- New message types:
  - `ProgressPollMsg` — carries the latest progress data from SQLite, fired every 500ms
    ```go
    type ProgressPollMsg struct {
        Jobs     []*db.Job
        Tasks    map[string][]*db.Task           // keyed by job ID
        Progress map[string][]*db.ProgressReport  // keyed by job ID, latest N per job
        Sessions []*db.AgentSession               // active sessions with token counts
    }
    ```
  - `progressPollTickMsg` — internal tick message that triggers the next poll
- New command: `progressPollCmd(store db.Store) tea.Cmd` — runs the SQLite queries in a goroutine, returns `ProgressPollMsg`
- New command: `scheduleProgressPoll() tea.Cmd` — fires `progressPollTickMsg` after 500ms
- Acceptance criteria: The polling loop runs every 500ms without blocking the TUI

**2.3.2 — Implement SQLite polling command**
- New file: `internal/tui/progress_poll.go`
- `progressPollCmd(store db.Store) tea.Cmd` implementation:
  - Queries `store.ListJobs(ctx, db.JobFilter{Status: &activeStatus})` for active jobs
  - For each active job: `store.ListTasksForJob(ctx, jobID)` and `store.GetRecentProgress(ctx, jobID, 5)`
  - Queries `store.GetActiveSessions(ctx)` for token usage
  - Assembles into `ProgressPollMsg` and returns
  - Uses `context.WithTimeout(ctx, 200ms)` to prevent slow queries from blocking the poll cycle
- The polling is only active when `store != nil` — graceful degradation
- Acceptance criteria: Polling returns fresh data from SQLite; timeout prevents blocking

**2.3.3 — Add progress state to TUI Model**
- Modify: `internal/tui/model.go`
- Add fields to `Model`:
  ```go
  progressTasks    map[string][]*db.Task           // keyed by job ID
  progressReports  map[string][]*db.ProgressReport  // keyed by job ID
  activeSessions   []*db.AgentSession
  progressPolling  bool  // true when polling is active
  ```
- In `Model.Init()`: if `m.store != nil`, start the polling loop by returning `scheduleProgressPoll()` as a batch command
- In `Model.Update()`:
  - Handle `progressPollTickMsg`: dispatch `progressPollCmd(m.store)` and return
  - Handle `ProgressPollMsg`: update `m.progressTasks`, `m.progressReports`, `m.activeSessions`; schedule next poll via `scheduleProgressPoll()`
- Acceptance criteria: Model state is updated every 500ms with fresh progress data

**2.3.4 — Render task status in the left panel**
- Modify: `internal/tui/panels.go` (the left panel rendering)
- Currently the left panel shows jobs from the markdown file system. Enhance it to show task status from SQLite when available:
  - For each job, if `m.progressTasks[jobID]` has entries, show task list with status indicators:
    - `○` pending (dim)
    - `◉` in_progress (cyan, animated)
    - `✓` completed (green)
    - `✗` failed (red)
    - `⊘` blocked (yellow)
    - `—` cancelled (dim)
  - Show latest progress message per task (truncated to fit panel width)
  - Show job-level summary: "3/7 tasks complete" or "BLOCKED" if any task is blocked
- Acceptance criteria: Task statuses are visible in the left panel; statuses update within 1 second of an agent reporting progress

**2.3.5 — Render blocker alerts**
- Modify: `internal/tui/panels.go` or `internal/tui/view.go`
- When any task has status "blocked", show a highlighted alert:
  - In the left panel: the blocked task row is rendered in yellow/orange with a `⚠` prefix
  - In the status bar (bottom): show "⚠ BLOCKER: <message>" in yellow when there are active blockers
  - Optionally: show a toast notification when a new blocker appears (compare previous poll state to current)
- Acceptance criteria: Blockers are visually distinct and immediately noticeable

**2.3.6 — Render token usage and cost per session**
- Modify: `internal/tui/panels.go` (the sidebar/right panel)
- In the session stats area or agents panel, show per-session token usage:
  - For each active session in `m.activeSessions`: show `tokens: {in}↓ {out}↑`
  - Show aggregate token usage across all active sessions
  - If `cost_usd` is available, show estimated cost
- Also update the existing `SessionStats` display to include aggregate runtime session tokens (not just the operator's tokens)
- Acceptance criteria: Token usage per session is visible; aggregate usage is shown

**2.3.7 — Write tests**
- Files: `internal/tui/progress_poll_test.go`
- Test strategy:
  - Test `progressPollCmd` with a real SQLite store: insert test data, verify the returned `ProgressPollMsg` contains correct jobs, tasks, progress reports
  - Test that the polling loop schedules correctly (mock the tick)
  - Test rendering: verify task status indicators appear in rendered output (use lipgloss string matching or snapshot testing)
- Target: ≥70% coverage on new TUI progress code
- All tests must pass with `-race`

**2.3.8 — Code review**
- Focus areas:
  - Polling performance — verify queries are efficient and don't cause TUI jank
  - Memory — verify old progress data is replaced, not accumulated
  - Rendering — verify panel layout doesn't break at various terminal sizes
  - Graceful degradation — verify TUI works fine when `store` is nil (no polling, no progress display)

---

## Key Design Decisions

### 1. MCPCaller interface for loose coupling
The `MCPCaller` interface (`Call(ctx, name, args) (string, error)`) is defined in `internal/llm/tools/` and `internal/runtime/` rather than importing `internal/mcp/` directly. This keeps the dependency graph clean: `llm/tools` and `runtime` don't depend on `mcp`, only on a small interface. The concrete `mcp.Manager` satisfies this interface.

### 2. Progress tools are native, not MCP-namespaced, for in-process agents
In-process agents get `report_progress` (not `toasters__report_progress`). The `toasters__` prefix is only used when exposing tools via the actual MCP protocol to external processes. This is simpler for agents and avoids unnecessary indirection.

### 3. `internal/progress/` as a separate package from `internal/mcp/`
The progress handlers are protocol-agnostic SQLite operations. They're used by both in-process agents (direct function calls) and external agents (via MCP server). Putting them in `internal/mcp/` would create a false dependency — in-process agents don't use MCP at all.

### 4. Polling over event-driven for v1 TUI updates
SQLite polling at 500ms is simpler and sufficient for v1. The alternative (emitting Bubble Tea messages from progress handlers) would require threading a `tea.Program` reference through the handler chain, which couples the progress system to the TUI. Polling is decoupled and works regardless of how progress is reported.

### 5. `toasters mcp-server` subcommand over separate binary
Adding a Cobra subcommand is cleaner than building a separate binary. Claude CLI launches `toasters mcp-server` as a subprocess, which opens the same SQLite database and exposes progress tools via stdio MCP. This reuses the existing config and database path resolution.

### 6. CompositeTools wrapper over modifying CoreTools directly
Rather than adding MCP dispatch logic directly into `CoreTools.Execute()`, a `CompositeTools` wrapper keeps concerns separated. `CoreTools` remains focused on the 8 core tools + progress tools. `CompositeTools` adds MCP routing on top. This is easier to test and reason about.

---

## Risks

| Risk | Severity | Mitigation |
|------|----------|-----------|
| Claude CLI `--mcp-config` format | High | Verify exact JSON format manually before building gateway integration. Fallback: skip gateway MCP and focus on in-process agents. |
| mcp-go API instability | Medium | Pin to specific version in go.mod. Read mcp-go source before building — don't rely solely on docs. |
| mcp-go stdio subprocess management | Medium | Test thoroughly with a real MCP server (e.g., GitHub). Verify `Close()` terminates the subprocess. Add a timeout to `Close()`. |
| Token budget from too many MCP tools | Medium | `enabled_tools` whitelist per server. Document that users should whitelist only the tools they need. |
| `toasters mcp-server` binary path | Medium | Use `os.Executable()` to get current binary path. Document that binary must be installed for Claude CLI integration. |
| Credentials in MCP server config | Medium | Support `${ENV_VAR}` expansion (same as provider config). Never log expanded values. Document `chmod 600` for config file. |
| SQLite polling performance | Low | WAL mode + indexed queries. 500ms interval is conservative. Add `context.WithTimeout(200ms)` to prevent slow queries from blocking. |
| TUI jank from progress rendering | Low | Progress data is small (a few rows per job). Rendering is O(tasks) which is bounded. Test with 20+ tasks to verify. |

---

## Review Checkpoints

| When | Who | What |
|------|-----|------|
| After 2.1.3 (Manager implementation) | concurrency-reviewer | Verify mutex usage, goroutine lifecycle, mcp-go client management |
| After 2.1.5 (operator wiring) | code-reviewer | Verify tool dispatch ordering (static > MCP), interface design |
| After 2.1.8 | security-auditor | Credential handling, subprocess lifecycle, input validation |
| After 2.2.2 (handlers) | code-reviewer | Verify SQLite writes are correct, error handling |
| After 2.2.4 (MCP server) | security-auditor | Verify the MCP server doesn't expose unintended capabilities |
| After 2.3.4 (TUI rendering) | code-reviewer | Verify rendering at various terminal sizes, no panics on nil data |
| After all PRs merged | code-reviewer | Integration review — verify end-to-end flow works |

---

## Out of Scope

- **OpenAPI-to-MCP bridges** — Phase 3 (deliverable 3.2)
- **Team templates and workflows** — Phase 3 (deliverable 3.1)
- **Wave 2 tech debt items** — Can be done opportunistically but are not deliverables
- **Event-driven TUI updates** (replacing polling) — Future optimization
- **MCP server HTTP transport** — stdio is sufficient for v1; HTTP can be added later if needed for non-Claude external agents
- **Provider selection per-agent in TUI** — Future feature
- **Cost estimation** — Token usage is tracked; cost calculation (price × tokens) can be added later when pricing data is available
- **MCP resource/prompt support** — Only MCP tools are consumed in Phase 2; MCP resources and prompts are future scope

---

## Delivery Sequence

```
Week 1:
  PR 1 (MCP client) ──────────────────────────────►  review + merge
  PR 2 (MCP server) ──────────────────────────────►  review + merge
       (parallel — different packages, no conflicts)

Week 2:
  PR 3 (TUI progress) ────────────────►  review + merge
       (depends on PR 2)
  Integration testing + end-to-end verification
```

---

## Phase 2 Exit Criteria

All criteria met (2026-02-25):

1. Configure a GitHub MCP server → operator sees `github__*` tools ✅
2. Operator uses `github__create_issue` → tool call routed to GitHub MCP server ✅
3. Agents report progress via `report_progress` / `update_task_status` → data in SQLite ✅
4. TUI shows real-time task status updates → polling every 500ms ✅
5. Blockers are visually highlighted → left panel + status bar ✅
6. Token usage is tracked → per-session display in sidebar ✅

---

## Post-Delivery Fixes

The following bug fixes were applied after the three PRs merged into `phase-2`:

| Fix | Description |
|-----|-------------|
| `fix: wire subagent TUI notifications and live session token counts` | Subagent sessions were not emitting TUI notification events; live token counts were not updating for child sessions. |
| `fix: filter display-only entries from messagesFromEntries` | Display-only chat entries (e.g. tool result separators) were incorrectly included when reconstructing the message history for the LLM, causing malformed conversation context. |
| `fix: propagate provider and model to child SpawnOpts in spawn_agent tool` | `CoreTools` was not passing `ProviderName` or `Model` to child `SpawnOpts`, causing `spawn_agent` to silently fail when the runtime had no default provider configured. |
| `fix: use job workspace dir instead of cwd for coordinator spawns` | Removed the `repoRoot = os.Getwd()` concept entirely. Coordinators now start in the job workspace directory (`jobDir`). Toasters is workspace-centric, not cwd-centric. |
| `fix: enforce max spawn depth of 1` | Added depth tracking to `SpawnOpts`. Coordinators (depth 0) may spawn workers (depth 1); workers may not spawn further agents. Attempts to exceed depth 1 return an error. |
| `fix: enforce spawn_agent tool filter` | `params.Tools` is now wired to `SpawnOpts.Tools`. A `filteredToolExecutor` wraps the child session's tool executor and enforces the allowlist at both `Definitions()` and `Execute()` time. |
| `feat: add MCP TUI visibility and fix context window bugs` | Server status tracking in `mcp.Manager`, `/mcp` slash command with full-screen modal, MCP summary in sidebar, startup toast notifications, MCP tool call annotations in chat, smart truncation of MCP tool results (JSON-aware, 16KB default), context bar fix (`PromptTokens =` instead of `+=`), "Prompt ctx" label. 33+ TUI tests, 25 truncation tests, 7 context bar tests. |
| `feat: add JSON slimming pass for MCP tool results` | Generic JSON slimming applied before truncation — strips nulls, `*_url` fields, URI templates, API URLs, `node_id`, `gravatar_id`, PGP/base64 blobs. Preserves `html_url` on primary resource objects. 32 slim tests including realistic GitHub issue response test. |
