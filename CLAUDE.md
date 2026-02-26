# CLAUDE.md

## Project Overview

Toasters is a Go-based TUI orchestration tool for agentic coding work. It coordinates multiple concurrent LLM-powered agents through a Bubble Tea interface. An operator LLM dispatches work to specialized agent teams, which execute autonomously via in-process API-driven agent sessions (with Claude Code subprocess fallback).

## Quick Reference

```bash
go build ./...          # Build
go test ./...           # Test (19 test packages, see list below)
go run main.go          # Run the TUI
```

## Project Structure

```
main.go                     # Entry point → cmd.Execute()
cmd/                        # Cobra CLI setup, launches TUI
  mcp_server.go             # `toasters mcp-server` subcommand (stdio MCP server for agents)
defaults/                   # Embedded default system team files (go:embed)
  embed.go                  # Package with //go:embed system directive
  system/                   # Default system team: operator, planner, scheduler, blocker-handler
    team.md                 # System team definition (operator as lead)
    agents/                 # System agent definitions (.md with YAML frontmatter)
    skills/                 # System skills (orchestration.md)
agents/                     # Built-in agent definition files (.md with YAML frontmatter)
internal/
  agentfmt/                 # YAML frontmatter parsing for agent/skill/team definitions (superset format)
                            #   Supports Toasters, Claude Code, and OpenCode formats with auto-detection
                            #   Import: ImportClaudeCode, ImportOpenCode (lossless)
                            #   Export: ExportClaudeCode, ExportOpenCode (lossy with Warning list)
  agents/                   # Agent discovery, parsing, team management (uses agentfmt for parsing)
  anthropic/                # Anthropic API client + OAuth/Keychain
  bootstrap/                # First-run bootstrap + upgrade migration
                            #   Copies embedded defaults to ~/.config/toasters/system/
                            #   Creates user/ directory structure, auto-team detection
  claude/                   # Shared Claude CLI stream-json types
  compose/                  # Runtime composition / prompt assembly
                            #   Loads agent → skills → team culture → merges tools → resolves provider/model
                            #   Returns ComposedAgent ready for session creation
  config/                   # Viper-based config from ~/.config/toasters/config.yaml
  db/                       # SQLite persistence (Store interface, migrations, CRUD)
                            #   Schema: jobs, tasks, skills, agents, teams, team_agents, feed_entries,
                            #   sessions, progress_reports, artifacts
                            #   RebuildDefinitions: transactional delete-all + insert-all for definition tables
  gateway/                  # Claude subprocess slot management (4 concurrent slots)
  llm/                      # Legacy LLM types (OpenAI wire format, used by llm/client)
    client/                 # OpenAI-compatible streaming client
    tools/                  # Tool executor with registry-based dispatch (18 handlers in 5 files)
  loader/                   # File-to-DB loader + fsnotify watcher
                            #   Walks system/ + user/ dirs, parses .md files with agentfmt
                            #   Resolves agent references (team-local → shared → system)
                            #   Watcher: 200ms debounce, .md filtering, dynamic dir watching
  mcp/                      # MCP client manager, tool conversion, namespacing, result truncation/slimming, server status tracking
  operator/                 # Operator event loop, typed events, system/team tools
                            #   Event loop: mechanical handling + selective LLM routing
                            #   System tools: create_job, create_task, assign_task, query_teams, query_job
                            #   Team lead tools: complete_task, report_blocker, report_progress
                            #   Worker tools: report_progress, query_team_context
                            #   Operator tools: consult_agent (composition-based), surface_to_user, query_job, query_teams
  orchestration/            # Cross-cutting orchestration types (GatewaySlot, AgentSpawner)
  progress/                 # Progress tool handlers, MCP server (report_progress, etc.)
  provider/                 # Multi-provider LLM client (OpenAI, Anthropic, registry)
  runtime/                  # In-process agent runtime (sessions, core tools, spawn)
    composite_tools.go      # CompositeTools wrapper combining CoreTools + MCP tools
  sse/                      # Shared SSE parsing (reader, Anthropic event types, OpenAI chunk types)
  tui/                      # Bubble Tea TUI (model, views, grid, modals, streaming, activity feed, CRUD)
    progress_poll.go        # SQLite polling loop for real-time progress display
    skills_modal.go         # Skills browse/CRUD modal (create, edit, delete skills)
    agents_modal.go         # Agents browse/CRUD modal (create, edit, delete agents)
    teams_modal.go          # Teams browse modal with auto-team promotion (Ctrl+P)
```

