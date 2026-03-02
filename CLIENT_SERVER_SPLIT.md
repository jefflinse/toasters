# Client/Server Architecture Split

**Created:** 2026-02-28
**Status:** Phase 1 complete ✅ — all steps done, comprehensive review passed (8 blocking issues fixed, 23 suggestions documented); Phase 2 not started
**Effort Estimate:** 10–17 days across 4 phases

---

## Objective

Split the Toasters monolithic TUI application into a client/server architecture where the orchestration engine (operator, runtime, store, MCP, loader, compose, providers) runs as a long-running server process and the TUI becomes a thin rendering client. The server must be embeddable (in-process or standalone), and the application must remain fully functional at every intermediate step.

---

## Design Constraints

- The server is embeddable — it can run in-process (current mode) OR as a standalone process
- REST API for operations + SSE for server-push event stream (no gRPC, no WebSocket)
- The TUI remains the primary client
- Multiple clients can connect simultaneously (e.g., two TUIs on different monitors showing different views)
- CLI subcommands on the same `toasters` binary provide a secondary non-TUI interface
- Jobs must survive TUI disconnects when running in standalone server mode
- The server owns all state — the TUI is purely a renderer + input collector
- No multi-user support (single user, single server)
- No TLS termination (use a reverse proxy for HTTPS)

---

## Protocol Decisions

**REST + SSE** was chosen over WebSocket for the following reasons:

- The operator's callback model (`onText`, `onEvent`, `onTurnDone`) maps naturally to server-push events
- User messages are infrequent (human typing speed) and fit regular HTTP POST
- SSE is simpler than WebSocket, works great with Go's `net/http`, and is more proxy/firewall-friendly
- A future web UI would benefit from SSE (native browser `EventSource` API with auto-reconnect)
- No need for full-duplex — the client rarely pushes data outside of "send a message"

---

## Out of Scope

- Multi-user / multi-tenancy
- Web UI client (future effort)
- Remote MCP server management (server-side only)
- Distributed agent runtime
- API versioning beyond v1
- Database migration from SQLite
- Refactoring the operator event loop
- Breaking changes to definition file format
- Conversation persistence (future feature — not required for the split)
- File editing in remote mode (`openInEditor()` disabled for remote clients; future feature)
- `Last-Event-ID` SSE replay (future optimization — on reconnect, clients fetch full state via REST instead; ring buffer replay would reduce reconnect latency for short disconnects)

---

## Design Review Feedback

Reviewed by **tui-engineer** and **api-designer** on 2026-02-28. Both approved the overall plan with the following findings incorporated.

### Resolved Design Decisions

| Decision | Resolution |
|----------|-----------|
| **Multi-client support** | Yes — multiple clients can connect simultaneously. All clients receive all SSE events. Use case: TUI on one monitor showing main view, another showing jobs. |
| **Progress polling** | Replace 500ms SQLite polling with server-push via SSE. The server pushes `progress.update` events when state changes. |
| **Service interface level** | Use-case level, NOT db.Store level. Composed of domain-specific sub-interfaces (Operator, Definitions, Jobs, Sessions, Events, System). |
| **LLM generation pattern** | Async: return `202 Accepted` with operation ID, push results via SSE `operation.completed` / `operation.failed` events. |
| **Message→response correlation** | `SendMessage` returns a `turnID`. All subsequent `operator.text`, `operator.done` events carry this `turnID` so clients know which response belongs to which message. |
| **textBatcher placement** | Server-side. Batch operator text tokens before SSE emission (reuse existing 16ms batching). Server must `Flush()` after every SSE event write. |
| **DB types in TUI** | Define service-level DTO types. The TUI imports only `service` types, never `db` types directly. |
| **`openInEditor()` in remote mode** | Disabled for remote clients. Show a toast explaining why. Fetch-edit-upload is a future feature. |
| **Definition source paths** | Include `source_path` in API responses. Useful for debugging, and this is a single-user tool. |
| **State on reconnect** | Full state fetch: on reconnect, the client calls REST endpoints (`ListActiveSessions`, `ListJobs`, `OperatorStatus`) to rebuild its entire view, then subscribes to SSE for future events. Always works regardless of disconnect duration. `Last-Event-ID` replay via server-side ring buffer is deferred as a future optimization. |
| **SSE event design** | Unified event envelope with sequence numbers, typed discriminator, and correlation IDs (`turn_id`, `operation_id`, `session_id`). 15-second heartbeat to keep connections alive. |
| **Endpoint naming** | Use `/skills/generate` not `/generate/skill`. Actions on resources, not verbs as top-level paths. |
| **Pagination** | Add pagination to all list endpoints from day one (cursor-based or offset/limit). |

### Blocking Concerns to Address During Implementation

| # | Source | Concern | Affects | Resolution |
|---|--------|---------|---------|------------|
| **B1** | API Designer | Service interface must be use-case-level, not db.Store-level | Phase 1, Step 1.1 | Design around TUI screens and user workflows, not internal data access. Use composed sub-interfaces. |
| **B2** | API Designer | Event subscription model must unify 3 current mechanisms (operator callbacks, session subscriptions, SQLite polling) | Phase 1, Step 1.1 | Single `Events().Subscribe(ctx)` method returns one channel carrying all event types. `LocalService` wires up all three sources internally. |
| **B3** | API Designer | Message→response correlation pattern (turn IDs) must be designed | Phase 1, Step 1.1 | `SendMessage` returns `turnID`. SSE events carry `turn_id` field for correlation. |
| **B4** | TUI Engineer | Session state reconstruction on reconnect — `runtimeSlot` accumulation doesn't survive reconnection | Phase 2 + Phase 4 | Add `Sessions().GetSession(id)` returning full `SessionDetail` (output buffer, activities, status, timing). Client calls this on reconnect to hydrate. |
| **B5** | TUI Engineer | Filesystem operations in modal handlers (~400 lines of team promotion, CRUD) are the hardest extraction | Phase 1, Step 1.2 | Move verbatim into service methods. Each becomes a single atomic service call (e.g., `PromoteTeam`, `SetCoordinator`, `AddAgentToTeam`). |
| **B6** | TUI Engineer | `openInEditor()` needs a decision for remote mode | Phase 1, Step 1.3 | Disabled in remote mode with a toast notification. Future feature. |

