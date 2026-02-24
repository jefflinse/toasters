# MCP Integration Plan

**Status:** Draft (revised 2026-02-24)  
**Date:** 2026-02-23 (original), 2026-02-24 (revised)  
**Scope:** Three-part MCP strategy: (1) consume external MCP servers, (2) host a Toasters MCP server for agent progress reporting, (3) auto-generate ephemeral MCP servers from OpenAPI specs.

---

## Table of Contents

1. [Overview](#1-overview)
2. [Architecture](#2-architecture)
3. [Part A: MCP Client — Consuming External Servers](#3-part-a-mcp-client--consuming-external-servers)
4. [Part B: MCP Server — Agent Progress Reporting](#4-part-b-mcp-server--agent-progress-reporting)
5. [Part C: Ephemeral OpenAPI-to-MCP Bridges](#5-part-c-ephemeral-openapi-to-mcp-bridges)
6. [MCP Transport Support](#6-mcp-transport-support)
7. [Configuration Schema](#7-configuration-schema)
8. [Key Risks and Mitigations](#8-key-risks-and-mitigations)
9. [Phase Summary](#9-phase-summary)

---

## 1. Overview

Toasters' MCP integration has three parts:

**Part A — MCP Client.** Toasters connects to external MCP servers (GitHub, Jira, Linear, etc.), discovers their tools, and makes them available to the operator and agents. When a tool is called, Toasters routes the call to the appropriate MCP server and returns the result.

**Part B — MCP Server.** Toasters runs its own MCP server that agents connect to. This creates a structured, bidirectional communication channel: agents report progress, flag blockers, update task status, and query job context — all through MCP tools rather than file-based detection. Progress data flows directly into SQLite.

**Part C — OpenAPI-to-MCP Bridges.** Toasters can auto-generate ephemeral MCP servers from OpenAPI specs. Configure a URL + credentials + spec, and Toasters spins up a lightweight server that translates MCP tool calls into HTTP requests against the backend service. Scoped to a job or ecosystem, cleaned up automatically.

---

## 2. Architecture

### Full MCP Pipeline

```
                    ┌──────────────────────────────────┐
                    │         External MCP Servers       │
                    │  (GitHub, Jira, Linear, git, ...)  │
                    └──────────────┬───────────────────┘
                                   │ tools/call
                                   ▼
┌──────────────────────────────────────────────────────────┐
│                      TOASTERS                             │
│                                                           │
│  ┌─────────────┐    ┌──────────────┐    ┌──────────────┐ │
│  │  MCP Client  │    │  MCP Server   │    │  OpenAPI→MCP │ │
│  │  (Part A)    │    │  (Part B)     │    │  (Part C)    │ │
│  │              │    │              │    │              │ │
│  │ Connects to  │    │ Agents call  │    │ Auto-gen MCP │ │
│  │ external     │    │ these tools  │    │ from specs   │ │
│  │ servers      │    │ to report    │    │              │ │
│  └──────┬───────┘    └──────┬───────┘    └──────┬───────┘ │
│         │                   │                   │         │
│         ▼                   ▼                   ▼         │
│  ┌─────────────────────────────────────────────────────┐ │
│  │              Operator + Agent Sessions               │ │
│  │  (goroutines with LLM conversation loops)            │ │
│  │  Tools = static + MCP client + MCP server + OpenAPI  │ │
│  └─────────────────────────┬───────────────────────────┘ │
│                             │                             │
│                             ▼                             │
│                    ┌────────────────┐                     │
│                    │    SQLite DB    │                     │
│                    │  (all state)    │                     │
│                    └────────────────┘                     │
└──────────────────────────────────────────────────────────┘
```

---

## 3. Part A: MCP Client — Consuming External Servers

### Implementation Phases

#### A1: Config Schema

**Goal:** Add an `mcp` section to the config struct and YAML schema so MCP servers can be declared.

**Files affected:**
- `internal/config/config.go`

**Work:**

Add `MCPConfig` and `MCPServerConfig` types to the config package and wire them into the top-level `Config` struct:

```go
type MCPServerConfig struct {
    Name         string            `mapstructure:"name"`
    Transport    string            `mapstructure:"transport"` // "stdio", "http", or "sse"
    Command      string            `mapstructure:"command"`   // stdio only
    Args         []string          `mapstructure:"args"`      // stdio only
    Env          map[string]string `mapstructure:"env"`       // stdio only
    URL          string            `mapstructure:"url"`       // http/sse only
    Headers      map[string]string `mapstructure:"headers"`   // http/sse only
    Enabled      bool              `mapstructure:"enabled"`
    EnabledTools []string          `mapstructure:"enabled_tools"` // optional whitelist
}

type MCPConfig struct {
    Servers []MCPServerConfig `mapstructure:"servers"`
}
```

**Acceptance criteria:**
- `config.Load()` unmarshals an `mcp.servers` list without error.
- An empty or absent `mcp` section produces a zero-value `MCPConfig` with no error.
- Existing config files without an `mcp` key continue to load correctly.

---

#### A2: MCP Client Package

**Goal:** Create `internal/mcp/` — a package that manages connections to MCP servers, discovers their tools, and dispatches tool calls.

**Dependency:** `github.com/mark3labs/mcp-go`

**Key types:**

```go
type Manager struct {
    mu        sync.RWMutex
    servers   []serverEntry
    toolIndex map[string]int // namespaced tool name → server index
}

func (m *Manager) Connect(ctx context.Context, servers []config.MCPServerConfig) []llm.Tool
func (m *Manager) Call(ctx context.Context, toolName, argsJSON string) (string, error)
func (m *Manager) Tools() []llm.Tool
func (m *Manager) Close() error
```

Tool conversion is a direct field mapping — MCP's `inputSchema` is already JSON Schema, same as `llm.ToolFunction.Parameters`.

Tool namespacing: `{server_name}__{tool_name}`. Static tools always win in a collision.

**Acceptance criteria:**
- Connects to stdio and HTTP MCP servers.
- Dispatches tool calls to the correct server.
- Failed servers are skipped with a warning.
- Unknown tool names return a descriptive error.

---

#### A3: Tool Registration and Async Execution

**Goal:** Wire MCP tools into the operator's tool set and make all tool execution async.

**Key change:** `ToolCallMsg` handling in `tui.Update()` becomes asynchronous — dispatches to a goroutine and returns a `tea.Cmd` rather than blocking. This is required for MCP calls (network I/O) and fixes the existing blocking behavior for `fetch_webpage`.

**Acceptance criteria:**
- MCP tools appear in the operator's available tools after startup.
- TUI remains responsive during tool execution.
- Tool results are injected in correct order.
- Escape cancels in-flight tool calls.

---

## 4. Part B: MCP Server — Agent Progress Reporting

### Overview

Toasters runs its own MCP server. When agents are spawned (whether in-process or as Claude CLI subprocesses), they receive access to this server. Agents use it to report progress, flag blockers, update task status, and query job context.

This replaces file-based progress detection with structured, typed, immediate state updates that flow directly into SQLite.

### Tools Exposed

```
toasters__report_progress
    job_id:    string (required)
    task_id:   string (required)
    status:    enum [in_progress, blocked, completed, failed]
    message:   string (required) — what the agent is doing / just did

toasters__report_blocker
    job_id:    string (required)
    task_id:   string (required)
    blocker:   string (required) — description of what's blocking progress
    severity:  enum [low, medium, high, critical]

toasters__update_task_status
    job_id:    string (required)
    task_id:   string (required)
    status:    enum [pending, in_progress, completed, failed, cancelled]
    summary:   string (optional) — completion summary or failure reason

toasters__request_review
    job_id:    string (required)
    task_id:   string (required)
    artifact:  string (required) — path or description of what needs review
    reviewer:  string (optional) — specific agent/team to review

toasters__query_job_context
    job_id:    string (required)
    question:  string (optional) — specific question about the job
    → Returns: job overview, task statuses, recent progress, active blockers

toasters__log_artifact
    job_id:    string (required)
    task_id:   string (required)
    type:      enum [code, report, investigation, test_results, other]
    path:      string (required) — path to the artifact
    summary:   string (required) — brief description
```

### Implementation

**Transport:** stdio for in-process agents (direct pipe), HTTP for external Claude CLI subprocesses.

**For in-process agents:** The MCP server tools are just Go functions — no actual MCP protocol needed. The agent's tool execution loop calls them directly. The MCP server is only needed for external processes.

**For Claude CLI subprocesses:** Pass the MCP server config via `--mcp-config` so Claude can discover and call the tools. The server listens on a local port or Unix socket.

**Storage:** All progress data writes directly to SQLite. The TUI polls or subscribes to changes for real-time display.

### Implementation Phases

#### B1: Define the Toasters MCP Server Tools

Create the tool definitions and the Go handler functions that write to SQLite. These handlers work for both in-process agents (called directly) and external agents (called via MCP protocol).

#### B2: MCP Server for External Agents

Use `mcp-go`'s server package to expose the tools over stdio or HTTP. Wire into the agent spawn path so Claude CLI subprocesses receive the `--mcp-config` flag pointing at the server.

#### B3: Wire into TUI

Subscribe to SQLite changes (or poll) to update the TUI's task status display, progress indicators, and blocker alerts in real-time.

---

## 5. Part C: Ephemeral OpenAPI-to-MCP Bridges

### Overview

Given an OpenAPI spec (URL or file path) and credentials, Toasters auto-generates an MCP server that can query the described service. Each API endpoint becomes an MCP tool. The server is ephemeral — scoped to a job or ecosystem, torn down when no longer needed.

### How It Works

1. **Parse** the OpenAPI spec (JSON or YAML, v3.x)
2. **Convert** each endpoint to an MCP tool:
   - Tool name: `{operationId}` or `{method}_{sanitized_path}`
   - Description: from the spec's `summary` / `description`
   - Input schema: from the spec's `requestBody` + `parameters`
3. **Spin up** a lightweight HTTP server that:
   - Receives MCP `tools/call` requests
   - Translates them into HTTP requests to the actual backend
   - Injects configured auth headers/tokens
   - Returns the response as MCP tool result
4. **Register** with the MCP manager as a new server entry
5. **Tear down** when the job/ecosystem completes

### Configuration

```yaml
mcp:
  openapi_bridges:
    - name: user-service
      spec: https://api.example.com/openapi.json  # or local file path
      base_url: https://api.example.com
      auth:
        type: bearer  # or basic, api_key, header
        token: "${USER_SERVICE_TOKEN}"
      enabled_operations:  # optional whitelist
        - getUser
        - listUsers
        - getUserOrders
```

### Implementation Phases

#### C1: OpenAPI Parser

Parse OpenAPI v3.x specs and extract operations with their schemas. Pure data transformation, no MCP involvement yet.

#### C2: Operation-to-MCP Tool Converter

Convert parsed operations into MCP tool definitions. Handle path parameters, query parameters, request bodies, and auth injection.

#### C3: Ephemeral MCP Server

Spin up a lightweight MCP server (HTTP transport) that proxies tool calls to the actual backend service. Register with the MCP manager. Lifecycle tied to job/ecosystem.

---

## 6. MCP Transport Support

The MCP specification defines two standard transports:

**stdio** — Client launches the MCP server as a subprocess, communicates over stdin/stdout. Most common for locally-installed servers.

**Streamable HTTP** — Server runs independently, accepts HTTP POST requests. Used for remote/hosted servers.

**HTTP+SSE** (legacy) — Older transport with separate SSE and POST endpoints. Supported for backward compatibility.

`mcp-go` provides all three with a uniform client interface. The config `transport` field selects the constructor.

For the Toasters MCP server (Part B), stdio is used for in-process agents and HTTP for external subprocesses.

---

## 7. Configuration Schema

Complete example with all three MCP parts:

```yaml
operator:
  endpoint: http://localhost:1234
  model: ""
  teams_dir: ~/.config/toasters/teams

claude:
  path: claude
  default_model: ""
  permission_mode: ""

mcp:
  # Part A: External MCP servers
  servers:
    - name: github
      transport: stdio
      command: npx
      args: ["-y", "@modelcontextprotocol/server-github"]
      env:
        GITHUB_PERSONAL_ACCESS_TOKEN: "${GITHUB_TOKEN}"
      enabled: true
      enabled_tools:
        - create_issue
        - list_issues
        - create_pull_request

    - name: linear
      transport: http
      url: https://mcp.linear.app/mcp
      headers:
        Authorization: "Bearer ${LINEAR_TOKEN}"
      enabled: true

  # Part C: OpenAPI bridges
  openapi_bridges:
    - name: user-service
      spec: https://api.example.com/openapi.json
      base_url: https://api.example.com
      auth:
        type: bearer
        token: "${USER_SERVICE_TOKEN}"
      enabled_operations:
        - getUser
        - listUsers
```

The Toasters MCP server (Part B) requires no user configuration — it starts automatically and is injected into agent sessions by the runtime.

---

## 8. Key Risks and Mitigations

**Risk: Synchronous tool execution blocks the TUI.**
Mitigated by making all tool execution async (Part A3). Must be completed before MCP tools are wired in.

**Risk: MCP server startup failure.**
Each server connection is independent. Failed servers are logged and skipped.

**Risk: Tool name collisions.**
Namespacing with `{server_name}__` prefix. Static tools always win.

**Risk: Token budget exhaustion from too many tools.**
Per-server `enabled_tools` whitelists. Optional `max_tools` global cap.

**Risk: stdio server process lifecycle.**
`Manager.Close()` called during graceful shutdown. `mcp-go` closes stdin to signal subprocess exit.

**Risk: Credentials in config.**
Support `${ENV_VAR}` syntax for reading values from environment variables. Document `chmod 600` for config file.

**Risk: OpenAPI spec complexity.**
Start with simple REST APIs (JSON request/response). Complex specs with polymorphism, webhooks, or streaming are out of scope for v1.

**Risk: Toasters MCP server availability.**
If the server crashes, agents lose the ability to report progress but continue working. Progress is best-effort, not a hard dependency on agent execution.

---

## 9. Phase Summary

| Phase | Description | Effort | Depends On |
|-------|-------------|--------|------------|
| A1 | Config schema for MCP servers | 1–2 hours | — |
| A2 | MCP client package (connect, discover, dispatch) | 1–2 days | A1 |
| A3 | Async tool execution + registration at startup | 1–2 days | A2 |
| B1 | Toasters MCP server tool definitions + handlers | 1 day | SQLite layer |
| B2 | MCP server for external agents (stdio/HTTP) | 1–2 days | B1 |
| B3 | Wire progress into TUI | 1 day | B2 |
| C1 | OpenAPI spec parser | 1 day | — |
| C2 | Operation-to-MCP tool converter | 1 day | C1 |
| C3 | Ephemeral MCP server + lifecycle management | 1–2 days | C2, A2 |

**Recommended order:** A1 → A2 → A3 (gets MCP client working) → B1 → B2 → B3 (gets progress reporting working) → C1 → C2 → C3 (gets OpenAPI bridges working).

Parts A, B, and C are largely independent and can be worked on in parallel once their respective dependencies are met.
