# CLAUDE.md

## Project Overview

Toasters is a TUI orchestrator for agentic coding work. It coordinates multiple `claude` CLI subprocess invocations to accomplish multi-step tasks. Go owns all state; LLMs are invoked fresh each time with accumulated context fed back in.

This is a prototype (`proto/tui-chat` branch). APIs, architecture, and agent formats are all subject to change.

## Tech Stack

- **Go 1.25** — requires Go 1.25+ toolchain
- **Bubble Tea v2** (`charm.land/bubbletea/v2 v2.0.0-rc.2`) — TUI framework
- **Lipgloss v2** (`charm.land/lipgloss/v2 v2.0.0-beta.3`) — styling
- **Bubbles v2** (`charm.land/bubbles/v2 v2.0.0-rc.1`) — TUI components (textarea, viewport)
- **Glamour** (`github.com/charmbracelet/glamour v0.10.0`) — markdown rendering
- **Cobra + Viper** — CLI flags and config management

The Charm v2 ecosystem uses `charm.land/` import paths (not `github.com/charmbracelet/`). These are pre-release; APIs may shift.

## Build & Test

```bash
go build ./...
go test ./...
```

No Makefile, Dockerfile, or CI/CD pipeline exists. The binary entry point is `main.go` → `cmd.Execute()`.

### Running

```bash
go run . [--operator-endpoint URL] [--claude-path PATH]
```

Requires LM Studio running at `localhost:1234` (or configured endpoint) and the `claude` CLI on `$PATH`.

## Project Structure

```
cmd/root.go              CLI entry point (Cobra), wires everything, starts TUI
internal/
  agents/                Agent loading, frontmatter parsing, registry, hot-reload watcher
  config/                Viper config from ~/.config/toasters/config.yaml
  gateway/               4-slot concurrent Claude subprocess manager
  llm/
    client.go            OpenAI-compatible streaming API client (LM Studio)
    tools.go             Tool definitions, executor, AgentSpawner interface
  tui/
    model.go             Root Bubble Tea model (~2300 lines)
    styles.go            Color palette and layout styles
    commands.go          Slash command definitions
    claude.go            Claude CLI subprocess integration (TUI-side)
  workeffort/            Work effort CRUD, OVERVIEW.md + TODO.md disk I/O
agents/                  Bundled agent definitions (operator, investigator, planner, executor, summarizer)
VISION.md                Product vision and architecture notes
```

## Architecture

Three-tier system:

1. **TUI** (Bubble Tea) — user interface, input, rendering, streaming display
2. **Gateway** — manages up to 4 concurrent Claude CLI subprocess slots (`gateway.MaxSlots = 4`), mutex-protected
3. **LLM Client** — talks to LM Studio (local operator) via OpenAI-compatible `/v1/chat/completions`

### Execution Flow

1. User input goes to the operator (coordinator agent) via LM Studio
2. Operator can call `run_agent` tool to spawn Claude CLI subprocesses in gateway slots
3. Gateway assembles prompt (agent body + work effort context + task), spawns `claude --print --output-format stream-json --include-partial-messages` via stdin
4. Stream output is parsed (system/init event, stream_event deltas, assistant events, result events) and piped back to TUI
5. Operator is notified when agents complete and can follow up

### Key Design Decisions

- **Prompts via stdin**: The `--allowedTools` flag greedily consumes positional args, so prompts are always passed via stdin
- **AgentSpawner interface**: `llm.AgentSpawner` breaks the import cycle between `llm` and `gateway`
- **Channel-based streaming**: Both LM Studio SSE and Claude CLI stream-json use the same `llm.StreamResponse` channel type
- **State on disk**: Work efforts persist as `OVERVIEW.md` (YAML frontmatter + markdown body) and `TODO.md` (GFM checkboxes) under `~/.config/toasters/work-efforts/{id}/`

## Configuration

Config file: `~/.config/toasters/config.yaml`

| Key | Type | Default | Description |
|---|---|---|---|
| `operator.endpoint` | string | `http://localhost:1234` | LM Studio API URL |
| `operator.api_key` | string | `""` | API key (if required) |
| `operator.model` | string | `""` | Model name (empty = LM Studio default) |
| `operator.coordinator_agent` | string | `""` | Agent name to use as coordinator |
| `operator.agents_dir` | string | `~/.opencode/agents/` | Directory to discover agent `.md` files |
| `operator.log_requests` | bool | `false` | Log outgoing LLM requests to `requests.log` |
| `claude.path` | string | `claude` | Path to `claude` binary |
| `claude.default_model` | string | `""` | Default model for Claude CLI invocations |
| `claude.permission_mode` | string | `""` | Default permission mode for Claude CLI |

## Agent Format

Agents are `.md` files with optional YAML-like frontmatter:

```markdown
---
description: One-line description
mode: primary
color: "#FF9800"
temperature: 0.7
tools:
  bash: false
  write: true
  edit: true
---
System prompt body goes here.
```

Frontmatter fields: `description`, `mode` (`primary` = coordinator, anything else = worker), `color` (hex), `temperature`, `tools` (block of `key: true/false`).

The `tools:` block controls Claude CLI permission flags:
- **No tools block** → `--dangerously-skip-permissions` (full access)
- **Tools block present** → `--permission-mode acceptEdits --allowedTools <allowed-list>` (denied tools subtracted from full set)
- Tool name mapping: `bash`→`Bash`, `write`→`Write`, `edit`→`Edit`

## Tool Catalog

Tools exposed to the operator LLM:

| Tool | Description |
|---|---|
| `run_agent` | Spawn a Claude CLI subprocess in a gateway slot |
| `work_effort_list` | List all work efforts |
| `work_effort_create` | Create a new work effort |
| `work_effort_read_overview` | Read OVERVIEW.md for a work effort |
| `work_effort_read_todos` | Read TODO.md for a work effort |
| `work_effort_update_overview` | Overwrite or append to OVERVIEW.md body |
| `work_effort_add_todo` | Append a new unchecked TODO item |
| `work_effort_complete_todo` | Mark a TODO item as done (by index or text match) |
| `fetch_webpage` | Fetch a URL and return plain text (HTML stripped) |
| `list_directory` | List directory contents |
| `ask_user` | Pause and ask the user a question with options |

## TUI Keyboard Shortcuts

| Key | Action |
|---|---|
| `Enter` | Send message |
| `Shift+Enter` | Insert newline |
| `Esc` | Cancel in-flight stream |
| `Ctrl+C` | Quit |
| `Ctrl+G` | Toggle grid view (2x2 agent slot overview) |
| `/` | Trigger slash command autocomplete |

Slash commands: `/help`, `/new`, `/exit`, `/quit`, `/claude <prompt>`, `/kill`

## Code Conventions

- **Error handling**: Errors returned with context via `fmt.Errorf("context: %w", err)`. Non-fatal errors logged with `log.Printf`.
- **Concurrency**: Goroutines + channels + `sync.Mutex`. Gateway mutex protects all slot state. Unlock before calling `notify()` to avoid deadlock.
- **Message types**: Bubble Tea messages suffixed with `Msg` (e.g., `StreamChunkMsg`, `ToolCallMsg`, `RegistryReloadedMsg`).
- **Naming**: Tool names in `snake_case`. Go types/functions in standard Go convention.
- **Commit messages**: Prefixed with `fix:`, `feat:`, `proto:`.
