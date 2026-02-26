# Phase 3: Implementation Plan

**Created:** 2026-02-25
**Status:** ✅ Complete (2026-02-26) — All 6 milestones, 30 tasks delivered. See `PHASE_3_RESUME.md` for execution details.

---

## Overview

Transform toasters from a dual-write (markdown + SQLite) system with flat agent definitions into a three-layer composition model (Skills → Agents → Teams) with a code-driven operator event loop, system team, and activity feed TUI. Consolidate job persistence to SQLite-only.

**Design doc:** `PHASE_3_DESIGN.md` (36 decisions logged, all open items resolved)

---

## Milestones

| # | Milestone | What it delivers |
|---|-----------|-----------------|
| 1 | SQLite-Only Job Persistence | Remove markdown dual-writes, kill `internal/job/` |
| 2 | Frontmatter Parsing + Agent Definition Format | YAML parsing upgrade, `internal/agentfmt` package, import/export |
| 3 | Directory Layout, Bootstrap, DB Schema | `system/` + `user/` layout, `go:embed`, composition DB schema, file-to-DB loader |
| 4 | System Team + Operator Event Loop | Event loop, system agents, `consult_agent`, `assign_task`, task lifecycle |
| 5 | TUI Evolution — Activity Feed | Chat → activity feed, updated views for new data model |
| 6 | TUI CRUD + Polish | Create/edit/delete skills/agents/teams, auto-team promotion, docs |

**Parallelism:** Milestones 1 and 2 can run in parallel. Milestone 4 spike (Task 4.1) can start anytime.

---

## Milestone 1: SQLite-Only Job Persistence

Remove the markdown dual-write for jobs. SQLite becomes the sole source of truth for operational state. Deletes the `internal/job/` package (~750 lines). Prerequisite for everything else — the new event loop and task lifecycle depend on SQLite being authoritative.

### Task 1.1: Audit `internal/job` call sites

- **Description**: Catalog every import of `internal/job` across the codebase. For each call site, document what it does today and what the SQLite-only replacement looks like. Planning only — no code changes.
- **Packages affected**: None (analysis only)
- **Acceptance criteria**: Checklist mapping every `job.*` call to its replacement
- **Dependencies**: None
- **Agent**: builder

### Task 1.2: Extend `db.Store` for missing job operations

- **Description**: Add missing capabilities to the Store:
  - `description` column on `jobs` table
  - `workspace_dir` column on `jobs` table
  - `team_id` column on `tasks` table (replacing `agent_id` for team assignment)
  - `UpdateTask`, `GetTasksForJob` methods
  - New migration
- **Packages affected**: `internal/db/`
- **Acceptance criteria**: Migration applies cleanly. New methods have tests. `go test ./internal/db/...` passes.
- **Dependencies**: Task 1.1
- **Agent**: db-architect

### Task 1.3: Rewrite tool handlers to use SQLite-only

- **Description**: Rewrite all job-related tool handlers in `handler_jobs.go` and `handler_interactive.go` to use `db.Store` instead of `internal/job`. Remove all markdown file reads/writes.
- **Packages affected**: `internal/llm/tools/`
- **Acceptance criteria**: All tool handlers work against SQLite only. No imports of `internal/job`. Tests pass.
- **Dependencies**: Task 1.2
- **Agent**: builder

### Task 1.4: Remove `internal/job` references from TUI and gateway

- **Description**: Replace all `internal/job` imports in TUI (messages, blocker modal, helpers, panels) and gateway. Replace `JobsReloadedMsg` to carry DB types. Remove the jobs filesystem watcher from `cmd/root.go`.
- **Packages affected**: `internal/tui/`, `internal/gateway/`, `cmd/root.go`
- **Acceptance criteria**: Zero imports of `internal/job` anywhere. `go build ./...` succeeds. `go test ./...` passes.
- **Dependencies**: Task 1.3
- **Agent**: builder

### Task 1.5: Delete `internal/job/` package

- **Description**: Remove the entire `internal/job/` directory. Verify clean build and tests.
- **Packages affected**: `internal/job/` (deleted)
- **Acceptance criteria**: Package deleted. Build and tests pass.
- **Dependencies**: Task 1.4
- **Agent**: builder

