# CLAUDE.md

## Project Overview

Toasters is a Go-based TUI orchestration tool for agentic coding work. It coordinates multiple concurrent LLM-powered agents through a Bubble Tea interface. An operator LLM dispatches work to specialized agent teams, which execute autonomously via in-process API-driven agent sessions (with Claude Code subprocess fallback).

## Quick Reference

```bash
go build ./...          # Build
go test ./...           # Test (12 test packages)
go run main.go          # Run the TUI
```

## Project Structure

```
main.go                     # Entry point → cmd.Execute()
cmd/                        # Cobra CLI setup, launches TUI
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
  orchestration/            # Cross-cutting orchestration types (GatewaySlot, AgentSpawner)
  provider/                 # Multi-provider LLM client (OpenAI, Anthropic, registry)
  runtime/                  # In-process agent runtime (sessions, core tools, spawn)
  tui/                      # Bubble Tea TUI (model, views, grid, modals, streaming)
  job/                      # Job file persistence (OVERVIEW.md + TODO.md)
```

## Architecture

- **Operator**: LLM that coordinates work. Receives user messages, decides which team to assign work to, and manages jobs. Can be backed by any configured provider (LM Studio, Anthropic, OpenAI).
- **Teams**: Groups of agents defined in `~/.config/toasters/teams/` (or configured via `operator.teams_dir`). Each team has one coordinator and multiple workers.
- **Agent Runtime**: In-process agent sessions running as goroutines. Each session is a conversation loop: send messages to the LLM → receive response → execute tool calls → loop. Core tools include file I/O, shell, glob, grep, web fetch, and subagent spawning. Sessions are tracked in SQLite and observable via the TUI.
- **Gateway**: Manages up to 4 concurrent Claude CLI subprocesses (`MaxSlots = 4`). Each slot runs a Claude agent with a specific prompt and job context. Retained as a fallback alongside the in-process runtime.
- **Provider Registry**: Multi-provider LLM abstraction supporting OpenAI-compatible APIs (LM Studio, Ollama, OpenAI) and Anthropic's Messages API. Providers are configured in YAML and looked up by name. Anthropic supports both API key and Keychain/OAuth authentication.
- **SQLite Persistence**: Operational state stored in SQLite via `modernc.org/sqlite` (pure Go). WAL mode for concurrent reads. Schema includes jobs, tasks, task dependencies, progress reports, agents, teams, sessions, and artifacts. Auto-migrating on open.
- **Jobs**: Dual-persisted — markdown files (`OVERVIEW.md` + `TODO.md`) for human readability, SQLite for structured queries. New jobs are written to both.
- **Agents**: Defined as `.md` files with YAML frontmatter (name, description, mode, color, temperature, tools). Discovered from directories and hot-reloaded via fsnotify.

## Tech Stack

- **Go 1.25.0**
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

Tests exist across 12 test packages. They use standard Go testing with `t.TempDir()` for file I/O and helper functions for assertions. Run `golangci-lint run` for linting — the codebase currently has 0 lint findings.

Key coverage numbers: `frontmatter` 100%, `llm/tools` 88.3%, `llm/client` 87.7%, `runtime` 87.0%, `job` 85.7%, `provider` 84.9%, `db` 83.6%, `agents` 72.1%, `config` 65.7%.