### Important Concerns

| # | Source | Concern | Notes |
|---|--------|---------|-------|
| **I1** | Both | Progress polling must become server-push via SSE | Addressed in resolved decisions above. |
| **I2** | TUI Engineer | `textBatcher` must live server-side to avoid double-batching | Addressed in resolved decisions above. |
| **I3** | TUI Engineer | Direct LLM usage in modals (5 call sites) must move to service | `GenerateSkill`, `GenerateAgent`, `GenerateTeam`, `DetectCoordinator`, `ListModels` — all become service methods. |
| **I4** | TUI Engineer | `operator.Event` types used in TUI rendering need service-level equivalents | Service defines its own event types; TUI never imports `operator` package. |
| **I5** | API Designer | Auth should apply to SSE from the start | Addressed in Phase 4 (auth applies to all endpoints including SSE). |

### Proposed Service Interface Structure

From the API designer review — the Service interface should be composed of domain-specific sub-interfaces:

```go
type Service interface {
    Operator()    OperatorService    // chat, messages, prompts
    Definitions() DefinitionService  // skills, agents, teams CRUD + generation
    Jobs()        JobService         // job listing, detail, cancellation
    Sessions()    SessionService     // agent session state, listing
    Events()      EventService       // SSE event subscription
    System()      SystemService      // health, models, MCP servers
}
```

### Proposed SSE Event Types

```
operator.text          — streamed token(s), carries turn_id
operator.done          — turn complete, carries turn_id
operator.prompt        — ask_user prompt, needs user response
task.assigned          — task assigned to team
task.started           — task execution started
task.completed         — task completed
task.failed            — task failed
blocker.reported       — blocker reported by agent
job.completed          — job completed
progress.update        — progress state changed (replaces polling)
session.started        — agent session started, carries session_id
session.text           — agent text delta, carries session_id
session.tool_call      — agent tool call, carries session_id
session.tool_result    — agent tool result, carries session_id
session.done           — agent session done, carries session_id
definitions.reloaded   — definition files changed, reload UI
operation.completed    — async operation finished, carries operation_id
operation.failed       — async operation failed, carries operation_id
heartbeat              — keepalive every 15s
```

### Proposed REST Endpoint Structure

```
# Operator
POST   /api/v1/operator/messages           → 202 {turn_id}
POST   /api/v1/operator/responses          → 204 (respond to ask_user)
GET    /api/v1/operator/status             → 200 {state, current_turn_id}

# SSE Event Stream
GET    /api/v1/events                      → SSE stream
GET    /api/v1/events?last_event_id=N      → SSE stream (resume)

# Skills
GET    /api/v1/skills                      → 200 [{skill}]
GET    /api/v1/skills/:id                  → 200 {skill}
POST   /api/v1/skills                      → 201 {skill}
DELETE /api/v1/skills/:id                  → 204
POST   /api/v1/skills/generate             → 202 {operation_id}

# Agents
GET    /api/v1/agents                      → 200 [{agent}]
GET    /api/v1/agents/:id                  → 200 {agent}
POST   /api/v1/agents                      → 201 {agent}
DELETE /api/v1/agents/:id                  → 204
POST   /api/v1/agents/:id/skills           → 204 (add skill)
POST   /api/v1/agents/generate             → 202 {operation_id}

# Teams
GET    /api/v1/teams                       → 200 [{team_view}]
GET    /api/v1/teams/:id                   → 200 {team_view}
POST   /api/v1/teams                       → 201 {team_view}
DELETE /api/v1/teams/:id                   → 204
POST   /api/v1/teams/:id/promote           → 202 {operation_id}
PUT    /api/v1/teams/:id/coordinator       → 204
POST   /api/v1/teams/:id/agents            → 204 (add agent)
POST   /api/v1/teams/generate              → 202 {operation_id}
POST   /api/v1/teams/:id/detect-coordinator → 202 {operation_id}

# Jobs (read-only + cancel)
GET    /api/v1/jobs                        → 200 [{job}]
GET    /api/v1/jobs/:id                    → 200 {job_detail}
POST   /api/v1/jobs/:id/cancel             → 204

# Sessions (read-only + cancel)
GET    /api/v1/sessions                    → 200 [{session_snapshot}]
GET    /api/v1/sessions/:id                → 200 {session_detail}
POST   /api/v1/sessions/:id/cancel         → 204

# System
GET    /api/v1/health                      → 200 {status, version, uptime}
GET    /api/v1/models                      → 200 [{model}]
GET    /api/v1/mcp/servers                 → 200 [{server_status}]
```

### Effort Estimate Revision

The TUI engineer estimates Phase 1 at 5–8 days (vs. original 3–5) due to filesystem coupling complexity in modal handlers. Updated estimates below reflect this.

---

## Phase 1: Extract Business Logic from TUI into Service Layer

**Goal:** Move all non-rendering logic out of the TUI package into a new `internal/service` package. This creates the seam where the network boundary will eventually be inserted. No networking yet.

**Estimated Effort:** 5–8 days

### Step 1.1: Create the Service Interface

