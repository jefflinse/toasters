# Toasters — Master Roadmap

**Created:** 2026-02-24  
**Status:** Active  

This is the master plan for evolving Toasters from a TUI prototype into a full agentic orchestration platform. Work is organized into four phases, each containing well-scoped deliverables that build on each other. Each deliverable is designed to be completable in 1–5 days and to produce a working, testable result.

---

## Table of Contents

- [Pre-Phase 1: Code Health](#pre-phase-1-code-health-completed-2026-02-24)
- [Phase 1: The Foundation](#phase-1-the-foundation)
- [Phase 2: Connect to the World](#phase-2-connect-to-the-world-completed-2026-02-25)
- [Phase 3: Structure and Polish](#phase-3-structure-and-polish)
- [Phase 4: Intelligence](#phase-4-intelligence)
- [Dependency Graph](#dependency-graph)
- [Principles](#principles)

---

## Pre-Phase 1: Code Health (Completed 2026-02-24)

**Goal:** Establish a clean, lint-free, well-structured codebase before starting Phase 1 feature work. No functional or visual changes — the TUI looks and functions identically.

**What was done:**

| Item | Description | Status |
|------|-------------|--------|
| HTTP client timeouts | Added connect/response timeouts to LLM client | ✅ Done |
| Permission mode default | Replaced `--dangerously-skip-permissions` with `--permission-mode plan` | ✅ Done |
| Dependency updates | Updated `x/crypto`, `x/net`, `x/sync`, `x/term`, `x/text` to latest | ✅ Done |
| Error handling | Fixed all 19 unchecked error returns across 8 files | ✅ Done |
| Global state elimination | Replaced package-level globals in `llm/tools.go` with `ToolExecutor` struct | ✅ Done |
| Type deduplication | Extracted shared Claude CLI stream types into `internal/claude` package | ✅ Done |
| Dead code removal | Removed unused fields, functions, and ineffectual assignments | ✅ Done |
| Lint cleanup | Fixed all staticcheck findings; `golangci-lint run` reports 0 issues | ✅ Done |

**Additional tech debt resolved (originally planned for Phase 1):**

| Item | Description | Status |
|------|-------------|--------|
| Break up `model.go` | Split from 5,300 lines into 11 focused files | ✅ Done |
| Parallel slices → struct | Replaced with `ChatEntry` struct and `appendEntry()` helper | ✅ Done |
| Unify frontmatter parsing | Created `internal/frontmatter` package with `Split()` + `Parse()` | ✅ Done |
| Split `internal/llm` | Split into `llm`, `llm/client`, `llm/tools` sub-packages | ✅ Done |
| macOS Keychain guard | Added runtime `GOOS` guard with clear error on non-macOS | ✅ Done |
| Charm v2 stable update | Updated all three to stable `v2.0.0` | ✅ Done |
| Test coverage | Raised from 12.1% to 42.9% (300+ tests, 10 packages) | ✅ Done |
| Vulnerability scan | `govulncheck` clean — no vulnerabilities found | ✅ Done |

All pre-Phase 1 tech debt is resolved. See `HEALTH_REPORT.md` for the full audit details.

---

## Phase 1: The Foundation (Completed 2026-02-24)

**Goal:** Toasters is a standalone agentic tool that talks directly to LLM providers, manages its own state in SQLite, and runs agents as in-process goroutines with a full tool set. No dependency on Claude CLI for core operation.

**Status:** ✅ Complete. All four deliverables built, integrated, and end-to-end verified. See `PHASE_1.md` for full implementation details.

**What was delivered:**

| Deliverable | Description | Status |
|-------------|-------------|--------|
| 1.1 — SQLite Persistence | `internal/db` package with Store interface, migrations, full CRUD (83.6% coverage) | ✅ Done |
| 1.2 — Multi-Provider LLM Client | `internal/provider` package with OpenAI + Anthropic providers, registry, Keychain/OAuth auth (84.9% coverage) | ✅ Done |
| 1.3 — In-Process Agent Runtime | `internal/runtime` package with session loop, 8 core tools, spawn_agent, SQLite tracking (87.0% coverage) | ✅ Done |
| 1.4 — Async Tool Execution | `executeToolsCmd` helper, goroutine dispatch, Escape cancellation, visual indicators | ✅ Done |
| Week 3 Integration | Dual-write jobs, assign_team runtime bridge, session event forwarding, operator tools, Keychain auth | ✅ Done |
| Post-merge fixes | Deadlock fix, runtime session UI (agents panel, grid, output/prompt modals, auto-tail) | ✅ Done |

**Post-delivery cleanup:** All Wave 1 safety fixes from the pre-Phase 2 tech debt audit completed. See `CLAUDE.md` for the full execution plan.

**Estimated total effort:** 2–3 weeks

---

### 1.1 — SQLite Persistence Layer

**Effort:** 2–3 days  
**Depends on:** Nothing  
**Unlocks:** Everything else (MCP server, progress reporting, team templates, ecosystems, operator memory)

**What to build:**

A `internal/db` package that owns all database access. Pure Go SQLite via `modernc.org/sqlite`. WAL mode for concurrent readers. Migrations managed in code (embed SQL files or use a simple version table).

**Schema (initial):**

```sql
-- Jobs
CREATE TABLE jobs (
    id          TEXT PRIMARY KEY,
    title       TEXT NOT NULL,
    type        TEXT NOT NULL,  -- bug_fix, new_feature, prototype, review
    status      TEXT NOT NULL DEFAULT 'pending',  -- pending, active, completed, failed, cancelled
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    metadata    TEXT  -- JSON blob for extensible fields
);

-- Tasks (belong to a job, form a DAG)
CREATE TABLE tasks (
    id          TEXT PRIMARY KEY,
    job_id      TEXT NOT NULL REFERENCES jobs(id),
    title       TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending',  -- pending, in_progress, completed, failed, blocked, cancelled
    agent_id    TEXT,  -- assigned agent
    parent_id   TEXT REFERENCES tasks(id),  -- DAG edge (nullable for root tasks)
    sort_order  INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    summary     TEXT,  -- completion summary or failure reason
    metadata    TEXT   -- JSON blob
);

-- Task dependencies (for DAG edges beyond simple parent-child)
CREATE TABLE task_deps (
    task_id     TEXT NOT NULL REFERENCES tasks(id),
    depends_on  TEXT NOT NULL REFERENCES tasks(id),
    PRIMARY KEY (task_id, depends_on)
);

-- Progress reports (from agents via MCP server or direct calls)
CREATE TABLE progress_reports (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id      TEXT NOT NULL REFERENCES jobs(id),
    task_id     TEXT REFERENCES tasks(id),
    agent_id    TEXT,
    status      TEXT NOT NULL,  -- in_progress, blocked, completed, failed
    message     TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Agents (registered agent definitions)
CREATE TABLE agents (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT,
    mode        TEXT,  -- coordinator, worker
    model       TEXT,  -- preferred model
    provider    TEXT,  -- preferred provider
    temperature REAL,
    system_prompt TEXT,
    tools       TEXT,  -- JSON array of allowed tool names
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    source      TEXT   -- 'file', 'database', 'template'
);

-- Teams
CREATE TABLE teams (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT,
    coordinator TEXT REFERENCES agents(id),
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    metadata    TEXT  -- JSON blob
);

-- Team members
CREATE TABLE team_members (
    team_id     TEXT NOT NULL REFERENCES teams(id),
    agent_id    TEXT NOT NULL REFERENCES agents(id),
    role        TEXT NOT NULL DEFAULT 'worker',  -- coordinator, worker
    PRIMARY KEY (team_id, agent_id)
);

-- Agent sessions (active LLM conversation loops)
CREATE TABLE agent_sessions (
    id          TEXT PRIMARY KEY,
    agent_id    TEXT NOT NULL REFERENCES agents(id),
    job_id      TEXT REFERENCES jobs(id),
    task_id     TEXT REFERENCES tasks(id),
    status      TEXT NOT NULL DEFAULT 'active',  -- active, completed, failed, cancelled
    model       TEXT NOT NULL,
    provider    TEXT NOT NULL,
    tokens_in   INTEGER NOT NULL DEFAULT 0,
    tokens_out  INTEGER NOT NULL DEFAULT 0,
    started_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ended_at    DATETIME,
    cost_usd    REAL  -- estimated cost
);

-- Artifacts (files produced by agents)
CREATE TABLE artifacts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id      TEXT NOT NULL REFERENCES jobs(id),
    task_id     TEXT REFERENCES tasks(id),
    type        TEXT NOT NULL,  -- code, report, investigation, test_results, other
    path        TEXT NOT NULL,
    summary     TEXT,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

**Go interface:**

```go
package db

type Store interface {
    // Jobs
    CreateJob(ctx context.Context, job *Job) error
    GetJob(ctx context.Context, id string) (*Job, error)
    ListJobs(ctx context.Context, filter JobFilter) ([]*Job, error)
    UpdateJobStatus(ctx context.Context, id string, status string) error

    // Tasks
    CreateTask(ctx context.Context, task *Task) error
    GetTask(ctx context.Context, id string) (*Task, error)
    ListTasksForJob(ctx context.Context, jobID string) ([]*Task, error)
    UpdateTaskStatus(ctx context.Context, id string, status string, summary string) error
    AddTaskDependency(ctx context.Context, taskID, dependsOn string) error
    GetReadyTasks(ctx context.Context, jobID string) ([]*Task, error)  // tasks with all deps met

    // Progress
    ReportProgress(ctx context.Context, report *ProgressReport) error
    GetRecentProgress(ctx context.Context, jobID string, limit int) ([]*ProgressReport, error)

    // Agents
    UpsertAgent(ctx context.Context, agent *Agent) error
    GetAgent(ctx context.Context, id string) (*Agent, error)
    ListAgents(ctx context.Context) ([]*Agent, error)

    // Teams
    CreateTeam(ctx context.Context, team *Team) error
    GetTeam(ctx context.Context, id string) (*Team, error)
    ListTeams(ctx context.Context) ([]*Team, error)

    // Sessions
    CreateSession(ctx context.Context, session *AgentSession) error
    UpdateSession(ctx context.Context, id string, update SessionUpdate) error
    GetActiveSessions(ctx context.Context) ([]*AgentSession, error)

    // Artifacts
    LogArtifact(ctx context.Context, artifact *Artifact) error
    ListArtifactsForJob(ctx context.Context, jobID string) ([]*Artifact, error)

    // Lifecycle
    Close() error
}
```

**Acceptance criteria:**
- `db.Open(path)` creates or opens a SQLite database with WAL mode.
- All CRUD operations work and are tested.
- Migrations run automatically on open (version table tracks schema version).
- Existing job `.md` files continue to work (read-only compatibility, not migration).
- The `internal/job` package can be updated to use the Store interface alongside or instead of file I/O.

---

### 1.2 — Multi-Provider LLM Client

**Effort:** 2–3 days  
**Depends on:** Nothing (can be done in parallel with 1.1)  
**Unlocks:** 1.3 (in-process agent runtime)

**What to build:**

A `internal/provider` package that abstracts LLM provider differences behind a common interface. The existing `internal/llm` client handles OpenAI-compatible APIs (LM Studio, Ollama, OpenAI). Add an Anthropic Messages API client.

**Provider interface:**

```go
package provider

type Provider interface {
    // ChatStream sends messages and streams the response.
    // Tools are optional — pass nil for a simple chat.
    ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)

    // Models returns available models from this provider.
    Models(ctx context.Context) ([]ModelInfo, error)

    // Name returns the provider identifier (e.g. "anthropic", "openai", "lmstudio").
    Name() string
}

type ChatRequest struct {
    Model       string
    Messages    []Message
    Tools       []Tool
    System      string   // system prompt
    MaxTokens   int
    Temperature *float64
    Stop        []string
}

type StreamEvent struct {
    Type      string  // "text", "tool_call", "usage", "done", "error"
    Text      string  // for "text" events
    ToolCall  *ToolCall  // for "tool_call" events
    Usage     *Usage     // for "usage" events
    Error     error      // for "error" events
}

type Message struct {
    Role       string
    Content    string
    ToolCalls  []ToolCall
    ToolCallID string  // for tool result messages
}
```

**Providers to implement:**

1. **OpenAI-compatible** — Refactor existing `internal/llm.Client` to implement the `Provider` interface. Covers LM Studio, Ollama, OpenAI, and any other OpenAI-compatible endpoint.

2. **Anthropic** — New client for the Anthropic Messages API (`POST /v1/messages`). SSE streaming. Handles the Anthropic-specific message format (content blocks, tool use blocks). API key auth via `x-api-key` header.

**Configuration:**

```yaml
providers:
  - name: lmstudio
    type: openai
    endpoint: http://localhost:1234
    # No API key needed for local

  - name: anthropic
    type: anthropic
    api_key: "${ANTHROPIC_API_KEY}"

  - name: openai
    type: openai
    endpoint: https://api.openai.com
    api_key: "${OPENAI_API_KEY}"

# Default provider for the operator
operator:
  provider: lmstudio
  model: ""  # use whatever is loaded

# Default provider for agents
agents:
  default_provider: anthropic
  default_model: claude-sonnet-4-20250514
```

**Acceptance criteria:**
- `provider.New(config)` returns the correct provider implementation based on `type`.
- OpenAI-compatible provider works with LM Studio (existing behavior preserved).
- Anthropic provider streams responses and handles tool calls.
- Both providers implement the same `Provider` interface.
- Provider selection is configurable per-agent (with a global default).
- API keys can be read from environment variables via `${ENV_VAR}` syntax.

---

### 1.3 — In-Process Agent Runtime

**Effort:** 3–5 days  
**Depends on:** 1.2 (multi-provider client)  
**Unlocks:** Everything that involves agents doing real work

**What to build:**

A `internal/runtime` package that runs agent sessions as goroutines. Each session is a conversation loop: send messages to the LLM, receive a response, if the response contains tool calls execute them and loop, otherwise the turn is done.

**The conversation loop:**

```go
func (s *Session) Run(ctx context.Context) error {
    for {
        // Send messages to LLM, stream response
        response, err := s.provider.ChatStream(ctx, ChatRequest{
            Model:    s.model,
            Messages: s.messages,
            Tools:    s.tools,
            System:   s.systemPrompt,
        })

        // Collect the full response (streaming to observers)
        assistantMsg, toolCalls, err := s.collectResponse(ctx, response)
        s.messages = append(s.messages, assistantMsg)

        // If no tool calls, the turn is done
        if len(toolCalls) == 0 {
            return nil
        }

        // Execute tool calls
        for _, call := range toolCalls {
            result, err := s.executeTool(ctx, call)
            s.messages = append(s.messages, toolResultMessage(call.ID, result, err))
        }

        // Loop — send tool results back to LLM
    }
}
```

**Core tool set:**

These are the tools that make an agent capable of doing real coding work. They mirror what Claude Code and OpenCode provide:

| Tool | Description | Implementation |
|------|-------------|----------------|
| `read_file` | Read a file (with offset/limit for large files) | `os.ReadFile` with line slicing |
| `write_file` | Write content to a file (create or overwrite) | `os.WriteFile` with directory creation |
| `edit_file` | Apply a targeted edit (old string → new string) | String replacement with uniqueness check |
| `glob` | Find files matching a glob pattern | `filepath.Glob` or `doublestar` library |
| `grep` | Search file contents with regex | `regexp` + file walking |
| `shell` | Execute a shell command, capture output | `exec.CommandContext` with timeout |
| `web_fetch` | HTTP GET a URL, return content | `http.Get` with timeout |
| `spawn_agent` | Create a child agent session | Recursive `Session.Run()` in a new goroutine |

**Tool execution:**

```go
type ToolExecutor interface {
    Execute(ctx context.Context, name string, args json.RawMessage) (string, error)
}

// CoreTools implements the standard tool set.
type CoreTools struct {
    workDir    string        // base directory for file operations
    allowShell bool          // whether shell execution is permitted
    runtime    *Runtime      // for spawn_agent
}
```

**Session management:**

```go
type Runtime struct {
    mu       sync.Mutex
    sessions map[string]*Session
    store    db.Store
    providers map[string]provider.Provider
}

func (r *Runtime) SpawnAgent(ctx context.Context, opts SpawnOpts) (*Session, error)
func (r *Runtime) GetSession(id string) (*Session, bool)
func (r *Runtime) CancelSession(id string) error
func (r *Runtime) ActiveSessions() []*SessionSnapshot
```

**Streaming to observers:**

Each session emits events (text deltas, tool calls, tool results, completion) that the TUI or other consumers can subscribe to:

```go
type SessionEvent struct {
    SessionID string
    Type      string  // "text", "tool_call", "tool_result", "done", "error"
    Text      string
    ToolCall  *ToolCall
    ToolResult *ToolResult
}

// Subscribe returns a channel that receives events for this session.
func (s *Session) Subscribe() <-chan SessionEvent
```

**Acceptance criteria:**
- An agent session can be spawned with a system prompt, tools, and a provider.
- The conversation loop runs: LLM responds → tool calls executed → results fed back → repeat until done.
- All core tools work: file read/write/edit, glob, grep, shell, web fetch.
- `spawn_agent` creates a child session that runs independently and returns its result.
- Sessions are tracked in SQLite (start time, token usage, status).
- Sessions can be cancelled via context.
- The TUI can subscribe to session events for streaming display.
- Existing Claude CLI subprocess path still works (not removed, just supplemented).

---

### 1.4 — Async Tool Execution Refactor

**Effort:** 1–2 days  
**Depends on:** Nothing (can be done in parallel with 1.1–1.3)  
**Unlocks:** 2.1 (MCP client needs non-blocking tool calls)

**What to build:**

Refactor `ToolCallMsg` handling in `tui.Update()` to execute tools in a goroutine rather than synchronously. This is a focused refactor of the existing TUI code.

**Changes:**

1. New message type: `ToolResultMsg` carrying results of completed tool calls.
2. New helper: `executeToolsCmd(calls)` returns a `tea.Cmd` that runs tool execution in a goroutine.
3. `ToolCallMsg` handler dispatches to the goroutine and returns immediately.
4. `ToolResultMsg` handler injects results into the conversation and re-invokes the LLM.
5. Visual feedback: show "calling tool..." indicators while tools execute.
6. Context cancellation: Escape cancels in-flight tool calls.

**Acceptance criteria:**
- TUI remains responsive during tool execution.
- `fetch_webpage` no longer blocks the event loop.
- Escape cancels in-flight tool calls.
- Tool results are injected in correct order.
- All existing tool behavior is preserved.

---

### Phase 1 Delivery Sequence

```
Week 1:
  1.1 (SQLite)  ─────────────────────►  done
  1.2 (Providers) ───────────────────►  done
  1.4 (Async tools) ─────►  done

Week 2:
  1.3 (Agent runtime) ──────────────────────────►  done
       depends on 1.2

Week 3 (buffer / polish):
  Wire 1.3 into TUI alongside existing Claude CLI path
  Wire 1.1 into existing job management
  Integration testing
```

**Phase 1 exit criteria:** You can type a task into the TUI, the operator dispatches it to an agent, the agent runs as an in-process goroutine talking to Anthropic's API, executes tools (reads files, writes code, runs tests), and the results are stored in SQLite. The Claude CLI subprocess path still works as a fallback.

---

## Phase 2: Connect to the World (Completed 2026-02-25)

**Goal:** Agents have access to external tools via MCP, and report progress back to Toasters through a structured protocol. The TUI shows real-time progress driven by database updates.

**Status:** ✅ Complete. All three deliverables built, integrated, and end-to-end verified. See `PHASE_2.md` for full implementation details.

**What was delivered:**

| Deliverable | Description | Status |
|-------------|-------------|--------|
| 2.1 — MCP Client | `internal/mcp` package with Manager, tool conversion, namespacing, operator + agent wiring | ✅ Done |
| 2.2 — Toasters MCP Server | `internal/progress` package with 6 progress tool handlers, MCP server, `toasters mcp-server` subcommand | ✅ Done |
| 2.3 — Real-Time TUI Progress | SQLite polling loop, task status rendering, blocker alerts, token usage display | ✅ Done |

**Post-delivery fixes:** 6 bug fixes applied after PRs merged — subagent TUI notifications, message history filtering, provider/model propagation to child agents, workspace-centric coordinator spawning, max spawn depth enforcement, and spawn_agent tool filter enforcement. Additionally, MCP TUI visibility enhancements added: `/mcp` modal, sidebar status, toast notifications, tool call annotations, smart result truncation/slimming (16KB default), and context bar fixes. See `PHASE_2.md` for details.

**Estimated total effort:** 1.5–2 weeks

---

### 2.1 — MCP Client (Consume External Servers)

**Effort:** 2–3 days  
**Depends on:** 1.4 (async tool execution)  
**Unlocks:** Agents can use GitHub, Jira, Linear, filesystem, git, and any other MCP server

**What to build:**

The `internal/mcp` package as described in `docs/mcp-integration-plan.md`, Parts A1–A3:

1. **Config schema** — `MCPServerConfig` and `MCPConfig` types in `internal/config`.
2. **Manager** — Connects to configured MCP servers at startup, discovers tools, dispatches calls.
3. **Tool conversion** — MCP tool definitions → `llm.Tool` / `provider.Tool` format.
4. **Namespacing** — `{server_name}__{tool_name}` to prevent collisions.
5. **Registration** — Merge MCP tools into the operator's and agents' available tool sets.
6. **Tool filtering** — Optional `enabled_tools` whitelist per server.

**Acceptance criteria:**
- With a GitHub MCP server configured, the operator sees `github__*` tools.
- Tool calls are routed to the correct MCP server.
- Failed servers are skipped with a warning.
- Tool filtering works (only whitelisted tools appear).
- Graceful shutdown closes all MCP server connections.

---

### 2.2 — Toasters MCP Server (Progress Reporting)

**Effort:** 2–3 days  
**Depends on:** 1.1 (SQLite), 1.3 (agent runtime)  
**Unlocks:** Real-time progress tracking, structured agent-to-orchestrator communication

**What to build:**

The Toasters MCP server as described in `docs/mcp-integration-plan.md`, Parts B1–B2:

1. **Tool handlers** — Go functions that implement `report_progress`, `report_blocker`, `update_task_status`, `request_review`, `query_job_context`, `log_artifact`. Each handler writes to SQLite via the `db.Store` interface.

2. **In-process integration** — For agents running as goroutines, the MCP server tools are just function calls added to the agent's tool set. No actual MCP protocol needed.

3. **External agent integration** — For Claude CLI subprocesses (backward compatibility), run an actual MCP server (using `mcp-go`'s server package) on a local port or Unix socket. Pass the server config to Claude via `--mcp-config`.

**Tool definitions:**

| Tool | Parameters | What it does |
|------|-----------|--------------|
| `report_progress` | job_id, task_id, status, message | Inserts a progress report into SQLite |
| `report_blocker` | job_id, task_id, blocker, severity | Inserts a blocker report, optionally alerts operator |
| `update_task_status` | job_id, task_id, status, summary | Updates task status in SQLite |
| `request_review` | job_id, task_id, artifact, reviewer | Creates a review request (future: triggers review workflow) |
| `query_job_context` | job_id, question | Returns job overview, task statuses, recent progress |
| `log_artifact` | job_id, task_id, type, path, summary | Records an artifact in SQLite |

**Acceptance criteria:**
- In-process agents can call progress tools and data appears in SQLite.
- Claude CLI subprocesses can discover and call the tools via MCP.
- `query_job_context` returns useful, structured information about a job.
- Progress reports include timestamps and are queryable by job/task.

---

### 2.3 — Real-Time TUI Progress Display

**Effort:** 1–2 days  
**Depends on:** 2.2 (MCP server writing to SQLite), 1.1 (SQLite)  
**Unlocks:** Users can see what agents are doing in real-time

**What to build:**

Wire the SQLite progress data into the TUI. The right panel's "Active Tasks" section and the left panel's task DAG visualization update in real-time as agents report progress.

**Approach:**

1. **Polling** (simple, v1): The TUI polls SQLite every 500ms for active job progress. Cheap because SQLite reads are fast and WAL mode means no contention with writers.

2. **Event-driven** (future): The agent runtime emits Bubble Tea messages when progress is reported. The TUI subscribes to these events. More responsive but more complex.

**What the TUI shows:**
- Task status indicators (pending → in_progress → completed/failed/blocked)
- Latest progress message per task
- Blocker alerts (highlighted, maybe with a notification)
- Token usage and cost per session (from `agent_sessions` table)
- Overall job progress (X of Y tasks complete)

**Acceptance criteria:**
- Task statuses update in the TUI within 1 second of an agent reporting progress.
- Blockers are visually highlighted.
- Job-level progress summary is accurate.
- No TUI jank from polling (reads are non-blocking).

---

### Phase 2 Delivery Sequence

```
Week 1:
  2.1 (MCP client) ──────────────────►  ✅ done
  2.2 (MCP server) ──────────────────►  ✅ done
       (can be parallel — different packages)

Week 2:
  2.3 (TUI progress) ────────►  ✅ done
  Integration testing + polish
```

**Phase 2 exit criteria:** All criteria met (2026-02-25). You can configure a GitHub MCP server, the operator uses `github__create_issue` to file a bug, agents report progress via `report_progress` / `update_task_status`, and the TUI shows real-time task status updates. Blockers are visible. Token usage is tracked.

---

## Phase 3: Teams & Agents

**Goal:** Build a complete teams and agents management system with composable agent definitions, curated teams, shared agents, and per-agent provider/model selection. Consolidate job persistence to SQLite-only.

**Status:** Planning. See `PHASE_3.md` for full details.

**Estimated total effort:** 1.5–2 weeks

---

### 3.1 — SQLite-Only Job Persistence

**Effort:** 1 day
**Depends on:** 1.1 (SQLite)

Stop dual-writing jobs to markdown files. SQLite is the sole source of truth for job state. Remove the `internal/job/` package or reduce it to read-only.

---

### 3.2 — Teams & Agents Management System

**Effort:** 1–2 weeks
**Depends on:** 1.1 (SQLite), 1.3 (agent runtime)
**Unlocks:** Reusable team compositions, composable agents, dynamic team assembly

The core Phase 3 deliverable. Key goals:
- Composable agent definitions (layered traits, role overlays, team-specific overrides)
- Agent generation (programmatic, not just hand-authored `.md` files)
- Curated teams for specific workflows
- Shared agents reusable across teams
- Per-agent provider/model selection
- TUI integration (`/teams` command, agents panel)

---

### Phase 3 Delivery Sequence

```
Week 1:
  3.1 (SQLite-only jobs) ────►  done
  3.2 design exploration ────────────────────►  in progress

Week 2:
  3.2 implementation ────────────────────────────────►  done
```

**Phase 3 exit criteria:** Job persistence is SQLite-only. You can define composable agent definitions, assemble curated teams with shared agents, assign per-agent providers/models, and the operator can discover and select teams for work assignment.

---

## Phase 4: Intelligence & Infrastructure

**Goal:** Cost visibility, external service integration, multi-repo ecosystems, operator memory, job personas, and a server/client architecture split for resilience.

**Status:** Future. See `PHASE_4.md` for full details.

**Estimated total effort:** 3–5 weeks

---

### Deliverables

| # | Deliverable | Effort | Key Dependencies |
|---|-------------|--------|-----------------|
| 4.1 | Cost Estimation | 1–2 days | Standalone |
| 4.2 | OpenAPI-to-MCP Bridges | 3–4 days | 2.1 (MCP client) |
| 4.3 | Server/Client Architecture Split | 5–7 days | Phases 1–3 stable |
| 4.4 | Ephemeral Ecosystems | 3–4 days | Standalone |
| 4.5 | Long-Lived Ecosystems | 4–5 days | 4.2, 4.4 |
| 4.6 | Operator Memory | 2–3 days | Standalone |
| 4.7 | Job Personas | 2–3 days | 4.6 |

**Phase 4 exit criteria:** Toasters runs as a persistent server. The TUI and CLI connect as clients. Jobs survive crashes. The operator dispatches multi-repo work across ecosystems, remembers past outcomes, and agents can query your backend services via OpenAPI bridges. Job personas provide queryable state. Cost is tracked and visible.

---

## Dependency Graph

```
1.1 SQLite ──────────────────┬──► 2.2 MCP Server ──► 2.3 TUI Progress
                             │
1.2 Providers ──► 1.3 Agent ─┤──► 2.2 MCP Server
                  Runtime    │
                             ├──► 3.2 Teams & Agents
                             │
                             ├──► 4.4 Ephemeral Ecosystems ──► 4.5 Long-lived Ecosystems
                             │
                             └──► 4.7 Job Personas

1.4 Async Tools ──► 2.1 MCP Client ──► 4.2 OpenAPI Bridges
                                  └──► 4.4 Ephemeral Ecosystems

1.1 SQLite ──► 3.1 SQLite-Only Jobs
          └──► 4.6 Operator Memory ──► 4.7 Job Personas

Phases 1–3 ──► 4.3 Server/Client Split
```

---

## Principles

1. **Each deliverable produces working code.** No deliverable is "just refactoring" or "just planning." Every item ends with something you can run and test.

2. **The existing system keeps working.** Claude CLI subprocess path is preserved as a fallback throughout Phase 1. Nothing is removed until the replacement is proven.

3. **SQLite is the source of truth for operational state.** Markdown files remain for human-readable artifacts. The database is not a cache — it's the primary store.

4. **MCP is the integration protocol.** External tools come in via MCP client. Agent progress goes out via MCP server. Backend services connect via OpenAPI bridges. One protocol for everything.

5. **Agents are goroutines, not subprocesses.** The in-process runtime is the target architecture. Claude CLI subprocesses are a compatibility layer, not the primary path.

6. **The operator gets smarter.** Every job outcome feeds back into the system. Team selection, workflow choice, and risk assessment improve over time.

7. **Ship incrementally.** Each phase is usable on its own. You don't need Phase 4 to get value from Phase 1. The platform gets more powerful as layers are added, but each layer stands alone.
