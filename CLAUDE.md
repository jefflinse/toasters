# CLAUDE.md

## Project Overview

Toasters is a Go-based TUI orchestration tool for agentic coding work. It coordinates multiple concurrent LLM-powered agents through a Bubble Tea interface. An operator LLM dispatches work to specialized agent teams, which execute autonomously via in-process API-driven agent sessions (with Claude Code subprocess fallback).

## Quick Reference

```bash
go build ./...          # Build
go test ./...           # Test (15 test packages)
go run main.go          # Run the TUI
```

## Project Structure

```
main.go                     # Entry point → cmd.Execute()
cmd/                        # Cobra CLI setup, launches TUI
  mcp_server.go             # `toasters mcp-server` subcommand (stdio MCP server for agents)
agents/                     # Built-in agent definition files (.md with YAML frontmatter)
internal/
  agents/                   # Agent discovery, parsing, team management
  anthropic/                # Anthropic API client + OAuth/Keychain
  claude/                   # Shared Claude CLI stream-json types
  config/                   # Viper-based config from ~/.config/toasters/config.yaml
  db/                       # SQLite persistence (Store interface, migrations, CRUD)
  frontmatter/              # Shared YAML frontmatter parsing (Split + Parse)
  gateway/                  # Claude subprocess slot management (4 concurrent slots)
  llm/                      # Legacy LLM types (OpenAI wire format, used by llm/client)
    client/                 # OpenAI-compatible streaming client
    tools/                  # Tool executor with registry-based dispatch (18 handlers in 5 files)
  mcp/                      # MCP client manager, tool conversion, namespacing, result truncation/slimming, server status tracking
  orchestration/            # Cross-cutting orchestration types (GatewaySlot, AgentSpawner)
  progress/                 # Progress tool handlers, MCP server (report_progress, etc.)
  provider/                 # Multi-provider LLM client (OpenAI, Anthropic, registry)
  runtime/                  # In-process agent runtime (sessions, core tools, spawn)
    composite_tools.go      # CompositeTools wrapper combining CoreTools + MCP tools
  sse/                      # Shared SSE parsing (reader, Anthropic event types, OpenAI chunk types)
  tui/                      # Bubble Tea TUI (model, views, grid, modals, streaming, MCP modal)
    progress_poll.go        # SQLite polling loop for real-time progress display
  job/                      # Job file persistence (OVERVIEW.md + TODO.md)
```

## Architecture

- **Operator**: LLM that coordinates work. Receives user messages, decides which team to assign work to, and manages jobs. Can be backed by any configured provider (LM Studio, Anthropic, OpenAI).
- **Teams**: Groups of agents defined in `~/.config/toasters/teams/` (or configured via `operator.teams_dir`). Each team has one coordinator and multiple workers.
- **Agent Runtime**: In-process agent sessions running as goroutines. Each session is a conversation loop: send messages to the LLM → receive response → execute tool calls → loop. Core tools include file I/O, shell, glob, grep, web fetch, subagent spawning, and progress reporting (`report_progress`, `update_task_status`, `report_blocker`, `request_review`, `query_job_context`, `log_artifact`). Sessions are tracked in SQLite and observable via the TUI. `spawn_agent` enforces a max depth of 1 (coordinators may spawn workers; workers may not spawn further agents) and propagates tool filtering via `filteredToolExecutor`.
- **MCP Client**: `internal/mcp` package manages connections to external MCP servers (GitHub, Jira, Linear, etc.). Tools are namespaced as `{server_name}__{tool_name}` and merged into both the operator and agent tool sets. Failed servers are skipped with a warning. Server connection status is tracked and exposed via `Servers()` accessor. MCP tool results are automatically slimmed (strips nulls, `*_url` fields, API URLs, `node_id`, opaque blobs) and truncated (JSON-aware array shrinking with UTF-8 safe byte fallback, 16KB default) to prevent context window exhaustion.
- **Toasters MCP Server**: `internal/progress` package exposes progress tools via an MCP server (`toasters mcp-server` subcommand). Claude CLI subprocesses use this to report progress back to SQLite. In-process agents call the same handlers directly without the MCP protocol.
- **Gateway**: Manages up to 4 concurrent Claude CLI subprocesses (`MaxSlots = 4`). Each slot runs a Claude agent with a specific prompt and job context. Retained as a fallback alongside the in-process runtime.
- **Provider Registry**: Multi-provider LLM abstraction supporting OpenAI-compatible APIs (LM Studio, Ollama, OpenAI) and Anthropic's Messages API. Providers are configured in YAML and looked up by name. Anthropic supports both API key and Keychain/OAuth authentication.
- **SQLite Persistence**: Operational state stored in SQLite via `modernc.org/sqlite` (pure Go). WAL mode for concurrent reads. Schema includes jobs, tasks, task dependencies, progress reports, agents, teams, sessions, and artifacts. Auto-migrating on open.
- **Jobs**: Dual-persisted — markdown files (`OVERVIEW.md` + `TODO.md`) for human readability, SQLite for structured queries. New jobs are written to both. Toasters is workspace-centric — coordinators start in the job workspace directory; there is no concept of a "current working directory."
- **Agents**: Defined as `.md` files with YAML frontmatter (name, description, mode, color, temperature, tools). Discovered from directories and hot-reloaded via fsnotify (debounced at 200ms).

## Tech Stack