- [x] **Status:** Complete (2026-03-01)
- **Agent:** api-designer
- **Description:** Design `internal/service` with a composed `Service` interface (see Proposed Service Interface Structure above). Must capture every operation the TUI currently performs against the store, LLM, or filesystem. Define service-level DTO types (not raw `db.*` types). Define the unified event stream contract that replaces operator callbacks, session subscriptions, and SQLite polling.
- **Key files:** `internal/service/service.go`, `internal/service/types.go`, `internal/service/events.go`
- **Blocking concerns addressed:**
  - **B1 ✅** Interface is use-case-level: 6 domain sub-interfaces (Operator, Definitions, Jobs, Sessions, Events, System)
  - **B2 ✅** `Events().Subscribe(ctx)` unifies operator callbacks, session subscriptions, and SQLite polling into one channel
  - **B3 ✅** `SendMessage` returns `turnID`; all `operator.text` / `operator.done` events carry `TurnID`
- **Acceptance criteria:**
  - [x] Composed `Service` interface with domain-specific sub-interfaces and godoc comments
  - [x] Service-level DTO types defined (not raw DB types) — zero internal imports in service package
  - [x] Unified event stream types with sequence numbers, typed discriminator, and correlation IDs
  - [x] `SendMessage` returns `turnID`; async operations return `operationID`
  - [x] No implementation — interface only
- **Design decisions made during review:**
  - `GetTeamDef` / `TeamDef` removed — `service.Team` already carries Skills/Provider/Model/Culture; TUI reads from `TeamView.Team` directly
  - `service.Blocker` + `service.BlockerQuestion` are canonical; TUI-local types in `blocker_modal.go` will be deleted in Step 1.3
  - `SessionDetail.Output` is a pre-formatted `string` (bounded ~50–100KB in practice; no pagination needed for Phase 1)
- **Gate:** ✅ Human-reviewed and approved 2026-03-01

### Step 1.2: Implement the In-Process Service

- [x] **Status:** Complete (2026-03-01)
- **Agent:** builder
- **Description:** Implement `internal/service.LocalService` satisfying the `Service` interface by delegating to existing components (`db.Store`, `provider.Provider`, `runtime.Runtime`, `operator.Operator`, etc.). Mechanical extraction of business logic from TUI methods into service methods. All logic lives in `internal/service/local.go`.
- **Blocking concern B5:** Filesystem operations in modal handlers (~400 lines of team promotion, CRUD) are the hardest extraction. Move verbatim, don't refactor.

#### LocalConfig struct

```go
type LocalConfig struct {
    Store        db.Store
    Runtime      *runtime.Runtime
    Operator     *operator.Operator
    MCPManager   *mcp.Manager
    Provider     provider.Provider  // operator's LLM provider (for ListModels, generation)
    Composer     *compose.Composer
    Loader       *loader.Loader
    ConfigDir    string
    WorkspaceDir string
    TeamsDir     string
    OperatorModel string  // for OperatorStatus.ModelName
    StartTime    time.Time  // for Health().Uptime
}
```

#### Operator callback wiring design

`operator.Operator` sets callbacks at construction time via `operator.Config` — they cannot be changed after `New()`. Therefore `LocalService` exposes exported broadcast methods that `cmd/root.go` calls from the operator callbacks:

- `BroadcastOperatorText(text, reasoning string)` — called from `OnText`
- `BroadcastOperatorEvent(ev operator.Event)` — called from `OnEvent`
- `BroadcastOperatorDone(modelName string, tokensIn, tokensOut, reasoningTokens int)` — called from `OnTurnDone`
- `BroadcastDefinitionsReloaded()` — called from `loader.NewWatcher` `onChange` callback

This avoids circular dependencies and keeps operator construction unchanged for Step 1.2.

#### Event stream design

`Events().Subscribe(ctx)` multiplexes 3 sources into one `chan Event` per subscriber:

1. **Operator callbacks** — `BroadcastOperator*` methods call `broadcast()` directly
2. **Progress state pushes** — background goroutine polls SQLite every 500ms (same data as `progressPollCmd`), broadcasts `EventTypeProgressUpdate`
3. **Session events** — deferred to Step 1.3 (would conflict with existing TUI callback wiring)

The progress polling goroutine and 15s heartbeat goroutine start lazily on the first `Subscribe()` call via `sync.Once`.

`broadcast(ev Event)` is non-blocking: under mutex, iterates subscriber channels, drops events on overflow (bounded buffer size 256).

#### Implementation subtasks

| # | What | Source | Complexity |
|---|------|--------|------------|
| 1 | Scaffold `local.go` — `LocalConfig`, `LocalService` struct, `NewLocal()`, sub-interface accessor stubs, `var _ Service = (*LocalService)(nil)` | New | Moderate |
| 2 | Type mapping helpers — `dbJobToService`, `dbTaskToService`, `dbSkillToService`, `dbAgentToService`, `dbTeamToService`, `runtimeSnapshotToService`, `dbFeedEntryToService`, `mcpServerStatusToService`, `providerModelInfoToService`, `buildTeamViews`, `isReadOnlyTeam`, `isSystemTeam`, `isAutoTeam` | `tui/team_view.go` | Moderate |
| 3 | Event stream infrastructure — `broadcast()`, subscriber management, progress polling goroutine (500ms), 15s heartbeat goroutine, `BroadcastOperator*()` and `BroadcastDefinitionsReloaded()` methods | `tui/progress_poll.go`, `cmd/root.go` | Moderate |
| 4 | `OperatorService` — `SendMessage` (generates turnID, sends to operator event loop), `RespondToPrompt`, `Status` | `tui/streaming.go` | Trivial |
| 5 | `DefinitionService` — Skills: `ListSkills`, `GetSkill`, `CreateSkill`, `DeleteSkill`, `GenerateSkill` (async) | `tui/skills_modal.go`, `tui/llm_generate.go` | Moderate |
| 6 | `DefinitionService` — Agents: `ListAgents`, `GetAgent`, `CreateAgent`, `DeleteAgent`, `AddSkillToAgent`, `GenerateAgent` (async) | `tui/agents_modal.go`, `tui/llm_generate.go` | Moderate |
| 7 | `DefinitionService` — Teams: `ListTeams`, `GetTeam`, `CreateTeam`, `DeleteTeam`, `AddAgentToTeam`, `SetCoordinator`, `PromoteTeam` (async, ~400 lines), `GenerateTeam` (async), `DetectCoordinator` (async) | `tui/teams_modal.go`, `tui/team_view.go`, `tui/llm_generate.go` | **Complex** |
| 8 | `JobService` — `List`, `ListAll`, `Get`, `Cancel` | `tui/jobs_modal.go` | Trivial |
| 9 | `SessionService` — `List`, `Get`, `Cancel` | `tui/progress_poll.go`, `runtime` | Trivial |
| 10 | `EventService` — `Subscribe(ctx)` delegates to internal `subscribe()` | Step 3 infrastructure | Trivial |
| 11 | `SystemService` — `Health`, `ListModels`, `ListMCPServers`, `ConfigDir`, `Slugify` | `tui/streaming.go`, `cmd/root.go` | Trivial |
| 12 | File-writing helpers copied verbatim from TUI: `writeAgentFile`, `writeTeamFile`, `rewriteMode`, `copyFile`, `writeGeneratedSkillFile`, `writeGeneratedAgentFile`, `stripCodeFences` | Various TUI files | Trivial |
| 13 | Build verification: `go build ./internal/service/...`, `go vet ./internal/service/...`, `go build ./...` | — | Trivial |

