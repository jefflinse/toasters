# Toasters — Vision

Toasters is an agentic orchestration platform with a TUI interface. It coordinates multiple concurrent LLM-powered agents through a Bubble Tea interface, dispatching work via an operator LLM and managing the full lifecycle of jobs, tasks, and agent sessions.

The core insight: LLMs are good at reasoning and writing code, but bad at maintaining state across long sessions. Toasters inverts this — Go owns the state, and LLMs are invoked with accumulated context fed back in. The orchestrator is the memory, not the model.

**Where we started:** A fun TUI project to play with a local LLM.

**Where we're going:** A persistent agentic operations platform that coordinates multi-model agent teams, integrates with external services via MCP, maintains knowledge of engineering ecosystems, and gets smarter over time.

---

## Table of Contents

- [What It Is](#what-it-is)
- [What It Is Not (Yet)](#what-it-is-not-yet)
- [Job Types](#job-types)
- [Architecture](#architecture)
  - [Operator LLM](#operator-llm)
  - [Agent Runtime](#agent-runtime)
  - [MCP Integration](#mcp-integration)
  - [State Persistence](#state-persistence)
  - [Task DAG and Concurrency](#task-dag-and-concurrency)
- [UI Layout](#ui-layout)
- [Tech Stack](#tech-stack)
- [Current State](#current-state)

---

## What It Is

- A TUI-first orchestration platform for agentic coding work
- A multi-agent coordinator: operator dispatches to teams of specialized agents
- An MCP client: consumes tools from external MCP servers (GitHub, Jira, Linear, etc.)
- An MCP server: exposes a progress-reporting API that agents use to report status back to the orchestrator
- A persistent state manager: SQLite for operational data, markdown for human-readable artifacts
- A multi-provider LLM client: talks directly to Anthropic, OpenAI, LM Studio, and other providers

## What It Is Not (Yet)

- Not a replacement for Claude Code or OpenCode — it orchestrates work at a higher level
- Not a web application (TUI-first, server architecture comes later)
- Not a multi-tenant platform (single-user for now)

---

## Job Types

Toasters automatically classifies incoming tasks into one of four types. The classification drives which agents are selected, what data sources are consulted, and how the task DAG is structured.

| Type | Description |
|---|---|
| **Bug Fix** | Usually has an associated Jira ticket, Slack thread, or error report. Requires investigation before planning. |
| **New Feature** | Fully implement a new feature on an existing project. Requires understanding the codebase before planning. |
| **Prototype** | Given basic requirements and constraints, produce a working prototype. Emphasis on speed and iteration. |
| **Review** | Perform a code review of a PR, branch, or diff. Produces findings and a structured report. |

---

## Architecture

### Operator LLM

The operator is the brain of the system. It receives user requests, classifies work, selects teams and workflows, dispatches jobs, and monitors progress. It can be backed by any provider — a local LM Studio model for cheap coordination, or a cloud model (Anthropic, OpenAI) for more capable reasoning.

Responsibilities:
- Classify incoming tasks into job types
- Select teams and workflows for jobs
- Dispatch work to agent teams
- Monitor agent progress via the Toasters MCP server
- Reach out to external data sources via MCP tools
- Maintain job state in SQLite

The operator has access to both static tools (job management, team dispatch) and dynamic tools from configured MCP servers.

### Agent Runtime

Agents are LLM conversation loops running as goroutines. Each agent has:
- A system prompt (from agent definition files or database)
- A set of available tools (file I/O, shell, web fetch, MCP tools, subagent spawning)
- A message history managed by the Go runtime
- A context for cancellation

The agent runtime replaces the previous `claude` CLI subprocess approach. Instead of shelling out to `claude` and parsing stream-json output, Toasters talks directly to LLM providers via their APIs. This gives full control over the request/response lifecycle, enables mixing models per agent, and eliminates subprocess fragility.

**Core tool set** (what agents need to do real work):
- File I/O: read, write, edit, glob, grep
- Shell execution: run commands, capture output
- Web fetch: HTTP GET for URLs
- Subagent spawning: create child agent sessions (equivalent to Claude Code's `Task` tool)
- MCP tools: any tools from configured MCP servers
- Toasters MCP tools: report progress, update task status, flag blockers

### MCP Integration

Toasters has a three-part MCP strategy:

**1. MCP Client — Consume external tools**
The operator and agents connect to external MCP servers (GitHub, Jira, Linear, filesystem, git, etc.) and use their tools. Configuration is per-server with support for stdio, HTTP, and SSE transports. Tools are namespaced to prevent collisions.

**2. MCP Server — Agent progress reporting**
Toasters runs its own MCP server that agents connect to. This creates a structured, bidirectional communication channel between the orchestrator and its agents. Agents report progress, flag blockers, update task status, and query job context — all through MCP tools rather than file-based detection.

Key tools exposed by the Toasters MCP server:
- `report_progress(job_id, task_id, status, message)` — agent reports what it's doing
- `report_blocker(job_id, task_id, description)` — agent flags it's stuck
- `update_task_status(job_id, task_id, status)` — agent marks a task done/failed
- `request_review(job_id, task_id, artifact_path)` — agent asks for peer review
- `query_job_context(job_id)` — agent asks about the broader job state

**3. Ephemeral OpenAPI-to-MCP Bridges**
Toasters can auto-generate MCP servers from OpenAPI specs. Point it at a spec URL + credentials, and it spins up a lightweight MCP server that translates tool calls into HTTP requests against the backend service. These are scoped to a job or ecosystem and cleaned up automatically.

### State Persistence

**SQLite** (operational state):
- Jobs, tasks, status, assignments
- Team and agent configurations
- Slot history, cost tracking
- Agent progress reports (from MCP server)
- Ecosystem metadata
- Operator memory

**Markdown on disk** (human-readable artifacts):
- Job overviews and reports
- Investigation findings
- Code review results
- Any artifact agents produce for human consumption

### Task DAG and Concurrency

- The operator (or a planning agent) creates a task DAG for each job
- Each task node carries: name, dependencies, status, assigned agent, last update
- Go manages concurrency — multiple agent sessions run simultaneously as goroutines
- Agents report progress back via the Toasters MCP server, updating task status in SQLite
- The TUI subscribes to database changes for real-time progress display

---

## UI Layout

The TUI uses a two-column layout. The left column is the primary work management surface; the right column is the information sidebar.

```
┌─────────────────────────────────────┬──────────────────┐
│                                     │  Connection      │
│  [Jobs List]                        │  Model: ...      │
│  (scrollable, focused)              │  Endpoint: ...   │
│                                     │  Status: ...     │
├─────────────────────────────────────│                  │
│                                     │  Session         │
│  [Task DAG]                         │  Messages: ...   │
│  (for selected job)                 │  Tokens in: ...  │
│  node statuses shown inline         │  Tokens out: ... │
│                                     │  Speed: ...      │
├─────────────────────────────────────│  Context: ████░░ │
│                                     │  Last resp: ...  │
│  [Streaming Updates]                │  Avg resp: ...   │
│  (live output from active tasks)    │                  │
│                                     ├──────────────────│
│                                     │  [Active Tasks]  │
│                                     │  task 1: done    │
│                                     │  task 2: running │
│                                     │  task 3: pending │
└─────────────────────────────────────┴──────────────────┘
```

### Left Panel (split horizontally into three sections)

- **Top**: Scrollable list of jobs. Receives focus for keyboard navigation.
- **Middle**: DAG visualization of tasks for the selected job, with per-node status indicators.
- **Bottom**: Live streaming output from tasks currently executing in the selected job.

### Right Panel (split vertically into two sections)

- **Top**: Stats, usage, and connection info — model name, endpoint, connection status, token counts (in/out/reasoning), generation speed (t/s), context window usage bar with color-coded fill (green/yellow/red), last and average response times.
- **Bottom**: Active task list for the selected job, showing each task's status and most recent update.

---

## Tech Stack

| Component | Version / Details |
|---|---|
| Go | 1.26 |
| Bubble Tea | `charm.land/bubbletea/v2 v2.0.0` |
| Lipgloss | `charm.land/lipgloss/v2 v2.0.0` |
| Bubbles | `charm.land/bubbles/v2 v2.0.0` |
| Glamour | `github.com/charmbracelet/glamour v0.10.0` |
| SQLite | `modernc.org/sqlite` (pure Go, no CGO) |
| MCP | `github.com/mark3labs/mcp-go` (client + server) |
| Local LLM | LM Studio at `localhost:1234` (OpenAI-compatible API) |
| Cloud LLMs | Anthropic, OpenAI, Google (direct API) |

---

## Current State

What exists today is a functional agentic orchestration TUI with operator chat, agent team dispatch, in-process agent runtime, SQLite persistence, multi-provider LLM support, and full MCP integration. The codebase has been through a comprehensive health audit (see `HEALTH_REPORT.md`) — all findings resolved, 0 lint issues, 0 vulnerabilities. Phase 1 (The Foundation) and Phase 2 (Connect to the World) are complete: agents run as in-process goroutines talking directly to LLM providers, consume external MCP tools, report progress via structured tools, and the TUI shows real-time progress. Claude CLI subprocess retained as a fallback. 15 test packages, key packages at 83–100% coverage.

### Implemented

**`internal/llm` — Shared LLM types and Provider interface**
- Shared types (`Message`, `Tool`, `ToolCall`, `Usage`, etc.) and `Provider` interface
- Split into three focused sub-packages for clean separation of concerns

**`internal/llm/client` — OpenAI-compatible streaming client**
- `Client` connects to any OpenAI-compatible API endpoint with proper HTTP timeouts
- `ChatCompletionStream` sends messages and returns a channel of streamed response chunks
- `ChatCompletionStreamWithTools` supports function calling
- `FetchModels` queries available models
- Shared `doStream` helper eliminates duplication between streaming methods
- Token usage tracking and context window monitoring

**`internal/llm/tools` — Tool executor**
- `ToolExecutor` struct with dependency injection (no global state)
- Executes operator tool calls (job management, team dispatch, web fetch)

**`internal/tui` — TUI application**
- Two-column layout: main chat area (left) + stats sidebar (right)
- Chat viewport with markdown rendering via Glamour
- Streaming response display with reasoning blocks
- Slash command system: `/help`, `/new`, `/exit`, `/quit`, `/claude`, `/kill`
- Grid view (Ctrl+G) showing 2×2 agent slot status
- Prompt mode for operator questions
- `ChatEntry` struct replaces parallel slices for message data
- Split into 11 focused files (model, view, grid, panels, modals, streaming, messages, prompt, helpers, update, commands)

**`internal/agents` — Agent system**
- Agent discovery from `.md` files with YAML frontmatter
- Team definitions with coordinator + worker roles
- Hot-reload via fsnotify

**`internal/claude` — Shared Claude CLI stream types**
- Exported types for parsing `--output-format stream-json` output
- Used by both `internal/tui` and `internal/gateway` (eliminates duplication)

**`internal/anthropic` — Anthropic API client**
- Direct Anthropic Messages API client with SSE streaming
- OAuth/Keychain integration for authentication (macOS, with platform guard)

**`internal/gateway` — Claude subprocess management**
- Up to 4 concurrent Claude CLI subprocess slots
- Stream-json output parsing
- Context cancellation and slot lifecycle management

**`internal/job` — Job persistence**
- OVERVIEW.md + TODO.md per job
- YAML frontmatter + markdown format

**`internal/frontmatter` — Shared YAML frontmatter parsing**
- `Split()` and `Parse()` functions used by `job/` and `agents/`
- Replaces four duplicate parsing implementations

**`internal/orchestration` — Cross-cutting orchestration types**
- `GatewaySlot` and `AgentSpawner` interfaces (moved out of `internal/llm` to break import cycles)

**`internal/config` — Configuration**
- Viper-based config from `~/.config/toasters/config.yaml`
- Operator, Claude CLI, provider, agent, and database settings

**`internal/db` — SQLite Persistence (Phase 1)**
- Pure Go SQLite via `modernc.org/sqlite`, WAL mode for concurrent reads
- Store interface with full CRUD for jobs, tasks, task dependencies, progress reports, agents, teams, sessions, artifacts
- Embedded SQL migrations with auto-apply on open
- Schema version tracking for safe upgrades

**`internal/provider` — Multi-Provider LLM Client (Phase 1)**
- `Provider` interface with `ChatStream` returning `<-chan StreamEvent`
- OpenAI-compatible provider (LM Studio, Ollama, OpenAI)
- Anthropic Messages API provider with SSE streaming and tool use
- Keychain/OAuth authentication fallback for Anthropic (macOS)
- Provider registry with config-driven factory and `${ENV_VAR}` expansion
- Conversion utilities bridging `llm.Provider` ↔ `provider.Provider` via `LLMProviderAdapter`

**`internal/runtime` — In-Process Agent Runtime (Phase 1)**
- Session conversation loop: stream → accumulate → execute tools → loop
- 8 core tools: read_file, write_file, edit_file, glob, grep, shell, web_fetch, spawn_agent
- Path traversal prevention (sandbox to workDir), shell timeout (30s), web fetch timeout (10s)
- Subagent spawning with max depth limit (3 levels)
- Fan-out observer pattern for streaming events to TUI
- SQLite session tracking (tokens, status, timing)
- Runtime manager with mutex-protected session map

**Async Tool Execution (Phase 1)**
- `executeToolsCmd` helper dispatches tool calls to goroutines
- TUI remains responsive during tool execution
- Escape cancels in-flight tool calls
- Visual "calling tool..." indicators

**`internal/mcp` — MCP Client Manager (Phase 2)**
- Manages connections to external MCP servers (GitHub, Jira, Linear, etc.)
- Tool discovery, namespacing (`{server_name}__{tool_name}`), and dispatch
- Server status tracking with `Servers()` accessor for TUI visibility
- Smart result truncation: JSON-aware array shrinking with UTF-8 safe byte fallback (16KB default)
- JSON slimming: strips nulls, `*_url` fields, API URLs, `node_id`, opaque blobs from responses
- `TruncatingCaller` decorator wraps MCP manager for runtime integration
- Failed servers skipped with warning; graceful shutdown via `Close()`

**`internal/progress` — Progress Tool Handlers + MCP Server (Phase 2)**
- Protocol-agnostic handlers for 6 progress tools: `report_progress`, `report_blocker`, `update_task_status`, `request_review`, `query_job_context`, `log_artifact`
- MCP server via `toasters mcp-server` subcommand for Claude CLI subprocess integration
- In-process agents call handlers directly without MCP protocol overhead

**TUI Enhancements (Phase 2 + MCP Visibility)**
- Real-time progress display via SQLite polling (500ms)
- Task status indicators, blocker alerts, token usage per session
- `/mcp` slash command with full-screen modal (server list, details panel, scrollable tool list)
- MCP summary section in sidebar with ✓/✗ status icons and tool counts
- Startup toast notifications for connected/failed MCP servers
- MCP tool calls annotated as `tool_name (via server)` in chat view
- Context bar fix: prompt tokens use assignment (not accumulation), "Prompt ctx" label

### Not Yet Built (Phase 3+)

- Ephemeral OpenAPI-to-MCP bridges
- Left panel: job list, task DAG visualization, streaming updates pane
- Team templates and workflows
- Ecosystems (ephemeral and long-lived)
- Operator memory
- Server/client architecture split
