# Pre-Phase 4 Architecture Review

**Date**: 2026-02-27
**Scope**: Full codebase (63K lines, 144 Go files, 19 test packages)
**Reviewers**: code-reviewer, security-auditor, concurrency-reviewer, explore (x5)
**Purpose**: Identify architectural issues blocking effective iteration and the client/server split

---

## Table of Contents

1. [Executive Summary](#1-executive-summary)
2. [Architecture Overview & Assessment](#2-architecture-overview--assessment)
3. [The Client/Server Split — Readiness Assessment](#3-the-clientserver-split--readiness-assessment)
4. [Core Event Loop & Interaction Model](#4-core-event-loop--interaction-model)
5. [Dead Code & Legacy Systems](#5-dead-code--legacy-systems)
6. [Structural Design Issues](#6-structural-design-issues)
7. [Security Findings](#7-security-findings)
8. [Concurrency Findings](#8-concurrency-findings)
9. [Code Quality & Patterns](#9-code-quality--patterns)
10. [Consolidated Findings Registry](#10-consolidated-findings-registry)
11. [Recommended Execution Order](#11-recommended-execution-order)

---

## 1. Executive Summary

### Overall Health: B+

The codebase demonstrates strong engineering fundamentals: clean unidirectional data flow, interface-based decoupling, consistent error handling, parameterized SQL, and deliberate security controls. The Wave 1-3 tech debt work was effective. All tests pass, `go vet` is clean, and the project conventions are well-documented.

However, the rapid prototyping phase has left several issues that will compound as the project grows:

### Critical Blockers for Phase 4

| # | Issue | Impact | Lines Affected |
|---|-------|--------|---------------|
| 1 | **~4,600 lines of dead code** (legacy `llm` package family) | Confusion, false coverage metrics, maintenance burden | 4,600 |
| 2 | **~1,100 lines of business logic in the TUI** | Blocks client/server split entirely | 1,100 |
| 3 | **Dual agent/team type systems** (`agents.*` vs `db.*`) | Double file loading, conceptual confusion, two watchers | ~800 |
| 4 | **Two parallel tool systems** (operator vs agent) with no shared abstraction | Duplicated SSRF, duplicated patterns | ~400 |
| 5 | **No conversation persistence** | Chat history lost on restart; server can't serve multiple clients | 0 (missing feature) |
| 6 | **Command injection in `setup_workspace`** | CRITICAL security vulnerability | 1 function |

### What's Working Well

- **Operator ↔ TUI coupling is already message-based** — the cleanest abstraction in the codebase
- **Runtime session events are already delivered via messages** — no polling of runtime state
- **Interface-based decoupling** (`ToolExecutor`, `Provider`, `Store`, `TeamLeadSpawner`) enables testing and swapping
- **Security controls** are above-average: path traversal protection, SSRF mitigation, spawn depth limiting, SQL parameterization
- **Event-driven operator with mechanical/LLM dispatch split** keeps costs and latency low

---

## 2. Architecture Overview & Assessment

### Current Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                        cmd/root.go                          │
│                    (wiring & startup)                        │
├─────────┬──────────┬──────────┬──────────┬─────────────────┤
│   TUI   │ Operator │ Runtime  │  Loader  │    Provider     │
│ (thick  │ (event   │ (session │ (file→DB │   Registry      │
│ client) │  loop)   │  mgmt)   │  sync)   │                 │
├─────────┴──────────┴──────────┴──────────┴─────────────────┤
│                      SQLite Store                           │
├─────────────────────────────────────────────────────────────┤
│              MCP Manager  │  Compose  │  agentfmt           │
└─────────────────────────────────────────────────────────────┘
```

### Data Flow

```
User Input → TUI → operator.Send(Event) → Operator Event Loop
                                              ↓
                                    LLM Conversation Loop
                                              ↓
                                    Tool Calls (consult_agent, assign_task, ...)
                                              ↓
                                    Runtime.SpawnTeamLead() / SpawnAndWait()
                                              ↓
                                    Agent Sessions (goroutines)
                                              ↓
                              Callbacks → p.Send(tea.Msg) → TUI Update
```

### Dependency Graph (Acyclic)

```
cmd → tui, operator, runtime, compose, loader, config, bootstrap, agents, mcp, provider, db
tui → operator, runtime, agents, db, provider, mcp, llm/tools
operator → runtime, compose, db, provider
runtime → compose, db, provider
compose → db
loader → db, agentfmt
mcp → config, provider, runtime (for ToolDef conversion)
```

No circular import dependencies exist. The `TeamLeadSpawner` interface in `runtime` specifically avoids `runtime → operator` cycles.

---

## 3. The Client/Server Split — Readiness Assessment

### What's Already Abstracted (Ready)

| Component | Current State | Server Readiness |
|-----------|--------------|-----------------|
| Operator ↔ TUI | Message-based via `Send()` + callbacks | Maps directly to WebSocket/SSE |
| Runtime sessions | Event-based via `Subscribe()` channels | Maps to server-push events |
| SQLite Store | Clean interface, 30+ methods | Becomes the server's data layer |
| Provider Registry | Interface-based, multi-provider | Stays server-side |
| Composition | Pure function: agent → composed agent | Stays server-side |
| MCP Manager | Self-contained, `MCPCaller` interface | Stays server-side |

### What's Blocking the Split

#### Block 1: Business Logic in the TUI (~1,100 lines)

The TUI contains substantial domain logic that must move server-side:

| Logic | Location | Lines | What It Does |
|-------|----------|-------|-------------|
| Team promotion | `teams_modal.go` | ~400 | File I/O, directory creation, symlink removal, agent copying |
| Agent/skill CRUD | `agents_modal.go`, `skills_modal.go` | ~200 | Template writing, file deduplication, skill attachment |
| LLM generation | `llm_generate.go` | ~230 | Direct LLM calls to generate definitions |
| Team generation handler | `model.go` | ~100 | Directory creation, file writing, agent copying from DB |
| Coordinator auto-detection | `teams_modal.go` | ~60 | Direct LLM call |
| Tool call orchestration | `prompt.go` | ~100 | Intercepts assign_team/ask_user, builds confirmations |

#### Block 2: Direct DB Access from TUI (7 patterns)

| Access Point | File | Operation |
|-------------|------|-----------|
| Progress polling | `progress_poll.go` | 5 queries every 500ms |
| Job management | `jobs_modal.go` | List, cancel, task/progress queries |
| Skills listing | `skills_modal.go` | `ListSkills()` |
| Agents listing | `agents_modal.go` | `ListAgents()`, `ListSkills()` |
| Teams listing | `teams_modal.go` | `ListAgents()` |
| Task status check | `model.go` | `GetTask()` on session completion |
| Agent copying | `model.go` | `ListAgents()` for team generation |

#### Block 3: Direct LLM Calls from TUI (7 call sites)

| Call Site | File | Purpose |
|----------|------|---------|
| `startStream()` | `streaming.go` | Legacy direct-stream path |
| `sendAnthropicMessage()` | `streaming.go` | `/anthropic` command |
| `fetchModels()` | `streaming.go` | Sidebar model listing |
| `maybeAutoDetectCoordinator()` | `teams_modal.go` | Auto-detect team lead |
| `generateSkillCmd()` | `llm_generate.go` | Generate skill definition |
| `generateAgentCmd()` | `llm_generate.go` | Generate agent definition |
| `generateTeamCmd()` | `llm_generate.go` | Generate team definition |

#### Block 4: No Conversation Persistence

Chat history (`chat.entries`) is purely in-memory. A server-side model needs persistent conversations so that:
- Multiple clients can view the same conversation
- Conversations survive client disconnects
- History is available across sessions

#### Block 5: Filesystem Operations in TUI

The TUI directly creates/deletes files and directories for team/agent/skill management. In a client/server model, all filesystem operations must be server-side API calls.

### Estimated Extraction Effort

| Component | Lines to Extract | New Server Code | Difficulty |
|-----------|-----------------|-----------------|------------|
| Team management service | ~400 | ~300 | Medium |
| Agent/skill management service | ~200 | ~150 | Low |
| LLM generation service | ~230 | ~200 | Low |
| DB access → API calls | ~150 call sites | ~400 (API routes) | Medium |
| Event streaming protocol | — | ~500 (WebSocket/SSE) | High |
| Conversation persistence | — | ~300 (schema + CRUD) | Medium |
| Operator integration | ~100 | ~200 | Medium |
| **Total** | **~1,100** | **~2,000-3,000** | |

### Key Architecture Decisions for the Split

1. **Streaming protocol**: The operator's callback model (`onText`, `onEvent`, `onTurnDone`) maps cleanly to Server-Sent Events or WebSocket messages.
2. **Authentication**: Currently none (local-only). A server needs auth for API key management and multi-user isolation.
3. **File editing**: The `openInEditor()` pattern (suspending TUI to launch `$EDITOR`) is inherently local. Needs a different approach for remote clients.
4. **State ownership**: Currently the TUI owns conversation state. The server must own it instead.

---

## 4. Core Event Loop & Interaction Model

### How It Works

The operator runs a single goroutine reading from a buffered channel (256 slots). Events are dispatched via type-switch:

- **Mechanical events** (no LLM call): `TaskStarted`, `ProgressUpdate`, `JobComplete`
- **LLM-routed events**: `UserMessage`, `TaskFailed`, `BlockerReported`, `NewTaskRequest`, `UserResponse`
- **Conditional**: `TaskCompleted` — mechanical if `HasNextTask`, LLM-routed if recommendations or no next task

The LLM conversation is a `[]provider.Message` with a sliding window cap at 200 messages.

### Assessment: Does It Make Sense?

**Yes, the core model is sound.** The mechanical/LLM dispatch split is the right design — it keeps costs low and latency minimal for routine events. The single-threaded event loop simplifies reasoning about conversation state.

### Issues with the Current Model

#### ARCH-1: Operator blocks during tool execution

When the operator LLM calls `consult_agent`, the tool execution blocks the event loop goroutine. During this time (which can be minutes for complex planning), no other events are processed. User messages, task completions, and blockers all queue up.

**Impact**: The operator can't react to urgent events (like blockers from other teams) while waiting for a system agent to finish.

**Recommendation**: Consider running tool executions in a separate goroutine with results fed back via the event channel. This is a significant refactor but would dramatically improve responsiveness.

#### ARCH-2: Self-send deadlock potential

The `assignNextTask` → `SystemTools.assignTask` → `trySendEvent(EventTaskStarted)` path sends events from the event loop goroutine back to its own channel. The 256-slot buffer makes overflow "practically impossible," but a pathological workload (256+ tasks completing in rapid succession) could deadlock.

**Recommendation**: Handle `EventTaskStarted` inline (same pattern as `checkJobComplete` already uses for `EventJobComplete`).

#### ARCH-3: Conversation window truncation is naive

`maxMessages=200` with `messages[len-200:]` truncation could split a tool-call/result pair, corrupting the LLM conversation. The LLM would see a tool result without the corresponding tool call, or vice versa.

**Recommendation**: Truncate at tool-call/result boundaries. Find the earliest complete exchange (user message or tool-call+result pair) and truncate before it.

#### ARCH-4: No backpressure from operator to TUI

`onText` fires for every streamed token with no batching. High-throughput models could flood the Bubble Tea message queue.

**Recommendation**: Batch text chunks (e.g., accumulate for 16ms before sending) or use a ring buffer.

#### ARCH-5: Legacy dual-path complexity

The TUI maintains a complete legacy direct-stream path (`StreamChunkMsg`, `StreamDoneMsg`, `ToolCallMsg`, `ToolResultMsg`, `startStream`, `waitForChunk`, `executeToolsCmd`) alongside the operator path. This doubles the streaming/tool-handling logic surface.

**Recommendation**: Remove the legacy path. If `operator == nil`, show an error rather than falling back to a completely different interaction model.

---

## 5. Dead Code & Legacy Systems

### DEAD-1: Legacy LLM Package Family (~4,600 lines)

**Severity**: BLOCKING — Must be removed before Phase 4

The codebase has two complete, parallel provider systems:

| | Legacy (`llm.Provider`) | Current (`provider.Provider`) |
|---|---|---|
| Interface | `internal/llm/provider.go` | `internal/provider/provider.go` |
| OpenAI | `internal/llm/client/client.go` | `internal/provider/openai.go` |
| Anthropic | `internal/anthropic/client.go` | `internal/provider/anthropic.go` |
| Types | `internal/llm/types.go` | `internal/provider/provider.go` |

**Nothing in the production code path uses `llm.Provider`.** The entire runtime uses `provider.Provider`.

The only actively-used code in the legacy packages is `anthropic.ReadKeychainAccessToken()` (~200 lines of keychain/OAuth helpers), called by `provider/anthropic.go`.

**Dead code inventory:**

| File | Lines | Status |
|------|-------|--------|
| `internal/llm/types.go` | 110 | Dead |
| `internal/llm/provider.go` | 23 | Dead |
| `internal/llm/doc.go` | 4 | Dead |
| `internal/llm/client/client.go` | 408 | Dead |
| `internal/llm/client/client_test.go` | 1,455 | Dead |
| `internal/llm/client/doc.go` | 3 | Dead |
| `internal/anthropic/client.go` | ~760 | Dead (except ~200 lines of keychain helpers) |
| `internal/anthropic/client_test.go` | ~1,800 | Mostly dead |
| **Total** | **~4,600** | |

**Fix**: Extract `ReadKeychainAccessToken()` and supporting functions into `internal/anthropic/keychain.go`. Delete everything else.

### DEAD-2: Dual Agent/Team Type Systems (~800 lines of confusion)

**Severity**: HIGH — Creates conceptual confusion and double file loading

The `internal/agents` package provides its own `Agent`, `Team`, `Registry` types and its own `Discover()`, `DiscoverTeams()`, `Watch()`, `WatchRecursive()` functions. Meanwhile, `internal/loader` + `internal/db` provide a parallel system with `db.Agent`, `db.Team`, and `loader.Load()`.

Both systems load the same `.md` files. Both have file watchers. `cmd/root.go` uses **both paths**:
- `agents.DiscoverTeams()` → feeds `llm/tools.ToolExecutor` and TUI
- `loader.Load()` → feeds `db.Store` → feeds `compose.Composer` and TUI modals

**Impact**: Team data is loaded twice on startup. Two file watchers run simultaneously. The `agents.Agent` type has fields (`Background`, `Isolation`) that `db.Agent` doesn't, and vice versa.

**Fix**: Consolidate onto the `loader` + `db` pipeline. The `agents` package should be reduced to just the types needed by `llm/tools` (or those types should move to `db`).

### DEAD-3: `llm/tools` Package Misplacement

**Severity**: MEDIUM — Confusing package path

`internal/llm/tools` has nothing to do with `internal/llm` types. It imports `provider.ToolCall`, not `llm.ToolCall`. After DEAD-1 cleanup, the `internal/llm` directory should be empty except for `tools/`.

**Fix**: Move to `internal/operator/dispatch` or `internal/optool` after dead code cleanup.

---

## 6. Structural Design Issues

### STRUCT-1: Two Parallel Tool Systems

**Severity**: HIGH

The codebase has two completely independent tool execution systems:

| | Operator Tools | Agent Tools |
|---|---|---|
| Package | `internal/llm/tools` | `internal/runtime` |
| Type | `ToolExecutor` (struct) | `ToolExecutor` (interface) |
| Dispatch | `map[string]toolHandler` | Switch statement in `CoreTools` |
| Args | `provider.ToolCall` | `(name string, args json.RawMessage)` |
| SSRF | Own copy of `privateNetworks` + `isPrivateIP` | Own copy of `privateNetworks` + `isPrivateIP` |

These share no code and use different type signatures. The SSRF protection is duplicated — a security-critical code path that exists in two places.

**Fix**: Extract shared infrastructure (`ssrf` package for HTTP clients, shared `ToolDef` type). Consider whether the operator tools should use the same `ToolExecutor` interface.

### STRUCT-2: `ToolDef` Type Duplication

**Severity**: MEDIUM

`progress.ToolDef` is an explicit copy of `runtime.ToolDef` to avoid an import cycle. The comment says "Keep in sync with runtime.ToolDef" — but there's no compiler enforcement.

**Fix**: Move `ToolDef` to a shared leaf package (e.g., `internal/tooldef`) that both `runtime` and `progress` import.

### STRUCT-3: `ProviderConfig` Type Duplication

**Severity**: LOW

`config.ProviderConfig` and `provider.ProviderConfig` are structurally identical but separate types. `cmd/root.go` manually copies fields between them.

**Fix**: Have `provider.NewFromConfig` accept `config.ProviderConfig` directly.

### STRUCT-4: `MCPCaller` Interface Duplication

**Severity**: LOW

Defined in both `internal/mcp` and `internal/runtime` with identical signatures. The `runtime` copy exists to avoid importing `mcp`. This is idiomatic Go (consumer-side interface), but since `mcp` already imports `runtime` (for `ToolDef` conversion), the `mcp.MCPCaller` definition is redundant.

### STRUCT-5: `ToolExecutor` Name Collision

**Severity**: LOW

Both `runtime.ToolExecutor` (interface) and `llm/tools.ToolExecutor` (struct) share the same name. Confusing when reading code that imports both.

**Fix**: Rename `llm/tools.ToolExecutor` to `OperatorToolDispatcher`.

### STRUCT-6: `llm/tools.ToolExecutor` Partial Construction

**Severity**: LOW

Six public fields are set after `NewToolExecutor()` returns in `cmd/root.go`. This is a construction anti-pattern.

**Fix**: Use an options struct or add fields to the constructor.

### STRUCT-7: SpawnTeamLead Coupling Risk

**Severity**: LOW

`SpawnTeamLead` creates a throwaway `CoreTools` instance just to call `Definitions()` and resolve tool names to `ToolDef` values. This tmp instance is constructed independently from the `CoreTools` that `SpawnAgent` will build for the actual session. If these construction paths diverge, tool definitions could mismatch.

**Fix**: Expose a `ToolDefsByName()` helper as the TODO in the code suggests.

---

## 7. Security Findings

### SEC-CRITICAL-1: Command Injection via `setup_workspace`

**Location**: `internal/operator/workspace_tools.go:71`
**Severity**: CRITICAL

The `setup_workspace` tool passes LLM-controlled `repo.URL` and `repo.Name` directly to `exec.CommandContext("git", "clone", repo.URL, name)`. Attack vectors:

1. **Flag injection**: URL like `--upload-pack=malicious_command` interpreted as git flag
2. **`ext::` protocol**: `ext::sh -c 'command'` executes arbitrary shell commands
3. **Name injection**: Name like `--config=core.sshCommand=malicious` interpreted as git flag

**Fix**:
```go
// Validate URL scheme
u, err := url.Parse(repo.URL)
if err != nil || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh" && u.Scheme != "git") {
    return "", fmt.Errorf("invalid git URL scheme")
}
// Reject flag injection
if strings.HasPrefix(repo.URL, "-") || strings.HasPrefix(name, "-") {
    return "", fmt.Errorf("invalid argument: must not start with '-'")
}
// Validate name: alphanumeric only
if repo.Name != "" && !regexp.MustCompile(`^[a-zA-Z0-9._-]+$`).MatchString(repo.Name) {
    return "", fmt.Errorf("invalid repo name")
}
// Use "--" to separate flags from positional arguments
cmd := exec.CommandContext(cloneCtx, "git", "clone", "--", repo.URL, name)
```

### SEC-HIGH-1: Shell Tool Has No Sandboxing

**Location**: `internal/runtime/tools.go:674-710`
**Severity**: HIGH (inherent design tradeoff)

The `shell` tool executes arbitrary commands via `/bin/sh -c` with full user privileges. `cmd.Dir` only sets CWD — it does not prevent accessing files outside the workspace.

**Recommendation**: Document the threat model clearly. Consider command allowlist/denylist per agent. Consider container-based sandboxing for production use.

### SEC-HIGH-2: Incomplete `.gitignore`

**Location**: `.gitignore`
**Severity**: HIGH

Only contains `toasters` (the binary). Missing: `*.db`, `*.log`, `.env`, `config.yaml`, `coverage.out`, `requests.log`.

**Fix**: Expand `.gitignore` immediately.

### SEC-HIGH-3: API Keys in Plaintext Config

**Location**: `internal/config/config.go`
**Severity**: HIGH

`ProviderConfig.APIKey` can contain plaintext API keys in `config.yaml`. No enforcement that `${ENV_VAR}` syntax must be used.

**Fix**: Warn at startup if API keys don't use `${...}` syntax. Set `0600` permissions on config file.

### SEC-MEDIUM-1: `editFile` Has No File Size Limit

**Location**: `internal/runtime/tools.go:463-498`
**Severity**: MEDIUM

`os.ReadFile(resolved)` with no size limit. An LLM directed to edit a multi-GB file causes OOM.

**Fix**: Add `os.Stat` check with 10MB limit before `os.ReadFile`.

### SEC-MEDIUM-2: `writeFile` Has No Content Size Limit

**Location**: `internal/runtime/tools.go:436-461`
**Severity**: MEDIUM

No size limit on `params.Content`. Partially mitigated by LLM token limits.

**Fix**: Add 50MB limit on content length.

### SEC-MEDIUM-3: Token Refresh Race Condition

**Location**: `internal/anthropic/client.go:678-719`
**Severity**: MEDIUM

Multiple goroutines can simultaneously refresh an expired token, causing race on Keychain writes and potential refresh token rotation issues.

**Fix**: Add `sync.Mutex` around `readKeychainCredentials`.

### SEC-MEDIUM-4: `glob` Pattern Traversal

**Location**: `internal/runtime/tools.go:500-545`
**Severity**: MEDIUM

Glob patterns with `..` can discover file names outside the workspace (though `readFile` prevents reading them).

**Fix**: Validate resolved base directory is within workspace.

### SEC-MEDIUM-5: MCP Subprocess Trust

**Location**: `internal/mcp/manager.go:344-359`
**Severity**: MEDIUM

MCP stdio transport executes commands from config. Environment variables like `LD_PRELOAD` could be injected via the `Env` map.

**Fix**: Filter dangerous environment variables. Set `0600` on config file.

---

## 8. Concurrency Findings

### CONC-1: `Session.messages` Mixed Synchronization

**Location**: `internal/runtime/session.go`
**Severity**: MEDIUM

`s.messages` is read/written in `Run()` without locks, but `FinalText()` and `InitialMessage()` acquire `s.mu`. Safety relies on call-site discipline (only call after `<-Done()`), not the type system.

**Fix**: Document the contract explicitly or make synchronization consistent.

### CONC-2: Operator Self-Send Deadlock Potential

**Location**: `internal/operator/operator.go`, `system_tools.go`
**Severity**: MEDIUM

See ARCH-2 above. The `assignNextTask` → `trySendEvent` path sends events from the event loop goroutine back to its own channel.

**Fix**: Handle `EventTaskStarted` inline.

### CONC-3: MCP Manager Close() Race

**Location**: `internal/mcp/manager.go`
**Severity**: MEDIUM

In-flight `Call()` can use a closed client after `Close()` runs. The code acknowledges this in comments. Only impacts shutdown.

**Fix**: Acceptable for current use case. Add `recover()` wrapper around `CallTool` if the MCP client panics on use-after-close.

### CONC-4: Runtime.Shutdown() Busy-Wait

**Location**: `internal/runtime/runtime.go:254-277`
**Severity**: MEDIUM

Polls `len(r.sessions)` with 10ms sleep. No timeout — a hung session blocks exit forever.

**Fix**: Use `sync.WaitGroup` with a 10-second timeout:
```go
done := make(chan struct{})
go func() { r.wg.Wait(); close(done) }()
select {
case <-done:
case <-time.After(10 * time.Second):
    slog.Warn("shutdown timed out")
}
```

### CONC-5: Operator Tool Execution Blocks Event Loop

**Location**: `internal/operator/operator.go`
**Severity**: MEDIUM

See ARCH-1 above. `consult_agent` blocks the event loop for the duration of the system agent session.

### CONC-6: Post-Shutdown TUI Sends

**Location**: `cmd/root.go`
**Severity**: LOW

`atomic.Pointer[tea.Program]` is never set to `nil` after `prog.Run()` returns. Session goroutines may call `prog.Send()` after the TUI exits.

**Fix**: Set `p.Store(nil)` after `prog.Run()` returns.

### CONC-7: Subscriber Event Drops

**Location**: `internal/runtime/session.go`
**Severity**: LOW

Non-blocking sends on subscriber channels silently drop events when the 64-slot buffer is full. Intentional design — acceptable for TUI use case.

### CONC-8: MCP Sequential Connection

**Location**: `internal/mcp/manager.go`
**Severity**: LOW

Servers connected sequentially. Startup latency scales linearly with server count.

**Fix**: Parallelize with `errgroup` if startup time becomes a problem.

---

## 9. Code Quality & Patterns

### QUAL-1: `fetchWebpage` Missing Context

**Location**: `internal/llm/tools/tools.go:413`
**Severity**: MEDIUM

Uses `http.NewRequest` instead of `http.NewRequestWithContext`. The handler receives a `context.Context` but doesn't propagate it.

**Fix**: Use `http.NewRequestWithContext(ctx, ...)`.

### QUAL-2: No Tests for `cmd/` Package

**Severity**: LOW

`cmd/awareness.go` has pure functions (`generateTeamAwareness`, `summarizeTeam`) that could be unit tested.

### QUAL-3: Store Optional Everywhere Pattern

**Severity**: LOW

All 6 progress tools in `CoreTools` have `if ct.store == nil` guards. Repetitive.

**Fix**: Consider a NullObject pattern for the store, or move the nil check to tool registration time.

### QUAL-4: `RebuildDefinitions` Duplicates Insert Logic

**Severity**: LOW

Insert logic for skills, agents, teams, and team_agents is duplicated between individual `Upsert*` methods and `RebuildDefinitions`. Schema changes must be updated in two places.

### QUAL-5: No Incremental Definition Updates

**Severity**: LOW

Every `.md` file change triggers a complete rebuild of all four definition tables. Acceptable at current scale but won't scale to hundreds of agents.

### QUAL-6: `agentfmt` Type Detection Is Heuristic

**Severity**: LOW

Auto-detection of file type (skill/agent/team) and format (Toasters/Claude Code/OpenCode) relies on field presence heuristics. Edge cases can cause misclassification.

### QUAL-7: `SplitFrontmatter` Windows Line Endings

**Severity**: LOW

`strings.Split(content, "\n")` with `TrimRight(l, " \t")` doesn't handle `\r`. Files with Windows line endings would fail to detect `---\r` as a delimiter.

**Fix**: Add `\r` to the `TrimRight` set, or use `strings.TrimSpace`.

---

## 10. Consolidated Findings Registry

### By Severity

| ID | Severity | Category | Summary |
|----|----------|----------|---------|
| SEC-CRITICAL-1 | CRITICAL | Security | Command injection in `setup_workspace` |
| DEAD-1 | BLOCKING | Dead Code | ~4,600 lines of legacy `llm` package family |
| DEAD-2 | HIGH | Architecture | Dual agent/team type systems |
| STRUCT-1 | HIGH | Architecture | Two parallel tool systems, duplicated SSRF |
| SEC-HIGH-1 | HIGH | Security | Shell tool has no sandboxing (design tradeoff) |
| SEC-HIGH-2 | HIGH | Security | Incomplete `.gitignore` |
| SEC-HIGH-3 | HIGH | Security | API keys in plaintext config |
| ARCH-1 | MEDIUM | Architecture | Operator blocks during tool execution |
| ARCH-2 | MEDIUM | Architecture | Self-send deadlock potential |
| ARCH-3 | MEDIUM | Architecture | Naive conversation window truncation |
| ARCH-4 | MEDIUM | Architecture | No backpressure from operator to TUI |
| ARCH-5 | MEDIUM | Architecture | Legacy dual-path complexity in TUI |
| STRUCT-2 | MEDIUM | Architecture | `ToolDef` type duplication |
| DEAD-3 | MEDIUM | Dead Code | `llm/tools` package misplacement |
| CONC-1 | MEDIUM | Concurrency | `Session.messages` mixed synchronization |
| CONC-2 | MEDIUM | Concurrency | Operator self-send deadlock potential |
| CONC-3 | MEDIUM | Concurrency | MCP Manager Close() race |
| CONC-4 | MEDIUM | Concurrency | Runtime.Shutdown() busy-wait, no timeout |
| CONC-5 | MEDIUM | Concurrency | Operator tool execution blocks event loop |
| SEC-MEDIUM-1 | MEDIUM | Security | `editFile` no size limit |
| SEC-MEDIUM-2 | MEDIUM | Security | `writeFile` no content size limit |
| SEC-MEDIUM-3 | MEDIUM | Security | Token refresh race condition |
| SEC-MEDIUM-4 | MEDIUM | Security | `glob` pattern traversal |
| SEC-MEDIUM-5 | MEDIUM | Security | MCP subprocess trust |
| QUAL-1 | MEDIUM | Quality | `fetchWebpage` missing context |
| STRUCT-3 | LOW | Architecture | `ProviderConfig` duplication |
| STRUCT-4 | LOW | Architecture | `MCPCaller` interface duplication |
| STRUCT-5 | LOW | Architecture | `ToolExecutor` name collision |
| STRUCT-6 | LOW | Architecture | `ToolExecutor` partial construction |
| STRUCT-7 | LOW | Architecture | SpawnTeamLead coupling risk |
| CONC-6 | LOW | Concurrency | Post-shutdown TUI sends |
| CONC-7 | LOW | Concurrency | Subscriber event drops (intentional) |
| CONC-8 | LOW | Concurrency | MCP sequential connection |
| QUAL-2 | LOW | Quality | No tests for `cmd/` package |
| QUAL-3 | LOW | Quality | Store optional everywhere pattern |
| QUAL-4 | LOW | Quality | `RebuildDefinitions` duplicates insert logic |
| QUAL-5 | LOW | Quality | No incremental definition updates |
| QUAL-6 | LOW | Quality | `agentfmt` type detection is heuristic |
| QUAL-7 | LOW | Quality | `SplitFrontmatter` Windows line endings |

### By Category

| Category | Critical | High | Medium | Low | Total |
|----------|----------|------|--------|-----|-------|
| Security | 1 | 3 | 5 | 0 | 9 |
| Architecture | 0 | 2 | 7 | 5 | 14 |
| Dead Code | 0 | 1 | 1 | 0 | 2 (+1 BLOCKING) |
| Concurrency | 0 | 0 | 5 | 3 | 8 |
| Quality | 0 | 0 | 1 | 6 | 7 |
| **Total** | **1** | **6** | **19** | **14** | **40** |

---

## 11. Recommended Execution Order

### Wave 1: Safety & Cleanup (Do Before Phase 4 Development)

These are prerequisite fixes that reduce risk and noise before the client/server split work begins.

| # | Finding | Effort | Impact |
|---|---------|--------|--------|
| 1 | **SEC-CRITICAL-1**: Fix `setup_workspace` command injection | Small | Eliminates critical vulnerability |
| 2 | **SEC-HIGH-2**: Expand `.gitignore` | Trivial | Prevents accidental secret commits |
| 3 | **DEAD-1**: Delete legacy `llm` package family | Medium | -4,600 lines, clearer codebase |
| 4 | **STRUCT-1** (partial): Extract shared SSRF protection | Small | Security code in one place |
| 5 | **SEC-MEDIUM-1/2**: Add file size limits to `editFile`/`writeFile` | Small | Prevents OOM |
| 6 | **CONC-4**: Add timeout to `Runtime.Shutdown()` | Small | Prevents hung exit |
| 7 | **QUAL-1**: Fix `fetchWebpage` missing context | Trivial | Correctness |
| 8 | **SEC-MEDIUM-3**: Add mutex to token refresh | Small | Prevents auth races |

### Wave 2: Structural Preparation (Do As Part of Phase 4)

These changes prepare the architecture for the client/server split.

| # | Finding | Effort | Impact |
|---|---------|--------|--------|
| 1 | **DEAD-2**: Consolidate agent/team type systems | Large | Eliminates dual loading, single source of truth |
| 2 | **DEAD-3**: Relocate `llm/tools` package | Small | Clean package structure |
| 3 | **ARCH-5**: Remove legacy dual-path in TUI | Medium | Simplifies TUI, removes dead code |
| 4 | **ARCH-3**: Fix conversation window truncation | Small | Prevents corrupted LLM conversations |
| 5 | **ARCH-2/CONC-2**: Fix self-send pattern | Small | Eliminates deadlock potential |
| 6 | **STRUCT-2**: Consolidate `ToolDef` type | Small | Eliminates manual sync requirement |
| 7 | **CONC-6**: Fix post-shutdown TUI sends | Trivial | Prevents potential panic |

### Wave 3: Client/Server Extraction (Phase 4 Core Work)

These are the actual client/server split tasks, informed by the analysis above.

| # | Task | Effort | Prerequisite |
|---|------|--------|-------------|
| 1 | Define server API schema (REST/gRPC + WebSocket/SSE events) | Medium | — |
| 2 | Extract business logic from TUI into service layer | Large | Wave 2 #1, #3 |
| 3 | Add conversation persistence to SQLite | Medium | — |
| 4 | Implement server with API routes | Large | #1, #2 |
| 5 | Implement event streaming (WebSocket/SSE) | Medium | #1 |
| 6 | Refactor TUI as thin client over server API | Large | #4, #5 |
| 7 | Add authentication layer | Medium | #4 |

### Wave 4: Hardening (Post-Split)

| # | Finding | Effort |
|---|---------|--------|
| 1 | **ARCH-1/CONC-5**: Non-blocking operator tool execution | Large |
| 2 | **SEC-HIGH-1**: Shell sandboxing / command allowlists | Large |
| 3 | **SEC-HIGH-3**: API key management improvements | Medium |
| 4 | **ARCH-4**: Operator → TUI backpressure | Small |
| 5 | **CONC-8**: Parallel MCP server connection | Small |
| 6 | Remaining LOW findings | Small each |

---

## Appendix A: TUI Model Field Inventory

The TUI `Model` struct has ~60 fields organized into sub-models:

| Sub-Model | Fields | Purpose |
|-----------|--------|---------|
| Core layout | `width`, `height`, `chatViewport`, `input`, `mdRender` | Terminal rendering |
| LLM client | `llmClient`, `stats` | Provider + session statistics |
| `streamingState` | `streaming`, `partialResponse`, `reasoning`, `streamCh`, `cancelStream`, `operatorByline` | Active LLM stream |
| `gridState` | `showGrid`, `focusCell`, `gridPage`, `gridCols`, `gridRows` | Agent grid view |
| `promptModeState` | `promptMode`, `question`, `options`, `selectedOption`, `customInput`, `pendingToolCall`, `dispatchConfirm` | Interactive prompts |
| `chatState` | `entries`, `completionIndices`, `expandedCompletions`, `pendingCompletions` | Conversation history |
| `progressState` | `jobs`, `tasks`, `reports`, `activeSessions`, `runtimeSnapshots`, `feedEntries` | SQLite poll results |
| 6 modal states | `teamsModal`, `skillsModal`, `agentsModal`, `mcpModal`, `blockerModal`, `jobsModal` | CRUD modals |
| Runtime | `mcpManager`, `store`, `runtime`, `runtimeSessions`, `operator` | Backend references |
| Animation | `loading`, `loadingFrame`, `flashText`, `spinnerFrame`, `spinnerRunning`, `focusAnimPanel`, `focusAnimFrames` | Visual effects |
| Layout cache | `lpWidth`, `sbWidth`, `leftPanelHidden`, `sidebarHidden`, `leftPanelWidthOverride` | Panel sizing |
| Toasts | `toasts`, `nextToastID` | Notification stack |

## Appendix B: Store Interface (30 Methods)

| Category | Methods | Count |
|----------|---------|-------|
| Jobs | `CreateJob`, `GetJob`, `ListJobs`, `ListAllJobs`, `UpdateJob`, `UpdateJobStatus` | 6 |
| Tasks | `CreateTask`, `GetTask`, `ListTasksForJob`, `UpdateTaskStatus`, `UpdateTaskResult`, `CompleteTask`, `AssignTask`, `PreAssignTaskTeam`, `AddTaskDependency`, `GetReadyTasks` | 10 |
| Progress | `ReportProgress`, `GetRecentProgress` | 2 |
| Definitions | `UpsertSkill`, `GetSkill`, `ListSkills`, `DeleteAllSkills`, `UpsertAgent`, `GetAgent`, `ListAgents`, `DeleteAllAgents`, `UpsertTeam`, `GetTeam`, `ListTeams`, `DeleteAllTeams` | 12 |
| Team Agents | `AddTeamAgent`, `ListTeamAgents`, `DeleteAllTeamAgents` | 3 |
| Feed | `CreateFeedEntry`, `ListFeedEntries`, `ListRecentFeedEntries` | 3 |
| Rebuild | `RebuildDefinitions` | 1 |
| Sessions | `CreateSession`, `UpdateSession`, `GetActiveSessions` | 3 |
| Artifacts | `LogArtifact`, `ListArtifactsForJob` | 2 |
| Lifecycle | `Close` | 1 |

## Appendix C: Event Types

| Event | Mechanical? | LLM-Routed? | Description |
|-------|-------------|-------------|-------------|
| `EventUserMessage` | | Yes | User sent a message |
| `EventTaskStarted` | Yes | | Team lead began working |
| `EventTaskCompleted` | Conditional | Conditional | Task finished (mechanical if HasNextTask) |
| `EventTaskFailed` | | Yes | Task execution failed |
| `EventBlockerReported` | | Yes | Agent hit a blocker |
| `EventProgressUpdate` | Yes | | Status update from agent |
| `EventJobComplete` | Yes | | All tasks in job done |
| `EventNewTaskRequest` | | Yes | Team lead requests more work |
| `EventUserResponse` | | Yes | User answered a prompt |
