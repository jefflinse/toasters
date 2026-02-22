# Toasters — Vision

Toasters is a TUI orchestrator for agentic coding work. It sits *above* tools like Claude Code, OpenCode, and similar assistants — it does not replace them. The user hands it a task, and it coordinates data sources and multiple `claude` CLI invocations to accomplish the work. It deliberately defers to the user's existing Claude Code configuration, agents, MCP servers, and authentication. Nothing is re-implemented that Claude already handles.

The core insight: LLMs are good at reasoning and writing code, but bad at maintaining state across long sessions. Toasters inverts this — Go owns the state, and the LLM is invoked fresh each time with accumulated context fed back in.

---

## Table of Contents

- [What It Is Not](#what-it-is-not)
- [Work Effort Types](#work-effort-types)
- [Architecture](#architecture)
  - [Local LLM (LM Studio)](#local-llm-lm-studio)
  - [The `claude` CLI](#the-claude-cli)
  - [Agent System](#agent-system)
  - [MCP Servers](#mcp-servers)
  - [State Persistence](#state-persistence)
  - [Task DAG and Concurrency](#task-dag-and-concurrency)
- [The `claude` CLI — Key Flags](#the-claude-cli--key-flags)
- [stream-json Event Format](#stream-json-event-format)
- [UI Layout](#ui-layout)
- [Tech Stack](#tech-stack)
- [Current State](#current-state)

---

## What It Is Not

- Not a chat interface to Claude (that is a side effect of the prototype, not the goal)
- Not another coding assistant TUI
- Not a replacement for Claude Code, OpenCode, or any other agentic tool
- Not an MCP server host
- Not responsible for auth, agent definitions, or model configuration — those live in the user's existing Claude setup

---

## Work Effort Types

Toasters automatically classifies incoming tasks into one of four types. The classification drives which agents are selected, what data sources are consulted, and how the task DAG is structured.

| Type | Description |
|---|---|
| **Bug Fix** | Usually has an associated Jira ticket, Slack thread, or error report. Requires investigation before planning. |
| **New Feature** | Fully implement a new feature on an existing project. Requires understanding the codebase before planning. |
| **Prototype** | Given basic requirements and constraints, produce a working prototype. Emphasis on speed and iteration. |
| **Review** | Perform a code review of a PR, branch, or diff. Produces findings and a structured report. |

---

## Architecture

### Local LLM (LM Studio)

The local LLM (served via LM Studio at `localhost:1234`) acts as a cheap coordinator and classifier. It is explicitly **not** responsible for planning or executing actual work — that is `claude`'s job.

Responsibilities:
- Classify incoming tasks into one of the four work effort types
- Reach out to external data sources (Jira, Slack, GitHub, etc.) or instruct `claude` to do so and report back
- Maintain overall work effort state as `.md` files that persist between sessions
- Shift orchestration state management away from LLM context — Go's concurrency model owns this, not the LLM

The local LLM is intentionally low-capability and low-cost. It coordinates; it does not think.

### The `claude` CLI

`claude` is the actual worker. It plans work, writes code, investigates issues, and reports findings. It is always invoked non-interactively.

Key invocation pattern:
```
claude --print --output-format stream-json --include-partial-messages <prompt>
```

Design principles:
- Each invocation receives fresh context plus accumulated state `.md` files as input
- Structured responses are enforced via `--json-schema` where machine-parseable output is needed
- The Go orchestrator is the memory — not the LLM. Context compaction losing state is not a concern because state lives on disk
- Multiple `claude` sessions can run in parallel; Go handles the concurrency

### Agent System

- By default, Toasters bundles a set of default agents for each work effort type
- If the user already has agents defined in their Claude config directory, those are auto-detected and preferred
- Specific agents can be configured per task type (e.g., "for debugging, always use agent X")
- Each agent invocation uses a specific prompt envelope with a defined request schema and response schema
- Response format: JSON for machine parsing; `.md` files for human-readable persistent state

### MCP Servers

Toasters **consumes** existing MCP servers — it does not host one.

- Supported integrations: Jira, GitHub, Slack, DataDog, and any other MCP server the user has configured
- If the user already has MCP servers configured in their Claude setup, Toasters simply prompts `claude` to use them — no additional configuration needed
- Auth is handled entirely by Claude's existing config; Toasters does not touch credentials

### State Persistence

Each work effort has a directory of associated `.md` files on disk:
- Investigations and findings
- Task lists and their statuses
- Results and summaries
- Any other structured output from `claude` invocations

Each new `claude` invocation receives the accumulated state files as part of its context. This sidesteps LLM context compaction — the Go process owns the state, not the LLM's context window.

### Task DAG and Concurrency

- `claude` plans the actual work and identifies what can be parallelized
- Claude's plan is translated back into Toasters' internal data model: a directed acyclic graph (DAG) of tasks
- Each task node carries: name, dependencies, status, assigned agent, last update
- Go handles concurrency — multiple `claude` sessions can run simultaneously without coordination overhead in the LLM

---

## The `claude` CLI — Key Flags

These flags were discovered through experimentation and are central to how Toasters drives `claude`:

| Flag | Purpose |
|---|---|
| `--print` | Non-interactive mode — exits after producing output |
| `--output-format stream-json` | Emit real-time streaming JSON lines |
| `--include-partial-messages` | Deliver content deltas as they arrive (required for streaming effect) |
| `--json-schema <schema>` | Enforce a structured JSON response format |
| `--input-format stream-json` | Stream large context/state files as input |
| `--agents <json>` / `--agent <agent>` | Inline agent definitions or named agents |
| `--mcp-config` | Inject MCP server configurations per invocation |
| `--worktree` | Give parallel sessions their own git worktree (prevents branch conflicts) |
| `--system-prompt` / `--append-system-prompt` | Inject or append system prompts |

---

## stream-json Event Format

The `--output-format stream-json` flag produces newline-delimited JSON. Two distinct shapes appear on stdout:

**Content delta** (wrapped in a `stream_event` envelope):
```json
{
  "type": "stream_event",
  "event": {
    "type": "content_block_delta",
    "delta": {
      "type": "text_delta",
      "text": "..."
    }
  }
}
```

**Terminal result** (unwrapped, arrives at the end):
```json
{
  "type": "result",
  "subtype": "success",
  "result": "...",
  "is_error": false
}
```

Blank lines are skipped. Malformed lines are skipped silently. The stream is considered done when either a `result` event arrives or stdout closes.

---

## UI Layout

The TUI uses a two-column layout. The left column is the primary work management surface; the right column is the information sidebar.

```
┌─────────────────────────────────────┬──────────────────┐
│                                     │  Connection      │
│  [Work Efforts List]                │  Model: ...      │
│  (scrollable, focused)              │  Endpoint: ...   │
│                                     │  Status: ...     │
├─────────────────────────────────────│                  │
│                                     │  Session         │
│  [Task DAG]                         │  Messages: ...   │
│  (for selected work effort)         │  Tokens in: ...  │
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

- **Top**: Scrollable list of work efforts. Receives focus for keyboard navigation.
- **Middle**: DAG visualization of tasks for the selected work effort, with per-node status indicators.
- **Bottom**: Live streaming output from tasks currently executing in the selected work effort.

### Right Panel (split vertically into two sections)

- **Top**: Stats, usage, and connection info — model name, endpoint, connection status, token counts (in/out/reasoning), generation speed, context window usage bar, last and average response times.
- **Bottom**: Active task list for the selected work effort, showing each task's status and most recent update.

---

## Tech Stack

| Component | Version / Details |
|---|---|
| Go | 1.25 |
| Bubble Tea | `charm.land/bubbletea/v2 v2.0.0-rc.2` |
| Lipgloss | `charm.land/lipgloss/v2 v2.0.0-beta.3` |
| Bubbles | `charm.land/bubbles/v2 v2.0.0-rc.1` |
| Glamour | `github.com/charmbracelet/glamour v0.10.0` |
| Local LLM | LM Studio at `localhost:1234` (OpenAI-compatible API) |
| Worker | `claude` CLI (non-interactive, stream-json mode) |

The Charmbracelet v2 ecosystem (`charm.land/` import paths) is used throughout. These are pre-release versions; import paths and APIs may shift before stable release.

---

## Current State

What exists today is a functional TUI prototype that establishes the streaming infrastructure and the right-panel sidebar. The work management UI (left panel) has not been built yet.

### Implemented

**`internal/llm` — LM Studio client**
- `Client` connects to any OpenAI-compatible API endpoint
- `ChatCompletionStream` sends messages and returns a channel of streamed response chunks
- `FetchModels` queries available models, preferring the LM Studio-specific `/api/v0/models` endpoint (richer metadata) with fallback to `/v1/models`
- `ModelInfo` exposes model ID, load state, max context length, and loaded context length
- Handles `stream_options.include_usage` correctly — reads past the `finish_reason: stop` chunk to capture the trailing usage-only chunk before `[DONE]`

**`internal/tui` — TUI application**
- Two-column layout: main chat area (left) + stats sidebar (right)
- Right sidebar fully implemented: model name, endpoint, connection status, message count, token counts (prompt/completion/reasoning), live token estimates during streaming, generation speed (t/s), context window usage bar with color-coded fill (green/yellow/red), last and average response times
- Chat viewport with markdown rendering via Glamour, mouse wheel scroll support
- Streaming response display with live cursor indicator and chain-of-thought reasoning block
- Slash command system with autocomplete popup: `/help`, `/new`, `/exit`, `/quit`, `/claude`
- `/claude <prompt>` command: invokes the `claude` CLI as a subprocess, parses `stream-json` output, and streams the response into the chat viewport using the same pipeline as the LM Studio client
- `Esc` cancels an in-flight stream; `Ctrl+C` quits
- `Shift+Enter` inserts a newline; `Enter` sends

**`internal/tui/claude.go` — `claude` CLI subprocess integration**
- Launches `claude --print --output-format stream-json --include-partial-messages`
- Parses both event shapes: wrapped `stream_event` content deltas and unwrapped `result` terminal events
- Context cancellation kills the subprocess automatically via `exec.CommandContext`
- Reuses the `llm.StreamResponse` channel type so the TUI's streaming pipeline is shared

### Not Yet Built

- Left panel: work effort list, task DAG visualization, streaming updates pane
- Right panel bottom section: active task list
- Work effort classification (local LLM integration beyond direct chat)
- Task DAG data model and planner
- State persistence (`.md` files on disk)
- Agent configuration and selection
- MCP server integration
- Parallel `claude` session management
- `--worktree`, `--json-schema`, `--agents`, `--mcp-config` flag usage