#### Key extraction notes

**Skills (subtask 5):**
- `ListSkills` ← `tui/skills_modal.go:reloadSkillsForModal()` — queries `store.ListSkills`, maps `*db.Skill` → `service.Skill`
- `CreateSkill` ← `tui/skills_modal.go:createSkillFile()` — writes template .md to `user/skills/`; replace `config.Dir()` with `cfg.ConfigDir`; trigger `cfg.Loader.Load(ctx)` after write
- `DeleteSkill` ← `tui/skills_modal.go` enter-key handler — `os.Remove(skill.SourcePath)`; trigger reload
- `GenerateSkill` ← `tui/llm_generate.go:generateSkillCmd()` — async; goroutine calls LLM, writes file via `writeGeneratedSkillFile`, triggers reload, broadcasts `operation.completed` with `Kind: "generate_skill"`

**Agents (subtask 6):**
- `ListAgents` ← `tui/agents_modal.go:reloadAgentsForModal()` — same 3-group sort (shared → team-local → system)
- `CreateAgent` ← `tui/agents_modal.go:createAgentFile()` — writes template .md to `user/agents/`
- `AddSkillToAgent` ← `tui/agents_modal.go:addSkillToAgent()` — parse agent .md, append skill name, write back
- `GenerateAgent` ← `tui/llm_generate.go:generateAgentCmd()` — async; broadcasts `Kind: "generate_agent"`

**Teams (subtask 7):**
- `ListTeams` ← `tui/team_view.go:BuildTeamViews()` + filter out `source == "system"` teams
- `SetCoordinator` ← `tui/team_view.go:SetCoordinator(teamDir, agentName)` — ~90 lines; globs agents dir, parses each, rewrites `mode:` in agent files, updates `team.md` lead field
- `PromoteTeam` ← `tui/teams_modal.go:promoteAutoTeam()` + `promoteReadOnlyAutoTeam()` + `promoteMarkerAutoTeam()` — ~150 lines; async; 2 code paths (read-only auto-team copies files to new managed dir; marker auto-team replaces symlink with real dir); replace `config.Dir()` with `cfg.ConfigDir`
- `GenerateTeam` ← `tui/llm_generate.go:generateTeamCmd()` + `tui/update.go:teamGeneratedMsg handler` — async; LLM returns JSON `{team_md, agent_names}`; goroutine writes team dir + team.md + copies agent files; triggers reload; broadcasts `Kind: "generate_team"` with `Result.AgentNames`
- `DetectCoordinator` ← `tui/teams_modal.go:maybeAutoDetectCoordinator()` — async; LLM picks coordinator from workers; calls `SetCoordinator` if match found; broadcasts `Kind: "detect_coordinator"` with `Result.Content: agentName`
- `AddAgentToTeam` ← `tui/teams_modal.go:addAgentToTeam()` — parse team.md, append agent name, write back, copy agent .md file into team's agents/ dir
- `DeleteTeam` ← `tui/teams_modal.go` enter-key in confirmDelete — write dismiss marker for auto-teams; symlink-aware path validation before `os.RemoveAll`

**Jobs (subtask 8):**
- `ListAll` ← `tui/jobs_modal.go:loadJobsForModal()` — `store.ListAllJobs`
- `Get` ← `tui/jobs_modal.go:loadJobDetail()` — `store.GetJob` + `store.ListTasksForJob` + `store.GetRecentProgress`
- `Cancel` ← `tui/jobs_modal.go` enter-key in confirmCancel — validate status is cancellable, `store.UpdateJobStatus(cancelled)`

**Operator (subtask 4):**
- `SendMessage` — generates UUID v4 as `turnID`, stores as `svc.currentTurnID` under mutex, sends `operator.EventUserMessage` via `cfg.Operator.Send(ctx, ev)`, returns `turnID`
- `RespondToPrompt` — sends `operator.EventUserResponse` via `cfg.Operator.Send`

**System (subtask 11):**
- `ListModels` ← `tui/streaming.go:fetchModels()` — `cfg.Provider.Models(ctx)`
- `ListMCPServers` — `cfg.MCPManager.Servers()`, map `mcp.ServerStatus` → `service.MCPServerStatus`; map `mcp.ServerConnected` → `service.MCPServerStateConnected`, `mcp.ServerFailed` → `service.MCPServerStateFailed`

#### Type mapping gotchas

