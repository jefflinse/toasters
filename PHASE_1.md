# Phase 1: The Foundation — Implementation Plan

**Created:** 2026-02-24
**Status:** ✅ Complete — All PRs merged, integration done, end-to-end verified
**Branch:** `phase-1`

---

## Objective

Transform Toasters into a standalone agentic tool with SQLite persistence, multi-provider LLM support, in-process agent runtime, and async tool execution. Delivered as 4 independent PRs.

---

## PR Overview

| PR | Deliverable | Branch | Effort | Dependencies | Status |
|----|------------|--------|--------|-------------|--------|
| PR 1 | 1.1 — SQLite Persistence Layer | `feat/sqlite-persistence` | 2–3 days | None | ✅ Complete (83.6% coverage) |
| PR 2 | 1.2 — Multi-Provider LLM Client | `feat/multi-provider` | 2–3 days | None | ✅ Complete (85.1% coverage) |
| PR 3 | 1.4 — Async Tool Execution | `feat/async-tool-execution` | 1–2 days | None | ✅ Complete |
| PR 4 | 1.3 — In-Process Agent Runtime | `feat/agent-runtime` | 3–5 days | PR 2 | ✅ Complete (87.8% coverage) |

**Merge order:** PR 3 → PR 1 → PR 2 → PR 4

---

## PR 1: SQLite Persistence Layer

**Branch:** `feat/sqlite-persistence`
**Depends on:** Nothing

### Steps

| # | Step | Agent | Status |
|---|------|-------|--------|
| 1.1.1 | Add `modernc.org/sqlite` dependency | builder | ✅ |
| 1.1.2 | Create `internal/db` types and Store interface | builder | ✅ |
| 1.1.3 | Create migration system with embedded SQL | builder | ✅ |
| 1.1.4 | Implement SQLite Store (all CRUD) | builder | ✅ |
| 1.1.5 | Write comprehensive tests (≥80% coverage) | test-writer | ✅ 83.6% |
| 1.1.6 | Add database path to config | builder | ✅ |
| 1.1.7 | Code review | code-reviewer | ✅ Findings addressed |

### Details

**1.1.1 — Add `modernc.org/sqlite` dependency**
- Run `go get modernc.org/sqlite` for pure-Go SQLite driver
- Verify `go build ./...` and `go test ./...` still pass
- Risk: Large dependency tree, verify no conflicts

**1.1.2 — Create `internal/db` types and Store interface**
- Files: `internal/db/doc.go`, `internal/db/types.go`, `internal/db/store.go`
- Go structs for: Job, Task, TaskDep, ProgressReport, Agent, Team, TeamMember, AgentSession, Artifact
- Status type constants (JobStatusPending, JobStatusActive, etc.)
- Store interface matching roadmap spec
- Filter types: JobFilter, SessionUpdate

**1.1.3 — Create migration system**
- Files: `internal/db/migrations.go`, `internal/db/migrations/001_initial.sql`
- Embedded SQL via `embed.FS`
- `schema_version` table tracks applied migrations
- Auto-apply on `Open()`
- WAL mode + foreign keys via PRAGMAs

**1.1.4 — Implement SQLite Store**
- File: `internal/db/sqlite.go`
- `Open(path) (*SQLiteStore, error)` constructor
- All CRUD methods with parameterized queries
- `GetReadyTasks` uses subquery for dependency resolution
- Concurrent reads via WAL mode

**1.1.5 — Write comprehensive tests**
- File: `internal/db/sqlite_test.go`
- Test all CRUD operations, task dependency resolution, migration idempotency
- Test WAL mode, concurrent reads, error cases
- Target ≥80% coverage, pass with `-race`

**1.1.6 — Add database path to config**
- Modify: `internal/config/config.go`
- Add `DatabasePath` field, default `~/.config/toasters/toasters.db`
- Backward compatible with existing configs

**1.1.7 — Code review**
- SQL injection safety, resource cleanup, error wrapping
- WAL mode correctness, migration safety
- Test coverage adequacy

---

## PR 2: Multi-Provider LLM Client

**Branch:** `feat/multi-provider`
**Depends on:** Nothing

### Steps

