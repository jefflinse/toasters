# CLAUDE.md

## Project Overview

Toasters is a Go-based TUI orchestration tool for agentic coding work. It coordinates multiple concurrent LLM-powered agents through a Bubble Tea interface. An operator LLM dispatches work to specialized agent teams, which execute autonomously via in-process API-driven agent sessions (with Claude Code subprocess fallback).

## Quick Reference

```bash
go build ./...          # Build
go test ./...           # Test (14 test packages)
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
  llm/                      # Shared LLM types and Provider interface
    client/                 # OpenAI-compatible streaming client
    tools/                  # Tool executor with dependency injection
  mcp/                      # MCP client manager, tool conversion, namespacing
  orchestration/            # Cross-cutting orchestration types (GatewaySlot, AgentSpawner)
  progress/                 # Progress tool handlers, MCP server (report_progress, etc.)
  provider/                 # Multi-provider LLM client (OpenAI, Anthropic, registry)
  runtime/                  # In-process agent runtime (sessions, core tools, spawn)
    composite_tools.go      # CompositeTools wrapper combining CoreTools + MCP tools
  tui/                      # Bubble Tea TUI (model, views, grid, modals, streaming)
    progress_poll.go        # SQLite polling loop for real-time progress display
  job/                      # Job file persistence (OVERVIEW.md + TODO.md)
```

## Architecture

- **Operator**: LLM that coordinates work. Receives user messages, decides which team to assign work to, and manages jobs. Can be backed by any configured provider (LM Studio, Anthropic, OpenAI).
- **Teams**: Groups of agents defined in `~/.config/toasters/teams/` (or configured via `operator.teams_dir`). Each team has one coordinator and multiple workers.
- **Agent Runtime**: In-process agent sessions running as goroutines. Each session is a conversation loop: send messages to the LLM → receive response → execute tool calls → loop. Core tools include file I/O, shell, glob, grep, web fetch, subagent spawning, and progress reporting (`report_progress`, `update_task_status`, `report_blocker`, `request_review`, `query_job_context`, `log_artifact`). Sessions are tracked in SQLite and observable via the TUI. `spawn_agent` enforces a max depth of 1 (coordinators may spawn workers; workers may not spawn further agents) and propagates tool filtering via `filteredToolExecutor`.
- **MCP Client**: `internal/mcp` package manages connections to external MCP servers (GitHub, Jira, Linear, etc.). Tools are namespaced as `{server_name}__{tool_name}` and merged into both the operator and agent tool sets. Failed servers are skipped with a warning.
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
- **Logging**: Minimal — `log.Printf()` for warnings. Optional request logging to `~/.config/toasters/requests.log`

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

## Key TUI Interactions

- **Enter**: Send message
- **Shift+Enter**: Newline in input
- **Ctrl+G**: Toggle grid screen (2×2 agent slot view)
- **Ctrl+C**: Quit
- **Slash commands**: `/help`, `/new`, `/exit`, `/quit`, `/claude <prompt>`, `/kill`
- **Prompt mode**: Numbered options when operator asks user a question

## Testing

Tests exist across 14 test packages. They use standard Go testing with `t.TempDir()` for file I/O and helper functions for assertions. Run `golangci-lint run` for linting — the codebase currently has 0 lint findings.

Key coverage numbers: `frontmatter` 100%, `llm/tools` 88.3%, `llm/client` 87.7%, `runtime` 87.0%, `job` 85.7%, `provider` 84.9%, `db` 83.6%, `agents` 72.1%, `config` 65.7%.

## Tech Debt Execution Plan (Pre-Phase 2)

Identified via comprehensive codebase health audit (code-reviewer, security-auditor, concurrency-reviewer, refactorer). Findings are organized into three waves by risk and dependency order.

### Wave 1 — Safety Fixes (data races + security)

These are correctness issues. Fix before any feature work.

- [x] **CONC-B1**: Add mutex protection to `Session.FinalText()` and `InitialMessage()` — they read `s.messages` without holding `s.mu`, concurrent with `Run()` appending to it (`runtime/session.go`)
- [x] **CONC-B2**: Add `sync.RWMutex` to `ToolExecutor` for `Teams` field — written from file watcher goroutine, read from tool execution goroutine without synchronization (`llm/tools/tools.go`, `cmd/root.go`)
- [x] **CONC-B3**: Fix Gateway `SpawnTeam` TOCTOU race — finds free slot under lock, releases lock, does I/O, re-acquires lock to assign; another goroutine can claim the same slot in between. Use slot reservation pattern (`gateway/gateway.go`)
- [x] **CONC-B4**: Read `g.notify`/`g.send` function pointers under lock via helper method — currently read without lock in subprocess goroutines, written via `SetNotify`/`SetSend` under lock (`gateway/gateway.go`)
- [x] **SEC-C1/C2**: Add HTTP client with timeouts to `anthropic.Client` and `provider.AnthropicProvider` — both use `http.DefaultClient` (no timeout), risking goroutine leaks on slow/unresponsive API servers (`anthropic/client.go`, `provider/anthropic.go`)
- [x] **SEC-C3**: Add SSRF protection to operator-level `fetch_webpage` — unlike the agent-level `web_fetch` which blocks private IPs, the operator tool has no protection (`llm/tools/tools.go`)
- [x] **SEC-C4**: Add path restriction to operator-level `list_directory` — currently accepts any path from the LLM with no validation (`llm/tools/tools.go`)
- [x] **SEC-H1**: Add `io.LimitReader` to all unbounded `io.ReadAll` response body reads (`anthropic/client.go`, `provider/anthropic.go`)
- [x] **SEC-H2**: Fix OAuth refresh token form body to use `url.Values` encoding instead of `fmt.Sprintf` (`anthropic/client.go`)