- `db.Skill.Tools` is `json.RawMessage` (JSON array of strings) → `service.Skill.Tools []string` via `json.Unmarshal`; nil on error
- Same for `db.Agent.Tools`, `db.Agent.Skills`, `db.Agent.DisallowedTools`, `db.Team.Skills`
- `db.Store` has no `GetSkillByName` — after `CreateSkill` writes file and reloads, call `ListSkills` and filter by name to return the created skill
- `service.TeamView` uses `*Agent` for `Coordinator` and `[]Agent` for `Workers` (value types); `tui.TeamView` uses `*db.Agent` (pointer types) — mapping required
- `isReadOnlyTeam` / `isSystemTeam` / `isAutoTeam` are pure functions in `tui/team_view.go` — copy verbatim as unexported helpers in `local.go`, adapting `tui.TeamView` → `service.TeamView`

#### Known gaps (deferred to Step 1.3)

- **Session event multiplexing** (`rt.OnSessionStarted` wiring into the event stream) — deferred to avoid conflicting with existing TUI callback wiring
- **`SessionDetail.Activities`** population — left empty; requires session event tracking infrastructure
- **`OperatorStatus.State`** accuracy — returns `OperatorStateIdle` always; real state tracking requires operator changes
- **`turnID` threading** through `OperatorDonePayload` — `turnID` is generated in `SendMessage` and stored in `LocalService`; `BroadcastOperatorDone` includes it; full correlation requires Step 1.3 TUI rewiring

#### Review checkpoints within this step

- After event stream infrastructure (subtask 3): **concurrency-reviewer** inspects `broadcast()`, subscriber management, `sync.Once` goroutine start pattern
- After team CRUD (subtask 7): **security-auditor** reviews `DeleteTeam` (symlink-aware path validation before `os.RemoveAll`) and `PromoteTeam` (file copies from arbitrary source directories)
- After build verification (subtask 13): **code-reviewer** full pass on `local.go` for correctness, error handling, "no TUI imports" constraint

- **Acceptance criteria:**
  - [x] `var _ Service = (*LocalService)(nil)` compiles (full interface satisfaction)
  - [x] Every `Service` interface method has a working implementation
  - [x] No TUI imports in the service package
  - [x] Event stream broadcasts `EventTypeProgressUpdate` via 500ms polling goroutine
  - [x] Async operations (`GenerateSkill`, `GenerateAgent`, `GenerateTeam`, `PromoteTeam`, `DetectCoordinator`) return `operationID` immediately and push `operation.completed`/`operation.failed` events
  - [x] `go build ./internal/service/...` passes
  - [x] `go vet ./internal/service/...` passes
  - [x] `go build ./...` passes (no regressions elsewhere)
- **Gate:** ✅ Human-reviewed and approved 2026-03-01
- **Risk:** Team promotion logic (~400 lines, 2 code paths) is the most complex extraction. Move verbatim, don't refactor. `GenerateTeam` requires tracing the TUI's `teamGeneratedMsg` handler in `tui/update.go` to understand what file writes it performs.

### Step 1.3: Rewire TUI to Use the Service

- [x] **Status:** Complete (2026-03-01)
- **Agent:** builder
- **Description:** Modify the TUI to use `service.Service` instead of directly accessing `db.Store`, `provider.Provider`, `runtime.Runtime`, and the filesystem. The `Model` struct should hold a `service.Service` instead of individual component references. The event stream subscription replaces the current `p.Send(tea.Msg)` callback wiring — a goroutine consumes service events and translates them to `tea.Msg` values.
- **Blocking concern B6:** `openInEditor()` stays in TUI but must be disabled when using a remote service. Show a toast explaining why.

#### Files changed

**Deleted:**
- `team_view.go` — all types/functions moved to service layer
- `progress_poll.go` — replaced by `event_consumer.go`

**Rewritten:**
- `messages.go` — all tea.Msg types use service types; `ChatEntry` removed (use `service.ChatEntry`)
- `model.go` — `ModelConfig` simplified to `Service service.Service`; `Model` struct loses `store`/`runtime`/`llmClient`/`mcpManager`/`operator`; gains `svc`/`configDir`
- `helpers.go` — `ChatEntry` type alias added; `formatFeedEntry`, `displayJobs`, `jobByID`, `hasBlocker` use service types; `formatOperatorEvent` removed (superseded by `formatServiceEvent` in model.go)
- `blocker_modal.go` — local `Blocker`/`BlockerQuestion` types deleted; using `service.Blocker`/`service.BlockerQuestion`
- `log_view.go` — `config.Dir()` replaced with `m.configDir`
- `panels.go` — all `db.JobStatus*`/`db.TaskStatus*`/`mcp.Server*` constants replaced with service equivalents
- `streaming.go` — `sendMessage`/`notifyOperator` use `svc.Operator().SendMessage()`; `fetchModels` uses `svc.System().ListModels()`
- `mcp_modal.go` — using `service.MCPServerStatus` flat fields
- `skills_modal.go` — `m.store`/`m.llmClient` → `m.svc.Definitions().*` calls; `service.Skill` types
- `agents_modal.go` — `m.store`/`m.llmClient` → `m.svc.Definitions().*` calls; `service.Agent`/`service.Skill` types
- `jobs_modal.go` — `m.store` → `m.svc.Jobs().*` calls; `service.Job`/`service.Task`/`service.ProgressReport` types
- `teams_modal.go` — `m.store`/`m.llmClient`/`db.Agent`/`agentfmt` → `m.svc.Definitions().*` calls; `service.TeamView`/`service.Agent` types; `isReadOnlyTeam`/`isSystemTeam`/`isAutoTeam` replicated using `service.TeamView` methods
- `llm_generate.go` — `generateSkillCmd`/`generateAgentCmd`/`generateTeamCmd` deleted; generation now async via `m.svc.Definitions().Generate*(ctx, prompt)`; only `stripCodeFences` helper retained
- `cmd/root.go` — rewritten to use `service.NewLocal()`; all component construction consolidated; callbacks wire to `svc.Broadcast*()` methods
- `cmd/awareness.go` — updated to use `service.TeamView` instead of `tui.TeamView`
- All test files updated to use `service.*` types

