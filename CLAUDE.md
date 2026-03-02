# CLAUDE.md

## Project Overview

Toasters is a Go-based TUI orchestration tool for agentic coding work. It coordinates multiple concurrent LLM-powered agents through a Bubble Tea interface. An operator LLM dispatches work to specialized agent teams, which execute autonomously via in-process API-driven agent sessions.

## Quick Reference

```bash
go build ./...          # Build
go test ./...           # Test (18 test packages)
go run main.go          # Run the TUI
```

## Project Structure

```
main.go                     # Entry point → cmd.Execute()
cmd/                        # Cobra CLI setup, launches TUI
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
                            #   Auto-detection via ParseAgent/ParseFile (inspects frontmatter fields)
                            #   Type detection: team fields → agent-only fields → skill (default)
                            #   Note: "tools" is NOT agent-only (skills can declare tools too)
                            #   1 MiB file size limit on all Parse* functions
  anthropic/                # Keychain/OAuth token management for Anthropic API
                            #   keychain.go: ReadKeychainAccessToken(), token refresh with mutex
  bootstrap/                # First-run bootstrap + upgrade migration
                            #   Copies embedded defaults to ~/.config/toasters/system/
                            #   Creates user/ directory structure, auto-team detection
                            #   Auto-team dismiss markers: .dismissed/<name> prevents re-import
  compose/                  # Runtime composition / prompt assembly
                            #   Loads agent → skills → team culture → merges tools → resolves provider/model
                            #   Returns ComposedAgent ready for session creation
  config/                   # Viper-based config from ~/.config/toasters/config.yaml
                            #   Warns on plaintext API keys at startup, enforces 0600 config file permissions
  db/                       # SQLite persistence (Store interface, migrations, CRUD)
                            #   Schema: jobs, tasks, skills, agents, teams, team_agents, feed_entries,
                            #   sessions, progress_reports, artifacts
                            #   RebuildDefinitions: transactional delete-all + insert-all for definition tables
                            #   db.Team and db.Agent are the canonical types used everywhere
  httputil/                 # Shared SSRF protection and safe HTTP clients
                            #   IsPrivateIP(), NewSafeClient(), SafeGet() — used by runtime and operator
  loader/                   # File-to-DB loader + fsnotify watcher (single source of truth for definitions)
                            #   Walks system/ + user/ dirs, parses .md files with agentfmt
                            #   Resolves agent references (team-local → shared → system)
                            #   Watcher: 200ms debounce, .md filtering, dynamic dir watching
  mcp/                      # MCP client manager, tool conversion, namespacing, result truncation/slimming, server status tracking
                            #   Parallel server connection via WaitGroup, recover() on Call for shutdown safety
                            #   Dangerous env var filtering (LD_PRELOAD, DYLD_*, etc.) on stdio subprocess creation
  operator/                 # Operator event loop, typed events, system/team tools
                            #   Event loop: mechanical handling + selective LLM routing
                            #   System tools: create_job, create_task, assign_task, query_teams, query_job
                            #   Team lead tools: complete_task, report_blocker, report_progress
                            #   Worker tools: report_progress, query_team_context
                            #   Operator tools: consult_agent (composition-based), surface_to_user, query_job, query_teams
                            #   Conversation truncation: boundary-aware (never splits tool-call/result pairs)
  progress/                 # Progress tool handlers (report_progress, etc.)
  provider/                 # Multi-provider LLM client (OpenAI, Anthropic, registry)
  runtime/                  # In-process agent runtime (sessions, core tools, spawn)
                            #   composite_tools.go: CompositeTools wrapper combining CoreTools + MCP tools
                            #   Shutdown: WaitGroup-based with 10s timeout (no busy-wait)
  service/                  # Use-case-level service interface (client/server split boundary)
                            #   service.go: composed Service interface + 6 sub-interfaces (Operator, Definitions,
                            #               Jobs, Sessions, Events, System)
                            #   types.go: all service-level DTOs — zero imports from internal packages
                            #   events.go: unified event stream (19 event types, Event envelope, EventService)
                            #   LocalService (Step 1.2): in-process impl delegating to db/operator/runtime/mcp
                            #   RemoteClient (Phase 2): HTTP+SSE impl for connecting to standalone server
  sse/                      # Shared SSE parsing (reader, Anthropic event types, OpenAI chunk types)
  tooldef/                  # Shared ToolDef and MCPCaller types (used by runtime, progress, mcp)
  tui/                      # Bubble Tea TUI (model, views, grid, modals, streaming, activity feed, CRUD)
                            #   All interaction flows through the operator event loop (no legacy direct-LLM path)
                            #   team_view.go: TeamView type bundles db.Team + coordinator + workers from store
                            #   progress_poll.go: SQLite polling loop for real-time progress display
                            #   skills_modal.go: Skills browse/CRUD modal (create, edit, delete skills)
                            #   agents_modal.go: Agents browse/CRUD modal (create, edit, delete agents)
                            #   teams_modal.go: Teams browse modal with auto-team promotion (Ctrl+P)
```