### Task 1.6: Update CLAUDE.md

- **Description**: Update project overview, architecture, and structure to reflect SQLite-only job persistence. Remove "dual-persisted" and "OVERVIEW.md + TODO.md" references.
- **Packages affected**: `CLAUDE.md`
- **Acceptance criteria**: CLAUDE.md accurately describes the post-Milestone-1 state.
- **Dependencies**: Task 1.5
- **Agent**: builder

---

## Milestone 2: Frontmatter Parsing + Agent Definition Format

Replace the line-by-line frontmatter parser with proper YAML parsing (`gopkg.in/yaml.v3`). Create the `internal/agentfmt` package for the full superset agent definition format. Foundation for all composition work.

### Task 2.1: Create `internal/agentfmt` package

- **Description**: New package using `gopkg.in/yaml.v3` for frontmatter parsing. Define `AgentDef`, `SkillDef`, `TeamDef` structs matching the superset field set. Implement `ParseFile(path)` with auto-detection. Handle nested maps (permissions, model_options, hooks), lists, color normalization, Claude Code camelCase → snake_case normalization.
- **Packages affected**: New `internal/agentfmt/`
- **Acceptance criteria**: Parses all three definition types. Handles nested YAML. Round-trip test passes. `go test ./internal/agentfmt/...` passes.
- **Dependencies**: None (can start in parallel with Milestone 1)
- **Agent**: builder

### Task 2.2: Implement Claude Code and OpenCode import

- **Description**: Format-specific import functions: `ImportClaudeCode`, `ImportOpenCode`. Auto-detection heuristic based on frontmatter field names. Handle model aliases, provider/model splitting, field renaming.
- **Packages affected**: `internal/agentfmt/`
- **Acceptance criteria**: Can import real-world agent files from both formats. Tests with sample files.
- **Dependencies**: Task 2.1
- **Agent**: builder

### Task 2.3: Implement export with lossy field warnings

- **Description**: `ExportClaudeCode` and `ExportOpenCode` functions. `Warning` type for dropped fields.
- **Packages affected**: `internal/agentfmt/`
- **Acceptance criteria**: Export produces valid format files. Warnings list all dropped fields.
- **Dependencies**: Task 2.2
- **Agent**: builder

### Task 2.4: Migrate `internal/agents` to use `agentfmt`

- **Description**: Replace `parseFrontmatter` with `agentfmt.ParseFile`. Enrich `Agent` struct with superset fields. Update `DiscoverTeams` to look for `team.md`. Maintain backwards compatibility with existing agent files.
- **Packages affected**: `internal/agents/`
- **Acceptance criteria**: Existing agent files still parse. New fields populated when present. Tests pass.
- **Dependencies**: Task 2.1
- **Agent**: builder

### Task 2.5: Retire `internal/frontmatter` package

- **Description**: Delete `internal/frontmatter/` after all consumers migrated.
- **Packages affected**: `internal/frontmatter/` (deleted)
- **Acceptance criteria**: Package deleted. Build succeeds.
- **Dependencies**: Tasks 1.5, 2.4
- **Agent**: builder

---

## Milestone 3: Directory Layout, Bootstrap, DB Schema

The `system/` + `user/` directory layout, first-run bootstrap with `go:embed`, auto-team detection, and SQLite schema for composition. Physical infrastructure for everything that follows.

### Task 3.1: Create embedded default system team files

- **Description**: Create `defaults/system/` with `team.md`, `agents/{operator,planner,scheduler,blocker-handler}.md`, and optionally `skills/orchestration.md`. Well-crafted system prompts for each role. Bundle via `go:embed`.
- **Packages affected**: New `defaults/` directory
- **Acceptance criteria**: All files parse with `agentfmt`. Prompts are coherent. `go:embed` compiles.
- **Dependencies**: Task 2.1
- **Agent**: builder

### Task 3.2: Implement first-run bootstrap and upgrade migration