## Architecture

- **Operator**: LLM that coordinates work. Receives user messages, decides which team to assign work to, and manages jobs. Can be backed by any configured provider (LM Studio, Anthropic, OpenAI). Runs as a code-driven event loop that handles routine events mechanically (task started/completed, progress updates) and only routes decision-requiring events to the LLM (user messages, failures, blockers, recommendations). Uses `consult_agent` to delegate to system agents (planner, scheduler, blocker-handler).
- **System Team**: The operator's own team, defined in `~/.config/toasters/system/`. Includes the operator (lead), planner (creates jobs/tasks), scheduler (breaks plans into tasks with assignments), and blocker-handler (triages blockers). System agents have orchestration tools (`create_job`, `create_task`, `assign_task`) but NO filesystem tools. Fully hackable — users can modify any system agent.
- **Teams**: Groups of agents defined in `~/.config/toasters/user/teams/`. Each team has a lead agent and worker agents. Team leads receive tasks from the operator, delegate to workers via `spawn_agent`, and report results via `complete_task`. Teams can also be auto-detected from `~/.claude/agents/` and `~/.config/Claude/agents/`. Auto-teams can be promoted to full teams via `Ctrl+P` in the teams modal.
- **Composition Model**: Three-layer composition: Skills (reusable capabilities with prompts + tools) → Agents (personas with skills, provider/model config) → Teams (agents + culture + lead). At runtime, `internal/compose` assembles the full system prompt, tool set, and provider/model for any agent. Skills are additive, tools are unioned with denylist, provider/model cascades (agent → team → global default).
- **Agent Runtime**: In-process agent sessions running as goroutines. Each session is a conversation loop: send messages to the LLM → receive response → execute tool calls → loop. Core tools include file I/O, shell, glob, grep, web fetch, subagent spawning, and progress reporting (`report_progress`, `update_task_status`, `report_blocker`, `request_review`, `query_job_context`, `log_artifact`). Sessions are tracked in SQLite and observable via the TUI. `spawn_agent` enforces a max depth of 1 (coordinators may spawn workers; workers may not spawn further agents) and propagates tool filtering via `filteredToolExecutor`.
- **MCP Client**: `internal/mcp` package manages connections to external MCP servers (GitHub, Jira, Linear, etc.). Tools are namespaced as `{server_name}__{tool_name}` and merged into both the operator and agent tool sets. Failed servers are skipped with a warning. Server connection status is tracked and exposed via `Servers()` accessor. MCP tool results are automatically slimmed (strips nulls, `*_url` fields, API URLs, `node_id`, opaque blobs) and truncated (JSON-aware array shrinking with UTF-8 safe byte fallback, 16KB default) to prevent context window exhaustion.
- **Toasters MCP Server**: `internal/progress` package exposes progress tools via an MCP server (`toasters mcp-server` subcommand). Claude CLI subprocesses use this to report progress back to SQLite. In-process agents call the same handlers directly without the MCP protocol.
- **Gateway**: Manages up to 16 concurrent Claude CLI subprocesses (`MaxSlots = 16`). Each slot runs a Claude agent with a specific prompt and job context. `SpawnTeam` accepts a per-job `jobDir` parameter that sets the subprocess working directory (`cmd.Dir`) and is embedded in the coordinator prompt. Retained as a fallback alongside the in-process runtime.
- **Provider Registry**: Multi-provider LLM abstraction supporting OpenAI-compatible APIs (LM Studio, Ollama, OpenAI) and Anthropic's Messages API. Providers are configured in YAML and looked up by name. Anthropic supports both API key and Keychain/OAuth authentication.
- **SQLite Persistence**: Operational state stored in SQLite via `modernc.org/sqlite` (pure Go). WAL mode for concurrent reads. Schema includes jobs, tasks, task dependencies, progress reports, skills, agents, teams, team_agents, feed_entries, sessions, and artifacts. Auto-migrating on open. Definition tables (skills, agents, teams) are a runtime cache rebuilt from files on startup; operational tables (jobs, tasks, sessions) are persistent.
- **Bootstrap**: On first run, `internal/bootstrap` copies embedded default system team files from `defaults/system/` to `~/.config/toasters/system/`, creates the `user/` directory structure, and detects auto-teams. On upgrade, migrates old `teams/` layout to `user/teams/`.
- **File-to-DB Loader**: `internal/loader` walks `system/` and `user/` directories on startup, parses all `.md` files with `agentfmt`, resolves agent references (team-local → shared → system), and rebuilds definition tables in SQLite via `RebuildDefinitions`. An fsnotify watcher (200ms debounce) triggers re-loads on file changes.
- **Jobs**: Persisted in SQLite only. Each job has a UUID v4 ID, description, workspace directory, and associated tasks. When a job is created, a per-job subdirectory is auto-created at `<workspace_dir>/<job_id>/` under the global workspace (default `~/toasters`). All agent operations for a job are sandboxed to this directory — team leads, workers, and gateway subprocesses all execute within the job's workspace. The `Job.WorkspaceDir` field stores the absolute path and is propagated through `assign_task` → `SpawnTeamLead` → `CoreTools.workDir` → child agents.
- **Agents**: Defined as `.md` files with YAML frontmatter (superset format supporting Toasters, Claude Code, and OpenCode fields). Key fields: name, description, mode, skills, temperature, max_turns, provider, model, tools, disallowed_tools, permission_mode, permissions, mcp_servers, color, hooks, memory, hidden, disabled. Discovered from directories and hot-reloaded via fsnotify (debounced at 200ms). Parsed via `internal/agentfmt` with auto-detection of source format.
- **Activity Feed**: Feed entries (task assignments, completions, progress updates, blockers) are persisted in SQLite and rendered in the chat viewport. The TUI polls for new entries and displays them as styled messages.
- **CRUD Operations**: Skills, agents, and teams can be created, edited, and deleted via TUI modals (`/skills`, `/agents`, `/teams`). Changes write `.md` files to disk, which triggers fsnotify → loader → DB rebuild, keeping the UI in sync.

