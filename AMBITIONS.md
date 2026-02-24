## User

Have I basically just wandered my way into the meat & potatoes of how things like Claude Code and OpenCode work with LLMs?

---

## Assistant (Prototyper · claude-opus-4-6 · 9.7s)

Pretty much, yeah. The whole thing is:

1. Spawn a process that talks to an LLM
2. Give it a system prompt (the agent instructions)
3. Tell the runtime which tools it's allowed to call
4. Let it run, stream the output back

That's it. That's what Claude Code is. That's what OpenCode is. That's what Toasters is becoming.

The "magic" is just: system prompt + tool permissions + a loop that executes tool calls and feeds results back to the LLM until it says it's done. Everything else — the TUI, the session management, the config files, the frontmatter formats — is scaffolding around that core loop.

And you've already built the interesting version of it, because yours orchestrates *multiple* of those loops concurrently with a coordinator dispatching to workers. Claude Code and OpenCode are single-agent loops (with Claude Code's `Task` tool being their way of nesting one loop inside another). You're doing the same thing but managing it from the outside with your operator + gateway + slot system.

---

## User

I really appreciate this conversation. Let's keep it rolling -- I keep developing this tool... what interesting workflows could I unlock? I'm already pretty convinced I should just incorporate MCP capabilities, and I really want to build out features like
- default teams of agents that can be configured
- auth with paid providers to avoid needing to shell out to claude
- really finish fleshing out my higher-level orchestration layer
- refine the operator agent into a polished thing
- incorporate data sources? (might just fall under using MCPs)
- incorporate the idea of "ecosystems" (collections of projects/services that it maintains knowledge about)
- MD files are cumbersome... maybe a database for operational things?
- shit, I should just make this whole thing a server so that it's resiliant to crashes/restarts, and make the TUI connect to it

---

## Assistant (Prototyper · claude-opus-4-6 · 43.0s)



You're basically designing an agentic platform. Let me react to each of these and then connect the dots on what gets really interesting when they combine.

---

### Default teams of agents that can be configured

This is table stakes and you're almost there. The interesting unlock is **team templates** — "spin up a coding team" vs "spin up an infrastructure team" vs "spin up a research team" with one command. Users customize from a base, not from scratch. Think of it like Docker Compose for agent teams.

### Auth with paid providers directly

This is a big one. Shelling out to `claude` means you're bound to Claude Code's process model, its permission system, its agent format, its streaming format. If you hit the Anthropic API directly (or OpenAI, or any provider), you:
- Control the full request/response lifecycle
- Can mix models per agent (Haiku for the planner's quick passes, Opus for the builder's heavy lifting)
- Can do proper token budgeting and cost tracking per job
- Eliminate the subprocess overhead and fragility
- Aren't dependent on Claude Code being installed

You already have an LM Studio client in `internal/llm/`. You're halfway there. The Claude subprocess becomes just one *backend* option, not the only one.

### Finishing the orchestration layer

The gap right now is that your operator is an LLM making dispatch decisions, but the *workflow structure* (what happens after dispatch) is baked into `BuildTeamCoordinatorPrompt`. The next level is making workflows first-class — a job has a defined lifecycle with states, transitions, and hooks. The operator picks the workflow, not just the team. "This is a bug fix" triggers a different workflow than "this is a greenfield feature" — different phases, different review gates, different completion criteria.

### Refining the operator

The operator gets really interesting when it has **memory across jobs**. Right now each job is independent. But if the operator remembers "last time we touched the auth module, the coding team missed the rate limiter and the security team caught it" — it can proactively assign the security team or add review steps. That's where the ecosystem idea connects.

### Data sources / MCPs

MCP is the right abstraction here. Rather than building bespoke integrations, you become an MCP host. Teams get access to MCP servers as part of their configuration — "this team can query the production database (read-only), access the GitHub API, and search Confluence." The data sources become part of the team definition, not hardcoded into the platform.

### Ecosystems

This is the most ambitious and potentially the most valuable idea. Right now, every agentic coding tool operates on a single repo at a time. But real engineering work spans multiple services. "Add a new field to the user profile" touches the API service, the frontend, the mobile app, the database migration, and the documentation site.

An ecosystem is a persistent knowledge graph: these repos exist, they relate to each other this way, this one is the API gateway, this one is the auth service, here are the shared contracts between them. When a job comes in, the operator doesn't just pick a team — it understands which repos are affected and can dispatch work across them.

### Database for operational data

Yes. The MD files work for job artifacts (OVERVIEW.md, REPORT.md — things humans read), but operational state (job status, task assignments, slot allocation, team configs, ecosystem metadata) belongs in a database. SQLite is the obvious choice — single file, no server, embeds trivially in Go, and you can query it properly instead of parsing frontmatter.

