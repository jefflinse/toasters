# Client/Server Architecture Split

**Created:** 2026-02-28
**Status:** Planning
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
| **State on reconnect** | Dual strategy: (1) `Last-Event-ID` replay for short disconnects via server-side ring buffer (~1000 events). (2) If gap too large, client falls back to full state fetch via REST endpoints (`ListActiveSessions`, `ListJobs`, `OperatorStatus`), then subscribes for future events. |
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

- [ ] **Status:** Not started
- **Agent:** api-designer
- **Description:** Design `internal/service` with a composed `Service` interface (see Proposed Service Interface Structure above). Must capture every operation the TUI currently performs against the store, LLM, or filesystem. Define service-level DTO types (not raw `db.*` types). Define the unified event stream contract that replaces operator callbacks, session subscriptions, and SQLite polling.
- **Key files:** `internal/service/service.go`, `internal/service/types.go`, `internal/service/events.go`
- **Blocking concerns to address:**
  - **B1:** Interface must be use-case-level, not db.Store-level
  - **B2:** Event subscription must unify all 3 current mechanisms into `Events().Subscribe(ctx)`
  - **B3:** `SendMessage` must return a `turnID` for response correlation
- **Acceptance criteria:**
  - [ ] Composed `Service` interface with domain-specific sub-interfaces and godoc comments
  - [ ] Service-level DTO types defined (not raw DB types)
  - [ ] Unified event stream types with sequence numbers, typed discriminator, and correlation IDs
  - [ ] `SendMessage` returns `turnID`; async operations return `operationID`
  - [ ] No implementation yet — interface only
- **Risk:** Getting the interface wrong means rework in every subsequent step. Must study every `m.store.*`, `m.llmClient.*`, filesystem operation, and `tea.Msg` type in the TUI.
- **Gate:** ✋ Human review of interface design before proceeding

### Step 1.2: Implement the In-Process Service

- [ ] **Status:** Not started
- **Agent:** builder
- **Description:** Implement `internal/service.LocalService` satisfying the `Service` interface by delegating to existing components (`db.Store`, `provider.Provider`, `runtime.Runtime`, `operator.Operator`, etc.). Mechanical extraction of business logic from TUI methods into service methods.
- **Blocking concern B5:** Filesystem operations in modal handlers (~400 lines of team promotion, CRUD) are the hardest extraction. Move verbatim, don't refactor.
- **Key extractions:**
  - `reloadSkillsForModal()` → `service.Definitions().ListSkills()`
  - `reloadAgentsForModal()` → `service.Definitions().ListAgents()`
  - `reloadTeamsForModal()` → `service.Definitions().ListTeams()`
  - `createSkillFile()` / `createAgentFile()` → `service.Definitions().CreateSkill()` / `.CreateAgent()`
  - `promoteAutoTeam()` → `service.Definitions().PromoteTeam()`
  - `generateSkillCmd()` / `generateAgentCmd()` / `generateTeamCmd()` → `service.Definitions().Generate*()`
  - `maybeAutoDetectCoordinator()` → `service.Definitions().DetectCoordinator()`
  - `fetchModels()` → `service.System().ListModels()`
  - `progressPollCmd()` → replaced by `service.Events().Subscribe()` pushing `progress.update` events
  - Operator callbacks (`onText`, `onEvent`, `onTurnDone`) → unified into `service.Events().Subscribe()`
  - Session subscriptions (`session.Subscribe()`) → unified into `service.Events().Subscribe()`
- **Acceptance criteria:**
  - [ ] `LocalService` passes all existing tests
  - [ ] Every `Service` interface method has a working implementation
  - [ ] No TUI imports in the service package
  - [ ] Event stream unifies operator callbacks, session subscriptions, and progress updates
- **Risk:** Team promotion logic (~400 lines) is the most complex extraction. Move verbatim, don't refactor.

### Step 1.3: Rewire TUI to Use the Service