## Tech Stack

- **Go 1.26.0**
- **TUI**: Charmbracelet v2 (bubbletea, bubbles, lipgloss) — all stable v2.0.0
- **CLI**: Cobra + Viper
- **Markdown rendering**: Glamour
- **File watching**: fsnotify
- **SQLite**: `modernc.org/sqlite` (pure Go, no CGO)
- **UUIDs**: `github.com/gofrs/uuid/v5` (v4 generation for job and task IDs)
- **LLM integration**: Multi-provider — Anthropic Messages API (direct, with Keychain/OAuth), OpenAI-compatible SSE streaming (LM Studio, OpenAI, Ollama), Claude CLI subprocess fallback

## Code Conventions

- **Packages**: lowercase single word (`agents`, `config`, `gateway`, `llm`, `tui`, `operator`)
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
- **Slash commands**: `/help`, `/new`, `/exit`, `/quit`, `/claude <prompt>`, `/kill`, `/mcp`, `/teams`, `/skills`, `/agents`, `/anthropic`, `/job`
- **Prompt mode**: Numbered options when operator asks user a question

## Testing

Tests exist across 19 test packages. They use standard Go testing with `t.TempDir()` for file I/O and helper functions for assertions. Run `golangci-lint run` for linting — the codebase currently has 0 lint findings.

Key coverage numbers: `llm/tools` 88.3%, `llm/client` 87.7%, `runtime` 87.0%, `provider` 84.9%, `db` 83.6%, `mcp` 83%, `agents` 72.1%, `config` 65.7%.

## Tech Debt Execution Plan (Pre-Phase 3)

Identified via comprehensive codebase health audit (code-reviewer, security-auditor, concurrency-reviewer, refactorer). Findings are organized into three waves by risk and dependency order. Waves 1 and 2 were completed pre-Phase 2. Wave 3 was completed pre-Phase 3. All tech debt is resolved.

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