## Architecture

- **Operator**: LLM that coordinates work. Receives user messages, decides which team to assign work to, and manages jobs. Can be backed by any configured provider (LM Studio, Anthropic, OpenAI). Runs as a code-driven event loop that handles routine events mechanically (task started/completed, progress updates) and only routes decision-requiring events to the LLM (user messages, failures, blockers, recommendations). Uses `consult_agent` to delegate to system agents (planner, scheduler, blocker-handler). The operator's system prompt is composed at startup via `composer.Compose(ctx, "operator", "system")`, reading from `defaults/system/agents/operator.md` through the same composition pipeline used by all other agents.
- **System Team**: The operator's own team, defined in `~/.config/toasters/system/`. Includes the operator (lead), planner (creates jobs/tasks), scheduler (breaks plans into tasks with assignments), and blocker-handler (triages blockers). System agents have orchestration tools (`create_job`, `create_task`, `assign_task`) but NO filesystem tools. Fully hackable — users can modify any system agent.
- **Teams**: Groups of agents defined in `~/.config/toasters/user/teams/`. Each team has a lead agent and worker agents. Team leads receive tasks from the operator, delegate to workers via `spawn_agent`, and report results via `complete_task`. Teams can also be auto-detected from `~/.claude/agents/` and `~/.config/Claude/agents/`. Auto-teams can be promoted to full teams via `Ctrl+P` in the teams modal.
- **Composition Model**: Three-layer composition: Skills (reusable capabilities with prompts + tools) → Agents (personas with skills, provider/model config) → Teams (agents + culture + lead). At runtime, `internal/compose` assembles the full system prompt, tool set, and provider/model for any agent. Skills are additive, tools are unioned with denylist, provider/model cascades (agent → team → global default).
- **Agent Runtime**: In-process agent sessions running as goroutines. Each session is a conversation loop: send messages to the LLM → receive response → execute tool calls → loop. Core tools include file I/O, shell, glob, grep, web fetch, subagent spawning, and progress reporting (`report_progress`, `update_task_status`, `report_blocker`, `request_review`, `query_job_context`, `log_artifact`). Sessions are tracked in SQLite and observable via the TUI. `spawn_agent` enforces a max depth of 1 (coordinators may spawn workers; workers may not spawn further agents) and propagates tool filtering via `filteredToolExecutor`. `disallowed_tools` denylist is enforced at both the definition layer (tools excluded from `Definitions()`) and execution layer (rejected in `Execute()`) for defense-in-depth.
- **MCP Client**: `internal/mcp` package manages connections to external MCP servers (GitHub, Jira, Linear, etc.). Tools are namespaced as `{server_name}__{tool_name}` and merged into both the operator and agent tool sets. Servers connect in parallel via `sync.WaitGroup`; failed servers are skipped with a warning. Server connection status is tracked and exposed via `Servers()` accessor. MCP tool results are automatically slimmed (strips nulls, `*_url` fields, API URLs, `node_id`, opaque blobs) and truncated (JSON-aware array shrinking with UTF-8 safe byte fallback, 16KB default) to prevent context window exhaustion.
- **Provider Registry**: Multi-provider LLM abstraction supporting OpenAI-compatible APIs (LM Studio, Ollama, OpenAI) and Anthropic's Messages API. Providers are configured in YAML and looked up by name. Anthropic supports both API key and Keychain/OAuth authentication.
- **SQLite Persistence**: Operational state stored in SQLite via `modernc.org/sqlite` (pure Go). WAL mode for concurrent reads. Schema includes jobs, tasks, task dependencies, progress reports, skills, agents, teams, team_agents, feed_entries, sessions, and artifacts. Auto-migrating on open. Definition tables (skills, agents, teams) are a runtime cache rebuilt from files on startup; operational tables (jobs, tasks, sessions) are persistent.
- **Bootstrap**: On first run, `internal/bootstrap` copies embedded default system team files from `defaults/system/` to `~/.config/toasters/system/`, creates the `user/` directory structure, and detects auto-teams. On upgrade, migrates old `teams/` layout to `user/teams/`.
- **File-to-DB Loader**: `internal/loader` walks `system/` and `user/` directories on startup, parses all `.md` files with `agentfmt`, resolves agent references (team-local → shared → system), and rebuilds definition tables in SQLite via `RebuildDefinitions`. An fsnotify watcher (200ms debounce) triggers re-loads on file changes.
- **Jobs**: Persisted in SQLite only. Each job has a UUID v4 ID, description, workspace directory, and associated tasks. When a job is created, a per-job subdirectory is auto-created at `<workspace_dir>/<job_id>/` under the global workspace (default `~/toasters`). All agent operations for a job are sandboxed to this directory — team leads and workers all execute within the job's workspace. The `Job.WorkspaceDir` field stores the absolute path and is propagated through `assign_task` → `SpawnTeamLead` → `CoreTools.workDir` → child agents.
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
- **LLM integration**: Multi-provider — Anthropic Messages API (direct, with Keychain/OAuth), OpenAI-compatible SSE streaming (LM Studio, OpenAI, Ollama)

