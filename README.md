# Toasters

A TUI-first agentic orchestration platform. Toasters coordinates concurrent LLM-powered worker sessions, dispatching them through declarative graphs and managing the full lifecycle of jobs, tasks, and worker state from a local SQLite database.

The operator LLM decomposes high-level jobs into tasks. Each task is bound to a graph; graph nodes are roles with typed inputs/outputs; the executor runs nodes as in-process worker goroutines and routes outputs along static or conditional edges. Workers are stateless — Go owns the state and feeds workers accumulated context.

## Highlights

- **Operator-driven orchestration** — A user-facing operator LLM creates jobs, decomposes work, and dispatches tasks to graphs
- **Graph-based execution** — Tasks run through declarative graphs (rhizome) with typed role inputs/outputs and conditional routing
- **Multi-provider** — Anthropic and OpenAI directly; LM Studio, llama.cpp, and Ollama via OpenAI-compatible endpoints
- **MCP integration** — Consumes external tools via MCP (GitHub, Jira, Linear, etc.) and exposes a progress-reporting MCP server back to workers
- **Composable roles** — Workers are defined by reusable roles with template-based prompt composition
- **SQLite persistence** — Jobs, tasks, sessions, transcripts, and progress tracked in a local database
- **Server + TUI** — Long-lived server owns all state; the TUI is a thin client over a unified SSE event stream

## Requirements

- Go 1.26+
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

Type `/providers` in the TUI to open the provider catalog. Local inference is the recommended default:

- [LM Studio](https://lmstudio.ai/)
- [Ollama](https://ollama.com/)
- llama.cpp (OpenAI-compatible endpoint)

Cloud providers (Anthropic, OpenAI) work too — set the API key in the provider config.

**4. Set the operator model**

Type `/operator` to select which provider and model the operator uses.

**5. Give it a job**

Type a task description and the operator takes it from there — decomposing the work, picking graphs, and dispatching workers.

## Configuration

Config lives in `~/.config/toasters/`. Key files:

| Path | Purpose |
|------|---------|
| `config.yaml` | Global settings (operator, task granularity, MCP servers) |
| `providers/*.yaml` | LLM provider definitions |
| `user/roles/*.md` | Worker role definitions (typed I/O, prompts) |
| `user/graphs/*.md` | Graph definitions (nodes, edges, schemas) |
| `user/skills/*.md` | Reusable capabilities composed into roles |
| `user/toolchains/*.md` | Language/framework knowledge |
| `user/instructions/*.md` | Reusable behavioral directives |
