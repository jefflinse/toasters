# Client/Server Architecture Split

**Created:** 2026-02-28
**Status:** Planning
**Effort Estimate:** 9–15 days across 4 phases

---

## Objective

Split the Toasters monolithic TUI application into a client/server architecture where the orchestration engine (operator, runtime, store, MCP, loader, compose, providers) runs as a long-running server process and the TUI becomes a thin rendering client. The server must be embeddable (in-process or standalone), and the application must remain fully functional at every intermediate step.

---

## Design Constraints

- The server is embeddable — it can run in-process (current mode) OR as a standalone process
- REST API for operations + SSE for server-push event stream (no gRPC, no WebSocket)
- The TUI remains the primary client
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
- Web UI client
- Remote MCP server management
- Distributed agent runtime
- API versioning beyond v1
- Database migration from SQLite
- Refactoring the operator event loop
- Breaking changes to definition file format
- Conversation persistence (future feature — not required for the split)

---

## Phase 1: Extract Business Logic from TUI into Service Layer

**Goal:** Move all non-rendering logic out of the TUI package into a new `internal/service` package. This creates the seam where the network boundary will eventually be inserted. No networking yet.

**Estimated Effort:** 3–5 days

### Step 1.1: Create the Service Interface

- [ ] **Status:** Not started
- **Agent:** api-designer
- **Description:** Design `internal/service` with a `Service` interface capturing every operation the TUI currently performs against the store, LLM, or filesystem. Organized by domain (definitions, jobs, generation, system). Define request/response types and event stream contract.
- **Key files:** `internal/service/service.go`, `internal/service/types.go`, `internal/service/events.go`
- **Acceptance criteria:**
  - [ ] `Service` interface defines all methods with godoc comments
  - [ ] Request/response types defined (not raw DB types where they leak implementation)
  - [ ] Event stream types defined (replacing `tea.Msg` types that carry server-side data)
  - [ ] No implementation yet — interface only
- **Risk:** Getting the interface wrong means rework in every subsequent step. Must study every `m.store.*`, `m.llmClient.*`, filesystem operation, and `tea.Msg` type in the TUI.
- **Gate:** ✋ Human review of interface design before proceeding

### Step 1.2: Implement the In-Process Service

- [ ] **Status:** Not started
- **Agent:** builder
- **Description:** Implement `internal/service.LocalService` satisfying the `Service` interface by delegating to existing components (`db.Store`, `provider.Provider`, `runtime.Runtime`, `operator.Operator`, etc.). Mechanical extraction of business logic from TUI methods into service methods.
- **Key extractions:**
  - `reloadSkillsForModal()` → `service.ListSkills()`
  - `reloadAgentsForModal()` → `service.ListAgents()`
  - `reloadTeamsForModal()` → `service.ListTeams()`
  - `createSkillFile()` / `createAgentFile()` → `service.CreateSkill()` / `service.CreateAgent()`
  - `promoteAutoTeam()` → `service.PromoteTeam()`
  - `generateSkillCmd()` / `generateAgentCmd()` / `generateTeamCmd()` → `service.Generate*()`
  - `maybeAutoDetectCoordinator()` → `service.AutoDetectCoordinator()`
  - `fetchModels()` → `service.ListModels()`
  - `progressPollCmd()` → `service.PollProgress()`
  - Event stream: `service.Subscribe()` returns channel of service events
- **Acceptance criteria:**
  - [ ] `LocalService` passes all existing tests
  - [ ] Every `Service` interface method has a working implementation
  - [ ] No TUI imports in the service package
- **Risk:** Team promotion logic (~400 lines) is the most complex extraction. Move verbatim, don't refactor.

### Step 1.3: Rewire TUI to Use the Service

- [ ] **Status:** Not started
- **Agent:** builder
- **Description:** Modify the TUI to use `service.Service` instead of directly accessing `db.Store`, `provider.Provider`, `runtime.Runtime`, and the filesystem. The `Model` struct should hold a `service.Service` instead of individual component references.
- **Acceptance criteria:**
  - [ ] TUI no longer imports `db`, `provider`, `runtime`, `compose`, `loader`, `mcp`, `config`, `agentfmt`, or `bootstrap` directly
  - [ ] `cmd/root.go` creates a `LocalService` and passes it to the TUI
  - [ ] All existing functionality works identically
  - [ ] All existing tests pass
  - [ ] `Model` struct no longer holds `store`, `runtime`, `llmClient`, `mcpManager`, or `operator` fields