| # | Step | Agent | Status |
|---|------|-------|--------|
| 1.2.1 | Design `provider.Provider` interface | api-designer | ✅ |
| 1.2.2 | Implement OpenAI-compatible provider | builder | ✅ |
| 1.2.3 | Implement Anthropic provider | builder | ✅ |
| 1.2.4 | Implement provider Registry and factory | builder | ✅ |
| 1.2.5 | Add providers config section | builder | ✅ (in registry.go, not config.go) |
| 1.2.6 | Create conversion utilities (llm ↔ provider) | builder | ✅ |
| 1.2.7 | Write tests for all providers (≥70% coverage) | test-writer | ✅ 85.1% |
| 1.2.8 | Code review | code-reviewer | ✅ Findings addressed |

### Details

**1.2.1 — Design provider.Provider interface**
- Files: `internal/provider/doc.go`, `internal/provider/types.go`, `internal/provider/provider.go`
- Channel-based `ChatStream` returning `<-chan StreamEvent`
- `StreamEvent` discriminated union: text, tool_call, usage, done, error
- `ChatRequest`, `Message`, `Tool`, `ToolCall` types
- Registry type for provider lookup

**1.2.2 — Implement OpenAI-compatible provider**
- File: `internal/provider/openai.go`
- New implementation wrapping SSE streaming logic
- Outputs `StreamEvent` on channel
- Supports LM Studio (no key), OpenAI (Bearer auth), Ollama

**1.2.3 — Implement Anthropic provider**
- File: `internal/provider/anthropic.go`
- Two auth modes: API key (`x-api-key`) and OAuth/Keychain
- SSE streaming with content blocks and tool_use blocks
- Anthropic message format conversion

**1.2.4 — Implement provider Registry and factory**
- Files: `internal/provider/registry.go`, `internal/provider/config.go`
- `ProviderConfig` struct, `NewFromConfig` factory
- `${ENV_VAR}` expansion for API keys
- Registry `Get(name)` lookup

**1.2.5 — Add providers config section**
- Modify: `internal/config/config.go`
- `Providers []ProviderConfig` field
- `agents.default_provider` and `agents.default_model`
- Backward compatible with existing operator config

**1.2.6 — Create conversion utilities**
- File: `internal/provider/convert.go`
- `llm.Message` ↔ `provider.Message`, `llm.Tool` ↔ `provider.Tool`
- `LLMProviderAdapter` wrapping `provider.Provider` → `llm.Provider`
- Claude-CLI-specific fields set to zero values for non-CLI providers

**1.2.7 — Write tests**
- Files: `internal/provider/openai_test.go`, `internal/provider/anthropic_test.go`, `internal/provider/registry_test.go`, `internal/provider/convert_test.go`
- Mock SSE endpoints via `httptest.Server`
- Test streaming, tool calls, errors, cancellation, adapter, round-trips

**1.2.8 — Code review**
- Interface design quality, SSE correctness, auth security
- Channel lifecycle (always closed, no goroutine leaks)
- Backward compatibility with existing TUI

---

## PR 3: Async Tool Execution Refactor

**Branch:** `feat/async-tool-execution`
**Depends on:** Nothing

### Steps

| # | Step | Agent | Status |
|---|------|-------|--------|
| 1.4.1 | Define `ToolResultMsg` and supporting types | builder | ✅ |
| 1.4.2 | Create `executeToolsCmd` helper | builder | ✅ |
| 1.4.3 | Refactor `handleToolCalls` to be async | builder | ✅ |
| 1.4.4 | Add `ToolResultMsg` handler in `Model.Update()` | builder | ✅ |
| 1.4.5 | Add Escape cancellation for in-flight tools | builder | ✅ |
| 1.4.6 | Update visual indicators (spinner, status bar) | builder | ✅ |
| 1.4.7 | Write tests | test-writer | ✅ |
| 1.4.8 | Code review (+ concurrency review) | code-reviewer | ✅ Findings addressed |

### Details

**1.4.1 — Define ToolResultMsg and supporting types**
- Modify: `internal/tui/messages.go`, `internal/tui/model.go`
- `ToolResultMsg` with Calls and Results
- `toolsInFlight` bool and `toolCancelFunc` on Model