**Created:**
- `event_consumer.go` — goroutine subscribing to `svc.Events().Subscribe(ctx)`, translating `service.Event` values to `tea.Msg` values

#### Key design decisions locked during implementation

- `progressPollCmd` eliminated — replaced by `ConsumeServiceEvents` goroutine subscribing to `svc.Events().Subscribe(ctx)`
- Session events deferred — `RuntimeSessionEventMsg` stays wired from `cmd/root.go` callbacks directly (not service event stream); `RuntimeSessionEventMsg` now carries inline fields (no `runtime.SessionEvent`)
- `textBatcher` stays in `cmd/root.go`
- `tui.TeamView` deleted — `service.TeamView` used everywhere
- `OperatorDoneMsg` gains `ModelName`, `TokensIn`, `TokensOut`, `ReasoningTokens` fields
- `service.ProgressState` field names: `Jobs`, `Tasks`, `Reports` (not `Progress`), `ActiveSessions`, `LiveSnapshots`, `FeedEntries`

- **Acceptance criteria:**
  - [x] TUI no longer imports `db`, `provider`, `runtime`, `compose`, `loader`, `mcp`, `config`, `agentfmt`, or `bootstrap` directly
  - [x] `cmd/root.go` creates a `LocalService` and passes it to the TUI
  - [x] All existing functionality works identically
  - [x] All existing tests pass (`go test ./...` — all 18 packages green)
  - [x] `Model` struct no longer holds `store`, `runtime`, `llmClient`, `mcpManager`, or `operator` fields
  - [x] `openInEditor()` disabled with toast when service is remote
- **Risk:** Largest single step. The 14 internal package imports each represent a coupling surface to abstract.

### Step 1.4: Write Tests for the Service Layer

- [x] **Status:** Complete (2026-03-01)
- **Agent:** test-writer
- **Description:** Unit tests for `internal/service.LocalService` covering all methods. Mock `provider.Provider` for LLM generation tests. Test event stream lifecycle (subscribe, receive events, cancel context, verify cleanup).
- **Acceptance criteria:**
  - [x] Test coverage for all service methods (67 test functions in `local_test.go`)
  - [x] Tests for event stream delivery and cleanup (subscribe, broadcast, Seq numbering, context cancellation, multi-subscriber, overflow drop)
  - [x] Tests for error cases (nil store, nil provider, nil operator, ErrNotFound wrapping, path traversal rejection)
  - [x] `go test ./internal/service/...` passes (0.298s, race-clean)

### Phase 1 Review Checkpoint

- [x] **Gate:** ✋ Code review of TUI decoupling — verify no residual direct dependencies
- **Outcome:** Passed (2026-03-01) — app runs correctly; no residual banned imports in TUI

### Phase 1 Comprehensive Review (2026-03-01)

**Reviewers:** code-reviewer, test-writer, concurrency-reviewer, security-auditor, api-designer  
**Scope:** 9 commits, ~14,700 lines of diff, 38 files changed (+8,286 / -3,779)  
**Tests:** All 18 packages pass with `-race -count=1`

#### Blocking Issues (fixed)

| # | File | Issue | Fix |
|---|------|-------|-----|
| **B1** | `cmd/root.go` | First LocalService leaked — `_ = svc.Shutdown` takes method reference but never calls it; double service + double operator creation is fragile | Added `SetOperator()` method to `LocalService`; eliminated double-creation entirely |
| **B2** | `cmd/root.go:283,333` | Token counts always zero in `OperatorDoneMsg` — `OnTurnDone` callbacks hardcode `0, 0, 0` | Documented as known gap — `operator.Config.OnTurnDone` signature needs token count parameters (requires operator changes) |
| **B3** | `local.go:1664-1665` | `GetJob` wraps all store errors as `ErrNotFound` — DB failures misreported as "not found" | Check for `db.ErrNotFound` specifically; pass through other errors unwrapped |
| **B4** | `skills_modal.go:158` | Direct `os.Remove` bypasses service layer security checks (path traversal, system skill rejection) | Replaced with `svc.Definitions().DeleteSkill(ctx, sk.ID)` |
| **B5** | `local.go:516-523, 768-776, 1047` | YAML frontmatter injection — user-supplied name interpolated raw into YAML via `fmt.Sprintf` | Sanitize names: strip newlines, carriage returns, and leading YAML special characters |
| **B6** | `event_consumer.go:26` | Post-shutdown `prog.Send()` potential panic — consumer holds direct `*tea.Program` reference | Changed to accept `*atomic.Pointer[tea.Program]` and nil-check before sending |
| **B7** | `cmd/root.go:160-224` + `event_consumer.go:69-131` | Duplicate session event delivery path — direct callbacks AND event consumer handle same events | Removed dead session event handlers from `event_consumer.go`; documented direct callback as canonical path |
| **B8** | `service.go` + `events.go` | `RespondToPrompt` and `EventTypeOperatorPrompt` defined but completely unwired | Documented as Phase 2 TODO — prompt flow needs full wiring when HTTP layer is added |

#### Suggestions (for Phase 2 pre-work)