- **Risk:** Largest single step. `openInEditor()` stays in TUI (it's a terminal concern, not a service operation).

### Step 1.4: Write Tests for the Service Layer

- [ ] **Status:** Not started
- **Agent:** test-writer
- **Description:** Unit tests for `internal/service.LocalService` covering all methods. Mock `provider.Provider` for LLM generation tests. Test event stream lifecycle.
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
- **Description:** Design REST API mapping 1:1 to the `Service` interface. REST for all operations, SSE for the event stream (operator text tokens, session events, progress updates, definition reloads).
- **Key endpoints:**
  - `GET/POST/DELETE /api/v1/skills`, `/agents`, `/teams`
  - `POST /api/v1/teams/:id/promote`, `/coordinator`, `/agents`
  - `POST /api/v1/generate/skill`, `/agent`, `/team`
  - `GET /api/v1/jobs`, `GET /api/v1/jobs/:id`, `POST /api/v1/jobs/:id/cancel`
  - `GET /api/v1/progress`
  - `POST /api/v1/messages` — send user message to operator
  - `GET /api/v1/events` — SSE event stream
  - `GET /api/v1/models` — list available models
  - `GET /api/v1/health` — health check
- **Acceptance criteria:**
  - [ ] API spec with all endpoints, methods, request/response schemas
  - [ ] SSE event message types defined with `type` discriminator
  - [ ] Error response format standardized
  - [ ] Auth model defined (none for localhost, token for remote)
- **Gate:** ✋ Human review of API design before implementation

### Step 2.2: Implement the Server

- [ ] **Status:** Not started
- **Agent:** builder
- **Description:** Implement `internal/server.Server` wrapping `service.Service` over HTTP with SSE. Use Go stdlib `net/http`. Support multiple concurrent SSE clients. Embeddable: `server.New(svc, opts...) *Server` with `Start(addr string) error` and `Shutdown(ctx context.Context) error`.
- **Acceptance criteria:**
  - [ ] All REST endpoints implemented
  - [ ] SSE event stream delivers all service events to connected clients
  - [ ] Server starts, serves, and shuts down cleanly
  - [ ] Multiple clients can connect simultaneously
  - [ ] Health endpoint returns server status

### Step 2.3: Implement the Remote Client

- [ ] **Status:** Not started
- **Agent:** builder
- **Description:** Implement `internal/client.RemoteClient` satisfying `service.Service` via HTTP calls to the server + SSE for the event stream. Drop-in replacement for `LocalService`.
- **Acceptance criteria:**
  - [ ] `RemoteClient` implements full `Service` interface
  - [ ] All operations work over HTTP
  - [ ] Event stream works over SSE with auto-reconnection
  - [ ] Connection errors surfaced as typed errors
  - [ ] TUI can use `RemoteClient` as drop-in for `LocalService`

### Step 2.4: Write Server Integration Tests

- [ ] **Status:** Not started
- **Agent:** test-writer
- **Description:** Integration tests: start server with `LocalService`, connect `RemoteClient`, verify all operations end-to-end. Use `httptest.Server`.
- **Acceptance criteria:**
  - [ ] Integration tests for all API endpoints
  - [ ] SSE event delivery tests
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
- **Description:** Auto-reconnection with exponential backoff, event replay after reconnect (server buffers recent events in a ring buffer), graceful degradation when server unreachable (TUI shows "disconnected" status).
- **Acceptance criteria:**
  - [ ] TUI reconnects automatically after server restart
  - [ ] No events lost during brief disconnects (server buffers recent events)
  - [ ] TUI shows connection status
  - [ ] Queued messages sent after reconnection

### Step 4.3: Security Audit

- [ ] **Status:** Not started
- **Agent:** security-auditor
- **Description:** Audit server for SSRF, path traversal, SSE origin validation, rate limiting, input validation.
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
| Step 1.1 | Service interface completeness and ergonomics |
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
