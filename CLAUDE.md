# CLAUDE.md

## Project Overview

Toasters is a Go-based TUI orchestration tool for agentic coding work. It coordinates multiple Claude CLI subprocess workers (up to 4 concurrent) through a Bubble Tea interface. An LM Studio "operator" LLM dispatches work to specialized agent teams, which execute autonomously via Claude Code subprocesses.

## Quick Reference

```bash
go build ./...          # Build
go test ./...           # Test (only internal/agents has tests currently)
go run main.go          # Run the TUI
```

## Project Structure

```
main.go                     # Entry point → cmd.Execute()
cmd/                        # Cobra CLI setup, launches TUI
agents/                     # Built-in agent definition files (.md with YAML frontmatter)
internal/
  agents/                   # Agent discovery, parsing, team management
  config/                   # Viper-based config from ~/.config/toasters/config.yaml
  gateway/                  # Claude subprocess slot management (4 concurrent slots)
  llm/                      # LM Studio OpenAI-compatible client + tool definitions
  tui/                      # Bubble Tea TUI (model, styles, commands, claude subprocess)
  workeffort/               # Work effort file persistence (OVERVIEW.md + TODO.md)
```

## Architecture

- **Operator**: LM Studio LLM that coordinates work. Receives user messages, decides which team to assign work to, and manages work efforts.
- **Teams**: Groups of agents defined in `~/.config/toasters/teams/` (or configured via `operator.teams_dir`). Each team has one coordinator and multiple workers.
- **Gateway**: Manages up to 4 concurrent Claude CLI subprocesses (`MaxSlots = 4`). Each slot runs a Claude agent with a specific prompt and work effort context.
- **Work Efforts**: Disk-persisted task units stored in `~/.config/toasters/work-efforts/`. Each has an `OVERVIEW.md` (YAML frontmatter + markdown) and `TODO.md` (GFM checkboxes).
- **Agents**: Defined as `.md` files with YAML frontmatter (name, description, mode, color, temperature, tools). Discovered from directories and hot-reloaded via fsnotify.

## Tech Stack

- **Go 1.25.0**
- **TUI**: Charmbracelet v2 (bubbletea, bubbles, lipgloss) — all pre-release
- **CLI**: Cobra + Viper
- **Markdown rendering**: Glamour
- **File watching**: fsnotify
- **LLM integration**: OpenAI-compatible SSE streaming (LM Studio), Claude CLI subprocess with `--output-format stream-json`

## Code Conventions

- **Packages**: lowercase single word (`agents`, `config`, `gateway`, `llm`, `tui`, `workeffort`)
- **Types**: PascalCase (`Agent`, `Team`, `Gateway`, `SlotSnapshot`, `WorkEffort`)
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
- `operator.teams_dir` — teams directory (default: `~/.config/toasters/teams`)
- `claude.path` — claude binary (default: `"claude"`)
- `claude.default_model` — model for Claude CLI
- `claude.permission_mode` — permission mode for Claude CLI

## Key TUI Interactions

- **Enter**: Send message
- **Shift+Enter**: Newline in input
- **Ctrl+G**: Toggle grid screen (2×2 agent slot view)
- **Ctrl+C**: Quit
- **Slash commands**: `/help`, `/new`, `/exit`, `/quit`, `/claude <prompt>`, `/kill`
- **Prompt mode**: Numbered options when operator asks user a question

## Testing

Tests exist only in `internal/agents/` currently. They use standard Go testing with `t.TempDir()` for file I/O and helper functions for assertions.