## Code Conventions

- **Packages**: lowercase single word (`config`, `compose`, `tui`, `operator`)
- **Types**: PascalCase (`Agent`, `Team`, `Job`)
- **Constants**: SCREAMING_SNAKE or PascalCase for exported (`InputHeight`)
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
- `mcp.servers` — list of MCP server configs (name, transport, command, args, env, url, headers, enabled, enabled_tools)

## Key TUI Interactions

- **Enter**: Send message
- **Shift+Enter**: Newline in input
- **Ctrl+G**: Toggle grid screen (dynamic NxM agent slot view, scales with terminal size)
- **Ctrl+C**: Quit
- **Slash commands**: `/help`, `/new`, `/exit`, `/quit`, `/mcp`, `/teams`, `/skills`, `/agents`, `/job`
- **Prompt mode**: Numbered options when operator asks user a question

## Testing

Tests exist across 18 test packages. They use standard Go testing with `t.TempDir()` for file I/O and helper functions for assertions. Run `golangci-lint run` for linting — the codebase currently has 0 lint findings.

## Tech Debt Execution Plan

Identified via comprehensive codebase health audits. Organized into waves by risk and dependency order.

### Pre-Phase 2 Waves 1-2 ✅

**Status: Complete (pre-Phase 2)**

Wave 1: All data race and security fixes (CONC-B1–B4, SEC-C1–C4, SEC-H1–H2).
Wave 2: All 16 quick wins (ARCH-H3/H4, DUP-M1, MOD-M1–M7, LINT, CONC-H1–H3/M1, SEC-H3).

### Pre-Phase 3 Wave 3 ✅

**Status: Complete (2026-02-25, pre-Phase 3)**

ARCH-H1 (SSE consolidation), ARCH-H2 (single provider interface), DESIGN-H1 (TUI decomposition), DESIGN-M1 (tool registry), MOD-M8 (slog migration), ARCH-GATEWAY (legacy gateway removal).

### Pre-Phase 4 Wave 1 — Safety & Cleanup ✅

**Status: Complete (2026-02-27)**

Full details: `PRE_PHASE_4_WAVE_1.md`

- [x] **SEC-CRITICAL-1**: Fixed `setup_workspace` command injection — URL scheme validation, flag injection rejection, repo name validation, `--` separator
- [x] **SEC-HIGH-2**: Expanded `.gitignore` (1 → 27 lines) — covers DB, logs, config, env, coverage, IDE files
- [x] **DEAD-1**: Deleted ~4,600 lines of legacy `llm` package family — extracted keychain helpers to `internal/anthropic/keychain.go`, deleted `internal/llm/client/`, `internal/llm/types.go`, `internal/llm/provider.go`, `internal/anthropic/client.go`
- [x] **STRUCT-1** (partial): Extracted shared SSRF protection into `internal/httputil/` — consolidated duplicate `privateNetworks`/`isPrivateIP` from runtime and operator tools
- [x] **SEC-MEDIUM-1/2**: Added 10MB limit to `editFile`, 50MB limit to `writeFile`
- [x] **CONC-4**: Replaced `Runtime.Shutdown()` busy-wait with `sync.WaitGroup` + 10s timeout
- [x] **QUAL-1**: Fixed `fetchWebpage` to use `http.NewRequestWithContext`
- [x] **SEC-MEDIUM-3**: Added `sync.Mutex` to `ReadKeychainAccessToken()` for token refresh serialization