**1.4.2 — Create executeToolsCmd helper**
- File: `internal/tui/tool_exec.go`
- Takes `[]llm.ToolCall`, `*tools.ToolExecutor`, `context.Context`
- Returns `tea.Cmd` running tools in goroutine
- Sequential execution within goroutine, context cancellation

**1.4.3 — Refactor handleToolCalls to be async**
- Modify: `internal/tui/prompt.go`
- Intercepted tools (ask_user, assign_team, kill_slot, escalate_to_user) stay synchronous
- All other tools dispatch to goroutine via `executeToolsCmd`
- Visual "⚙ calling tool..." indicators

**1.4.4 — Add ToolResultMsg handler**
- Modify: `internal/tui/model.go`
- Append results to entries, update viewport
- Re-invoke stream with `startStream(messagesFromEntries())`

**1.4.5 — Add Escape cancellation**
- Modify: `internal/tui/model.go`
- If `toolsInFlight`, Escape calls cancel func
- "[tool calls cancelled]" message in chat

**1.4.6 — Update visual indicators**
- Modify: `internal/tui/view.go` or `internal/tui/panels.go`
- Spinner animation during tool execution
- Status bar shows "executing tools..."

**1.4.7 — Write tests**
- Files: `internal/tui/tool_exec_test.go`
- Test `executeToolsCmd`, cancellation, result ordering

**1.4.8 — Code review**
- Race conditions, cancellation propagation, goroutine leaks
- Preserved behavior for intercepted tools
- `go test -race ./internal/tui/...`

---

## PR 4: In-Process Agent Runtime

**Branch:** `feat/agent-runtime`
**Depends on:** PR 2 (multi-provider)

### Steps

| # | Step | Agent | Status |
|---|------|-------|--------|
| 1.3.1 | Define runtime types and interfaces | builder | ✅ |
| 1.3.2 | Implement core tools (8 tools) | builder | ✅ |
| 1.3.3 | Implement the conversation loop | builder | ✅ |
| 1.3.4 | Implement `spawn_agent` tool | builder | ✅ |
| 1.3.5 | Implement Runtime manager | builder | ✅ |
| 1.3.6 | Write comprehensive tests (≥70% coverage) | test-writer | ✅ 87.8% |
| 1.3.7 | Security audit of core tools | security-auditor | ✅ 11 findings addressed |
| 1.3.8 | Code review | code-reviewer | ✅ Findings addressed |

### Details

**1.3.1 — Define runtime types and interfaces**
- Files: `internal/runtime/doc.go`, `internal/runtime/types.go`, `internal/runtime/session.go`
- Session, SessionEvent, SpawnOpts, SessionSnapshot, Runtime structs
- ToolExecutor interface

**1.3.2 — Implement core tools**
- File: `internal/runtime/tools.go`
- 7 tools: read_file, write_file, edit_file, glob, grep, shell, web_fetch
- Path traversal prevention (sandbox to workDir)
- Shell timeout (30s default), web fetch timeout (10s)
- JSON Schema definitions for each tool

**1.3.3 — Implement conversation loop**
- File: `internal/runtime/session.go`
- `Session.Run(ctx)`: stream → accumulate → execute tools → loop
- Fan-out observer pattern with buffered channels
- Token usage tracking, max-iterations guard (50 turns)

**1.3.4 — Implement spawn_agent tool**
- Modify: `internal/runtime/tools.go`
- Creates child Session via Runtime
- Parent blocks until child completes
- Max depth limit (3 levels)

**1.3.5 — Implement Runtime manager**
- File: `internal/runtime/runtime.go`
- `New`, `SpawnAgent`, `GetSession`, `CancelSession`, `ActiveSessions`
- SQLite persistence via db.Store (graceful degradation if nil)
- Mutex-protected session map

**1.3.6 — Write comprehensive tests**
- Files: `internal/runtime/tools_test.go`, `internal/runtime/session_test.go`, `internal/runtime/runtime_test.go`
- Mock provider for conversation loop tests
- Path traversal, shell timeout, web fetch tests
- Target ≥70% coverage, pass with `-race`

