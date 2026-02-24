# MCP Integration Plan

**Status:** Draft  
**Date:** 2026-02-23  
**Scope:** Operator LLM consumes tools from external MCP servers. We are not building an MCP server.

---

## Table of Contents

1. [Overview](#1-overview)
2. [Architecture](#2-architecture)
3. [Implementation Phases](#3-implementation-phases)
4. [MCP Transport Support](#4-mcp-transport-support)
5. [Configuration Schema](#5-configuration-schema)
6. [Key Risks and Mitigations](#6-key-risks-and-mitigations)
7. [Out of Scope](#7-out-of-scope)
8. [Phase Summary](#8-phase-summary)

---

## 1. Overview

Toasters' operator LLM (running in LM Studio) coordinates agentic work by calling tools. Today those tools are defined statically in `internal/llm/tools.go` and cover job management, team dispatch, and a handful of utility functions. The goal of this integration is to let the operator call tools exposed by external **MCP servers** — GitHub, Jira, Linear, or any other server that speaks the [Model Context Protocol](https://modelcontextprotocol.io).

The operator already uses the OpenAI function-calling format. MCP tools also use JSON Schema for their parameter definitions. The conversion between the two formats is nearly mechanical, which makes this integration tractable without touching the LLM client layer at all.

**What we are building:** An MCP *client* inside Toasters. At startup, Toasters connects to each configured MCP server, fetches its tool list, converts those tools into `llm.Tool` structs, and merges them into the operator's available tool set. When the operator calls one of those tools, Toasters routes the call to the appropriate MCP server and returns the result.

**What we are not building:** An MCP server. Toasters will not expose its own tools over MCP to any external consumer.

---

## 2. Architecture

### Current Tool-Calling Pipeline

```
User input
    │
    ▼
llm.Client.ChatCompletionStreamWithTools(messages, llm.AvailableTools)
    │
    │  SSE stream (tool_calls finish_reason)
    ▼
tui.Update() receives ToolCallMsg
    │
    ├─► kill_slot / assign_team / ask_user / escalate_to_user
    │       └─► prompt mode (user interaction, no ExecuteTool)
    │
    └─► all other tools
            └─► llm.ExecuteTool(call)   ← synchronous, blocks Update()
                    │
                    ├─► switch on call.Function.Name
                    │       known cases: fetch_webpage, job_*, list_slots, …
                    │       default:     return "unknown tool" error
                    │
                    └─► result injected as {role:"tool"} message
                            └─► startStream(messages)  ← re-invokes LLM
```

### Proposed Pipeline with MCP

```
Startup (cmd/root.go)
    │
    ├─► mcp.Manager.Connect(cfg.MCP.Servers)
    │       ├─► for each server: Start() → Initialize() → ListTools()
    │       └─► convert mcp.Tool → llm.Tool (with namespace prefix)
    │
    └─► llm.SetAvailableTools(staticTools + mcpTools)

                        ┌─────────────────────────────────┐
                        │  operator LLM (LM Studio)        │
                        │  sees full merged tool list       │
                        └──────────────┬──────────────────┘
                                       │ tool_calls
                                       ▼
                              tui.Update() ToolCallMsg
                                       │
                          ┌────────────┴────────────┐
                          │                         │
                   intercepted tools          all other tools
                   (kill_slot, etc.)               │
                          │                         ▼
                   prompt mode            tea.Cmd (goroutine)
                                               │
                                    ┌──────────┴──────────┐
                                    │                      │
                              static tool           MCP tool
                              ExecuteTool()    mcp.Manager.Call()
                                    │                      │
                                    └──────────┬──────────┘
                                               │ result
                                               ▼
                                    ToolResultMsg → inject {role:"tool"}
                                               │
                                               └─► startStream(messages)
```

The key structural change is that `ToolCallMsg` handling in `tui.Update()` must become **asynchronous** — it must dispatch tool execution to a goroutine and return a `tea.Cmd` rather than blocking. This is necessary for MCP calls (which involve network I/O) but also fixes a latent bug for the existing `fetch_webpage` tool, which already does HTTP and currently blocks the event loop.

---

## 3. Implementation Phases

### Phase 1: Config Schema

**Goal:** Add an `mcp` section to the config struct and YAML schema so MCP servers can be declared.

**Files affected:**
- `internal/config/config.go`

**Work:**

Add `MCPConfig` and `MCPServerConfig` types to the config package and wire them into the top-level `Config` struct:

```go
// MCPServerConfig holds configuration for a single MCP server.
type MCPServerConfig struct {
    Name      string            `mapstructure:"name"`
    Transport string            `mapstructure:"transport"` // "stdio" or "http"
    Command   string            `mapstructure:"command"`   // stdio only: executable path
    Args      []string          `mapstructure:"args"`      // stdio only: arguments
    Env       map[string]string `mapstructure:"env"`       // stdio only: extra env vars
    URL       string            `mapstructure:"url"`       // http only: server URL
    Headers   map[string]string `mapstructure:"headers"`   // http only: request headers
    Enabled   bool              `mapstructure:"enabled"`
}

// MCPConfig holds configuration for all MCP server connections.
type MCPConfig struct {
    Servers []MCPServerConfig `mapstructure:"servers"`
}
```

Add `MCP MCPConfig \`mapstructure:"mcp"\`` to the `Config` struct. No Viper defaults are needed — an absent `mcp` section means no MCP servers, which is the correct zero value.

**Acceptance criteria:**
- `config.Load()` unmarshals an `mcp.servers` list without error.
- An empty or absent `mcp` section produces a zero-value `MCPConfig` with no error.
- Existing config files without an `mcp` key continue to load correctly.

---

### Phase 2: MCP Client Package

**Goal:** Create `internal/mcp/` — a package that manages connections to MCP servers, discovers their tools, and dispatches tool calls to them.

**Files affected:**
- `internal/mcp/manager.go` (new)
- `internal/mcp/convert.go` (new)

**Dependency:**

Use [`github.com/mark3labs/mcp-go`](https://pkg.go.dev/github.com/mark3labs/mcp-go) as the MCP client library. It provides `client.NewStdioMCPClient`, `client.NewStreamableHttpClient`, and `client.NewSSEMCPClient`, all of which expose a uniform `client.Client` interface with `Start()`, `Initialize()`, `ListTools()`, and `CallTool()` methods.

```
go get github.com/mark3labs/mcp-go
```

**Manager:**

```go
// Package mcp manages connections to external MCP servers and provides
// tool discovery and dispatch for the operator LLM.
package mcp

import (
    "context"
    "fmt"
    "sync"

    mcpclient "github.com/mark3labs/mcp-go/client"
    mcptypes  "github.com/mark3labs/mcp-go/mcp"

    "github.com/jefflinse/toasters/internal/config"
    "github.com/jefflinse/toasters/internal/llm"
)

// serverEntry holds a connected MCP client and the namespace prefix
// used to disambiguate its tools from other servers.
type serverEntry struct {
    name   string
    prefix string // e.g. "github__"
    client *mcpclient.Client
}

// Manager connects to MCP servers, discovers their tools, and dispatches
// tool calls. Safe for concurrent use after Connect returns.
type Manager struct {
    mu      sync.RWMutex
    servers []serverEntry
    // toolIndex maps namespaced tool name → server index for O(1) dispatch.
    toolIndex map[string]int
}

// Connect initializes connections to all enabled MCP servers in cfg.
// It is called once at startup. Servers that fail to connect are logged
// and skipped — they do not prevent other servers from loading.
func (m *Manager) Connect(ctx context.Context, servers []config.MCPServerConfig) []llm.Tool

// Call dispatches a tool call to the appropriate MCP server and returns
// the result as a plain string. Returns an error if the tool is unknown
// or the server call fails.
func (m *Manager) Call(ctx context.Context, toolName, argsJSON string) (string, error)

// Tools returns the current set of discovered MCP tools as llm.Tool values,
// ready to be merged into llm.AvailableTools.
func (m *Manager) Tools() []llm.Tool
```

**Tool conversion** (`convert.go`):

MCP's `tools/list` response returns tools with an `inputSchema` field that is already a JSON Schema object — the same format that `llm.ToolFunction.Parameters` accepts. The conversion is a direct field mapping:

```go
// convertTool converts an MCP tool definition to the llm.Tool format used
// by the operator LLM. The namespacedName argument is the already-prefixed
// tool name (e.g. "github__create_issue").
func convertTool(t mcptypes.Tool, namespacedName string) llm.Tool {
    return llm.Tool{
        Type: "function",
        Function: llm.ToolFunction{
            Name:        namespacedName,
            Description: t.Description,
            Parameters:  t.InputSchema, // already map[string]any / JSON Schema
        },
    }
}
```

**Result extraction:**

MCP `tools/call` returns a `*mcp.CallToolResult` whose `Content` field is a slice of content items (text, image, etc.). For v1, concatenate all text content items into a single string and return it. Non-text content (images, audio) should be represented as a placeholder string noting the content type.

**Acceptance criteria:**
- `Manager.Connect()` connects to a stdio MCP server (e.g. a local test binary) and returns its tools as `[]llm.Tool`.
- `Manager.Call()` dispatches to the correct server and returns the tool result as a string.
- A server that fails to start is skipped with a log warning; other servers are unaffected.
- `Manager.Call()` with an unknown tool name returns a descriptive error.

---

### Phase 3: Tool Registration at Startup

**Goal:** Wire the MCP manager into `cmd/root.go` so that discovered MCP tools are merged into `llm.AvailableTools` before the TUI starts.

**Files affected:**
- `cmd/root.go`

**Work:**

After loading config and before creating the TUI model, instantiate the MCP manager and connect to servers:

```go
// In runTUI(), after cfg is loaded:

mcpManager := &mcp.Manager{}
mcpTools := mcpManager.Connect(context.Background(), cfg.MCP.Servers)

// Merge static tools with MCP tools.
allTools := append(llm.AvailableTools, mcpTools...)
llm.SetAvailableTools(allTools)

// Wire the manager into ExecuteTool's dispatch path.
llm.SetMCPManager(mcpManager)
```

`llm.SetMCPManager` is a new package-level setter (analogous to `SetGateway`) that stores the manager in a package-level variable used by the `default:` case in `ExecuteTool`. This keeps the MCP dependency out of the TUI package.

The `Connect` call happens synchronously before `tea.NewProgram` runs. For v1, this is acceptable — startup latency is bounded by the number of MCP servers and their initialization time. If startup latency becomes a problem, this can be moved to a goroutine that sends an `MCPReadyMsg` (similar to `AppReadyMsg`), but that adds complexity that is not warranted yet.

**Acceptance criteria:**
- With a GitHub MCP server configured, `llm.AvailableTools` contains `github__*` tools after startup.
- The operator's system prompt receives the full merged tool list on every request.
- Removing all `mcp.servers` entries from config produces identical behavior to today.

---

### Phase 4: Async Tool Execution

**Goal:** Refactor `ToolCallMsg` handling in `tui.Update()` to execute tools in a goroutine rather than synchronously. This is required for MCP calls (network I/O) and fixes the existing blocking behavior for `fetch_webpage`.

**Files affected:**
- `internal/tui/model.go`
- `internal/llm/tools.go` (minor: `ExecuteTool` signature may gain a `context.Context`)

**The problem in detail:**

Currently, `tui.Update()` handles `ToolCallMsg` by calling `llm.ExecuteTool(call)` directly in the message handler. Bubble Tea's `Update()` runs on the main goroutine and must return quickly. Any blocking call inside `Update()` freezes the entire TUI — no redraws, no input handling, no agent output updates — for the duration of the call. `fetch_webpage` already violates this contract (10-second HTTP timeout). MCP calls over a network will make this much worse.

**Refactor approach:**

Introduce a `ToolResultMsg` type and a `executeToolsCmd` helper that runs tool execution in a goroutine:

```go
// ToolResultMsg carries the results of one or more completed tool calls.
type ToolResultMsg struct {
    // Results is parallel to the Calls slice from the original ToolCallMsg.
    Results []toolResult
}

type toolResult struct {
    Call   llm.ToolCall
    Result string // empty string on error
    Err    error
}
```

```go
// executeToolsCmd returns a tea.Cmd that executes all tool calls concurrently
// in a goroutine and sends a ToolResultMsg when all are done.
func executeToolsCmd(calls []llm.ToolCall) tea.Cmd {
    return func() tea.Msg {
        results := make([]toolResult, len(calls))
        // For v1, execute sequentially to avoid ordering issues with
        // tool results that depend on each other. Parallelize in v2 if needed.
        for i, call := range calls {
            result, err := llm.ExecuteTool(call)
            results[i] = toolResult{Call: call, Result: result, Err: err}
        }
        return ToolResultMsg{Results: results}
    }
}
```

In `Update()`, the `ToolCallMsg` handler changes from:

```go
// BEFORE: synchronous, blocks Update()
case ToolCallMsg:
    // ... intercept special tools ...
    for _, call := range msg.Calls {
        result, err := llm.ExecuteTool(call)
        // inject result message ...
    }
    return m, m.startStream(m.messages)
```

To:

```go
// AFTER: async, returns immediately
case ToolCallMsg:
    // ... intercept special tools (unchanged) ...

    // Append the assistant tool-call turn immediately (for visual feedback).
    m.messages = append(m.messages, llm.Message{
        Role:      "assistant",
        ToolCalls: msg.Calls,
    })
    m.reasoning = append(m.reasoning, "")
    m.claudeMeta = append(m.claudeMeta, "")

    // Show "calling tool..." indicators.
    for _, call := range msg.Calls {
        indicator := fmt.Sprintf("⚙ calling `%s`…", call.Function.Name)
        m.messages = append(m.messages, llm.Message{Role: "assistant", Content: indicator})
        m.reasoning = append(m.reasoning, "")
        m.claudeMeta = append(m.claudeMeta, "tool-call-indicator")
    }
    m.updateViewportContent()

    return m, executeToolsCmd(msg.Calls)

case ToolResultMsg:
    // Inject tool results into message history.
    for _, r := range msg.Results {
        result := r.Result
        if r.Err != nil {
            result = fmt.Sprintf("error: %s", r.Err.Error())
        }
        m.messages = append(m.messages, llm.Message{
            Role:       "tool",
            ToolCallID: r.Call.ID,
            Content:    result,
        })
    }
    m.drainPendingCompletions()
    m.updateViewportContent()
    return m, m.startStream(m.messages)
```

**Context cancellation:** Pass `context.Context` into `ExecuteTool` so that tool calls can be cancelled when the user presses Escape. The context should be derived from the stream's cancel context. This is a minor API change to `ExecuteTool`'s signature.

**Acceptance criteria:**
- The TUI remains responsive (redraws, input handling) while a tool call is in flight.
- `fetch_webpage` no longer blocks the event loop.
- Pressing Escape during a tool call cancels it cleanly.
- Tool results are injected into the conversation in the correct order.
- Existing tool behavior is unchanged (same results, same error handling).

---

### Phase 5: Tool Namespacing and Collision Prevention

**Goal:** Ensure MCP tool names never collide with static tool names or with each other across multiple MCP servers.

**Files affected:**
- `internal/mcp/manager.go`
- `internal/mcp/convert.go`

**Namespacing scheme:**

Each MCP server's tools are prefixed with a sanitized version of the server's configured `name`, followed by a double underscore:

```
{server_name}__{tool_name}
```

For example, a GitHub MCP server named `github` exposes a tool `create_issue` as `github__create_issue`. A Jira server named `jira` exposes `create_issue` as `jira__create_issue`. No collision.

The server name is sanitized to lowercase alphanumeric and underscores before use as a prefix. Invalid characters are replaced with underscores.

**Collision detection:**

At connect time, `Manager.Connect()` checks each namespaced tool name against the existing static tool names in `llm.AvailableTools`. If a collision is detected, it logs a warning and skips the conflicting MCP tool. This is a conservative policy — the static tools always win.

```go
func sanitizeName(s string) string {
    // Replace any character that is not [a-z0-9_] with '_', then lowercase.
}

func namespacedName(serverName, toolName string) string {
    return sanitizeName(serverName) + "__" + toolName
}
```

**Acceptance criteria:**
- Two MCP servers with a tool of the same name produce distinct namespaced names.
- A namespaced MCP tool name that collides with a static tool name is skipped with a warning.
- The operator LLM receives the namespaced names and uses them correctly in tool calls.

---

### Phase 6 (Optional): Token Budget and Tool Filtering

**Goal:** Prevent the tool list from growing so large that it consumes a significant fraction of the operator's context window.

**Background:**

Each tool definition in the system prompt costs tokens. The operator model's context window is finite (visible in the sidebar as "Context"). With many MCP servers each exposing dozens of tools, the tool list alone could consume thousands of tokens per request.

**Approach:**

Add an optional `max_tools` setting to `MCPConfig`. When set, `Manager.Connect()` limits the total number of MCP tools registered. Within that budget, tools are selected by server order (earlier servers in the config list get priority).

A more sophisticated approach would be per-server `enabled_tools` lists in config, allowing the user to explicitly whitelist the tools they actually need:

```yaml
mcp:
  servers:
    - name: github
      transport: stdio
      command: npx
      args: ["-y", "@modelcontextprotocol/server-github"]
      enabled_tools:
        - create_issue
        - list_issues
        - create_pull_request
```

When `enabled_tools` is present, only those tools are registered from that server. This is the recommended approach for production use.

**Acceptance criteria:**
- With `enabled_tools` configured, only the listed tools appear in `llm.AvailableTools`.
- Without `enabled_tools`, all tools from the server are registered.
- The token count displayed in the sidebar reflects the actual tool list size.

---

## 4. MCP Transport Support

The MCP specification (as of protocol revision 2025-06-18) defines two standard transports:

**stdio** — The client launches the MCP server as a subprocess and communicates over stdin/stdout using newline-delimited JSON-RPC. This is the most common transport for locally-installed MCP servers (e.g. `npx @modelcontextprotocol/server-github`, `uvx mcp-server-git`).

**Streamable HTTP** — The server runs as an independent process and accepts HTTP POST requests at a single endpoint, optionally streaming responses via SSE. This is used for remote or hosted MCP servers.

The older **HTTP+SSE** transport (protocol version 2024-11-05) used separate SSE and POST endpoints. `mcp-go` supports it via `client.NewSSEMCPClient` for backward compatibility with servers that have not yet migrated.

### Priority for v1

**Prioritize stdio.** The vast majority of publicly available MCP servers (GitHub, filesystem, git, Jira, Linear, Slack, etc.) are distributed as stdio servers run via `npx`, `uvx`, or a direct binary path. Stdio is also simpler to implement and test — no network configuration, no authentication headers, no TLS.

Streamable HTTP support should be included from the start because `mcp-go` provides it with the same `client.Client` interface — the only difference is which constructor is called. The config `transport` field selects the constructor:

```go
switch srv.Transport {
case "stdio":
    c, err = mcpclient.NewStdioMCPClient(srv.Command, envSlice, srv.Args...)
case "http":
    c, err = mcpclient.NewStreamableHttpClient(srv.URL,
        mcpclient.WithHeaders(srv.Headers))
case "sse":
    // Legacy HTTP+SSE transport for older servers.
    c, err = mcpclient.NewSSEMCPClient(srv.URL,
        mcpclient.WithHeaders(srv.Headers))
default:
    return fmt.Errorf("unknown transport %q for MCP server %q", srv.Transport, srv.Name)
}
```

After construction, all three transports are used identically: `Start()` → `Initialize()` → `ListTools()` → `CallTool()`.

---

## 5. Configuration Schema

The following shows a complete `~/.config/toasters/config.yaml` with MCP servers configured alongside the existing operator and claude settings:

```yaml
operator:
  endpoint: http://localhost:1234
  model: ""
  teams_dir: ~/.config/toasters/teams
  log_requests: false

claude:
  path: claude
  default_model: ""
  permission_mode: ""
  slot_timeout_minutes: 15

mcp:
  servers:
    # GitHub MCP server via stdio (npx).
    # Requires GITHUB_PERSONAL_ACCESS_TOKEN in env.
    - name: github
      transport: stdio
      command: npx
      args:
        - "-y"
        - "@modelcontextprotocol/server-github"
      env:
        GITHUB_PERSONAL_ACCESS_TOKEN: "ghp_..."
      enabled: true

    # Git MCP server — operates on local repositories.
    - name: git
      transport: stdio
      command: uvx
      args:
        - "mcp-server-git"
        - "--repository"
        - "/Users/jeff/dev/myproject"
      enabled: true

    # A remote Streamable HTTP MCP server.
    - name: linear
      transport: http
      url: https://mcp.linear.app/mcp
      headers:
        Authorization: "Bearer lin_api_..."
      enabled: true

    # A legacy HTTP+SSE server (pre-2025-06-18 protocol).
    - name: legacy-server
      transport: sse
      url: http://localhost:8080/sse
      enabled: false   # disabled; won't be connected at startup
```

With the GitHub server above, the operator LLM would see tools named `github__create_issue`, `github__list_issues`, `github__create_pull_request`, etc. The operator can call them like any other tool, and Toasters routes the call to the GitHub MCP subprocess.

---

## 6. Key Risks and Mitigations

**Risk: Synchronous tool execution blocks the TUI.**  
This is the most impactful risk and is addressed directly by Phase 4. Until Phase 4 is complete, MCP tool calls should not be wired into `ExecuteTool` — they would freeze the TUI for the duration of every network call. Phase 4 must be completed before Phase 3 is deployed to users.

**Risk: MCP server startup failure at launch.**  
A misconfigured or missing MCP server binary (e.g. `npx` not installed, wrong path) will fail during `Manager.Connect()`. The mitigation is to treat each server connection independently: log the error, skip the server, and continue. The operator starts with whatever tools are available. This is already the pattern used for team discovery.

**Risk: Tool name collisions.**  
Addressed by Phase 5. The double-underscore namespace convention is simple and human-readable. The static tools always win in a collision.

**Risk: Token budget exhaustion.**  
A GitHub MCP server alone exposes 30+ tools. With multiple servers, the tool list can easily exceed 50–100 tools, adding thousands of tokens to every request. This degrades response quality and increases cost. Phase 6 (tool filtering) mitigates this. In the interim, users should configure only the servers they actively need.

**Risk: MCP protocol version mismatch.**  
`mcp-go` handles protocol negotiation during `Initialize()`. If a server speaks an older protocol version, `mcp-go` negotiates down. This should be transparent. The risk is servers that are so old they predate `mcp-go`'s backward compatibility range — these will fail at `Initialize()` and be skipped.

**Risk: stdio server process lifecycle.**  
Stdio MCP servers are subprocesses. If Toasters crashes or is killed with SIGKILL, the subprocess may be orphaned. `mcp-go`'s stdio transport closes stdin on `Close()`, which signals the subprocess to exit. The `Manager` should call `Close()` on all clients during graceful shutdown. Wire this into the `watchCancel` / defer chain in `cmd/root.go`.

**Risk: Sensitive credentials in config.**  
API tokens for GitHub, Linear, etc. will appear in `~/.config/toasters/config.yaml` in plaintext. This is consistent with how other tools (e.g. Claude's config) handle credentials. Document clearly that the config file should have restricted permissions (`chmod 600`). A future improvement could support reading values from environment variables using a `${ENV_VAR}` syntax in the YAML.

---

## 7. Out of Scope

The following are explicitly not part of this plan:

- **Building an MCP server.** Toasters will not expose its own tools (job management, team dispatch, etc.) over MCP to external consumers.
- **Streaming tool results.** MCP supports streaming tool results via SSE. For v1, all tool results are buffered and returned as a complete string. Streaming would require changes to the `ToolResultMsg` flow and is not worth the complexity yet.
- **MCP resources and prompts.** The MCP protocol also defines Resources and Prompts. This plan covers only Tools, which is the relevant primitive for the operator's tool-calling workflow.
- **Authentication flows.** OAuth-based MCP servers (e.g. servers requiring browser-based auth) are out of scope. `mcp-go` supports OAuth, but wiring it into the TUI would require a significant UX effort. For v1, only static credentials (API tokens in headers or env vars) are supported.
- **Hot-reloading MCP servers.** Unlike teams (which are watched via fsnotify), MCP server connections are established once at startup. Adding or removing servers requires a restart.
- **Per-agent MCP tool access.** Claude CLI subprocesses (the gateway slots) do not get access to MCP tools. MCP tools are available only to the operator LLM. Giving agents MCP access would require a different architecture (e.g. an MCP proxy server that the Claude CLI can connect to).

---

## 8. Phase Summary

| Phase | Description | Files Changed | Effort | Depends On |
|-------|-------------|---------------|--------|------------|
| 1 | Config schema | `internal/config/config.go` | 1–2 hours | — |
| 2 | MCP client package | `internal/mcp/` (new) | 1–2 days | Phase 1 |
| 3 | Tool registration at startup | `cmd/root.go`, `internal/llm/tools.go` | 2–4 hours | Phase 2 |
| 4 | Async tool execution | `internal/tui/model.go`, `internal/llm/tools.go` | 1–2 days | — |
| 5 | Namespacing + collision prevention | `internal/mcp/manager.go`, `internal/mcp/convert.go` | 2–4 hours | Phase 2 |
| 6 | Token budget / tool filtering | `internal/config/config.go`, `internal/mcp/manager.go` | 2–4 hours | Phase 2 |

**Recommended implementation order:** Phase 4 → Phase 1 → Phase 2 → Phase 5 → Phase 3 → Phase 6.

Phase 4 (async tool execution) is the highest-risk refactor and should be done first, independently of MCP, so it can be tested against the existing static tools before MCP is introduced. Once async execution is solid, the MCP plumbing (Phases 1–3, 5) can be layered on top cleanly.

**Total estimated effort:** 3–5 days of focused work.