### Pre-Phase 4 Wave 2 — Structural Preparation ✅

**Status: Complete (2026-02-27)**

Full details: `PRE_PHASE_4_WAVE_2.md`

- [x] **DEAD-2**: Consolidated dual agent/team type systems — deleted `internal/agents/` package (755 lines), replaced with `TeamView` in TUI backed by `db.Store` queries, removed duplicate file watcher and `DiscoverTeams()` from boot sequence. Single source of truth: `loader` → `db.Store`.
- [x] **DEAD-3 + STRUCT-1**: Deleted `internal/llm/tools/` operator tool dispatcher (2,802 lines) — superseded by operator's `SystemTools`. Removed `internal/llm/` directory entirely.
- [x] **ARCH-5**: Removed legacy TUI streaming path — deleted `startStream`, `sendAnthropicMessage`, `waitForChunk`, `StreamChunkMsg`/`StreamDoneMsg`/`ToolCallMsg`/`ToolResultMsg` handlers, `executeToolsCmd`, `tool_exec.go`, `/anthropic` command. All interaction now flows through the operator event loop.
- [x] **ARCH-3**: Fixed conversation window truncation — boundary-aware `truncateMessages()` that never splits tool-call/result pairs
- [x] **ARCH-2/CONC-2**: Fixed self-send deadlock potential — `EventTaskStarted` handled inline instead of sent through event channel
- [x] **STRUCT-2**: Consolidated `ToolDef` type — shared `internal/tooldef/` package replaces duplicate definitions in runtime and progress
- [x] **CONC-6**: Fixed post-shutdown TUI sends — nil-guard all `prog.Send()` call sites, clear atomic pointer after `prog.Run()` returns

### Pre-Phase 4 Wave 3 — QOL Batch ✅

**Status: Complete (2026-02-28)**

20 QOL fixes across 13 packages — security hardening, concurrency fixes, type consolidation, style cleanup, and documentation.

- [x] **QUAL-7**: Fixed `SplitFrontmatter` Windows line endings — added `\r` to delimiter detection
- [x] **QUAL-3**: Removed all store nil guards from `CoreTools` — store is required, not optional
- [x] **SEC-HIGH-3**: Added plaintext API key warning at startup + `chmod 0600` on config file
- [x] **SEC-MEDIUM-4**: Fixed `glob` pattern traversal — base directory validated within workspace
- [x] **CONC-3**: Fixed MCP Manager `Close()` race — `recover()` wrapper in `Call()` catches use-after-close panics
- [x] **CONC-8**: Parallelized MCP server connections via `sync.WaitGroup`
- [x] **CONC-1**: Documented `Session.messages` concurrency contract
- [x] **STRUCT-3**: Consolidated `ProviderConfig` — single definition in `provider` package, removed duplicate from `config`
- [x] **STRUCT-4**: Consolidated `MCPCaller` interface — canonical definition in `tooldef` package, type alias in `runtime`
- [x] **STRUCT-7**: Added `DefinitionsByName()` helper to `CoreTools`, eliminated manual map construction in `SpawnTeamLead`
- [x] Standardized UUID library — all code uses `gofrs/uuid/v5`, removed `google/uuid` as direct dependency
- [x] Added `disallowed_tools` denylist enforcement at execution time — defense-in-depth in `CoreTools.Execute()`
- [x] Added 1 MiB file size limit to `agentfmt.ParseFile` (matches loader limit)
- [x] Deduplicated tool schemas in `progress/server.go` — driven by `ProgressToolDefs()`, removed ~100 lines of inline JSON
- [x] Standardized TUI modal styles — renamed `Teams*` prefixes to `Modal*`, removed alias indirection
- [x] Moved `editorFinishedMsg` to `messages.go` (shared TUI message types)
- [x] Fixed `humanizeDirName` abbreviations — QA, CI, CD, API, UI, UX, DB, ML, AI, SRE, DevOps
- [x] Added doc comments on exported `agentfmt` export functions and `loader.Slugify`
- [x] Cleaned up 14 stale/resolved deferred items from `PHASE_4.md`