- **Description**: New `internal/bootstrap` package:
  - First run: copy embedded defaults to `~/.config/toasters/system/`, create empty `user/{skills,agents,teams}/`
  - Upgrade: move old `teams/` → `user/teams/`, generate basic `team.md` where missing, delete old dir
  - Auto-team detection: check `~/.claude/agents/` and `~/.config/opencode/agents/`, create symlinks
  - Wire into `cmd/root.go` startup
- **Packages affected**: New `internal/bootstrap/`, `cmd/root.go`
- **Acceptance criteria**: First run creates correct structure. Upgrade moves files. Auto-team symlinks work. Idempotent. Tests with `t.TempDir()`.
- **Dependencies**: Task 3.1
- **Agent**: builder

### Task 3.3: New SQLite schema for composition model

- **Description**: New migration that:
  - Drops/recreates `agents` table with superset columns
  - Drops/recreates `teams` table with composition columns
  - Creates `skills` table, `team_agents` junction table
  - Creates `feed_entries` table for activity feed
  - Updates `tasks` table (add `team_id`, `result_summary`, `recommendations`)
  - Preserves operational tables (jobs, tasks, sessions, progress_reports, artifacts)
- **Packages affected**: `internal/db/`
- **Acceptance criteria**: Migration applies on fresh and existing DB. New types and Store methods defined. Tests pass.
- **Dependencies**: Task 1.2
- **Agent**: db-architect

### Task 3.4: Implement file-to-DB loader

- **Description**: New `internal/loader` package that walks `system/` and `user/` directories, parses all `.md` files with `agentfmt`, resolves references (skill names, agent names with team-local → shared → builtin order), inserts into SQLite. Handles auto-teams. Wire into startup.
- **Packages affected**: New `internal/loader/`, `cmd/root.go`
- **Acceptance criteria**: All definitions loaded into DB. Resolution order correct. Auto-teams loaded. Unresolved references produce warnings. Tests with fixtures.
- **Dependencies**: Tasks 2.1, 3.2, 3.3
- **Agent**: builder

### Task 3.5: Wire fsnotify for live reload

- **Description**: Watch `system/` and `user/` directories. On `.md` file change (debounced 200ms), re-run loader. Send `DefinitionsReloadedMsg` to TUI.
- **Packages affected**: `internal/loader/`, `cmd/root.go`, `internal/tui/`
- **Acceptance criteria**: File edits trigger DB reload within ~200ms. TUI reflects changes. No races.
- **Dependencies**: Task 3.4
- **Agent**: builder

### Task 3.6: Implement runtime composition (prompt assembly)

- **Description**: New `internal/compose` package implementing the composition algorithm:
  1. Load agent definition
  2. Load skills (frontmatter order), concatenate prompts, union tools
  3. Load team-wide skills
  4. Compose system prompt (agent body + skills + team culture)
  5. Merge tool sets with denylist
  6. Resolve provider/model cascade
  7. Return `ComposedAgent` ready for session creation
- **Packages affected**: New `internal/compose/`
- **Acceptance criteria**: Correct prompts for all roles (system lead/worker, team lead/worker). Tools merged correctly. Provider cascade works. Tests with fixtures.
- **Dependencies**: Tasks 3.3, 3.4
- **Agent**: builder

---

## Milestone 4: System Team + Operator Event Loop

The code-driven operator event loop, system agents, `consult_agent`, `assign_task`, and event-driven task lifecycle. The brain of the new toasters.

**⚠️ Risk:** The LLM-as-orchestrator pattern hasn't been prototyped. Task 4.1 is an early spike to validate the architecture.

### Task 4.1: Spike — Operator event loop with consult_agent

- **Description**: Minimal proof-of-concept: goroutine with event channel, long-lived operator LLM session, `consult_agent` tool that spawns a fresh system agent. Hardcoded prompts, no DB, no feed. Throwaway code to validate the pattern.
- **Packages affected**: Throwaway spike code
- **Acceptance criteria**: User message → event loop → operator LLM → consult_agent → system agent → back to operator → response. Operator session survives between interactions.
- **Dependencies**: None (can start anytime)
- **Agent**: builder

### Task 4.2: Define event types and channel