| # | Source | Issue | Priority |
|---|--------|-------|----------|
| S1 | Security | No input validation on `SendMessage` — unbounded message string | High (before Phase 2) |
| S2 | Security | No input validation on Generate prompts — unbounded | High (before Phase 2) |
| S3 | Security | `SourcePath` fields expose absolute filesystem paths in DTOs | High (before Phase 2) |
| S4 | Security | Error messages leak internal paths | High (before Phase 2) |
| S5 | Security | No rate limiting on async operations — unbounded goroutines | High (before Phase 2) |
| S6 | Security | `writeGeneratedTeamFiles` missing path traversal check | Medium |
| S7 | Security | `copyFile` has no size limit | Medium |
| S8 | API Design | `ProgressState` full snapshot every 500ms won't scale over HTTP — switch to deltas | High (before Phase 2) |
| S9 | API Design | `SystemService.Slugify` and `ConfigDir` are client-side concerns | Medium |
| S10 | API Design | `JobService.ListAll` redundant with `List(ctx, nil)` | Low |
| S11 | API Design | `SessionSnapshot.Status` and `SessionDonePayload.Status` are `string` not `SessionStatus` | Low |
| S12 | API Design | `BlockerReportedPayload` too thin vs. `Blocker` type | Medium |
| S13 | API Design | No `History()` method for conversation reconnect hydration | High (before Phase 2) |
| S14 | API Design | No `RespondToBlocker()` method for submitting blocker answers | Medium |
| S15 | API Design | `OperationResult.Error` conflates success and failure fields | Low |
| S16 | Concurrency | Subscriber cleanup goroutine not bounded by service context | Medium |
| S17 | Concurrency | Two mutexes (`mu`, `turnMu`) with no documented lock ordering | Low |
| S18 | Concurrency | `buildProgressState` uses `context.Background()` instead of service context | Low |
| S19 | Code Review | Duplicate `stripCodeFences` in service and TUI | Low |
| S20 | Code Review | Duplicate `cachedHomeDir`/`cachedHomeDirOnce` in teams_modal and local.go | Low |
| S21 | Code Review | Variable shadowing of receiver `s` in `writeGeneratedSkillFile` | Low |
| S22 | Code Review | Unbounded dedup loop in `writeGenerated*File` | Low |
| S23 | Code Review | `SendMessage` error silently swallowed in `streaming.go` | Medium |

#### Phase 2 Readiness Checklist

Before exposing this service over HTTP, these items must be addressed:

| Priority | Item | Findings |
|----------|------|----------|
| Critical | YAML injection fix | B5 (fixed) |
| Critical | Prompt flow wiring | B8 (documented) |
| High | Input validation on all string params | S1, S2 |
| High | Path/info leakage in errors | S3, S4 |
| High | Rate limiting on async ops | S5 |
| High | Delta-based progress events | S8 |
| High | Authorization model design | Security review |
| High | Conversation history endpoint | S13 |
| Medium | Event gap notification for slow consumers | Security review |
| Medium | Blocker answer submission endpoint | S14 |
| Medium | `Slugify`/`ConfigDir` client-side extraction | S9 |

#### Positive Observations

- Clean interface decomposition — 6 sub-interfaces well-scoped and following Go conventions
- Zero internal imports in `types.go` — DTO layer completely decoupled
- Event envelope design — sequence numbers, correlation IDs, typed payloads ready for SSE
- `event_consumer.go` — clean, exhaustive translation layer
- Non-blocking broadcast — `select/default` drop-on-overflow is correct pattern
- Path traversal defense on deletes — `EvalSymlinks` + `HasPrefix` with separator suffix
- System definition protection — system skills/agents/teams immutable through service layer
- LLM output validation — generated content parsed through `agentfmt.ParseBytes` before writing
- Graceful nil handling — service handles nil Store/Runtime/Operator/Provider with clear errors

---

## Phase 2: Build the Server

**Goal:** Create the HTTP server with SSE event streaming that exposes the service layer over the network. Embeddable — can run in-process or standalone.

**Estimated Effort:** 3–5 days

### Step 2.1: Design the API

- [ ] **Status:** Not started
- **Agent:** api-designer
- **Description:** Finalize the REST API design (see Proposed REST Endpoint Structure above). Define JSON request/response schemas for each endpoint. Define the SSE event envelope format with sequence numbers and correlation IDs. Define error response format. Add pagination to all list endpoints.
- **Acceptance criteria:**
  - [ ] API spec with all endpoints, methods, request/response schemas
  - [ ] SSE event envelope format with sequence numbers, `turn_id`, `operation_id`, `session_id`
  - [ ] Error response format standardized (code, message, details)
  - [ ] Pagination on all list endpoints
  - [ ] Auth model defined (none for localhost, token for remote)
- **Gate:** ✋ Human review of API design before implementation

### Step 2.2: Implement the Server

- [ ] **Status:** Not started
- **Agent:** builder
- **Description:** Implement `internal/server.Server` wrapping `service.Service` over HTTP with SSE. Use Go stdlib `net/http` (Go 1.22+ method routing). Support multiple concurrent SSE clients via fan-out broadcast. Embeddable: `server.New(svc, opts...) *Server` with `Start(addr string) error` and `Shutdown(ctx context.Context) error`. Server must `Flush()` after every SSE event write.
- **Blocking concern B4:** Implement `GET /api/v1/sessions/:id` returning full session detail for reconnection hydration.
- **Acceptance criteria:**
  - [ ] All REST endpoints implemented
  - [ ] SSE event stream delivers all service events to all connected clients
  - [ ] SSE events include sequence numbers for ordering
  - [ ] 15-second heartbeat on SSE stream
  - [ ] Server starts, serves, and shuts down cleanly
  - [ ] Multiple clients can connect simultaneously
  - [ ] Health endpoint returns server status
  - [ ] `Flush()` after every SSE write

### Step 2.3: Implement the Remote Client

- [ ] **Status:** Not started
- **Agent:** builder
- **Description:** Implement `internal/client.RemoteClient` satisfying `service.Service` via HTTP calls to the server + SSE for the event stream. Drop-in replacement for `LocalService`. On SSE reconnect: fetch full state via REST endpoints (`ListActiveSessions`, `ListJobs`, `OperatorStatus`), then re-subscribe to SSE for future events.
- **Acceptance criteria:**
  - [ ] `RemoteClient` implements full `Service` interface
  - [ ] All operations work over HTTP
  - [ ] Event stream works over SSE with auto-reconnection
  - [ ] On reconnect, client fetches full state via REST then re-subscribes to SSE
  - [ ] Connection errors surfaced as typed errors
  - [ ] TUI can use `RemoteClient` as drop-in for `LocalService`

