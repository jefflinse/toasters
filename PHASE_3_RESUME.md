# Phase 3 Execution — Resumption Context

**Last updated:** 2026-02-26
**Branch:** `phase-3`
**Build:** ✅ passes
**Tests:** ✅ all 19 test packages pass

---

## What Was Done

### Spike (Step 12) ✅
- Created `internal/operator/` package with event loop, `consult_agent` tool, and 14 tests
- **Architecture validated:** long-lived operator LLM session, synchronous `consult_agent` via `SpawnAndWait`, mechanical event handling, clean shutdown, no data races

### Session A — Complete (Milestones 1–2)

| Step | Description | Status |
|------|-------------|--------|
| 1 | Create `internal/agentfmt` package | ✅ Complete |
| 2 | Audit `internal/job` call sites | ✅ Complete |
| 3 | Extend `db.Store` for missing job operations | ✅ Complete |
| 4 | Rewrite tool handlers to SQLite-only | ✅ Complete |
| 5 | Remove `internal/job` from TUI and gateway | ✅ Complete |
| 6 | Delete `internal/job/` package + update CLAUDE.md | ✅ Complete |
| 7 | Implement Claude Code and OpenCode import in agentfmt | ✅ Complete |
| 8 | Implement export with lossy field warnings | ✅ Complete |
| 9 | Migrate `internal/agents` to use agentfmt | ✅ Complete |
| 10 | Retire `internal/frontmatter` package | ✅ Complete |
| 11 | Update CLAUDE.md for Session A | ✅ Complete |

### Session B — Complete (Milestones 3–4)

| Task | Description | Status |
|------|-------------|--------|
| 3.1 | Create embedded default system team files (`defaults/system/` with `go:embed`) | ✅ Complete |
| 3.2 | Implement first-run bootstrap + upgrade migration (`internal/bootstrap/`) | ✅ Complete |
| 3.3 | New SQLite schema for composition model (skills, team_agents, feed_entries) | ✅ Complete |
| 3.4 | File-to-DB loader (`internal/loader/`) — walk dirs, parse, resolve, insert | ✅ Complete |
| 3.5 | Wire fsnotify for live reload of definitions | ✅ Complete |
| 3.6 | Runtime composition / prompt assembly (`internal/compose/`) | ✅ Complete |
| 4.2 | Expand event types (TaskStarted, ProgressUpdate, JobComplete, etc.) | ✅ Complete |
| 4.3 | System-level tools (create_job, create_task, assign_task, query_teams, query_job) | ✅ Complete |
| 4.4 | Team lead + worker tools (complete_task, report_blocker, report_progress, etc.) | ✅ Complete |
| 4.5 | Production consult_agent (composition system lookup, replaces spike hardcoded prompts) | ✅ Complete |
| 4.6 | Full operator event loop (mechanical handling + selective LLM routing) | ✅ Complete |
| 4.7 | Wire event loop into startup (cmd/root.go, TUI integration) | ✅ Complete |

### Key Commits
- `d102c26` — feat: Phase 3 Milestone 1 (tasks 1.1-1.4), agentfmt (task 2.1), operator skeleton (task 4.2)
- `bea3f99` — feat: delete internal/job package, update CLAUDE.md (Milestone 1 tasks 1.5-1.6)
- `aa3f5df` — feat: Phase 3 Milestone 2 — agentfmt import/export, agents migration, retire frontmatter
- (Session B commit pending)

---

## Current Codebase State

- **`defaults/system/`** — Embedded system team files: team.md, 4 agents (operator, planner, scheduler, blocker-handler), 1 skill (orchestration). Bundled via `go:embed`.
- **`internal/bootstrap/`** — First-run bootstrap copies embedded defaults to `~/.config/toasters/system/`, creates `user/` directory structure, auto-team detection for `~/.claude/agents/` and `~/.config/opencode/agents/`. Upgrade migration moves old `teams/` to `user/teams/`. Idempotent. 6 tests.
- **`internal/compose/`** — Runtime composition: loads agent → skills → team culture → merges tools (with denylist) → resolves provider/model cascade → returns `ComposedAgent`. Role-based tools injected (lead, worker, system lead, system worker). 10 tests.
- **`internal/loader/`** — File-to-DB loader walks `system/` + `user/` dirs, parses `.md` files with agentfmt, resolves agent references (team-local → shared → system), slugifies IDs, calls `RebuildDefinitions`. fsnotify watcher with 200ms debounce, `.md` filtering, dynamic dir watching. 19 tests.
- **`internal/db/`** — Migration 003 adds skills, team_agents, feed_entries tables. Enriched agents (23 cols) and teams (13 cols). `RebuildDefinitions` transactional rebuild. `AssignTask`, `UpdateTaskResult` methods. 62 tests.
- **`internal/operator/`** — Full event loop with mechanical handling (task started/completed/progress) + LLM routing (user messages, failures, blockers, recommendations). 9 event types. SystemTools (7 tools for system agents). TeamLeadTools (6 tools). WorkerTools (2 tools). Production `consult_agent` via composition system. Feed entries for all visible events. 62 tests.
- **`internal/runtime/`** — `SpawnOpts.ToolExecutor` field for custom tool injection (used by consult_agent to give system agents SystemTools instead of CoreTools).
- **`cmd/root.go`** — Startup wires bootstrap → loader → composer → operator event loop. Definitions watcher runs alongside existing agents watcher. Operator callbacks send TUI messages.
- **`internal/tui/`** — New messages: `DefinitionsReloadedMsg`, `OperatorTextMsg`, `OperatorEventMsg`. Minimal handlers (log only — activity feed comes in Session C). Model holds `*operator.Operator`.

---

## What Comes Next

### Session C (Milestones 5–6): TUI + Polish
- **5.1**: Define feed entry types and storage (mostly done — FeedEntry types exist, store methods exist)
- **5.2**: Implement activity feed view (replace chat view with chronological feed)
- **5.3**: Update remaining TUI views (teams modal, grid view, progress panel, job view)
- **6.1**: Implement skill/agent/team CRUD via TUI
- **6.2**: Implement auto-team promotion
- **6.3**: Final documentation update

---

## Key Reference Documents

- **`PHASE_3_PLAN.md`** — Full 30-task implementation plan with 6 milestones
- **`PHASE_3_DESIGN.md`** — Comprehensive design doc (1,109 lines, 36 decisions)
- **`PHASE_3.md`** — Roadmap overview
- **`CLAUDE.md`** — Project conventions and architecture (updated through Session B)

---

## How to Resume

```
Ready to resume Phase 3 execution. See PHASE_3_RESUME.md for full context.

Current state: Session B complete. Milestones 1-4 done.
Next: Session C — TUI evolution (activity feed, CRUD, polish).

Use the feature-workflow skill. The plan is already approved — proceed with execution.
```