- **Description**: Create `internal/operator/events.go` with typed event structs: `UserMessage`, `TaskStarted`, `TaskCompleted`, `TaskFailed`, `BlockerReported`, `ProgressUpdate`, `JobComplete`. Buffered event channel.
- **Packages affected**: New `internal/operator/`
- **Acceptance criteria**: All event types defined with typed payloads.
- **Dependencies**: Task 4.1 (spike validates pattern)
- **Agent**: builder

### Task 4.3: Implement system-level tools

- **Description**: `SystemTools` type implementing `runtime.ToolExecutor`: `create_job`, `create_task`, `assign_task` (fire-and-forget, spawns team lead goroutine), `query_teams`, `query_job`, `surface_to_user`, `relay_to_team`.
- **Packages affected**: `internal/operator/`
- **Acceptance criteria**: All tools implemented. `assign_task` spawns team lead using composition system. Tests with mock DB.
- **Dependencies**: Tasks 3.6, 4.2
- **Agent**: builder

### Task 4.4: Implement team lead and worker tools

- **Description**: Team lead tools: `complete_task` (updates DB + sends event), `request_new_task`, `report_blocker`, `report_progress`, `query_job_context`, `query_team_context`, plus all worker tools. Worker tools: existing CoreTools (`read_file`, `write_file`, etc.) plus MCP tools.
- **Packages affected**: `internal/runtime/`, `internal/operator/`
- **Acceptance criteria**: `complete_task` triggers correct event. `query_team_context` returns culture doc. Tests verify event emission.
- **Dependencies**: Tasks 3.6, 4.2, 4.3
- **Agent**: builder

### Task 4.5: Implement `consult_agent` tool

- **Description**: Operator-only tool. Looks up system agent, composes via composition system, calls `SpawnAndWait`, returns result. Synchronous (blocks operator turn). Fresh session each time.
- **Packages affected**: `internal/operator/`
- **Acceptance criteria**: `consult_agent("planner", ...)` → planner creates job/tasks → result flows back. Tests with mock provider.
- **Dependencies**: Tasks 4.3, 4.4
- **Agent**: builder

### Task 4.6: Implement the operator event loop

- **Description**: Core event loop goroutine:
  - Mechanical handling: `TaskStarted` → DB + feed. `TaskCompleted` (next queued) → DB + feed + assign next. `JobComplete` → DB + feed.
  - LLM handling: `UserMessage` → operator LLM. `TaskCompleted` (with recommendations) → consult scheduler. `TaskFailed` → operator LLM. `BlockerReported` → consult blocker-handler.
  - Start conservative — route more to LLM, tighten later.
- **Packages affected**: `internal/operator/`
- **Acceptance criteria**: Full event loop runs. User messages → operator. Task completions → next task. Blockers → blocker-handler. Feed entries created. Tests with mocks.
- **Dependencies**: Tasks 4.3, 4.4, 4.5
- **Agent**: builder

### Task 4.7: Wire event loop into startup

- **Description**: Integrate into `cmd/root.go`: create event channel, start event loop, create operator LLM session, wire TUI input → event channel, wire team events → event channel. Replace current direct-LLM-call path.
- **Packages affected**: `cmd/root.go`, `internal/tui/model.go`
- **Acceptance criteria**: TUI starts with event loop. User messages flow through it. Operator consults system agents. Task lifecycle works end-to-end.
- **Dependencies**: Task 4.6
- **Agent**: builder

---

## Milestone 5: TUI Evolution — Activity Feed

Chat window becomes a chronological activity feed. System events interleaved with LLM interactions. Other TUI views read from DB directly.

### Task 5.1: Define feed entry types and storage

- **Description**: `FeedEntry` type with entry types: `UserMessage`, `OperatorMessage`, `SystemEvent`, `ConsultationTrace`, `TaskStarted`, `TaskCompleted`, `TaskFailed`, `BlockerReported`, `JobComplete`. Store methods for create/query.
- **Packages affected**: `internal/db/`, `internal/operator/`
- **Acceptance criteria**: Feed entries can be created and queried. Types cover all events.
- **Dependencies**: Task 3.3, Task 4.6
- **Agent**: db-architect