The split would be:
- **SQLite**: jobs, tasks, teams, agents, ecosystems, slot history, cost tracking, operator memory
- **Markdown files on disk**: job artifacts that agents read/write (overviews, reports, blockers, code)

### Making it a server

This is the architectural unlock that makes everything else work properly. Right now if the TUI crashes, your running jobs are orphaned Claude subprocesses. A server gives you:

- **Resilience**: jobs survive TUI crashes, reconnects, even machine restarts (if you're hitting APIs directly instead of subprocesses)
- **Multiple clients**: TUI is one frontend. A web UI is another. A CLI that just submits jobs is another. A Slack bot is another.
- **Proper state management**: the server owns the database, the job lifecycle, the slot allocation
- **Remote operation**: run the server on a beefy machine, connect from your laptop
- **API surface**: other tools can submit jobs, query status, integrate with your ecosystem

The TUI becomes a thin client that connects over a socket (Unix domain socket for local, WebSocket or gRPC for remote). It renders state and sends commands. All the brains live in the server.

---

### Where it gets really interesting

These features aren't independent — they multiply each other:

**Server + Direct API auth + Ecosystems** = a persistent orchestration platform that maintains awareness of your entire engineering surface and can dispatch multi-repo work autonomously, surviving crashes and restarts.

**Ecosystems + Operator memory + Database** = the operator learns which teams and workflows work best for which kinds of changes in which parts of your ecosystem. It gets better at dispatching over time.

**MCP + Teams + Ecosystems** = teams get context-appropriate data access. The infrastructure team gets access to the cloud provider MCP. The coding team gets the repo's test database. The research team gets web search and documentation sources. All configured per-ecosystem.

**Server + Multiple clients** = you submit a job from Slack, it runs overnight, you check the results from the TUI in the morning, the web dashboard shows cost and progress across all active jobs.

You're not building a coding assistant. You're building an **agentic operations platform**. The coding part is just the first workload type.

---

## User

Welp. This was supposed to be a fun TUI project to play with a local LLM.

Now I'm ready to go DEEP with it.

What do we prioritize?
- Direct provider auth (OAuth/API key auth for Anthropic, OpenAI, Google, etc)
- MCP integrations (start basic, just basic endpoint+auth config)
- Resilience: move most logic into a backend service, keep TUI as client
- Persistence: move away from .md files for everything, add a database layer
- "Ecosystems" concept/feature
- Team templates and workflows for creating teams and orchestrating them
- Operator memory
- Idea: spawning one LLM session per job in goroutines; these are fed with information as jobs progress and become queryable personas that have deep knowledge of a job's progress and state and details; the operator can ask these agents about jobs
- My thoughts around ecosystems:
  - They are ephemeral: you need to do some work (i.e. fix a bug that spans multiple services), so the job clones multiple repos into a new "ecosystem" directory (maybe as submodules, with the ecosystem directory as a coordination/harness repo). Then all the work is done in the ecosystem, and has access to--and the ability to make changes in--multiple repos, etc.
  - But we could also have "long-lived" ecosystems that represent larger, "source of truth" kind of situations. For example, as a staff engineer, I'd love for this tool to be able to maintain (or load on demand from persisted state) my entire company's "backend ecosystem": all of our services and their details, MCP capabilities that can interface with them, knowledge graphs, etc. All sorts of useful stuff to feed to agents and LLMs.

Might as well prioritize and create a master plan now; there's no going back.

---

## Assistant (Prototyper · claude-opus-4-6 · 46.8s)



This is a real platform now. Let me think through the dependency graph and give you a sequenced plan.

---

## The Dependency Graph

Some of these features are foundations that others build on. Let me map it:

```
Direct API Auth ──────────────────────┐
                                      ▼
Persistence (SQLite) ───────► Server Architecture ───► Multiple Clients
                                      │
                                      ▼
MCP Integration ──────────► Team Templates/Workflows
                                      │
                                      ▼
                              Ecosystems (ephemeral)
                                      │
                                      ▼
                           Ecosystems (long-lived)
                                      │
                                      ▼
                    Operator Memory + Job Personas
```

The logic:

- **Direct API auth** has to come first because the server architecture needs to own the LLM connections, not shell out to subprocesses. You can't build a resilient server around `exec.Command("claude", ...)`.
- **Persistence** has to come before or alongside the server, because the server needs durable state to be resilient.
- **Server architecture** is the backbone everything else plugs into.
- **MCP integration** can happen in parallel with the server work — it's an outbound capability layer.
- **Team templates** need the database (to store templates) and benefit from MCP (teams reference MCP servers).
- **Ecosystems** need all of the above: persistent state, server-managed jobs, MCP for data access, teams for dispatch.
- **Operator memory** and **job personas** are the capstone — they need ecosystems and persistence to be meaningful.

---

## The Plan

### Phase 1: Foundation — "Cut the cord"
**Goal**: Toasters talks to LLMs directly and owns its own state.

1. **Direct provider auth** — Build a multi-provider LLM client. Anthropic first (you're already closest to it), then OpenAI-compatible (covers LM Studio, Ollama, OpenAI, and many others). OAuth where needed, API key for the rest. Config in `~/.config/toasters/providers.yaml` or similar. Each agent definition can specify a model/provider.

2. **Persistence layer** — SQLite via `modernc.org/sqlite` (pure Go, no CGO). Migrate jobs, tasks, teams, agents, slot history, and config out of markdown frontmatter. Keep markdown files as *artifacts* (reports, overviews — things agents and humans read), but operational state lives in the database.

3. **Goroutine-based agent execution** — Replace `exec.Command("claude", ...)` with goroutines that run the LLM client directly. Each agent session is a goroutine with a context, a message history, and tool execution loop. This is the core runtime change — you go from "spawn a subprocess and parse its stdout" to "run a conversation loop in-process." Way more control, way less fragility.

### Phase 2: Architecture — "Become a server"
**Goal**: TUI is a client, all logic lives in a persistent backend.

4. **Server/client split** — Extract the orchestration engine into a long-running server process. gRPC or Unix domain socket for the local case. The server owns: job lifecycle, agent sessions, slot management, database, provider connections. The TUI connects, subscribes to state updates, sends commands.

5. **MCP host integration** — The server becomes an MCP host. Team configs reference MCP servers. When a team spins up, its agents get access to the configured MCP tools. Start simple: endpoint + auth config, stdio and SSE transports. This gives agents access to databases, APIs, documentation, cloud providers — whatever has an MCP server.

6. **Team templates and workflows** — Predefined team compositions stored in the database. "Coding team" = coordinator + builder + reviewer + test-writer. "Infrastructure team" = coordinator + ci-ops + security-auditor. Workflows become first-class: a workflow defines phases, agent assignments, gates, and completion criteria. The operator selects a workflow, not just a team.

### Phase 3: Intelligence — "Know things"
**Goal**: The system has persistent knowledge and gets smarter over time.

7. **Ephemeral ecosystems** — A job can declare "I need repos X, Y, Z." The system clones them into a workspace directory, sets up the ecosystem context (which repo is what, how they relate), and gives the team access. When the job completes, the ecosystem can be torn down or archived. This is your multi-repo coordination layer.

8. **Long-lived ecosystems** — Persistent ecosystem definitions: "the backend ecosystem is these 12 services, here's how they connect, here are the MCP servers for their databases and APIs, here's the knowledge graph." Loaded on demand, updated incrementally. This is the staff engineer's dream — ask the operator "which services would be affected if I change the user ID format?" and it actually knows.

9. **Operator memory** — The operator accumulates knowledge across jobs. Stored in the database: which teams succeeded at which kinds of tasks, which repos have which quirks, which patterns of work tend to need security review. The operator's system prompt gets augmented with relevant memories when dispatching new work. Start simple (structured logs of job outcomes), get sophisticated later (embeddings, retrieval).

10. **Job personas** — Each active job gets a dedicated LLM session (goroutine with message history) that accumulates context as the job progresses. The operator can query these: "hey job-47, what's your current blocker?" and get an informed answer without re-reading all the artifacts. These are cheap once you're running LLM sessions in-process — it's just another goroutine with a conversation history. When a job completes, the persona's knowledge gets distilled into the operator's memory.

---

## What You Keep

The TUI stays. It's your primary interface and it's good. It just becomes a client that connects to the server instead of owning the runtime directly.

The markdown agent definition files stay as one *input format*. Teams can also be defined in the database, via API, via templates. The `.md` files become one way to define agents, not the only way.

The operator concept stays and gets stronger. It goes from "an LLM that picks teams" to "a persistent intelligence that knows your engineering organization."

---

## What This Becomes

Phase 1 gives you a standalone agentic coding tool that doesn't depend on Claude Code or OpenCode.

Phase 2 gives you a resilient platform that survives crashes, supports multiple interfaces, and integrates with external tools via MCP.

Phase 3 gives you something that doesn't exist yet — an agentic orchestration platform with persistent knowledge of your engineering ecosystem that gets better at its job over time.

The fun TUI project is still in there. It's just the face of something much bigger now.

---