### Wave 2 — Quick Wins (low-risk cleanup)

Small, mechanical improvements. Each is independent.

**Status: ✅ Complete (2026-02-25)**

- [x] **ARCH-H3**: Merge `streamMessages`/`streamMessagesWithTools` — merged into single method; also fixed a latent `http.DefaultClient` bug in the deleted code (`anthropic/client.go`)
- [x] **ARCH-H4**: Delete standalone `StreamMessage`/`streamMessage` — dead weight third copy of same logic removed (`anthropic/client.go`)
- [x] **DUP-M1**: Extract `expandTilde(path, fallback)` helper — tilde expansion extracted from `WorkspaceDir`, `DatabasePath`, and `Dir` (`config/config.go`)
- [x] **MOD-M1**: `sort.Slice` → `slices.SortFunc`, `sort.Ints` → `slices.Sort`, `sort.Strings` → `slices.Sort` (9 call sites across multiple packages)
- [x] **MOD-M2**: Range-over-int where applicable — `for i := 0; i < N; i++` → `for i := range N` (`tui/helpers.go`, `tui/grid.go`, `tui/view.go`)
- [x] **MOD-M3**: `copy(dst, src)` → `slices.Clone` (`agents/agents.go`)
- [x] **MOD-M4**: `for k := range m { delete(m, k) }` → `clear(m)` (`provider/openai.go`)
- [x] **MOD-M5**: No-op — migration loop uses early-return pattern, not multi-error collection; `errors.Join` not applicable (`db/sqlite.go`)
- [x] **MOD-M6**: `context.AfterFunc` for context merging — eliminates goroutine in `Session.Run()` (`runtime/session.go`)
- [x] **MOD-M7**: Struct conversion instead of field-by-field copy (staticcheck S1016) (`provider/openai.go`)
- [x] **LINT**: Fixed 15 lint findings (not 6 as originally estimated) across 4 files — errcheck (unchecked `Close`/`Fprint`) + staticcheck (`runtime/tools.go`, `runtime/tools_test.go`, `provider/openai.go`)
- [x] **CONC-H1**: Add session cleanup to `Runtime` — completed sessions removed from `sessions` map immediately after `Run()` returns (`runtime/runtime.go`)
- [x] **CONC-H2**: Fix late `Subscribe()` — returns already-closed channel if session is done; uses `closed bool` flag under mutex (`runtime/session.go`)
- [x] **CONC-H3**: Add debouncing to file watcher — debounced with `time.After` channel in select loop (200ms window) (`agents/agents.go`)
- [x] **CONC-M1**: Move regex compilation to package level — `regexp.MustCompile` moved out of `fetchWebpage` (`llm/tools/tools.go`)
- [x] **SEC-H3**: Fix `http.Post` without context in `refreshAccessToken` — use `http.NewRequestWithContext` (`anthropic/client.go`) (completed in Wave 1)

### Wave 3 — Structural Improvements (architecture)

Larger refactorings. Each is independent and can be done incrementally.

- [ ] **ARCH-H1**: Consolidate Anthropic SSE parsing — 3 separate implementations with duplicated event types across `anthropic/`, `provider/`, `llm/client/`. Extract shared `internal/sse` package (~400 lines of duplication eliminated)
- [ ] **ARCH-H2**: Converge on single Provider interface — `llm.Provider` vs `provider.Provider` with bridge adapter and 261-line `convert.go`. Migrate to `provider.Provider` as canonical interface, eliminate adapter layer. *Highest-impact refactoring in the codebase.*
- [ ] **DESIGN-H1**: Decompose TUI Model — 60+ field god object with 1068-line Update. Extract sub-models: `teamsModal`, `blockerModal`, `PromptModel`, `gridScreen`, `ChatState`. Introduce `ModelConfig` struct for 11-parameter constructor.
- [ ] **DESIGN-M1**: Tool registry pattern for `ExecuteTool` — replace 360-line switch with `map[string]toolHandler` dispatch, each handler individually testable (`llm/tools/tools.go`)
- [ ] **MOD-M8**: `log.Printf` → `slog` structured logging — 29 call sites with inconsistent prefixes. Migrate to `slog.Warn`/`slog.Info` with structured fields.