### Task 5.2: Implement activity feed view

- **Description**: Replace chat view with activity feed. Different formatting per entry type (user messages, operator messages, system events with icons, consultation traces indented, blockers, job complete). Auto-scroll. Streaming operator responses show incrementally.
- **Packages affected**: `internal/tui/`
- **Acceptance criteria**: Feed renders all entry types correctly. Auto-scrolls. Streaming works. User messages appear immediately.
- **Dependencies**: Tasks 4.7, 5.1
- **Agent**: tui-engineer

### Task 5.3: Update remaining TUI views

- **Description**: Update teams modal (show composition, system team badge, auto-team badge), grid view (sessions by team), progress panel (task-level progress), job view (tasks with status and team assignments).
- **Packages affected**: `internal/tui/`
- **Acceptance criteria**: All views render correctly with new data model.
- **Dependencies**: Task 5.2
- **Agent**: tui-engineer

---

## Milestone 6: TUI CRUD + Polish

CRUD operations for skills, agents, teams via TUI. Auto-team promotion. Final documentation.

### Task 6.1: Implement skill/agent/team CRUD

- **Description**: TUI modals for browse, create (writes `.md` files → fsnotify → DB), edit (open in `$EDITOR`), delete. New slash commands: `/skills`, `/agents`. Navigation between related entities.
- **Packages affected**: `internal/tui/`, `internal/agentfmt/`
- **Acceptance criteria**: Can create, view, edit, delete skills/agents/teams through TUI. Changes persist as files. DB reflects via fsnotify.
- **Dependencies**: Tasks 3.5, 5.3
- **Agent**: tui-engineer

### Task 6.2: Implement auto-team promotion

- **Description**: "Promote to managed team" action: analyze auto-team agents, translate to toasters format via `agentfmt` import, generate `team.md`, copy files, remove marker/symlink.
- **Packages affected**: `internal/tui/`, `internal/bootstrap/` or `internal/loader/`
- **Acceptance criteria**: Auto-team can be promoted. Resulting team has proper `team.md`, copied files, no symlink.
- **Dependencies**: Tasks 2.2, 6.1
- **Agent**: builder

### Task 6.3: Final documentation update

- **Description**: Full rewrite of `CLAUDE.md` for post-Phase-3 state. Mark `PHASE_3.md` deliverables complete. Update test coverage numbers. Run `golangci-lint`, fix findings. Full test suite green.
- **Packages affected**: Documentation, lint fixes
- **Acceptance criteria**: Docs accurate. Tests pass. Zero lint findings.
- **Dependencies**: All previous tasks
- **Agent**: builder

---

## Parallelism Opportunities

- **Milestone 1 and Task 2.1** can run in parallel
- **Task 3.1** (embedded files) can start after Task 2.1, independent of Milestone 1
- **Task 4.1** (spike) can start anytime
- Within Milestone 2: Tasks 2.2 and 2.3 can run in parallel after 2.1
- Within Milestone 3: Tasks 3.1 and 3.3 can run in parallel
- Within Milestone 4: Tasks 4.2 and 4.3 can run in parallel

---

## Key Risks

| Risk | Mitigation |
|------|------------|
| LLM-as-orchestrator pattern unvalidated | Task 4.1 spike validates early, before full build |
| Scope is large | Each milestone is independently shippable and leaves codebase working |
| Event loop "needs decision?" boundary is fuzzy | Start conservative (route more to LLM), tighten later |
| Context window pressure on team leads | Monitor prompt sizes, add summarization if needed |
| Operator LLM session lifecycle is novel | Spike proves the long-lived session pattern works |

---

## Out of Scope

- Parallel task execution (serial only)
- Sophisticated error recovery (fail job + inform user)
- AI-assisted agent/team generation (after core CRUD works)
- Cost estimation
- OpenAPI-to-MCP bridges
- Event-driven TUI updates (keep polling)
- MCP server HTTP transport
- MCP resource/prompt support
- Removing Claude CLI subprocess fallback
- Online registry for agents/skills/teams