- **Go 1.26.0**
- **TUI**: Charmbracelet v2 (bubbletea, bubbles, lipgloss) — all stable v2.0.0
- **CLI**: Cobra + Viper
- **Markdown rendering**: Glamour
- **File watching**: fsnotify
- **SQLite**: `modernc.org/sqlite` (pure Go, no CGO)
- **LLM integration**: Multi-provider — Anthropic Messages API (direct, with Keychain/OAuth), OpenAI-compatible SSE streaming (LM Studio, OpenAI, Ollama), Claude CLI subprocess fallback

## Code Conventions

- **Packages**: lowercase single word (`agents`, `config`, `gateway`, `llm`, `tui`, `job`)
- **Types**: PascalCase (`Agent`, `Team`, `Gateway`, `SlotSnapshot`, `Job`)
- **Constants**: SCREAMING_SNAKE or PascalCase for exported (`MaxSlots`, `InputHeight`)
- **Error handling**: Always `if err != nil` with `fmt.Errorf("context: %w", err)` wrapping. Return errors, don't log and swallow.
- **Concurrency**: `sync.Mutex` for shared state, channels for TUI messages, `context.Context` for cancellation
- **Logging**: Structured via `log/slog` — `slog.Warn`/`slog.Info`/`slog.Error` with key-value fields. Optional request logging to `~/.config/toasters/requests.log`

## Commit Message Style

Uses conventional commits: `type: description`
- `feat:` new feature
- `fix:` bug fix
- `proto:` prototype/experimental work

## Configuration

Config file: `~/.config/toasters/config.yaml`

Key settings:
- `operator.endpoint` — LM Studio URL (default: `http://localhost:1234`)
- `operator.model` — model name (default: loaded model)
- `operator.provider` — provider name for operator (e.g. `anthropic`, `lmstudio`)
- `operator.teams_dir` — teams directory (default: `~/.config/toasters/teams`)
- `providers` — list of provider configs (name, type, endpoint, api_key)
- `agents.default_provider` — default provider for agents
- `agents.default_model` — default model for agents
- `database_path` — SQLite database path (default: `~/.config/toasters/toasters.db`)
- `claude.path` — claude binary (default: `"claude"`)
- `claude.default_model` — model for Claude CLI
- `claude.permission_mode` — permission mode for Claude CLI
  - If `claude.permission_mode` is not set, defaults to `plan` with a warning log
- `mcp.servers` — list of MCP server configs (name, transport, command, args, env, url, headers, enabled, enabled_tools)

## Key TUI Interactions

- **Enter**: Send message
- **Shift+Enter**: Newline in input
- **Ctrl+G**: Toggle grid screen (2×2 agent slot view)
- **Ctrl+C**: Quit
- **Slash commands**: `/help`, `/new`, `/exit`, `/quit`, `/claude <prompt>`, `/kill`, `/mcp`, `/teams`, `/anthropic`, `/job`
- **Prompt mode**: Numbered options when operator asks user a question

## Testing

Tests exist across 15 test packages. They use standard Go testing with `t.TempDir()` for file I/O and helper functions for assertions. Run `golangci-lint run` for linting — the codebase currently has 0 lint findings.

Key coverage numbers: `frontmatter` 100%, `llm/tools` 88.3%, `llm/client` 87.7%, `runtime` 87.0%, `job` 85.7%, `provider` 84.9%, `db` 83.6%, `mcp` 83%, `agents` 72.1%, `config` 65.7%.

## Tech Debt Execution Plan (Pre-Phase 3)

Identified via comprehensive codebase health audit (code-reviewer, security-auditor, concurrency-reviewer, refactorer). Findings are organized into three waves by risk and dependency order. Waves 1 and 2 were completed pre-Phase 2. Wave 3 is being completed pre-Phase 3.

### Wave 1 — Safety Fixes ✅

**Status: Complete (pre-Phase 2)**

All data race and security fixes completed: CONC-B1–B4 (mutex/lock fixes), SEC-C1–C4 (HTTP timeouts, SSRF, path restriction), SEC-H1–H2 (response limits, OAuth encoding).

### Wave 2 — Quick Wins ✅

**Status: Complete (2026-02-25, pre-Phase 2)**

All 16 items completed: ARCH-H3/H4 (Anthropic client consolidation), DUP-M1 (tilde helper), MOD-M1–M7 (modern Go idioms), LINT (15 findings), CONC-H1–H3/M1 (session cleanup, subscribe fix, debounce, regex), SEC-H3 (HTTP context).

### Wave 3 — Structural Improvements ✅

**Status: Complete (2026-02-25, pre-Phase 3)**

- [x] **ARCH-H1**: Consolidated Anthropic SSE parsing — extracted shared `internal/sse` package with SSE reader, Anthropic event types, and OpenAI chunk types. ~400 lines of duplication eliminated across `anthropic/`, `provider/`, `llm/client/`.
- [x] **ARCH-H2**: Converged on single `provider.Provider` interface — migrated TUI, cmd, tools, gateway from `llm.*` types to `provider.*` types. Deleted 261-line adapter layer (`convert.go`) and 638-line adapter tests. Net -1,041 lines.
- [x] **DESIGN-H1**: Decomposed TUI Model — extracted `chatState`, `streamingState`, `gridState`, `promptState`, `progressState`, `modalState` sub-models. Introduced `ModelConfig` struct replacing 11-parameter constructor.
- [x] **DESIGN-M1**: Tool registry pattern for `ExecuteTool` — replaced 365-line switch with `map[string]toolHandler` dispatch. 18 handlers extracted into 5 focused files (`handler_web.go`, `handler_jobs.go`, `handler_gateway.go`, `handler_sessions.go`, `handler_interactive.go`).
- [x] **MOD-M8**: Migrated 43 `log.Printf` calls across 14 files to `slog.Warn`/`slog.Info`/`slog.Error` structured logging with key-value fields.
