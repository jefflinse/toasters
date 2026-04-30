# Session Handoff

## Context

Continuing feature development on the `dev` branch. This session completed the prompt engine migration, fixed a team lead pushback bug, and removed the entire legacy agent system (~6700 lines deleted).

## What was built this session

### 1. System agent prompt engine migration (complete)
- All 5 system agents (operator, planner, decomposer, scheduler, blocker-handler) migrated from legacy Composer to prompt engine
- System roles live in `defaults/system/roles/` with template support (`{{ globals.now.date }}`, `{{ instructions.discover-teams }}`, etc.)
- Shared system instructions created: `discover-teams`, `task-specificity`, `do-exact`, `stop-and-request-if-unclear`
- `LoadDir` now takes a `source` parameter ("system" or "user"); `Role.Source` enables access control
- Bootstrap syncs missing system files on every startup via `syncEmbeddedFS` (additive — doesn't overwrite existing)
- `consultAgent()` tries prompt engine first with `role.Source == "system"` guard
- Operator prompt composed via prompt engine at startup and on live provider activation

### 2. Team lead pushback fix (complete)
- Safety-net watcher now force-**fails** tasks (not force-completes) when a team lead session ends without calling any terminal tool
- `CompletedCalled` → `TerminalActionTaken`, tracking both `complete_task` and `report_blocker`
- `ForceComplete` → `ForceFail`, emits `TaskFailed` event so operator can decide next steps
- Team-lead role prompt updated to use `report_blocker` for out-of-scope work

### 3. Legacy agent system removal (complete)
- Deleted `internal/compose/` — the entire legacy Composer
- Deleted `defaults/system/agents/` — old system agent .md files
- Stripped `internal/agentfmt/` — removed `AgentDef`, `ParseAgent`, Claude Code/OpenCode import/export, format detection, color handling. Kept `SkillDef`, `TeamDef`, `ParseSkill`, `ParseTeam`
- Removed agent loading from loader (`loadAgents`, `convertAgent`, `resolveAgent`)
- Stubbed agent CRUD in service (`CreateAgent`, `DeleteAgent`, `GenerateAgent`, `AddSkillToAgent`)
- Removed Composer wiring from serve.go, operator Config, operatorTools, SystemTools
- Added `runtime.ComposedAgent` struct replacing `compose.ComposedAgent`
- Prompt engine is now the **sole** composition path for all agents

## What's next

1. **Task granularity tuning** — Local models (Gemma 4 26B) loop on open-ended tasks. Operator subtask descriptions need to be more specific. May need max-turn limits on workers.

2. **Clean up remaining agent DB infrastructure** — `db.Agent` table and methods still exist for team membership tracking via synthetic agents. Could be simplified to just role references on teams.

3. **Remove stubbed agent CRUD endpoints** — `ListAgents`, `GetAgent`, `CreateAgent`, `DeleteAgent`, `AddSkillToAgent`, `GenerateAgent` are stubbed. The API endpoints and TUI modal could be removed entirely or repurposed for roles.

4. **Bootstrap config overwrite** — Bootstrap writes a fresh config.yaml on every "first run" (when config doesn't exist). Users who delete their config lose their operator provider setting.

## Key files changed

| Area | Files |
|---|---|
| Prompt engine | `internal/prompt/engine.go` (Source field, LoadDir source param) |
| System roles | `defaults/system/roles/*.md`, `defaults/system/instructions/*.md` |
| Bootstrap sync | `internal/bootstrap/bootstrap.go` (syncEmbeddedFS) |
| Operator wiring | `cmd/serve.go`, `internal/operator/operator.go` |
| consultAgent | `internal/operator/tools.go` (prompt-engine-only, source guard) |
| assignTask | `internal/operator/system_tools.go` (prompt-engine-only) |
| Team lead safety net | `internal/runtime/team_lead.go`, `internal/operator/team_tools.go` |
| Team-lead prompt | (file later removed during graph-executor migration; report_blocker for pushback) |
| Deleted: Composer | `internal/compose/` (entire package) |
| Deleted: old agents | `defaults/system/agents/` (5 files) |
| Stripped: agentfmt | `internal/agentfmt/` (agent-specific code removed) |
| Loader cleanup | `internal/loader/loader.go` (agent loading removed) |
| Service cleanup | `internal/service/local.go` (agent CRUD stubbed, Composer removed) |

## Branch state

- Branch: `dev`
- All tests passing, race detector clean
- Build is clean (`go build ./...`)
