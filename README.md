# Toasters

An agentic work orchestration platform. Toasters coordinates multiple concurrent LLM-powered workers, dispatching work via an operator LLM and managing the full lifecycle of jobs, tasks, and worker sessions.

Work is assigned to teams, not individual agents. Each team has a pool of workers with specialized roles. The operator LLM decomposes high-level jobs into tasks, selects which teams to involve, and dispatches tasks to teams based on their roles and expertise.

## Features

- **Multi-agent orchestration** — An operator LLM dispatches work to specialized teams of workers running as in-process goroutines
- **Multi-provider** — Supports local models (llama.cpp, Ollama, LM Studio) and cloud providers (Anthropic, OpenAI, Google)
- **MCP integration** — Consumes external tools via MCP (GitHub, Jira, Linear, etc.) and exposes a progress-reporting MCP server back to workers
- **Default teams** — Ships with Go, TypeScript, Python, and QA teams out of the box
- **Composable roles** — Workers are defined by reusable roles with template-based prompt composition
- **SQLite persistence** — Jobs, tasks, sessions, and progress tracked in a local database
- **TUI-first** — Real-time progress display, team/worker management, and operator chat

## Requirements

- Go 1.25+
- An LLM provider (local or cloud)

## Install

```bash
go install github.com/jefflinse/toasters@latest
```

## Quick Start

**1. Start the server**

```bash
toasters serve
```

The server starts on `:8421` by default. Use `--addr` to change the listen address.

**2. Connect the TUI**

In a new terminal:

```bash
toasters
```

**3. Configure a provider**

Type `/providers` in the TUI to open the provider catalog and configure an LLM provider. Local inference is recommended:

- [LM Studio](https://lmstudio.ai/)
- [Ollama](https://ollama.com/)
- llama.cpp (OpenAI-compatible endpoint)

Cloud providers (Anthropic, OpenAI) work too — set the API key in your provider config.

**4. Set the operator model**

Type `/operator` to select which provider and model the operator uses.

**5. Give it a job**

Type a task description and the operator takes it from there — decomposing the work, selecting teams, and dispatching workers.

## Configuration

Config lives in `~/.config/toasters/`. Key files:

| Path | Purpose |
|------|---------|
| `config.yaml` | Global settings (operator, task granularity, MCP servers) |
| `providers/*.yaml` | LLM provider definitions |
| `user/roles/*.md` | Worker role definitions |
| `user/teams/*/team.md` | Team definitions |
| `user/toolchains/*.md` | Language/framework knowledge |
| `user/instructions/*.md` | Reusable behavioral directives |
