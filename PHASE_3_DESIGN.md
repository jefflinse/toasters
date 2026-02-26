# Phase 3.2 Design: Teams & Agents Management System

**Created:** 2026-02-25
**Status:** Draft — all 8 open items resolved, ready for implementation planning

---

## Table of Contents

- [Vision](#vision)
- [Architecture Overview](#architecture-overview)
- [Composition Model](#composition-model)
  - [Skills](#skills)
  - [Agents](#agents)
  - [Teams](#teams)
- [System Team](#system-team)
  - [The Operator](#the-operator)
  - [System Agents](#system-agents)
  - [System vs. User Tool Boundaries](#system-vs-user-tool-boundaries)
- [Job Execution Model](#job-execution-model)
  - [Task Lifecycle](#task-lifecycle)
  - [Blocker Flow](#blocker-flow)
  - [Error Recovery](#error-recovery)
- [File Format](#file-format)
  - [Skill Definition](#skill-definition)
  - [Agent Definition](#agent-definition)
  - [Team Definition](#team-definition)
- [Directory Layout](#directory-layout)
- [Source of Truth: Files vs. Database](#source-of-truth-files-vs-database)
- [Composition at Runtime](#composition-at-runtime)
- [Auto-Team Detection](#auto-team-detection)
- [First-Run Bootstrap](#first-run-bootstrap)
- [TUI Integration](#tui-integration)
- [Decisions Log](#decisions-log)
- [Operator Event Loop Design](#operator-event-loop-design)
- [Agent Definition Format (Superset)](#agent-definition-format-superset)
- [Open Items](#open-items)
- [Future Ideas (Out of Scope)](#future-ideas-out-of-scope)

---

## Vision

Toasters orchestrates work through a three-layer composition model:

```
Skills (reusable capabilities)
  ↓ composed into
Agents (personas with skills, tools, provider/model config)
  ↓ assembled into
Teams (agents + culture + lead + orchestration rules)
  ↓ assigned work by
Operator (user interface + thin router, delegates to system agents)
```

The **operator** is the user's interface. It understands what the user wants, maintains conversation context, and delegates decision-making to specialized **system agents** (planning, scheduling, blocker handling). The system team uses the same team infrastructure as user-defined teams — same file format, same composition rules, same runtime. The only difference is the tool boundary: system agents have orchestration tools (create jobs, assign tasks), user agents have work tools (read/write files, run commands).

**User-defined teams** do the actual work. Each team has a **lead agent** who receives tasks from the operator, delegates to **worker agents**, and reports results back. Teams are composable — agents can be shared across teams, skills can be shared across agents, and team culture documents define coordination norms.

The user has **full hackability** over everything, including the system team. They can modify the operator's personality, swap out the scheduler for a custom one, or add new system agents. Toasters ships sensible defaults that get copied to the user's config directory on first run and are never overwritten.

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────┐
│                       User                           │
└───────────────────────┬─────────────────────────────┘
                        │
┌───────────────────────▼─────────────────────────────┐
│           Operator (User Interface Agent)             │
│                                                      │
│  Maintains conversation with user. Understands        │
│  intent. Delegates to system agents for decisions     │
│  and actions. Can run on a local model.               │
│                                                      │
│  System Team (~/.config/toasters/system/):            │
│  ├── Operator (lead): user interface, thin router     │
│  ├── Planner: assesses requests, picks teams,         │
│  │           creates jobs and initial tasks            │
│  ├── Scheduler: turns completed plans into tasks      │
│  │             with team assignments and dependencies  │
│  └── Blocker Handler: triages blockers, decides       │
│                       whether to involve the user      │
│                                                      │
│  System agents have orchestration tools               │
│  (create_job, create_task, assign_task, etc.)         │
│  but NO filesystem tools.                             │
└──────┬──────────┬──────────┬────────────────────────┘
       │          │          │
┌──────▼───┐ ┌───▼────┐ ┌───▼────┐
│ Research  │ │  Dev   │ │   QA   │  ... user-defined teams
│  Team     │ │  Team  │ │  Team  │
│           │ │        │ │        │
│ Lead ─────│─│─Lead───│─│─Lead   │  Leads receive tasks, delegate,
│ ├─Agent A │ │ ├─Ag D │ │ ├─Ag G │  report progress/blockers,
│ └─Agent B │ │ ├─Ag E │ │ └─Ag H │  call complete_task when done
│           │ │ └─Ag F │ │        │
└───────────┘ └────────┘ └────────┘
```

---

## Composition Model

### Skills

A skill is a reusable building block that gives an agent a specific capability. Skills are coarse-grained — each one represents a meaningful, self-contained area of expertise.

**What a skill contains:**
- Name and description (YAML frontmatter)
- Tool declarations (tools this skill needs)
- Prompt content (the markdown body — instructions, domain knowledge, constraints)

**Examples:**
- `go-development` — Go idioms, testing patterns, module structure
- `code-review` — How to review code, what to look for, feedback style
- `security-audit` — OWASP, vulnerability patterns, threat modeling
- `technical-writing` — Documentation style, README structure, API references
- `git-workflow` — Branching, commit conventions, PR creation

**Granularity:** Start coarse, split later. A skill should be meaningful on its own.

**Composition:** Skills are additive. When an agent has multiple skills, their prompt content is concatenated in the order listed in the agent's frontmatter. Tool declarations are unioned across all skills.

### Agents

An agent is a persona — a combination of skills, personality, tools, and provider/model config.

**What an agent contains:**
- Name, description (YAML frontmatter)
- Skills (list of skill references, resolved by name)
- Tool allowlist/denylist (beyond what skills bring in)
- Provider + model (or inherit from team/global default)
- Temperature, max tokens, etc.
- Prompt content (the markdown body — personality, role framing, agent-specific instructions)

**Shared agents** live in `~/.config/toasters/user/agents/` and can be referenced by multiple teams.

**Team-specific agents** live in `~/.config/toasters/user/teams/<team>/agents/` and belong to one team.

**Agent generation via AI:** The user describes what they want ("I need an agent that's an expert in React and accessibility") and a system agent (or TUI flow) generates the agent definition file.

### Teams

A team is a collection of agents with a designated lead, a culture document, and coordination rules.

**What a team contains:**
- Name, description (YAML frontmatter)
- Lead agent (required — the orchestrator interface between the operator and the team)
- Member agents (list of agent references, resolved by name)
- Team-wide skills (applied to all members, additive with agent-level skills)
- Default provider/model (team-level, overridable per-agent)
- Culture document (the markdown body — team norms, handoff protocols, member descriptions)

**The lead agent** is the team's interface to the operator:
- Receives task assignments from the operator
- Delegates subtasks to worker agents via `spawn_agent`
- Reports progress and blockers upward
- Calls `complete_task` when the team's work is done

**Culture document** handling:
- **Injected into the lead's system prompt** — the lead needs full context about the team
- **Available to workers via `query_team_context` tool** — workers can look up team norms on demand, saving context window tokens

---

## System Team

### The Operator

The operator is the user's interface to toasters. It is the **lead agent of the system team**.

**Responsibilities:**
- Maintain conversation with the user
- Understand user intent
- Delegate to system agents via `consult_agent` for decisions and actions
- Relay results back to the user
- React to events (task completions, blockers)

**The operator is an event loop:**
```
while true:
  event = wait_for_event()
  if event is user_message:
    // Operator LLM processes, decides which system agent to consult
    // May call consult_agent("planner", ...) for new requests
  if event is task_completed:
    // Operator calls consult_agent("scheduler", ...) to determine next steps
  if event is blocker:
    // Operator calls consult_agent("blocker-handler", ...) to triage
  // Act on the result
```

**The operator can run on a local model** to save on paid API tokens. It's a thin router — it doesn't need to be the smartest model, just good enough to understand intent and delegate.

**The operator is fully hackable.** Its definition lives at `~/.config/toasters/system/agents/operator.md`. Users can modify its personality, routing behavior, or system prompt.

### System Agents

System agents are the operator's workers. They handle specific aspects of orchestration.

**Default system agents (shipped with toasters):**

| Agent | Role | Key Tools |
|-------|------|-----------|
| **Planner** | Assesses user requests, picks the right team for initial planning, creates jobs and initial tasks | `create_job`, `create_task`, `assign_task`, `query_teams` |
| **Scheduler** | Takes completed plans from teams and breaks them into tasks with team assignments and dependencies | `create_task`, `assign_task`, `query_teams`, `query_job` |
| **Blocker Handler** | Triages blocker reports, decides whether the user needs to be involved, formulates questions | `query_job`, `surface_to_user` |

**System agents are NOT purely advisory.** They have orchestration tools and can take real actions (creating jobs, tasks, assignments in SQLite). However, they have **no filesystem tools** — they don't read files, write code, or run commands. They operate at the job/task/team level, not the work level.

**System agents use the full team composition model** — they can have skills, provider/model config, temperature settings, etc. The system team has a culture document describing how the operator delegates to its agents.

**Users can add, remove, or modify system agents.** Want a more aggressive scheduler? Edit `scheduler.md`. Want a new "cost estimator" system agent? Add one. The system team is fully hackable.

### System vs. User Tool Boundaries

The tool boundary between system and user levels is enforced in code:

**System-level tools** (available to system team agents only):

| Tool | Description |
|------|-------------|
| `consult_agent` | Operator invokes a system agent. Distinct from `spawn_agent`. |
| `create_job` | Create a new job in SQLite |
| `create_task` | Create a new task on a job |
| `assign_task` | Assign a task to a user team |
| `query_teams` | Get list of available user teams + capabilities |
| `query_job` | Get current job state, tasks, progress |
| `surface_to_user` | Send a message/question to the user via the operator |
| `relay_to_team` | Send the user's response back to a team |

**Team lead tools** (available to user team leads):

| Tool | Description |
|------|-------------|
| `spawn_agent` | Spawn a worker agent on the team |
| `complete_task` | Mark the team's current task as done, return results + follow-up recommendations |
| `request_new_task` | Recommend that a new job task be created (operator decides) |
| `report_progress` | Report progress on current task |
| `report_blocker` | Report a blocker (flows up to operator → blocker handler) |
| `query_job_context` | Get context about the broader job |
| *(plus all worker tools)* | Leads can also do work directly |

**Worker tools** (available to user team workers):

| Tool | Description |
|------|-------------|
| `read_file` | Read a file |
| `write_file` | Write a file |
| `edit_file` | Edit a file |
| `glob` | Find files by pattern |
| `grep` | Search file contents |
| `shell` | Execute shell commands |
| `web_fetch` | Fetch a URL |
| `report_progress` | Report progress |
| `query_team_context` | Get team culture/context on demand |
| *(plus MCP tools)* | From configured MCP servers |

**Key enforcement:** `consult_agent` is never available to user-defined teams or agents. `spawn_agent` is never available to system agents. This prevents user teams from controlling toasters' internal orchestration.

---

## Job Execution Model

### Task Lifecycle

```
User: "Build an app that does A, B, and C"
  │
  ▼
Operator receives message
  │ calls consult_agent("planner", ...)
  ▼
Planner:
  - Looks at available teams (via query_teams)
  - Creates a Job (via create_job)
  - Creates Task 1: "Plan the work" (via create_task)
  - Assigns Task 1 to Research Team (via assign_task)
  │
  ▼
Research Team lead receives Task 1
  - Delegates research to workers
  - Produces a work plan
  - Calls complete_task with the plan + recommendations
  │
  ▼
Operator receives task completion event
  │ calls consult_agent("scheduler", ...) with the plan
  ▼
Scheduler:
  - Breaks the plan into tasks
  - Identifies team assignments
  - Notes: Tasks 2 and 3 are independent (parallelizable later)
  - Creates Task 2: "Build barebones app" → Dev Team
  - Creates Task 3: "Build feature A" → Dev Team (after Task 2 for now)
  - Creates Task 4: "Build feature B" → Dev Team (after Task 3)
  │
  ▼
Tasks execute serially (parallelism deferred)
  │
  ▼
Dev Team completes Task 4, recommends QA
  │ calls request_new_task("QA pass needed")
  ▼
Operator receives recommendation
  │ calls consult_agent("scheduler", ...) to create QA task
  ▼
Scheduler creates Task 5: "QA pass" → QA Team
  │
  ▼
...and so on until all tasks complete
  │
  ▼
Job marked complete. User notified.
```

**Key points:**
- The operator does NOT break down work — it delegates that to the planner/scheduler
- The planner creates the job and the initial planning task
- The scheduler creates subsequent tasks based on team outputs
- Tasks can spawn new tasks via `request_new_task` (team recommends, operator/scheduler decides)
- Task execution is **serial for now** — parallelism will be layered in later
- Each task assignment spins up a fresh team lead session

### Blocker Flow

```
Worker hits a problem
  │ calls report_blocker(...)
  ▼
Team lead receives blocker, may attempt resolution
  │ if unresolvable, calls report_blocker(...) to escalate
  ▼
Operator receives blocker event
  │ calls consult_agent("blocker-handler", ...)
  ▼
Blocker Handler triages:
  - Can the team resolve this with more context? → relay instructions
  - Does the user need to decide? → calls surface_to_user(...)
  │
  ▼
User sees the question in the TUI
  │ responds
  ▼
Operator relays answer back to team via relay_to_team(...)
  │
  ▼
Team continues work
```

### Error Recovery

**For now: fail the job and inform the user.** Keep it simple.

When a team fails a task:
1. Team lead calls `complete_task` with a failure status and explanation
2. Operator receives the failure event
3. Job is marked as failed
4. User is informed with the failure details

More sophisticated recovery (retry, reassign to different team, partial rollback) is deferred to a later iteration.

---

## File Format

All definitions use **`.md` files with YAML frontmatter**. This is consistent with the ecosystem (Claude Code, OpenCode) and makes definitions human-readable, git-friendly, and portable.

The markdown body is always the prompt content — for a skill it's capability instructions, for an agent it's personality/role framing, for a team it's the culture document.

### Skill Definition

```markdown
---
name: Go Development
description: Expert knowledge of Go idioms, patterns, testing, and module structure
tools:
  - shell       # needs go build, go test, etc.
---

## Go Development Expertise

You have deep expertise in Go development...

### Idioms
- Always handle errors explicitly with `if err != nil`
- Use `fmt.Errorf("context: %w", err)` for error wrapping
- Prefer returning errors over panicking
...

### Testing
- Write table-driven tests
- Use `t.Helper()` in test helpers
- Use `t.TempDir()` for file I/O tests
...
```

### Agent Definition

```markdown
---
name: Senior Go Developer
description: Expert Go developer with deep knowledge of idioms, testing, and architecture
skills:
  - go-development
  - code-review
  - git-workflow
tools:
  - read_file
  - write_file
  - edit_file
  - glob
  - grep
  - shell
  - spawn_agent
provider: anthropic
model: claude-sonnet-4-20250514
temperature: 0.3
---

You are a senior Go developer with 10+ years of experience. You write clean,
idiomatic Go code and have strong opinions about code organization, error
handling, and testing.

When reviewing code, you focus on correctness first, then clarity, then
performance. You give specific, actionable feedback with code examples.
```

### Team Definition

```markdown
---
name: Dev Team
description: General-purpose development team for building and maintaining Go applications
lead: senior-go-dev
agents:
  - senior-go-dev              # shared agent (from user/agents/)
  - technical-writer           # shared agent
  - frontend-specialist        # team-specific (from teams/dev-team/agents/)
skills:                         # team-wide skills, additive with agent-level
  - git-workflow
provider: anthropic             # team default, overridable per-agent
model: claude-sonnet-4-20250514
---

## Culture

You are part of the Dev Team. Your team lead is the Senior Go Developer.

### Team Members
- **Senior Go Developer** (lead): Architects solutions, reviews code, delegates implementation
- **Technical Writer**: Writes documentation, READMEs, API references
- **Frontend Specialist**: React/TypeScript expert, accessibility-aware

### Handoff Protocols
- Implementation tasks go to the appropriate specialist
- All code changes require review by the lead before completion
- Documentation updates accompany every feature
- Report blockers immediately via report_blocker

### Norms
- Write tests for all new code
- Follow existing code conventions
- Keep PRs focused and small
```

---

## Directory Layout

```
~/.config/toasters/
  config.yaml                              # global config (providers, defaults, etc.)

  system/                                  # toasters' own team — shipped defaults, fully hackable
    team.md                                # system team definition (operator as lead)
    agents/
      operator.md                          # user interface agent, thin router
      planner.md                           # assesses requests, creates jobs/tasks, picks teams
      scheduler.md                         # turns plans into tasks with assignments + deps
      blocker-handler.md                   # triages blockers, decides user involvement
    skills/                                # system-level skills (if any)
      orchestration.md                     # how to coordinate teams, manage task flow, etc.

  user/                                    # user-defined content
    skills/                                # shared skills
      go-development.md
      code-review.md
      security-audit.md
      technical-writing.md
    agents/                                # shared agents
      senior-go-dev.md
      technical-writer.md
      qa-engineer.md
    teams/
      dev-team/
        team.md                            # team definition
        agents/                            # team-specific agents
          frontend-specialist.md
      qa-team/
        team.md
        agents/
      research-team/
        team.md
      auto-claude/                         # auto-detected from ~/.claude/agents
        .auto-team                         # marker file
        agents/ → ~/.claude/agents         # symlink
      auto-opencode/                       # auto-detected from ~/.config/opencode/agents
        .auto-team                         # marker file
        agents/ → ~/.config/opencode/agents  # symlink
```

**Resolution order for agent names:**
1. `user/teams/<team>/agents/` — team-specific agents
2. `user/agents/` — shared agents
3. Built-in agents (bundled in the toasters binary, used as fallback)

**Resolution order for skill names:**
1. `user/skills/` — user-defined skills
2. `system/skills/` — system-level skills
3. Built-in skills (bundled in the toasters binary)

---

## Source of Truth: Files vs. Database

**Files are the source of truth.** The database is a runtime cache.

```
Files (.md)                        SQLite (runtime cache)
─────────────                      ──────────────────────
system/team.md          ──load──►  teams table
system/agents/*.md      ──load──►  agents table
user/skills/*.md        ──load──►  skills table
user/agents/*.md        ──load──►  agents table
user/teams/*/team.md    ──load──►  teams table
user/teams/*/agents/*.md ──load──► agents table + team_members table
```

- On startup, all files are parsed and loaded into SQLite.
- fsnotify watches all directories for changes. On file change, the affected definitions are reloaded (debounced at 200ms, extending existing infrastructure).
- The database is ephemeral — delete it and it rebuilds from files on next startup.
- TUI CRUD operations write `.md` files, which trigger fsnotify, which updates the DB. Same pipeline for human edits, TUI edits, and AI generation.
- Jobs, tasks, sessions, and progress reports are **database-only** (not files). These are operational state, not configuration.

---

## Composition at Runtime

When the runtime needs to build the full context for an agent session:

```
1. Load agent definition (name, metadata, base prompt from .md body)

2. Load each referenced skill (in frontmatter order)
   - Concatenate skill prompt content
   - Union skill tool declarations

3. Load team-wide skills (if agent is on a team, from team.md frontmatter)
   - Concatenate after agent-level skills
   - Union tool declarations

4. Compose the system prompt:
   ├── Agent's own prompt (from .md body)
   ├── Each skill's prompt (concatenated in frontmatter order)
   ├── Team-wide skill prompts (concatenated)
   └── Team culture:
       ├── For team lead: full culture document (from team.md body)
       └── For workers: NOT injected (available via query_team_context tool)

5. Merge tool sets:
   ├── Agent's own tools (from frontmatter)
   ├── Skill tools (union of all skills)
   ├── Team-wide skill tools
   ├── Role-based tools (lead gets spawn_agent, complete_task, etc.)
   └── MCP tools (from configured servers)
   Apply agent-level denylist if specified.

6. Resolve provider/model (cascade):
   ├── Agent-level override? → use it
   ├── Team-level default? → use it
   └── Global default (from config.yaml)? → use it

7. Cache the composed result.
   Invalidate on any file change (agent, skill, or team definition).
```

---

## Auto-Team Detection

On startup, toasters checks for user-level agent directories from other tools:
- `~/.claude/agents/` — Claude Code agents
- `~/.config/opencode/agents/` — OpenCode agents

**Not** CWD-scoped directories. Only user-level config.

For each detected directory:
1. Create `user/teams/auto-<tool>/` if not exists
2. Create `.auto-team` marker file
3. Create symlink `agents/` → source directory
4. Do NOT create a `team.md`

**Auto-team behavior:**
- No composition — agents are used as-is, no skill injection, no culture document
- No explicit lead — the operator talks to agents directly (or picks one heuristically)
- Show up in the TUI with an "auto" badge
- The system treats them as available teams but with limited capabilities

**Promotion to managed team:**
The user can "promote" an auto-team to a fully managed team. This invokes a specialized flow (potentially using a system agent) that:
1. Analyzes the auto-team's agent files
2. Translates them to toasters format (preserving behavior, permissions, tools)
3. Generates a `team.md` with culture document, lead designation, etc.
4. Copies (not symlinks) the agent files into the team directory
5. Removes the `.auto-team` marker
6. The team is now fully managed with composition, skills, culture, etc.

---

## First-Run Bootstrap

On first run (or when `~/.config/toasters/system/` doesn't exist):

1. Detect missing `system/` directory
2. Copy default system team from bundled assets in the toasters binary:
   ```
   system/
     team.md
     agents/
       operator.md
       planner.md
       scheduler.md
       blocker-handler.md
     skills/
   ```
3. Create empty `user/` structure:
   ```
   user/
     skills/
     agents/
     teams/
   ```
4. Run auto-team detection
5. Log: "Initialized toasters config at ~/.config/toasters/"

**On upgrade:** Never overwrite existing system files. If new/improved system agents ship with a new version, toasters could notify the user ("New system agent definitions available") but does not auto-update. User modifications are always preserved.

---

## TUI Integration

### Current State
- `/teams` command shows team listings
- Agents panel shows active sessions
- Grid view shows agent slots

### Phase 3 Additions
- **CRUD for skills, agents, teams** — create, view, edit, delete via TUI
- **AI generation** — "Generate an agent" flow: user describes what they want, a system agent produces the `.md` file
- **AI team generation** — "Generate a team" flow: user describes the team's purpose, system agent produces team.md + agent files
- **Job view** — tasks, their status, assigned teams, dependencies
- **Team view** — which team is working on what, lead status, worker sessions
- **System team visibility** — show the system team (with badge), allow viewing/editing
- **Auto-team badge** — auto-detected teams shown with "auto" indicator + promotion option

### Session Organization
Active sessions organized by team:
```
Dev Team
  ├── senior-go-dev (lead) — working on Task 3
  │   ├── frontend-specialist — implementing component
  │   └── technical-writer — updating docs
QA Team
  └── (idle)
System
  └── operator — waiting for input
```

---

## Decisions Log

| # | Decision | Choice | Rationale |
|---|----------|--------|-----------|
| 1 | File format | `.md` with YAML frontmatter | Consistent with ecosystem (Claude Code, OpenCode), human-readable, git-friendly |
| 2 | Source of truth | Files. DB is runtime cache. | Hackable, portable, version-controllable. TUI writes files → fsnotify → DB. |
| 3 | Config location | `~/.config/toasters/` only | No CWD-scoped config. Toasters manages its own workspaces. |
| 4 | Config layout | `system/` + `user/` directories | Clean separation of toasters internals vs. user content |
| 5 | Skill granularity | Coarse for now | Start with meaningful, self-contained skills. Split later if needed. |
| 6 | Skill composition order | Frontmatter order | Concatenated in the order listed in the agent's skills list |
| 7 | Skill tool merging | Union of all skill tools + agent tools | Additive. Restrict via agent-level denylist if needed. |
| 8 | Team-wide skills | Additive with agent-level skills | Team skills injected into all members, stacking with their own |
| 9 | Team culture handling | Injected into lead's prompt; tool for workers | Lead needs full context. Workers get it on demand via `query_team_context`. |
| 10 | Agent name resolution | team-local → shared → built-in | First match wins. Team-local agents can shadow shared agents. |
| 11 | Operator model | LLM-based thin router, lead of system team | Conversational interface + delegates to system agents. Can use local model. |
| 12 | System team | Full team using same infrastructure | Same file format, composition rules, runtime. Just different tool boundary. |
| 13 | System team location | `~/.config/toasters/system/` | Separate from `user/teams/` to prevent operator from assigning work to itself |
| 14 | System team hackability | Fully hackable | Copied to user config on first run, never overwritten on upgrade |
| 15 | System agent invocation | `consult_agent` tool (distinct from `spawn_agent`) | Prevents user teams from invoking system agents or controlling toasters internals |
| 16 | System agent capabilities | Have orchestration tools, no filesystem tools | Can create jobs/tasks/assignments. Cannot read/write files or run commands. |
| 17 | Job execution | Operator delegates planning to a team, scheduler creates tasks from the plan | Operator is strategic (sequences tasks), team leads are tactical (delegate work) |
| 18 | Task assignment | Tasks assigned to teams, not individual agents | Operator thinks in teams. Team leads think in agents. |
| 19 | Task spawning | Task outcomes can trigger new tasks via `request_new_task` | Team recommends, operator/scheduler decides whether to create new job tasks |
| 20 | Task execution | Serial for now | Parallelism layered in later as an iteration |
| 21 | Error recovery | Fail the job, inform the user | Simple for now. Retry/reassign/rollback deferred to later iteration. |
| 22 | Session model | Fresh session per task | Clean context per task. Lead can use `query_job_context` for continuity. |
| 23 | Auto-team detection | `~/.claude/agents` and `~/.config/opencode/agents` (user-level only) | Symlinked into `user/teams/auto-<tool>/`, used as-is without composition |
| 24 | Auto-team promotion | Specialized flow translates to toasters format | Preserves behavior, generates team.md + culture, copies files |
| 25 | Team capability discovery | Team name + description + lead agent description | Injected into system agents that need to pick teams. Simple, sufficient for LLM. |
| 26 | Operator event loop | Code-driven event loop with selective LLM involvement | A Go event loop receives all events, handles routine ones mechanically (DB updates, feed posts, next-task assignment), and only sends decision-requiring events to the operator LLM session. The operator is reactive to the user and to situations that need decisions — not a monitoring daemon. See [Operator Event Loop Design](#operator-event-loop-design). |
| 27 | Task-to-team handoff | `assign_task` tool spawns team lead goroutine (fire-and-forget) | System agents (planner/scheduler) call `assign_task`, which spins up the team lead session as a goroutine and returns immediately. Team lead reports back via `complete_task` → event channel. |
| 28 | `consult_agent` mechanics | Synchronous (blocks operator session) | Operator calls `consult_agent("planner", ...)` which blocks via `SpawnAndWait`. System agents are short-lived — they create jobs/tasks/assignments and return. Need to know if actions succeeded. |
| 29 | Built-in agent/skill bundling | `go:embed` | Simplest approach. Default system team files embedded in binary, copied to `~/.config/toasters/system/` on first run. |
| 30 | Agent definition richness | Superset of Claude Code + OpenCode fields | Enables lossless import from both formats. Export is lossy for toasters-specific fields (skills composition, multi-provider, etc.) with warnings. See [Agent Definition Format (Superset)](#agent-definition-format-superset). |
| 31 | Frontmatter parsing | Proper YAML parsing (`gopkg.in/yaml.v3`) | Current line-by-line parser can't handle nested maps/lists needed for permissions, hooks, mcp_servers. Prerequisite for rich agent definitions. |
| 32 | TUI role change | Activity feed + direct DB views | Chat window becomes an activity feed showing system events (pure logic) interleaved with LLM interactions. Other TUI views (progress, agent output, team management) read from DB directly — no LLM involved. User chat with the operator is one kind of feed entry among many. |
| 33 | Team capability injection | System prompt injection (option 1) | Team names + descriptions + lead descriptions baked into system agent's system prompt at session creation. Fresh sessions get fresh data automatically. Operator has `query_teams` as fallback if teams change mid-session. No need for live-update mechanism. |
| 34 | System agent session lifecycle | Fresh session per consultation | `consult_agent` spins up a new session each time. No token savings from reuse — system prompt (personality + team list) must be sent regardless. Simple, stateless, no history management. Consistent with Decision #22 (fresh session per task). |
| 35 | Schema strategy | Blow away definition tables, rebuild from files | No migrations for definition tables (agents, teams, skills, etc.) — DB is a runtime cache (Decision #2). Operational tables (jobs, tasks, sessions, progress) preserved where possible, but no backwards-compat guarantees pre-release. |
| 36 | Upgrade path | Move files, no safety net | On startup: if `system/` missing → bootstrap from `go:embed`. If old `teams/` exists → move to `user/teams/`, generate basic `team.md` where missing, delete old `teams/`. Create `user/skills/` and `user/agents/` if missing. No copies, no markers, not cautious. |

---

## Operator Event Loop Design

### Core Principle

**The operator is reactive to the user and reactive to situations that need decisions. It is not a monitoring daemon that watches everything.**

The operator LLM doesn't see every event. Routine operations (task started, task completed with next task queued, progress updates) are handled mechanically by a code-driven event loop. The operator LLM only gets involved when intelligence is needed — user messages, ambiguous situations, blockers that need triage.

### Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     TUI (Bubble Tea)                         │
│                                                              │
│  ┌─────────────────────────────────────────────────────┐     │
│  │              Activity Feed                           │     │
│  │                                                      │     │
│  │  You: Build me a production-ready HTTP server        │     │
│  │                                                      │     │
│  │  Operator: On it. I've created a job and assigned    │     │
│  │  planning to the research team.                      │     │
│  │    ↳ consulting planner...                           │     │
│  │    ↳ planner created job "Build HTTP server"         │     │
│  │    ↳ planner assigned task 1 to research-team        │     │
│  │                                                      │     │
│  │  ⚡ research-team started task 1: "Plan the work"    │     │
│  │  ⚡ research-team completed task 1                    │     │
│  │  ⚡ dev-team started task 2: "Scaffold project"      │     │
│  │  ⚡ dev-team completed task 2                         │     │
│  │  ⚡ dev-team started task 3: "Implement routes"      │     │
│  │                                                      │     │
│  │  🚫 dev-team reported blocker on task 3              │     │
│  │    ↳ consulting blocker-handler...                   │     │
│  │                                                      │     │
│  │  Operator: The dev team needs your input — which     │     │
│  │  auth middleware should they use?                     │     │
│  │  1. JWT    2. OAuth2    3. Basic auth                │     │
│  │                                                      │     │
│  │  You: 1                                              │     │
│  │                                                      │     │
│  │  ⚡ dev-team resumed task 3                           │     │
│  │  ⚡ dev-team completed task 3                         │     │
│  │  ✅ Job "Build HTTP server" complete (3 tasks)       │     │
│  └─────────────────────────────────────────────────────┘     │
│                                                              │
│  Other TUI views (progress, agents, teams) read from DB      │
│  directly — no LLM involved.                                 │
└──────────┬──────────────────────────────────────────────────┘
           │
           │ user input / events
           │
┌──────────▼──────────────────────────────────────────────────┐
│              Code-Driven Event Loop (goroutine)               │
│                                                              │
│  Receives ALL events. Handles routine ones mechanically.     │
│  Only sends decision-requiring events to the operator LLM.   │
│                                                              │
│  for event := range eventCh {                                │
│      updateDB(event)           // always                     │
│      postToFeed(event)         // always                     │
│      if needsDecision(event) {                               │
│          sendToOperatorLLM(event)                             │
│      } else {                                                │
│          handleMechanically(event)                            │
│      }                                                       │
│  }                                                           │
└──────────┬──────────────────────────────────────────────────┘
           │ (only when decisions needed)
           │
┌──────────▼──────────────────────────────────────────────────┐
│              Operator LLM Session (long-lived)                │
│                                                              │
│  Receives: user messages, decision-requiring events          │
│  Responds: text (displayed in feed) + tool calls             │
│  Tools: consult_agent, query_job, query_teams, ask_user      │
│                                                              │
│  Does NOT receive: routine task starts/completions,          │
│  progress updates, or any event the code loop handles.       │
│  Can query DB on demand (e.g., user asks "status of job?")   │
└─────────────────────────────────────────────────────────────┘
```

### Event Types and Routing

```go
type OperatorEvent struct {
    Type    OperatorEventType
    Payload any // event-specific data
}
```

| Event | Needs LLM? | Code-driven handling |
|-------|-----------|---------------------|
| User message | **Yes** — always | Post to feed, then send to operator LLM |
| Task started | **No** | Update DB, post to feed |
| Task completed, next task queued | **No** | Update DB, post to feed, assign next task |
| Task completed, job done | **No** | Update DB, mark job complete, post to feed |
| Task completed with follow-up recommendations | **Yes** — scheduler decides | Update DB, post to feed, consult scheduler to create new tasks |
| Task failed | **Yes** — need to decide next steps | Update DB, post to feed, send to operator LLM |
| Blocker reported | **Yes** — need to triage | Update DB, post to feed, consult blocker-handler |
| Progress update | **No** | Update DB (TUI polls for display) |

### The Activity Feed

The chat window evolves into an **activity feed** — a chronological stream of everything happening. Two kinds of entries:

**System events** (no LLM, pure logic):
```
⚡ dev-team started task 2: "Scaffold project"
⚡ dev-team completed task 2
✅ Job "Build HTTP server" complete (3 tasks)
```

**LLM interactions** (operator thinking, consulting agents):
```
You: Build me a production-ready HTTP server

Operator: On it. I've created a job and assigned planning to the research team.
  ↳ consulting planner...
  ↳ planner created job "Build HTTP server"
  ↳ planner assigned task 1 to research-team
```

Both appear interleaved chronologically. Chatting with the operator is just one kind of thing in the feed.

### The `consult_agent` → `assign_task` Chain

When the event loop sends a user message to the operator LLM:

```
Event loop receives user message: "Build me a todo app"
  │
  ├─ [LOGIC] Post to feed: "You: Build me a todo app"
  ├─ [LOGIC] Send to operator LLM session
  │
  ├─ [LLM]  Operator responds: "I'll get this planned out."
  │          + tool call: consult_agent("planner", "User wants a todo app")
  │
  ├─ [LOGIC] Post to feed: "Operator: I'll get this planned out."
  ├─ [LOGIC] Post to feed: "  ↳ consulting planner..."
  ├─ [LOGIC] Execute consult_agent → SpawnAndWait("planner", ...)
  │   │
  │   ├─ [LLM]  Planner session (fresh, short-lived):
  │   │          Calls create_job → [LOGIC] SQLite insert
  │   │          Calls create_task → [LOGIC] SQLite insert
  │   │          Calls assign_task("research-team", "task-1")
  │   │            → [LOGIC] SQLite update + spawn team lead goroutine
  │   │          Returns: "Created job, assigned to research-team"
  │   │
  │   └─ [LOGIC] Post to feed: "  ↳ planner created job..."
  │              Post to feed: "  ↳ planner assigned task 1 to research-team"
  │
  ├─ [LLM]  Operator sees planner result, responds with text
  ├─ [LOGIC] Post to feed: operator's response
  ├─ [LOGIC] Operator LLM turn complete. Event loop resumes waiting.
  │
  │   ... time passes, team works ...
  │
  ├─ [LOGIC] Event: research-team completed task 1
  │          Post to feed: "⚡ research-team completed task 1"
  │          Task has follow-up recommendations → needs LLM
  │          Consult scheduler → scheduler creates tasks 2, 3, 4
  │          Assign task 2 to dev-team (next in queue)
  │          Post to feed: "⚡ dev-team started task 2"
  │
  ├─ [LOGIC] Event: dev-team completed task 2
  │          Post to feed: "⚡ dev-team completed task 2"
  │          Next task queued (task 3) → assign mechanically
  │          Post to feed: "⚡ dev-team started task 3"
  │
  └─ ... continues until job complete ...
```

### Operator LLM Session Details

The operator LLM is a **long-lived `runtime.Session`** but it's not running continuously. It sits idle between interactions. The event loop sends it messages when decisions are needed, and the LLM responds with text and/or tool calls.

**Tools available to the operator LLM:**

| Tool | Description |
|------|-------------|
| `consult_agent` | Synchronous — blocks, spawns system agent via `SpawnAndWait`, returns result |
| `query_job` | Read job state from SQLite |
| `query_teams` | Read available teams from SQLite |
| `ask_user` | Send a question to the user via the feed, block on response |

The operator LLM does **not** have `create_job`, `create_task`, or `assign_task` directly. Those are system agent tools (planner, scheduler). The operator delegates to system agents for orchestration actions.

**When the user asks "what's the status of job XYZ?"** — that's a user message, so it goes to the operator LLM. The LLM calls `query_job`, gets the current state from SQLite, and formulates a response. It doesn't need to have "seen" every task event because the DB is the source of truth.

### TUI Integration

**The activity feed** replaces the current chat window. It renders a chronological stream of `FeedEntry` items, each with a type (user message, operator message, system event, LLM interaction trace) and display formatting.

**Other TUI views** (progress panel, agent sessions, team management, grid view) continue to read from SQLite directly. No LLM involved.

**User input** goes to the event loop, which routes it to the operator LLM. The feed shows the user's message immediately (before the LLM responds).

**`ask_user` flow**: Operator LLM calls `ask_user` → tool handler posts the question to the feed via `p.Send()` → TUI enters prompt mode → user responds → response sent back to tool handler via one-shot channel → tool handler unblocks → LLM processes the answer.

### Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Operator LLM quality (routing) | Strong system prompt. Operator only makes high-level decisions; routine routing is code. |
| Deadlock (operator ↔ team) | `assign_task` is fire-and-forget; only path back is async event channel. Operator LLM is not in the loop for routine events. |
| TUI responsiveness | Event loop runs in goroutine, communicates via `p.Send()` — TUI never blocked. |
| `ask_user` response channel lifetime | Operator context cancellation on TUI shutdown unblocks the select. |
| Conversation history growth | Operator only sees user messages + decision events, not every task update. Much slower growth. Can still add summarization/truncation later. |
| Token cost | Dramatically reduced — routine events don't touch the LLM. Only user messages and decision points consume tokens. |

---

## Agent Definition Format (Superset)

Toasters agent definitions are a **superset** of Claude Code and OpenCode fields, enabling lossless import from both formats and lossy export with warnings.

### Complete Field Set

**Identity:**

| Field | Type | Default | Source |
|-------|------|---------|--------|
| `name` | string | filename stem | CC, OC, T |
| `description` | string | *(required)* | CC, OC, T |

**Behavior:**

| Field | Type | Default | Source |
|-------|------|---------|--------|
| `mode` | string | `worker` | OC, T — values: `lead`, `worker` |
| `skills` | list[string] | `[]` | CC, T — skill references for composition |
| `temperature` | float | provider default | OC, T |
| `top_p` | float | provider default | OC |
| `max_turns` | int | unlimited | CC (`maxTurns`), OC (`steps`) |

**Provider/Model:**

| Field | Type | Default | Source |
|-------|------|---------|--------|
| `provider` | string | team/global default | T — multi-provider support |
| `model` | string | team/global default | CC, OC, T |
| `model_options` | map[string]any | `{}` | OC — provider passthrough (e.g., `reasoningEffort`) |

**Tools:**

| Field | Type | Default | Source |
|-------|------|---------|--------|
| `tools` | list or map | inherit all | CC (list/allowlist), OC (map/enable-disable) — both accepted |
| `disallowed_tools` | list[string] | `[]` | CC (`disallowedTools`) — explicit denylist |

**Permissions:**

| Field | Type | Default | Source |
|-------|------|---------|--------|
| `permission_mode` | string | `default` | CC (`permissionMode`) — coarse: `default`, `accept_edits`, `plan`, `bypass` |
| `permissions` | map | `{}` | OC (`permission`) — granular per-tool with glob patterns |

**MCP:**

| Field | Type | Default | Source |
|-------|------|---------|--------|
| `mcp_servers` | list or map | `[]` | CC (`mcpServers`) — per-agent MCP server access |

**Memory:**

| Field | Type | Default | Source |
|-------|------|---------|--------|
| `memory` | string | none | CC — persistent memory scope: `user`, `project`, `local` |

**UI/Display:**

| Field | Type | Default | Source |
|-------|------|---------|--------|
| `color` | string | none | CC, OC, T — hex (`#FF9800`) or named (`blue`), normalized to hex |
| `hidden` | bool | `false` | OC — hide from UI autocomplete |
| `disabled` | bool | `false` | OC (`disable`) — disable without deleting |

**Lifecycle (parsed for round-trip fidelity, runtime behavior deferred):**

| Field | Type | Default | Source |
|-------|------|---------|--------|
| `hooks` | map | `{}` | CC — lifecycle hooks (`PreToolUse`, `PostToolUse`, `Stop`) |
| `background` | bool | `false` | CC — always run as background task |
| `isolation` | string | none | CC — `worktree` for git worktree isolation |

### Example

```markdown
---
name: Senior Go Developer
description: Expert Go developer with deep knowledge of idioms, testing, and architecture
mode: worker
skills:
  - go-development
  - code-review
  - git-workflow
tools:
  - read_file
  - write_file
  - edit_file
  - glob
  - grep
  - shell
disallowed_tools:
  - web_fetch
provider: anthropic
model: claude-sonnet-4-20250514
temperature: 0.3
max_turns: 50
mcp_servers:
  - github
permissions:
  bash:
    "*": allow
    "rm -rf *": deny
color: "#4CAF50"
---

You are a senior Go developer with 10+ years of experience...
```

### Import/Export Mapping

**Import (lossless in both directions):**

| From | Key Transformations |
|------|-------------------|
| Claude Code | `maxTurns` → `max_turns`, `disallowedTools` → `disallowed_tools`, `permissionMode` → `permission_mode`, `mcpServers` → `mcp_servers`, named colors → hex, model aliases (`sonnet`) → full model ID + `provider: anthropic` |
| OpenCode | `steps` → `max_turns`, `disable` → `disabled`, `permission` → `permissions`, `provider/model` string → split into `provider` + `model`, passthrough fields → `model_options` |

**Export (lossy — warnings emitted for dropped fields):**

| To | Lossy Fields |
|----|-------------|
| Claude Code | `mode`, `temperature`, `top_p`, `permissions` (granular), `hidden`, `disabled`, `model_options`, non-Anthropic `provider` |
| OpenCode | `skills` (composition), `mcp_servers`, `permission_mode`, `memory`, `hooks`, `background`, `isolation` |

### Implementation

New `internal/agentfmt` package:
- `format.go` — Format enum (Toasters, ClaudeCode, OpenCode)
- `parse.go` — `ParseFile(path)` with auto-detection
- `toasters.go` / `claudecode.go` / `opencode.go` — per-format import/export
- Export returns `[]Warning` for lossy fields

**Prerequisite:** Switch frontmatter parsing from line-by-line to `gopkg.in/yaml.v3` (Decision #31).

---

## Open Items

These areas still need design work before or during implementation:

1. ~~**Operator event loop implementation**~~ → Resolved (Decision #26). See [Operator Event Loop Design](#operator-event-loop-design).

2. ~~**Task-to-team session handoff**~~ → Resolved (Decision #27). `assign_task` tool spawns team lead goroutine.

3. ~~**`consult_agent` mechanics**~~ → Resolved (Decision #28). Synchronous via `SpawnAndWait`.

4. ~~**Team capability injection**~~ → Resolved (Decision #33). System prompt injection; fresh sessions get fresh data.

5. ~~**Schema changes**~~ → Resolved (Decision #35). Blow away definition tables, rebuild from files. No migrations.

6. ~~**Upgrade path for existing users**~~ → Resolved (Decision #36). Move files on startup, no safety net.

7. ~~**Built-in agent/skill bundling**~~ → Resolved (Decision #29). `go:embed`.

8. ~~**Agent definition richness**~~ → Resolved (Decision #30). See [Agent Definition Format (Superset)](#agent-definition-format-superset).

---

## Future Ideas (Out of Scope)

These ideas came up during brainstorming but are explicitly out of scope for Phase 3:

- **Online registry for agents, skills, and teams** — A public/private registry where users can publish and discover agent definitions, skills, and team templates. Think npm for AI agents.
- **Parallel task execution** — The scheduler can identify parallelizable tasks, but execution is serial for now.
- **Sophisticated error recovery** — Retry with same team, try different team, partial rollback, ask user.
- **Blocker escalation policies** — Configurable rules for when/how blockers get surfaced to the user.
- **Agent self-description for dynamic team assembly** — Agents describe their own capabilities, operator assembles teams on the fly.
- ~~**Export to other formats**~~ — Now in scope as part of the agent definition superset design (Decision #30).
