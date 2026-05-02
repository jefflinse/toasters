# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project summary

**toasters** is a TUI-first agentic orchestration platform written in Go. An operator LLM dispatches work to specialized worker sessions that run as in-process goroutines, scheduled through declarative graphs (rhizome). It speaks to multiple LLM providers (Anthropic, OpenAI, LM Studio/llama.cpp/Ollama via OpenAI-compatible endpoints), acts as both an MCP client (consuming external tools) and an MCP server (exposing progress-reporting tools to workers), and persists state in SQLite. UI is Bubble Tea v2 / Lipgloss; CLI is Cobra; config is Viper/YAML.

Core philosophy from `VISION.md` and worth keeping in mind when designing changes: **Go owns the state; LLMs are stateless tools invoked with accumulated context. The orchestrator is the memory, not the model.**

Terminology note: this codebase says **worker** and **node**, never "agent". Roles are also worker definitions, not agents. See feedback memory.

## Build, test, run

No Makefile or task runner — pure Go module (Go 1.26, see `go.mod`).

```bash
go build -o toasters ./                     # build the binary
go run . serve                              # HTTP/SSE server mode (start this first)
go run .                                    # connect TUI to localhost:8421 (default)
go run . --server <addr>                    # connect TUI to a different server

go test ./...                               # all tests
go test -race ./...                         # race detector (kept clean across all packages)
go test -run TestName ./internal/service    # single test in one package
go test -v ./internal/service               # verbose tests for one package

gofmt -w . && goimports -w .                # formatting (no golangci config exists)
```

## Run modes

There are exactly two run modes — server and client — and they're always
in separate processes.

1. **Server** — `cmd/serve.go`. Owns all state: runtime, operator, loader,
   MCP, SQLite, graph executor. Exposes REST + SSE on `:8421` by default.
   Bearer-token auth from `~/.config/toasters/server.token` (mode 0600),
   constant-time comparison; `--no-auth` is dev-only.
2. **TUI client** — `cmd/root.go`. Always a remote client. TCP-probes the
   server before opening the alt-screen so a missing server gives a clean
   error rather than a stranded loading screen. All state comes from the
   service interface; all events come from the unified SSE stream.

## Architecture

The codebase is a layered hub-and-spoke around `internal/service`. Read this section before making changes that cross package boundaries.

- **`internal/service`** is the central hub. The `Service` interface composes 6 sub-interfaces: `OperatorService`, `DefinitionService`, `JobService`, `SessionService`, `EventService`, `SystemService`. `LocalService` in `internal/service/local.go` is the in-process implementation. **All state mutation and queries go through this interface from the TUI and the HTTP server.** The service emits a unified event stream that both the local TUI and SSE clients subscribe to. Event types live in `internal/service/events.go`.

- **`internal/runtime`** spawns and manages worker sessions as goroutines. `runtime.go` is the spawner; `session.go` is one worker's conversation loop; `tools.go` is the built-in tool set (file I/O, shell, web, spawn_worker, MCP routing); `layered_tools.go` handles tool access scoping. There is no team_lead anymore — task dispatch flows through `internal/graphexec`.

- **`internal/operator`** is the operator LLM orchestration layer — a special "session" with its own tool set (`create_job`, `assign_task`, `query_graphs`, `surface_to_user`, `ask_user`, etc.). It drives the top-level state machine. The operator's `LifetimeCtx` config field carries the service lifetime context so detached graph dispatch goroutines are bounded by service shutdown.

- **`internal/graphexec`** runs declarative graphs (rhizome). Tasks dispatch through this engine: each task is bound to a graph, each graph node is a role with typed inputs/outputs, edges are conditional or static. Decomposition (coarse and fine) is itself a graph dispatched through this executor — there is no special "decomposer worker" anymore.

- **`internal/tui`** is a thin Bubble Tea client. `event_consumer.go` translates service events into Bubble Tea messages; `update.go` / `view.go` / `model.go` are the standard Bubble Tea triplet. **The TUI is fully decoupled from DB and runtime internals — don't reintroduce direct access.** It is also a *viewer*, not a *router*: don't add code that pushes state back into the operator from the TUI. Worker completion is reported to the operator through graphexec, not via the TUI.

