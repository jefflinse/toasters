# Toasters — Ambitions

This document captures the evolving ambitions for Toasters, from its origins as a TUI project to its trajectory as an agentic operations platform.

---

## The Realization

Toasters started as a fun TUI project to play with a local LLM. But the core loop — system prompt + tools + execute-tool-calls-until-done — is exactly what Claude Code, OpenCode, and every other agentic coding tool does. The difference is that Toasters orchestrates *multiple* of those loops concurrently with an operator dispatching to workers.

The "magic" is just: system prompt + tool permissions + a loop that executes tool calls and feeds results back to the LLM until it says it's done. Everything else is scaffolding around that core loop.

---

## Foundation (Realized)

The ambitions that defined the early phases are now built and live in the codebase:

- **Multi-provider, multi-model** — direct provider auth across Anthropic, OpenAI, and OpenAI-compatible local endpoints; per-worker model selection; token and cost tracking per job.
- **In-process worker runtime** — each worker session is a goroutine with its own context, message history, and tool loop (file I/O, shell, web fetch, worker spawning, MCP routing).
- **MCP client and server** — Toasters consumes external MCP tools and runs its own MCP server that exposes progress-reporting tools back to workers; progress flows straight into SQLite.
- **SQLite persistence** — jobs, tasks, sessions, transcripts, and progress all persist; markdown remains only for human-readable artifacts.
- **Server/client architecture** — a long-running server owns all state (runtime, operator, graphs, DB); the TUI is a thin remote client over REST + SSE, so work survives client restarts.
- **Declarative graphs (rhizome)** — workflows are first-class: graph nodes are roles with typed inputs/outputs, edges are static or conditional, and decomposition is itself a graph dispatched through the executor.

See `CLAUDE.md` for how these fit together today.

---

## Forward Ambitions

### Ecosystems
**Ephemeral**: A job clones multiple repos into a workspace, sets up cross-repo context, and workers operate across all of them. Torn down or archived when done.

**Long-lived**: Persistent definitions of service ecosystems — "the backend ecosystem is these 12 services, here's how they connect, here are the MCP servers for their APIs." The staff engineer's dream: ask "which services would be affected if I change the user ID format?" and the system actually knows.

### Operator Memory
The operator accumulates knowledge across jobs: which roles succeed at which tasks, which repos have quirks, which patterns need security review. Stored in SQLite, augmented into the operator's system prompt for future dispatching decisions.

### Job Personas
Each active job gets a dedicated LLM session that accumulates context as the job progresses. The operator can query these: "hey job-47, what's your current blocker?" When a job completes, the persona's knowledge gets distilled into operator memory.

---

## The Multiplier Effects

The forward ambitions aren't independent — they multiply each other and the existing foundation:

**Ecosystems + MCP** = workers query your actual backend services as part of their work, scoped to the ecosystem they're operating in.

**Ecosystems + Operator memory** = the operator learns which roles and graphs work best for which kinds of changes in which parts of your ecosystem.

**Job personas + Operator memory** = finished jobs distill into durable institutional knowledge instead of evaporating when their sessions end.

---

## What This Becomes

The fun TUI project is still in there. It's just the face of something much bigger.

The foundation — a provider-agnostic, in-process orchestration server with persistent state and declarative graphs — is built. What's ahead is the part that doesn't exist yet: an agentic orchestration platform with persistent knowledge of your engineering ecosystem that gets better at its job over time.