**1.3.7 — Security audit**
- Path traversal (symlink resolution)
- Shell injection safety
- Resource exhaustion (glob/grep limits)
- Web fetch SSRF
- Spawn depth enforcement

**1.3.8 — Code review**
- Goroutine lifecycle, channel management, mutex usage
- Error handling, integration with provider and db packages

---

## Key Design Decisions

1. **Existing `llm.Provider` preserved** — new `provider.Provider` is a parallel interface. An `LLMProviderAdapter` bridges them.
2. **Claude CLI subprocess path NOT removed** — supplemented by the new runtime.
3. **Markdown job files continue working** — SQLite is additive.
4. **Core tools are sandboxed** — file operations restricted to workDir, shell has timeout, spawn has depth limit.
5. **Two interface worlds coexist** — TUI uses `llm.Provider`, runtime uses `provider.Provider`. Adapter bridges them.

## Risks

| Risk | Mitigation |
|------|-----------|
| `modernc.org/sqlite` adds build time | Accept — pure Go is worth it vs CGO |
| SSE parsing duplicated in new Anthropic provider | Intentional for clean separation; can consolidate later |
| Path traversal in core tools | Security audit step, `filepath.Abs` + prefix check |
| Recursive `spawn_agent` unbounded goroutines | Max depth limit (3 levels) |
| `llm.Provider` vs `provider.Provider` confusion | Clear documentation, adapter pattern |

## Week 3: Integration and Post-Merge Fixes

**Branch:** `feat/phase-1-integration` (merged to `phase-1`) + direct commits on `phase-1`

The Week 3 integration work wired all four Phase 1 subsystems into the running TUI and resolved issues discovered during end-to-end testing.

### Integration Commits (feat/phase-1-integration)

| Commit | Description |
|--------|-------------|
| Wire startup | SQLite `db.Open()`, provider registry `NewRegistry()`, and `runtime.New()` initialized in `cmd/root.go` |
| Dual-write + bridge | Jobs written to both markdown files and SQLite; `assign_team` bridges to runtime when provider is configured |
| Operator tools | `list_sessions` and `cancel_session` tools for operator to manage runtime sessions |
| Integration tests | 11 new tests covering dual-write, runtime bridge, session tools |
| Code review fixes | Memory leak fix (cancel context on session done), `shortID` safety, spinner re-arm, lint |
| Keychain auth | `ReadKeychainAccessToken()` exported from `internal/anthropic`; Anthropic provider uses `Authorization: Bearer` + `anthropic-beta: oauth-2025-04-20` headers when no API key configured |

### Post-Merge Fixes (direct on phase-1)

| Commit | Description |
|--------|-------------|
| `fix: deadlock on assign_team dispatch` | **Critical bug**: `ExecuteTool` called synchronously from `Update()` caused deadlock when runtime path was active. `assign_team` → `OnSessionStarted` → `p.Send()` wrote to unbuffered channel blocked by `Update()`. Fixed by making both dispatch paths use async `executeToolsCmd`. |
| `feat: show runtime sessions in agents panel and grid view` | Runtime sessions displayed in agents panel with ⚡ prefix; empty grid cells show runtime session overlay with cyan borders |
| `feat: persist completed runtime sessions` | Completed sessions no longer deleted — stay visible like gateway slots. Grid Enter/p keys work for runtime sessions (output modal, prompt modal). Output modal auto-tails (opens scrolled to bottom, live-updates while session active). |

### End-to-End Verification

The full flow was verified working: user prompt → operator creates job → dispatches to team → runtime agent runs as goroutine talking to Anthropic API → executes tools (reads files, writes code, runs tests) → output visible in grid and full-screen modal → session tracked in SQLite.

### Coverage After Integration

| Package | Coverage |
|---------|----------|
| `frontmatter` | 100% |
| `llm/tools` | 88.3% |
| `llm/client` | 87.7% |
| `runtime` | 87.0% |
| `job` | 85.7% |
| `provider` | 84.9% |
| `db` | 83.6% |
| `agents` | 72.1% |
| `config` | 65.7% |

## Still Out of Scope (Phase 2+)

- Removing Claude CLI path (retained as fallback)
- MCP integration (Phase 2)
- Provider selection per-agent in TUI
- Database migration from markdown jobs to SQLite
