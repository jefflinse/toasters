# Toasters — Master Roadmap

**Created:** 2026-02-24  
**Status:** Active  

This is the master plan for evolving Toasters from a TUI prototype into a full agentic orchestration platform. Work is organized into four phases, each containing well-scoped deliverables that build on each other. Each deliverable is designed to be completable in 1–5 days and to produce a working, testable result.

---

## Table of Contents

- [Pre-Phase 1: Code Health](#pre-phase-1-code-health-completed-2026-02-24)
- [Phase 1: The Foundation](#phase-1-the-foundation)
- [Phase 2: Connect to the World](#phase-2-connect-to-the-world)
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

## Phase 1: The Foundation

**Goal:** Toasters is a standalone agentic tool that talks directly to LLM providers, manages its own state in SQLite, and runs agents as in-process goroutines with a full tool set. No dependency on Claude CLI for core operation.

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

## Phase 2: Connect to the World

**Goal:** Agents have access to external tools via MCP, and report progress back to Toasters through a structured protocol. The TUI shows real-time progress driven by database updates.

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
  2.1 (MCP client) ──────────────────►  done
  2.2 (MCP server) ──────────────────►  done
       (can be parallel — different packages)

Week 2:
  2.3 (TUI progress) ────────►  done
  Integration testing + polish
```

**Phase 2 exit criteria:** You can configure a GitHub MCP server, the operator uses `github__create_issue` to file a bug, agents report progress via `report_progress` / `update_task_status`, and the TUI shows real-time task status updates. Blockers are visible. Token usage is tracked.

---

## Phase 3: Structure and Polish

**Goal:** The platform is configurable with team templates and workflows, and can connect to your backend services via auto-generated MCP bridges.

**Estimated total effort:** 1.5–2 weeks

---

### 3.1 — Team Templates and Workflows

**Effort:** 2–3 days  
**Depends on:** 1.1 (SQLite), 1.3 (agent runtime)  
**Unlocks:** Reusable team compositions, structured job execution

**What to build:**

1. **Team templates** — Predefined team compositions stored in SQLite (or YAML files). A template defines: name, description, coordinator agent, worker agents, default MCP servers, and default workflow.

   ```yaml
   # ~/.config/toasters/teams/coding.yaml
   name: coding
   description: General-purpose coding team
   coordinator: planner
   workers:
     - builder
     - reviewer
     - test-writer
   mcp_servers:
     - github
     - filesystem
   workflow: standard-dev
   ```

2. **Workflows** — A workflow defines the phases of a job and what happens in each phase. Stored in SQLite or YAML.

   ```yaml
   name: standard-dev
   phases:
     - name: planning
       agent: coordinator
       gate: operator_approval  # operator must approve the plan
     - name: implementation
       agents: [builder]
       parallel: true  # multiple builders can work concurrently
     - name: review
       agent: reviewer
       gate: auto  # proceeds automatically if review passes
     - name: testing
       agent: test-writer
       gate: auto
   ```

3. **Operator integration** — The operator selects a team template and workflow when dispatching a job. The runtime executes the workflow phases in order, respecting gates.

**Acceptance criteria:**
- Team templates can be defined in YAML files and loaded at startup.
- Workflows define phases with agent assignments and gates.
- The operator can select a team + workflow when dispatching a job.
- Phases execute in order; gates pause execution until conditions are met.
- Teams and workflows are stored in SQLite for runtime access.

---

### 3.2 — Ephemeral OpenAPI-to-MCP Bridges

**Effort:** 3–4 days  
**Depends on:** 2.1 (MCP client)  
**Unlocks:** Agents can query your actual backend services

**What to build:**

A `internal/mcp/openapi` package that:

1. **Parses OpenAPI v3.x specs** — Extract operations, parameters, request bodies, response schemas. Support both JSON and YAML specs, loaded from URL or file path.

2. **Converts operations to MCP tools** — Each operation becomes a tool. Tool name from `operationId` (or `method_path` fallback). Input schema from parameters + request body. Description from summary/description.

3. **Runs an ephemeral HTTP server** — Receives MCP `tools/call` requests, translates to HTTP requests against the actual backend, injects auth, returns the response.

4. **Lifecycle management** — Bridges are created on demand (when a job or ecosystem needs them) and torn down when no longer needed. Registered with the MCP manager as dynamic server entries.

**Scope limitations for v1:**
- JSON request/response bodies only (no multipart, no streaming)
- Path parameters, query parameters, and request body supported
- Auth: Bearer token, API key (header or query), Basic auth
- No OAuth flows (static credentials only)
- No webhooks or WebSocket endpoints

**Acceptance criteria:**
- Given an OpenAPI spec URL and credentials, a bridge MCP server starts and registers its tools.
- Agents can call the bridge tools and receive actual HTTP responses from the backend.
- Auth headers are injected correctly.
- The bridge shuts down cleanly when the job/ecosystem completes.
- Invalid or unsupported operations are skipped with warnings.

---

### Phase 3 Delivery Sequence

```
Week 1:
  3.1 (Team templates + workflows) ──────────────────►  done

Week 2:
  3.2 (OpenAPI bridges) ────────────────────────────►  done
```

**Phase 3 exit criteria:** You can define a "coding team" template with a standard-dev workflow, dispatch a job to it, and the workflow executes through planning → implementation → review → testing phases. You can also point Toasters at your user service's OpenAPI spec and agents can query it.

---

## Phase 4: Intelligence

**Goal:** The system has persistent knowledge, supports multi-repo work, and gets smarter over time. The architecture supports resilience and multiple clients.

**Estimated total effort:** 3–5 weeks

---

### 4.1 — Server/Client Architecture Split

**Effort:** 5–7 days  
**Depends on:** Phases 1–3 (everything should be stable first)  
**Unlocks:** Resilience, multiple clients, remote operation

**What to build:**

Extract the orchestration engine into a long-running server process. The TUI becomes a thin client.

1. **Server process** — Owns: SQLite database, agent runtime, MCP connections, job lifecycle, provider connections. Exposes a gRPC (or WebSocket) API for clients.

2. **Client API:**
   - `SubmitJob(request)` → job ID
   - `GetJob(id)` → job state
   - `ListJobs(filter)` → job list
   - `SubscribeJobEvents(id)` → stream of progress events
   - `SubscribeAllEvents()` → stream of all events (for TUI dashboard)
   - `SendMessage(message)` → operator response
   - `CancelJob(id)`
   - `GetActiveSessions()` → session snapshots

3. **TUI client** — Connects to the server, subscribes to events, renders state, sends commands. All business logic removed from the TUI.

4. **CLI client** — Simple command-line client for submitting jobs and checking status without the TUI.

**Acceptance criteria:**
- Server starts independently and persists across TUI restarts.
- TUI connects to a running server and displays current state.
- Jobs survive TUI crashes.
- CLI client can submit jobs and query status.
- Server handles graceful shutdown (cancels agents, closes MCP connections, closes database).

---

### 4.2 — Ephemeral Ecosystems

**Effort:** 3–4 days  
**Depends on:** 1.1 (SQLite), 1.3 (agent runtime), 2.1 (MCP client)  
**Unlocks:** Multi-repo work

**What to build:**

1. **Ecosystem definition** — A job can declare "I need repos X, Y, Z." Stored in SQLite.

   ```yaml
   ecosystem:
     repos:
       - url: git@github.com:company/api-service.git
         role: api  # human-readable role label
       - url: git@github.com:company/frontend.git
         role: frontend
       - url: git@github.com:company/shared-contracts.git
         role: contracts
   ```

2. **Workspace setup** — Clone repos into a workspace directory (e.g. `~/.config/toasters/workspaces/{ecosystem-id}/`). Each repo gets its own subdirectory.

3. **Cross-repo context** — Agents receive context about the ecosystem: which repos exist, what role each plays, how they relate. This is injected into the agent's system prompt.

4. **Cleanup** — When a job completes, the workspace can be archived or deleted (configurable).

**Acceptance criteria:**
- A job can declare an ecosystem with multiple repos.
- Repos are cloned into a workspace directory.
- Agents receive ecosystem context and can work across repos.
- Workspaces are cleaned up when jobs complete.

---

### 4.3 — Long-Lived Ecosystems

**Effort:** 4–5 days  
**Depends on:** 4.2 (ephemeral ecosystems), 3.2 (OpenAPI bridges)  
**Unlocks:** Persistent knowledge of your engineering surface

**What to build:**

1. **Persistent ecosystem definitions** — Stored in SQLite. Define services, their repos, their APIs (OpenAPI specs), their relationships, and their MCP servers.

   ```yaml
   name: backend
   description: Company backend services
   services:
     - name: user-service
       repo: git@github.com:company/user-service.git
       api_spec: https://api.company.com/user/openapi.json
       description: User management, auth, profiles
       depends_on: [database, cache]
     - name: order-service
       repo: git@github.com:company/order-service.git
       api_spec: https://api.company.com/orders/openapi.json
       depends_on: [user-service, database, payment-gateway]
     - name: database
       type: infrastructure
       mcp_server: postgres  # references a configured MCP server
   ```

2. **On-demand loading** — Ecosystems are loaded into memory when needed (not all at startup). The operator can query ecosystem metadata without loading full repo clones.

3. **Knowledge queries** — The operator can ask "which services would be affected if I change the user ID format?" and get an answer based on the dependency graph and service descriptions.

4. **Auto-bridge setup** — When a job references an ecosystem, OpenAPI bridges are automatically spun up for the relevant services.

**Acceptance criteria:**
- Ecosystems can be defined and persisted.
- The operator can query ecosystem metadata.
- Dependency graphs are navigable.
- OpenAPI bridges are auto-created for ecosystem services.

---

### 4.4 — Operator Memory

**Effort:** 2–3 days  
**Depends on:** 1.1 (SQLite)  
**Unlocks:** The operator gets smarter over time

**What to build:**

1. **Memory storage** — A `memories` table in SQLite:

   ```sql
   CREATE TABLE memories (
       id          INTEGER PRIMARY KEY AUTOINCREMENT,
       type        TEXT NOT NULL,  -- job_outcome, team_performance, repo_quirk, pattern
       content     TEXT NOT NULL,  -- structured JSON
       relevance   TEXT,           -- tags for retrieval (e.g. "auth,security,user-service")
       created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
   );
   ```

2. **Memory capture** — When a job completes, distill key learnings:
   - Which team was assigned? Did it succeed?
   - Were there blockers? What resolved them?
   - Which repos were involved? Any quirks discovered?
   - How long did it take? What was the cost?

3. **Memory retrieval** — When the operator dispatches a new job, query for relevant memories based on job type, repos involved, and keywords. Inject the top N memories into the operator's system prompt.

4. **Memory decay** — Old memories are weighted less. Contradicted memories (e.g. "this team fails at X" followed by "this team succeeded at X") are reconciled.

**Acceptance criteria:**
- Job outcomes are automatically captured as memories.
- Relevant memories are retrieved and injected into the operator's context.
- The operator's dispatching decisions improve based on past experience.
- Memory storage grows bounded (old/irrelevant memories are pruned).

---

### 4.5 — Job Personas

**Effort:** 2–3 days  
**Depends on:** 1.3 (agent runtime), 4.4 (operator memory)  
**Unlocks:** Queryable job state without re-reading artifacts

**What to build:**

1. **Persona session** — Each active job gets a dedicated LLM session (lightweight, cheap model) that accumulates context as the job progresses. Fed with: job overview, task updates, progress reports, blocker alerts.

2. **Queryable** — The operator can ask a job persona: "what's your current status?", "what's blocking you?", "summarize what you've done so far." The persona answers from its accumulated context.

3. **Knowledge distillation** — When a job completes, the persona produces a structured summary that feeds into operator memory (4.4).

**Acceptance criteria:**
- Active jobs have a persona session that tracks progress.
- The operator can query personas and get informed answers.
- Completed job personas produce summaries for operator memory.
- Persona sessions use a cheap model (not the expensive agent model).

---

### Phase 4 Delivery Sequence

```
Week 1–2:
  4.1 (Server/client split) ──────────────────────────────────►  done

Week 2–3:
  4.2 (Ephemeral ecosystems) ─────────────────►  done
  4.4 (Operator memory) ──────────────►  done
       (can be parallel)

Week 3–4:
  4.3 (Long-lived ecosystems) ────────────────────►  done
       depends on 4.2

Week 4–5:
  4.5 (Job personas) ─────────────►  done
       depends on 4.4
```

**Phase 4 exit criteria:** Toasters runs as a persistent server. The TUI and CLI connect as clients. Jobs survive crashes. The operator dispatches multi-repo work across ecosystems, remembers past outcomes, and agents can query your backend services. Job personas provide queryable state.

---

## Dependency Graph

```
1.1 SQLite ──────────────────┬──► 2.2 MCP Server ──► 2.3 TUI Progress
                             │
1.2 Providers ──► 1.3 Agent ─┤──► 2.2 MCP Server
                  Runtime    │
                             ├──► 3.1 Teams/Workflows
                             │
                             ├──► 4.2 Ephemeral Ecosystems ──► 4.3 Long-lived Ecosystems
                             │
                             └──► 4.5 Job Personas

1.4 Async Tools ──► 2.1 MCP Client ──► 3.2 OpenAPI Bridges
                                  └──► 4.2 Ephemeral Ecosystems

1.1 SQLite ──► 4.4 Operator Memory ──► 4.5 Job Personas

Phases 1–3 ──► 4.1 Server/Client Split
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