### Pre-Phase 4 Wave 4 — QOL Batch ✅

**Status: Complete (2026-02-28)**

6 QOL fixes with security hardening and test coverage — reviewed by code-reviewer, security-auditor, and concurrency-reviewer.

- [x] **SEC-MEDIUM-5**: Filter dangerous env vars (`LD_PRELOAD`, `DYLD_*`, `LD_DEBUG_OUTPUT`, etc.) from MCP subprocess config — 12-var denylist
- [x] **workspace_dir validation**: `create_job` and `assign_task` reject workspace directories outside `$HOME` — symlink-aware with `EvalSymlinks`
- [x] **QUAL-2**: Added 13 tests for `cmd/awareness.go` pure functions with mock provider
- [x] **Auto-team dismiss bug**: Persist `.dismissed/<name>` marker files so deleted auto-teams are not re-created on restart
- [x] **QUAL-6**: Removed `tools` from `agentOnlyFields` — skills with tools no longer misclassified as agents
- [x] **ARCH-4**: Batch operator text tokens (~16ms) via `textBatcher` before flushing to TUI — prevents message queue flooding

### Remaining Findings (from PRE_PHASE_4_ARCH_REVIEW.md)

33 findings resolved across Waves 1-4. Remaining open findings (7) are tracked in `PRE_PHASE_4_ARCH_REVIEW.md` Section 10. Key remaining items for future waves:
- **ARCH-1/CONC-5**: Operator blocks during tool execution (non-blocking tool execution — large effort)
- **SEC-HIGH-1**: Shell tool sandboxing (design tradeoff — large effort)
- **CONC-7**: Subscriber event drops (intentional design, no fix needed)
- **QUAL-4**: `RebuildDefinitions` duplicates insert logic (low impact)
- **QUAL-5**: No incremental definition updates (not needed at current scale)

## Current Work: Client/Server Architecture Split

**Status:** Phase 1 in progress — Steps 1.1 and 1.2 complete, Step 1.3 in progress
**Tracking document:** [`CLIENT_SERVER_SPLIT.md`](CLIENT_SERVER_SPLIT.md)

Splitting the monolithic TUI into a client/server architecture. The orchestration engine (operator, runtime, store, MCP, loader, compose, providers) becomes a long-running server; the TUI becomes a thin client. REST + SSE protocol. 4 phases:

1. **Phase 1: Service Extraction** (5–8 days) — Extract business logic from TUI into `internal/service` package with composed `Service` interface. Rewire TUI to use it. No networking yet.
2. **Phase 2: Server** (3–5 days) — HTTP server with SSE event streaming. `RemoteClient` as drop-in for `LocalService`.
3. **Phase 3: Mode Wiring** (1–2 days) — `toasters serve` (headless), `toasters --server <addr>` (remote TUI), CLI subcommands.
4. **Phase 4: Hardening** (2–3 days) — Token auth, connection resilience, security audit.

Plan reviewed by tui-engineer and api-designer. All blocking concerns documented in the tracking doc.

### Phase 1 Progress

**Step 1.1: Service Interface** ✅ — `internal/service/` created with three files:
- `service.go` — composed `Service` interface with 6 sub-interfaces: `OperatorService`, `DefinitionService`, `JobService`, `SessionService`, `EventService`, `SystemService`
- `types.go` — all service-level DTO types; zero imports from internal packages
- `events.go` — unified event stream: 19 event types, `Event` envelope with sequence numbers + correlation IDs, `EventService.Subscribe(ctx)`

**Step 1.2: Implement LocalService** ✅ — `internal/service/local.go` created; full `Service` interface satisfied; reviewed by concurrency-reviewer, security-auditor, and code-reviewer; all blocking findings fixed
**Step 1.3: Rewire TUI** ✅ — complete; all TUI files rewired to use `service.Service`; zero banned imports; `progressPollCmd` replaced by `event_consumer.go`; `team_view.go` and `progress_poll.go` deleted; `cmd/root.go` rewritten
**Step 1.4: Service Tests** ✅ — complete; 67 test functions in `internal/service/local_test.go`; all pass race-clean

**Phase 1 Review Checkpoint** — next gate: human review of TUI decoupling to verify no residual direct dependencies between TUI and internals
