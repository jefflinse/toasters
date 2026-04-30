# Toasters — Ambitions

This document captures the evolving ambitions for Toasters, from its origins as a TUI project to its trajectory as an agentic operations platform.

---

## The Realization

Toasters started as a fun TUI project to play with a local LLM. But the core loop — system prompt + tools + execute-tool-calls-until-done — is exactly what Claude Code, OpenCode, and every other agentic coding tool does. The difference is that Toasters orchestrates *multiple* of those loops concurrently with an operator dispatching to workers.

The "magic" is just: system prompt + tool permissions + a loop that executes tool calls and feeds results back to the LLM until it says it's done. Everything else is scaffolding around that core loop.

---

## Feature Ambitions

### Direct Provider Auth
Shelling out to `claude` means being bound to Claude Code's process model, permission system, and streaming format. Hitting LLM APIs directly enables:
- Full control over the request/response lifecycle
- Mixing models per agent (cheap models for coordination, powerful models for heavy lifting)
- Proper token budgeting and cost tracking per job
- Elimination of subprocess overhead and fragility
- Independence from any specific CLI tool being installed

### In-Process Agent Runtime
Replace `exec.Command("claude", ...)` with goroutines that run LLM conversation loops directly. Each agent session is a goroutine with a context, message history, and tool execution loop. Core tools needed:
- File I/O: read, write, edit, glob, grep
- Shell execution: run commands, capture output
- Web fetch: HTTP GET
- Subagent spawning: create child agent sessions
- MCP tools: from configured external servers
- Toasters MCP tools: progress reporting back to orchestrator

### MCP Client Integration
Toasters becomes an MCP client, connecting to external MCP servers (GitHub, Jira, Linear, etc.) and making their tools available to both the operator and agents. See `docs/mcp-integration-plan.md` for the detailed implementation plan.

### MCP Server — Agent Progress Reporting
**New idea (2026-02-24).** Toasters runs its own MCP server that agents connect to. This creates a structured bidirectional communication channel:
- Agents report progress, flag blockers, update task status
- Agents query job context and ecosystem information
- All progress data flows directly into SQLite
- The TUI gets real-time updates without file watching or output parsing
- Works for both in-process agents and external Claude CLI subprocesses (via `--mcp-config`)

This flips the earlier "we are NOT building an MCP server" decision. The progress-reporting use case is compelling enough to warrant it.

### Ephemeral OpenAPI-to-MCP Bridges
**New idea (2026-02-24).** Auto-generate MCP servers from OpenAPI specs. Configure a URL, credentials, and point at a spec — Toasters spins up a lightweight MCP server that translates tool calls into HTTP requests against the backend service. Scoped to a job or ecosystem, cleaned up automatically.

This is the bridge between "agents can use MCP tools" and "agents can query your actual backend services." Especially powerful for the ecosystems concept.

### SQLite Persistence
Operational state moves from markdown files to SQLite:
- Jobs, tasks, status, assignments
- Team and agent configurations
- Slot history, cost tracking
- Agent progress reports
- Ecosystem metadata
- Operator memory

Markdown files remain for human-readable artifacts (reports, overviews, findings).

### Server/Client Architecture
Extract the orchestration engine into a long-running server process. The TUI becomes a thin client. Benefits:
- Jobs survive TUI crashes and reconnects
- Multiple clients: TUI, web UI, CLI, Slack bot
- Server owns database, job lifecycle, agent sessions
- Remote operation: run server on a beefy machine, connect from laptop

### Team Templates and Workflows
Predefined team compositions: "coding team" = team-lead + builder + reviewer. Workflows become first-class: a workflow defines phases, worker assignments, gates, and completion criteria. The operator selects a workflow, not just a team.

### Ecosystems
**Ephemeral**: A job clones multiple repos into a workspace, sets up cross-repo context, and agents work across all of them. Torn down or archived when done.

**Long-lived**: Persistent definitions of service ecosystems — "the backend ecosystem is these 12 services, here's how they connect, here are the MCP servers for their APIs." The staff engineer's dream: ask "which services would be affected if I change the user ID format?" and the system actually knows.

### Operator Memory
The operator accumulates knowledge across jobs: which teams succeed at which tasks, which repos have quirks, which patterns need security review. Stored in SQLite, augmented into the operator's system prompt for future dispatching decisions.

### Job Personas
Each active job gets a dedicated LLM session that accumulates context as the job progresses. The operator can query these: "hey job-47, what's your current blocker?" When a job completes, the persona's knowledge gets distilled into operator memory.

---

## The Multiplier Effects

These features aren't independent — they multiply each other:

**In-process agents + MCP server** = agents report progress in real-time through a structured protocol, no file parsing needed.

**MCP client + OpenAPI bridges + Ecosystems** = agents can query your actual backend services as part of their work, scoped to the ecosystem they're operating in.

**SQLite + MCP server + TUI** = real-time progress display driven by structured database writes from agents, not file watching.

**Server architecture + Direct API auth + Ecosystems** = a persistent orchestration platform that maintains awareness of your entire engineering surface and dispatches multi-repo work autonomously.

**Ecosystems + Operator memory + Database** = the operator learns which teams and workflows work best for which kinds of changes in which parts of your ecosystem.

---

## What This Becomes

The fun TUI project is still in there. It's just the face of something much bigger:

- **Phase 1** gives you a standalone agentic coding tool that doesn't depend on Claude Code or OpenCode.
- **Phase 2** gives you a resilient platform that integrates with external tools via MCP and reports progress in real-time.
- **Phase 3** gives you something that doesn't exist yet — an agentic orchestration platform with persistent knowledge of your engineering ecosystem that gets better at its job over time.