### Step 2.4: Write Server Integration Tests

- [ ] **Status:** Not started
- **Agent:** test-writer
- **Description:** Integration tests: start server with `LocalService`, connect `RemoteClient`, verify all operations end-to-end. Use `httptest.Server`.
- **Acceptance criteria:**
  - [ ] Integration tests for all API endpoints
  - [ ] SSE event delivery tests (including multi-client fan-out)
  - [ ] Reconnect state hydration tests (full state fetch + re-subscribe)
  - [ ] Concurrent client tests
  - [ ] Graceful shutdown tests

### Phase 2 Review Checkpoint

- [ ] **Gate:** ✋ Review integration test coverage and server correctness

---

## Phase 3: Wire Up the Modes

**Goal:** Add CLI subcommands to run in embedded mode (current behavior), server mode (standalone), or client mode (TUI connecting to remote server). All modes use the same `toasters` binary.

**Estimated Effort:** 1–2 days

### Step 3.1: Add Server and Client CLI Commands

- [ ] **Status:** Not started
- **Agent:** builder
- **Description:** Extend Cobra CLI on the existing `toasters` binary:
  - `toasters` (default) — embedded mode: `LocalService` + TUI in-process (current behavior, unchanged)
  - `toasters serve` — server mode: `LocalService` + HTTP/SSE server, no TUI
  - `toasters --server <addr>` — client mode: `RemoteClient` + TUI connecting to remote server
- **Acceptance criteria:**
  - [ ] `toasters` works exactly as before
  - [ ] `toasters serve` starts headless server accepting connections
  - [ ] `toasters --server localhost:8080` connects to running server and shows TUI
  - [ ] Graceful shutdown in all modes (SIGINT/SIGTERM)
  - [ ] Jobs survive TUI disconnect in server mode

### Step 3.2: Add CLI Subcommands

- [ ] **Status:** Not started
- **Agent:** builder
- **Description:** Add non-TUI subcommands to the same `toasters` binary for scripting and quick queries. All accept `--server <addr>` to target a remote server.
  - `toasters send "message"` — send message, stream operator response to stdout
  - `toasters jobs` — list active jobs
  - `toasters teams` — list configured teams
  - `toasters status` — show server health
- **Acceptance criteria:**
  - [ ] `toasters send "message"` streams operator response to stdout via SSE
  - [ ] `toasters jobs` lists active jobs
  - [ ] `toasters teams` lists configured teams
  - [ ] `toasters status` shows server health

### Phase 3 Review Checkpoint

- [ ] **Gate:** ✋ Review mode switching correctness; verify no regressions

---

## Phase 4: Hardening and Polish

**Goal:** Security, reliability, and operational polish for server mode.

**Estimated Effort:** 2–3 days

### Step 4.1: Add Authentication

- [ ] **Status:** Not started
- **Agent:** security-auditor + builder
- **Description:** Token-based auth. Auto-generate token on first `toasters serve`, write to `~/.config/toasters/server.token` (0600). TUI/CLI client auto-discovers token. Server validates on every request including SSE. `--no-auth` for development.
- **Acceptance criteria:**
  - [ ] Server rejects unauthenticated requests with 401
  - [ ] Token auto-generated on first run
  - [ ] TUI/CLI client auto-discovers token
  - [ ] `--no-auth` flag disables auth
  - [ ] SSE connection also authenticated

### Step 4.2: Connection Resilience

- [ ] **Status:** Not started
- **Agent:** builder
- **Description:** Auto-reconnection with exponential backoff (cap at 30s), full state fetch on reconnect (REST endpoints → re-subscribe SSE), graceful degradation when server unreachable (TUI shows "disconnected" status in sidebar), queued messages sent after reconnection.
- **Acceptance criteria:**
  - [ ] TUI reconnects automatically after server restart
  - [ ] On reconnect, full state is fetched via REST before re-subscribing to SSE
  - [ ] TUI shows connection status (connected/reconnecting/disconnected)
  - [ ] Queued messages sent after reconnection
  - [ ] Exponential backoff caps at 30 seconds

### Step 4.3: Security Audit

- [ ] **Status:** Not started
- **Agent:** security-auditor
- **Description:** Audit server for SSRF, path traversal, rate limiting, input validation. Verify existing SSRF protections (`internal/httputil`) apply to server-initiated requests.
- **Acceptance criteria:**
  - [ ] No SSRF vectors in API
  - [ ] No path traversal in file operations
  - [ ] All inputs validated and bounded
  - [ ] Findings documented and fixed

### Phase 4 Review Checkpoint

- [ ] **Gate:** ✋ Full security audit review

---

## Review Checkpoints Summary

| After | Reviewer Focus |
|-------|---------------|
| Step 1.1 | Service interface completeness, use-case-level design, event model |
| Step 1.3 | TUI decoupling is complete; no residual direct dependencies |
| Step 2.1 | API design review before implementation |
| Phase 2 | Integration test coverage and server correctness |
| Phase 3 | Mode switching works correctly; no regressions |
| Phase 4 | Full security audit |

---

## Dependency Graph

```
Phase 1: Service Extraction
  1.1 Service Interface
    → 1.2 In-Process Implementation
      → 1.3 Rewire TUI
        → 1.4 Service Tests

Phase 2: Server (depends on Phase 1)
  2.1 API Design
    → 2.2 Server Implementation
    → 2.3 Remote Client
      → 2.4 Integration Tests

Phase 3: Mode Wiring (depends on Phase 2)
  3.1 CLI Commands (serve, --server)
    → 3.2 CLI Subcommands (send, jobs, teams, status)

Phase 4: Hardening (depends on Phase 3)
  4.1 Authentication
  4.2 Connection Resilience
  4.3 Security Audit
```
