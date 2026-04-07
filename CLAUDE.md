# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project summary

**toasters** is a TUI-first agentic orchestration platform written in Go. An operator LLM dispatches work to specialized agent teams that run as in-process goroutines. It speaks to multiple LLM providers (Anthropic, OpenAI, LM Studio/opencode), acts as both an MCP client (consuming external tools) and an MCP server (exposing progress-reporting tools to agents), and persists state in SQLite. UI is Bubble Tea v2 / Lipgloss; CLI is Cobra; config is Viper/YAML.

Core philosophy from `VISION.md` and worth keeping in mind when designing changes: **Go owns the state; LLMs are stateless tools invoked with accumulated context. The orchestrator is the memory, not the model.**

## Build, test, run

No Makefile or task runner — pure Go module (Go 1.25, see `go.mod`).

```bash
go build -o toasters ./                     # build the binary
go run .                                    # embedded TUI mode
go run . serve                              # HTTP/SSE server mode
go run . --server <addr>                    # remote TUI client mode

go test ./...                               # all tests
go test -race ./...                         # race detector (kept clean across all packages)
go test -run TestName ./internal/service    # single test in one package
go test -v ./internal/service               # verbose tests for one package

gofmt -w . && goimports -w .                # formatting (no golangci config exists)
```

## Run modes

The same backend can be exercised three ways — knowing which one you're working in matters because session/event wiring differs:

1. **Embedded TUI** — `cmd/root.go`. LocalService + Runtime + Operator + TUI in one process. Session-started events flow via a direct `rt.OnSessionStarted` callback into Bubble Tea.
2. **Server** — `cmd/serve.go`. Same backend, no TUI, exposes REST + SSE for remote clients. Bearer-token auth from `~/.config/toasters/server.token` (mode 0600), constant-time comparison; `--no-auth` is dev-only.
3. **Remote client** — `cmd/client_mode.go`. The TUI runs locally and talks to a remote server over HTTP + SSE.

## Architecture

The codebase is a layered hub-and-spoke around `internal/service`. Read this section before making changes that cross package boundaries.

- **`internal/service`** is the central hub. The `Service` interface composes 5 sub-interfaces: `OperatorService`, `DefinitionService`, `JobService`, `SessionService`, `SystemService`. `LocalService` in `internal/service/local.go` is the in-process implementation. **All state mutation and queries go through this interface** — the TUI and the HTTP server never touch the DB directly. The service emits a unified event stream that both the local TUI and SSE clients subscribe to. Event types live in `internal/service/events.go`.

- **`internal/runtime`** spawns and manages agent sessions as goroutines. `runtime.go` is the spawner; `session.go` is one agent's conversation loop; `tools.go` is the built-in tool set (file I/O, shell, web, subagents, MCP routing); `team_lead.go` is the coordinator agent for team dispatch; `layered_tools.go` handles tool access scoping.

- **`internal/operator`** is the operator LLM coordination layer — a special "session" with its own tool set (`createJob`, `assignTask`, `reportBlocker`, team queries, decomposer tools). It drives the top-level state machine.

- **`internal/tui`** is a thin Bubble Tea client. `event_consumer.go` translates service events into Bubble Tea messages; `update.go` / `view.go` / `model.go` are the standard Bubble Tea triplet. **The TUI is fully decoupled from DB and runtime internals — don't reintroduce direct access** (see `TUI_COUPLING_AUDIT.md` and recent commit `accae68` which explicitly removed the last filesystem access from TUI code).

- **`internal/server`** + **`internal/client`** + **`internal/sse`** — REST/SSE server, HTTP client used in remote-client mode, and SSE protocol utilities.

- **`internal/provider`** — multi-provider LLM client. Provider config was recently restructured (commit `12de16d`) with stable IDs and nested agent defaults, and API keys are required (no OS keychain — see commit `79906ec`).

- **`internal/db`** — SQLite (`modernc.org/sqlite`) schema and queries. **Only `internal/service` should call into this package.**

- **`internal/loader`** — loads skills/agents/teams from disk (`~/.config/toasters/`, plus `~/.opencode/agents/` for auto-team detection); uses fsnotify to watch for changes.

- **`internal/mcp`** is the MCP client manager (consumes external tools). **`internal/progress`** is the MCP server side that exposes progress-reporting tools back to agents.

- Supporting packages: `internal/auth`, `internal/bootstrap`, `internal/compose`, `internal/agentfmt`, `internal/tooldef`, `internal/httputil`, `internal/config`.

## Conventions worth knowing

- **TUI never accesses state directly.** All reads return service DTOs; all updates come through the service event stream. When adding a new event type, emit it through the service — not via a side channel — so SSE clients receive it for free.
- **Concurrency.** Sessions use atomic counters for token counts (lock-free reads); event subscriptions are buffered channels (size 64); per-session state is mutex-protected.
- **Tests are co-located** (`*_test.go` next to source). Integration tests live in `cmd/integration_test.go`. The race detector is expected to stay clean across all packages.
- **Definitions** (skills, agents, teams) are markdown files with YAML front matter, parsed by `internal/agentfmt` and `internal/tooldef`.
- **Errors**: package-level sentinels in `internal/service/errors.go` (`ErrNotFound`, `ErrConflict`, …); HTTP status mapping lives in `internal/server/server.go`.

## Where the deeper docs live

The repo has no README; design intent is captured in root-level markdown files. Reach for these only when you need them — don't pre-load them all:

- `VISION.md` — long-term philosophy and the "Go owns state" insight
- `AMBITIONS.md` — feature ambitions and rationale
- `ROADMAP.md`, `PHASE_1.md` … `PHASE_4.md`, `PRE_PHASE_4_*.md` — phased delivery roadmap
- `CLIENT_SERVER_SPLIT.md` and `CLIENT_SERVER_SPLIT_REMAINING.md` — current architectural work and known gaps
- `API_SPEC.md` — REST + SSE API specification
- `TUI_COUPLING_AUDIT.md` — rationale behind the TUI decoupling rule
- `docs/mcp-integration-plan.md` — MCP integration design

## Current focus (mid-2026)

The project is mid-way through a client-server split refactor. The in-flight P0 is wiring session events through the service event stream so server mode actually broadcasts agent activity — currently `rt.OnSessionStarted` is set to a no-op in `cmd/serve.go` and remote SSE clients see nothing for session lifecycle. See `CLIENT_SERVER_SPLIT_REMAINING.md` and `P0.md` for the live picture before starting work in `internal/service`, `cmd/serve.go`, or `internal/tui/event_consumer.go`.