- [ ] **Status:** Not started
- **Agent:** builder
- **Description:** Modify the TUI to use `service.Service` instead of directly accessing `db.Store`, `provider.Provider`, `runtime.Runtime`, and the filesystem. The `Model` struct should hold a `service.Service` instead of individual component references. The event stream subscription replaces the current `p.Send(tea.Msg)` callback wiring — a goroutine consumes service events and translates them to `tea.Msg` values.
- **Blocking concern B6:** `openInEditor()` stays in TUI but must be disabled when using a remote service. Show a toast explaining why.
- **Acceptance criteria:**
  - [ ] TUI no longer imports `db`, `provider`, `runtime`, `compose`, `loader`, `mcp`, `config`, `agentfmt`, or `bootstrap` directly
  - [ ] `cmd/root.go` creates a `LocalService` and passes it to the TUI
  - [ ] All existing functionality works identically
  - [ ] All existing tests pass
  - [ ] `Model` struct no longer holds `store`, `runtime`, `llmClient`, `mcpManager`, or `operator` fields
  - [ ] `openInEditor()` disabled with toast when service is remote
- **Risk:** Largest single step. The 14 internal package imports each represent a coupling surface to abstract.

### Step 1.4: Write Tests for the Service Layer

- [ ] **Status:** Not started
- **Agent:** test-writer
- **Description:** Unit tests for `internal/service.LocalService` covering all methods. Mock `provider.Provider` for LLM generation tests. Test event stream lifecycle (subscribe, receive events, cancel context, verify cleanup).
- **Acceptance criteria:**
  - [ ] Test coverage for all service methods
  - [ ] Tests for event stream delivery and cleanup
  - [ ] Tests for error cases
  - [ ] `go test ./internal/service/...` passes

### Phase 1 Review Checkpoint

- [ ] **Gate:** ✋ Code review of TUI decoupling — verify no residual direct dependencies

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
- **Description:** Implement `internal/server.Server` wrapping `service.Service` over HTTP with SSE. Use Go stdlib `net/http` (Go 1.22+ method routing). Support multiple concurrent SSE clients via fan-out broadcast. Embeddable: `server.New(svc, opts...) *Server` with `Start(addr string) error` and `Shutdown(ctx context.Context) error`. Server must `Flush()` after every SSE event write. Buffer last ~1000 events in a ring buffer for `Last-Event-ID` replay on reconnect.
- **Blocking concern B4:** Implement `GET /api/v1/sessions/:id` returning full session detail for reconnection hydration.
- **Acceptance criteria:**
  - [ ] All REST endpoints implemented
  - [ ] SSE event stream delivers all service events to all connected clients
  - [ ] SSE events include sequence numbers; `Last-Event-ID` replay works
  - [ ] 15-second heartbeat on SSE stream
  - [ ] Server starts, serves, and shuts down cleanly
  - [ ] Multiple clients can connect simultaneously
  - [ ] Health endpoint returns server status
  - [ ] `Flush()` after every SSE write

### Step 2.3: Implement the Remote Client

- [ ] **Status:** Not started
- **Agent:** builder
- **Description:** Implement `internal/client.RemoteClient` satisfying `service.Service` via HTTP calls to the server + SSE for the event stream. Drop-in replacement for `LocalService`. On SSE reconnect: attempt `Last-Event-ID` replay first; if gap too large, fall back to full state fetch via REST endpoints then re-subscribe.
- **Acceptance criteria:**
  - [ ] `RemoteClient` implements full `Service` interface
  - [ ] All operations work over HTTP
  - [ ] Event stream works over SSE with auto-reconnection
  - [ ] Reconnect uses `Last-Event-ID` replay; falls back to full state fetch if gap too large
  - [ ] Connection errors surfaced as typed errors
  - [ ] TUI can use `RemoteClient` as drop-in for `LocalService`

### Step 2.4: Write Server Integration Tests

- [ ] **Status:** Not started
- **Agent:** test-writer
- **Description:** Integration tests: start server with `LocalService`, connect `RemoteClient`, verify all operations end-to-end. Use `httptest.Server`.
- **Acceptance criteria:**
  - [ ] Integration tests for all API endpoints
  - [ ] SSE event delivery tests (including multi-client fan-out)
  - [ ] `Last-Event-ID` replay tests
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
- **Description:** Auto-reconnection with exponential backoff (cap at 30s), event replay after reconnect via `Last-Event-ID`, graceful degradation when server unreachable (TUI shows "disconnected" status in sidebar), queued messages sent after reconnection.
- **Acceptance criteria:**
  - [ ] TUI reconnects automatically after server restart
  - [ ] No events lost during brief disconnects (server buffers ~1000 recent events)
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