- **`internal/server`** + **`internal/client`** + **`internal/sse`** — REST/SSE server, HTTP client used in remote-client mode, and SSE protocol utilities. The SSE reader runs a single background pump goroutine; close the underlying response body to unblock it on shutdown.

- **`internal/provider`** — multi-provider LLM client. Aliased over `mycelium/agent` provider types. API keys are required (no OS keychain).

- **`internal/db`** — SQLite (`modernc.org/sqlite`) schema and queries. The "only service touches db" rule is mostly true: TUI/server/client/sse/loader/mcp/hitl/mdfmt/tooldef all go through service. Stateful subsystems that own their own persistence (runtime, operator, graphexec, progress) import `internal/db` directly. If you ever extract `internal/graphexec` as a standalone library, that boundary needs a local repository interface.

- **`internal/loader`** — loads skills/roles/graphs from disk (`~/.config/toasters/`); uses fsnotify to watch for changes.

- **`internal/mcp`** is the MCP client manager (consumes external tools). **`internal/progress`** is the MCP server side that exposes progress-reporting tools back to workers.

- **`internal/prompt`** — role-based prompt composition engine. Roles live in `defaults/system/roles/` and `~/.config/toasters/user/roles/`.

- **`internal/hitl`** — human-in-the-loop broker. Both the operator's `ask_user` tool and graph nodes' `rhizome.Interrupt` route through this single broker.

- Supporting packages: `internal/auth`, `internal/bootstrap`, `internal/compose`, `internal/mdfmt`, `internal/tooldef`, `internal/httputil`, `internal/config`, `internal/modelsdev`.

## Conventions worth knowing

- **TUI never accesses state directly.** All reads return service DTOs; all updates come through the service event stream. When adding a new event type, emit it through the service — not via a side channel — so SSE clients receive it for free.
- **Concurrency.** Sessions use atomic counters for token counts (lock-free reads); event subscriptions are buffered channels (size 256 service-wide, 64 per-worker session); per-session state is mutex-protected. Detached goroutines that outlive a single operator turn (graph dispatch) take the service-lifetime ctx so Shutdown can cancel them.
- **Tests are co-located** (`*_test.go` next to source). Integration tests live in `cmd/integration_test.go`. The race detector is expected to stay clean across all packages.
- **Definitions** (skills, roles, graphs) are markdown files with YAML front matter, parsed by `internal/mdfmt` and `internal/tooldef`. The `mdfmt` package is skill-only; roles and graphs are parsed elsewhere.
- **Errors**: package-level sentinels in `internal/service/errors.go` (`ErrNotFound`, `ErrConflict`, …); HTTP status mapping lives in `internal/server/server.go`.

## Debugging with session transcripts

Every LLM session (worker and operator) is captured for debugging.

**Worker sessions** — stored in SQLite (`toasters.db` in the workspace dir):
```bash
# List recent sessions with their system prompts and tools
sqlite3 toasters.db "SELECT id, worker_id, status, tools_json FROM worker_sessions ORDER BY started_at DESC LIMIT 10"

# Get the full message transcript for a session
sqlite3 toasters.db "SELECT seq, role, content, tool_calls, tool_call_id FROM session_messages WHERE session_id = '<id>' ORDER BY seq"

# Find sessions for a specific job
sqlite3 toasters.db "SELECT id, worker_id, status FROM worker_sessions WHERE job_id = '<job-id>'"

# Read a session's system prompt
sqlite3 toasters.db "SELECT system_prompt FROM worker_sessions WHERE id = '<id>'"
```

**Operator session** — persisted to `~/.config/toasters/sessions/operator.json` after every message. Contains the full conversation (system prompt, tools, all messages with tool calls/results). Useful for debugging why the operator did or didn't call a specific tool.

## Where the deeper docs live

Design intent is captured in root-level markdown files. Reach for these only when you need them:

- `VISION.md` — long-term philosophy and the "Go owns state" insight
- `AMBITIONS.md` — feature ambitions and rationale
- `ROADMAP.md` — high-level roadmap (note: parts predate the graphexec/role-schema refactors and may be stale)
- `API_SPEC.md` — REST + SSE API specification (note: known stale; regenerating it from `internal/server/server.go` route table is on the to-do list)
- `DESIGN_DAG_RUNTIME.md` — the design behind `internal/graphexec`
- `docs/mcp-integration-plan.md` — MCP integration design
