# Step 2.3 Plan: Remote Client Implementation

**Created:** 2026-03-02
**Status:** Complete (2026-03-02)
**Branch:** feat/client-server-split

---

## Objective

Build `internal/client/` — a `RemoteClient` implementing `service.Service` over HTTP+SSE, enabling the TUI to connect to a standalone Toasters server as a drop-in replacement for `LocalService`.

---

## Build Phase (Steps 1–7)

### Step 1: Wire Types & JSON Deserialization

- **Agent:** builder
- **File:** `internal/client/types.go`
- **Depends on:** —
- **Description:** Create JSON-tagged structs mirroring the server's wire types (snake_case). These are the client's own types — they must NOT import `internal/server`. Include:
  - All entity wire types: `wireJob`, `wireTask`, `wireProgressReport`, `wireJobDetail`, `wireSkill`, `wireAgent`, `wireTeam`, `wireTeamView`, `wireSessionSnapshot`, `wireSessionDetail`, `wireActivityItem`, `wireAgentSession`, `wireFeedEntry`, `wireProgressState`, `wireChatMessage`, `wireToolCall`, `wireToolCallResult`, `wireChatEntry`, `wireModelInfo`, `wireMCPToolInfo`, `wireMCPServerStatus`
  - Response envelope types: `paginatedResponse[T]` (or non-generic equivalent), `errorResponse`, `errorDetail`, `asyncResponse`, `turnResponse`, `healthResponse`, `operatorStatusResponse`
  - SSE event envelope: `sseEvent` with `Seq`, `Type`, `Timestamp`, `TurnID`, `SessionID`, `OperationID`, `Payload json.RawMessage`
  - All 19 SSE payload wire types (matching server's `wire*Payload` types)
  - Converter functions: `wireJobToService`, `wireTaskToService`, `wireSkillToService`, `wireAgentToService`, `wireTeamToService`, `wireTeamViewToService`, etc. — each converts a client wire type to the corresponding `service.*` type
  - A `parseSSEPayload(eventType string, raw json.RawMessage) (any, error)` function that deserializes the raw JSON payload into the correct `service.*Payload` type based on event type (the inverse of the server's `eventPayloadToWire`)
- **Acceptance criteria:**
  - [x] All wire types have JSON tags matching the server's wire types exactly (verified by inspection against `internal/server/types.go`)
  - [x] All converter functions produce correct `service.*` types (verified by unit tests in Step 8)
  - [x] `parseSSEPayload` handles all 19 event types plus nil payload for `definitions.reloaded`
  - [x] No imports from `internal/server`
- **Risk notes:**
  - The wire types must match the server's JSON output exactly — any mismatch causes silent data loss. The `omitempty` tags must match too.
  - `wireProgressState` has nested maps (`map[string][]wireTask`) which need careful deserialization.
  - The `Payload` field in `sseEvent` must be `json.RawMessage` (not `any`) so we can do two-pass deserialization: first unmarshal the envelope, then unmarshal the payload based on `Type`.

### Step 2: HTTP Transport Layer

- **Agent:** builder
- **File:** `internal/client/http.go`
- **Depends on:** Step 1
- **Description:** Create the core HTTP transport:
  - A private `httpClient` struct wrapping `*http.Client` and a base URL
  - Methods: `get(ctx, path) (*http.Response, error)`, `post(ctx, path, body) (*http.Response, error)`, `put(ctx, path, body) (*http.Response, error)`, `delete(ctx, path) (*http.Response, error)`
  - A `decodeResponse[T](resp *http.Response) (T, error)` helper that checks status codes, reads the body, and either returns the decoded value or a typed error
  - Error handling: map HTTP status codes to typed errors:
    - 404 → `service.ErrNotFound`
    - 409 → `ErrConflict` (new)
    - 422 → `ErrUnprocessable` (new)
    - 429 → `ErrRateLimited` (new)
    - 500 → `ErrServerError` (new)
    - 503 → `ErrServiceUnavailable` (new)
    - Connection refused / timeout → `ErrConnectionFailed` (new)
  - Parse the `errorResponse` body for non-2xx responses and include the server's error message in the Go error
  - Respect `context.Context` cancellation on all requests
  - Set `Content-Type: application/json` on POST/PUT requests
  - Set `Accept: application/json` on all requests
- **Acceptance criteria:**
  - [x] All HTTP methods correctly construct requests with the base URL
  - [x] Non-2xx responses produce typed errors with the server's error message
  - [x] Connection failures produce `ErrConnectionFailed`
  - [x] Context cancellation is respected
- **Risk notes:**
  - Must handle the case where the server returns a non-JSON body (e.g., nginx error page) gracefully
  - The `decodeResponse` helper should handle both 204 No Content (no body) and responses with bodies

### Step 3: RemoteClient Struct & Service Interface Wiring

- **Agent:** builder
- **File:** `internal/client/client.go`
- **Depends on:** Step 2
- **Description:** Create the main client struct:
  - `RemoteClient` struct holding: `httpClient`, base URL, `context.Context` + `cancel` for lifecycle, SSE reconnect state
  - Constructor: `New(baseURL string, opts ...Option) *RemoteClient` with options for custom `http.Client`, logger
  - `Close()` method that cancels the context and cleans up
  - Sub-interface wrapper types (same pattern as `LocalService`): `remoteOperatorService`, `remoteDefinitionService`, `remoteJobService`, `remoteSessionService`, `remoteEventService`, `remoteSystemService`
  - Top-level methods: `Operator()`, `Definitions()`, `Jobs()`, `Sessions()`, `Events()`, `System()` — each returns the corresponding wrapper
  - Compile-time assertion: `var _ service.Service = (*RemoteClient)(nil)`
- **Acceptance criteria:**
  - [x] `RemoteClient` satisfies `service.Service` at compile time
  - [x] All 6 sub-interface accessors return non-nil values
  - [x] `Close()` cancels the internal context
- **Risk notes:**
  - The sub-interface wrappers for `JobService` and `SessionService` must use separate types (same as `LocalService`) because both have `List`, `Get`, `Cancel` methods

### Step 4: REST Methods — Operator, System, Jobs, Sessions

- **Agent:** builder
- **File:** `internal/client/client.go` (methods on wrapper types)
- **Depends on:** Steps 1, 2, 3
- **Description:** Implement all REST-backed methods for the simpler sub-interfaces:
  - **OperatorService:** `SendMessage` (POST /operator/messages → 202 TurnResponse), `RespondToPrompt` (POST /operator/prompts/{id}/respond → 204), `Status` (GET /operator/status → OperatorStatusResponse), `History` (GET /operator/history → PaginatedResponse[wireChatEntry]), `RespondToBlocker` (POST /operator/blockers/{jobId}/{taskId}/respond → 204)
  - **SystemService:** `Health` (GET /health → HealthResponse, convert `uptime_seconds` back to `time.Duration`), `ListModels` (GET /models → PaginatedResponse[wireModelInfo]), `ListMCPServers` (GET /mcp/servers → PaginatedResponse[wireMCPServerStatus]), `GetProgressState` (GET /progress → wireProgressState)
  - **JobService:** `List` (GET /jobs?status=&type=&limit=&offset= → PaginatedResponse[wireJob]), `ListAll` (GET /jobs?all=true → PaginatedResponse[wireJob]), `Get` (GET /jobs/{id} → wireJobDetail), `Cancel` (POST /jobs/{id}/cancel → 204)
  - **SessionService:** `List` (GET /sessions → PaginatedResponse[wireSessionSnapshot]), `Get` (GET /sessions/{id} → wireSessionDetail), `Cancel` (POST /sessions/{id}/cancel → 204)
  - Each method: construct request → call HTTP transport → decode wire type → convert to service type → return
- **Acceptance criteria:**
  - [x] Every method constructs the correct URL path with path parameters and query parameters
  - [x] Every method correctly deserializes the response and converts to the service type
  - [x] 204 No Content responses are handled (no body to decode)
  - [x] Error responses are mapped to typed errors
  - [x] `JobListFilter` fields are correctly mapped to query parameters (nil fields omitted)
- **Risk notes:**
  - `HealthResponse.UptimeSeconds` is a float64 that must be converted back to `time.Duration` — use `time.Duration(seconds * float64(time.Second))`
  - `History` returns `PaginatedResponse[wireChatEntry]` — extract `.Items` and convert each

### Step 5: REST Methods — Definitions (Skills, Agents, Teams)

- **Agent:** builder
- **File:** `internal/client/client.go` (methods on wrapper types)
- **Depends on:** Steps 1, 2, 3
- **Description:** Implement all 20 `DefinitionService` methods:
  - **Skills:** `ListSkills` (GET /skills), `GetSkill` (GET /skills/{id}), `CreateSkill` (POST /skills), `DeleteSkill` (DELETE /skills/{id}), `GenerateSkill` (POST /skills/generate → 202 AsyncResponse)
  - **Agents:** `ListAgents` (GET /agents), `GetAgent` (GET /agents/{id}), `CreateAgent` (POST /agents), `DeleteAgent` (DELETE /agents/{id}), `AddSkillToAgent` (POST /agents/{id}/skills), `GenerateAgent` (POST /agents/generate → 202)
  - **Teams:** `ListTeams` (GET /teams), `GetTeam` (GET /teams/{id}), `CreateTeam` (POST /teams), `DeleteTeam` (DELETE /teams/{id}), `AddAgentToTeam` (POST /teams/{id}/agents), `SetCoordinator` (PUT /teams/{id}/coordinator), `PromoteTeam` (POST /teams/{id}/promote → 202), `GenerateTeam` (POST /teams/generate → 202), `DetectCoordinator` (POST /teams/{id}/detect-coordinator → 202)
  - Async methods return `(operationID string, err error)` — extract from `AsyncResponse.OperationID`
  - List methods extract `.Items` from `PaginatedResponse` and convert each wire type
- **Acceptance criteria:**
  - [x] All 20 DefinitionService methods implemented
  - [x] Create methods send correct JSON request bodies
  - [x] Async methods (Generate*, Promote, DetectCoordinator) return the operation ID from the 202 response
  - [x] Delete methods handle 204 correctly
  - [x] List methods handle pagination response format
- **Risk notes:**
  - `wireTeamView` has nested `wireTeam`, optional `*wireAgent` coordinator, and `[]wireAgent` workers — the converter must handle nil coordinator

### Step 6: SSE Event Stream & Subscribe()

- **Agent:** builder
- **File:** `internal/client/events.go`
- **Depends on:** Steps 1, 3
- **Description:** Implement `EventService.Subscribe()`:
  - `Subscribe(ctx context.Context) <-chan service.Event` — returns a channel that delivers events
  - Connects to `GET /api/v1/events` with `Accept: text/event-stream`
  - Uses `sse.NewReader(resp.Body)` to parse the SSE stream
  - For each SSE event: parse the JSON `sseEvent` envelope from `Data`, then call `parseSSEPayload(type, payload)` to get the typed payload, construct a `service.Event` with all correlation IDs, and send to the channel
  - The goroutine reading from SSE exits when `ctx` is cancelled or the connection drops
  - On connection drop: log a warning, close the current channel, and trigger reconnect (Step 7)
  - Channel buffer size: 256 (matches LocalService subscriber buffer)
  - Heartbeat events are converted to `service.Event{Type: EventTypeHeartbeat, Payload: HeartbeatPayload{...}}` and sent through the channel (the TUI's event consumer already ignores them)
- **Acceptance criteria:**
  - [x] `Subscribe()` returns a channel that delivers `service.Event` values
  - [x] Events have correct `Type`, `Seq`, `Timestamp`, `TurnID`, `SessionID`, `OperationID`, and typed `Payload`
  - [x] The channel is closed when ctx is cancelled
  - [x] Connection errors cause the goroutine to exit cleanly (no panic, no goroutine leak)
  - [x] All 19 event types are correctly deserialized
- **Risk notes:**
  - The existing `sse.Reader` returns the `event:` type and `data:` payload. The `data:` is the full JSON `SSEEvent` envelope (which also contains the type). The client should use the envelope's `type` field for payload dispatch (not the SSE `event:` line) to stay consistent.
  - The `sse.Reader` currently ignores `id:` lines — this is fine since we get `seq` from the JSON envelope.
  - Must handle the case where `resp.Body` is closed by the server (graceful shutdown) vs. network error.

### Step 7: Auto-Reconnection with Exponential Backoff

- **Agent:** builder
- **File:** `internal/client/events.go` (extends Step 6)
- **Depends on:** Steps 4, 6
- **Description:** Add reconnection logic to the SSE event stream:
  - When the SSE connection drops (reader returns false, or network error), start reconnect loop
  - Exponential backoff: 1s, 2s, 4s, 8s, 16s, 30s (cap at 30s), with 10% jitter
  - On each reconnect attempt:
    1. Execute the reconnect protocol (API spec Section 16): fetch operator status, history, progress state, and active session details in parallel
    2. Emit a synthetic `progress.update` event with the fetched state so the TUI rebuilds
    3. Re-subscribe to SSE
  - If `ctx` is cancelled during reconnect, stop immediately
  - Log reconnect attempts and successes at `slog.Info` level
  - The `Subscribe()` method should return a single channel that survives reconnects — the goroutine replaces the underlying SSE connection but keeps sending to the same channel
  - Add a `connected` state field (atomic bool) that `Health()` can check to return an error when disconnected
- **Acceptance criteria:**
  - [x] After SSE disconnect, the client automatically reconnects with backoff
  - [x] On successful reconnect, a synthetic `progress.update` event is emitted
  - [x] The channel returned by `Subscribe()` continues to deliver events after reconnect (no new channel needed)
  - [x] Backoff caps at 30s with jitter
  - [x] Context cancellation stops reconnect attempts
  - [x] The `connected` state is updated on connect/disconnect
- **Risk notes:**
  - The reconnect protocol fetches 4 endpoints in parallel — must use `errgroup` or `sync.WaitGroup` with proper error handling
  - There's a brief window between REST fetches and SSE subscription where events may be missed — this is acceptable per the API spec (the next `progress.update` will carry full state)
  - Must not leak goroutines on rapid connect/disconnect cycles
  - The synthetic `progress.update` event needs a valid `service.ProgressState` assembled from the REST responses — this requires fetching session details for each live session in the progress state

---

## Test Phase (Steps 8–11)

### Step 8: Unit Tests — Wire Type Round-Trip & Deserialization

- **Agent:** test-writer
- **File:** `internal/client/types_test.go`
- **Depends on:** Step 1
- **Description:** Tests for:
  - Round-trip tests: for each entity type, create a `service.*` value, convert to wire (using the server's converter as reference JSON), marshal to JSON, unmarshal into client wire type, convert back to `service.*`, and verify equality
  - `parseSSEPayload` tests: for each of the 19 event types, provide a JSON payload string and verify the returned `service.*Payload` type and field values
  - Edge cases: nil coordinator in TeamView, empty slices vs nil, zero-value times, nil Metadata, optional fields with omitempty
  - `wireProgressState` deserialization with nested maps
- **Acceptance criteria:**
  - [x] At least one round-trip test per entity type (Job, Task, Skill, Agent, Team, TeamView, SessionSnapshot, SessionDetail, ChatEntry, ProgressReport, AgentSession, FeedEntry, ModelInfo, MCPServerStatus, ProgressState)
  - [x] At least one test per SSE event type for `parseSSEPayload`
  - [x] All tests pass with `go test -race`
- **Risk notes:**
  - The round-trip tests need reference JSON that matches what the server produces. The test should marshal the wire type and verify the JSON structure rather than comparing against hardcoded strings (which are brittle).

### Step 9: Unit Tests — HTTP Transport & Error Mapping

- **Agent:** test-writer
- **File:** `internal/client/http_test.go`
- **Depends on:** Step 2
- **Description:** Tests for:
  - HTTP transport: use `httptest.NewServer` to verify correct URL construction, request headers, body encoding
  - Error mapping: verify each HTTP status code maps to the correct typed error
  - 204 No Content handling
  - Connection refused → `ErrConnectionFailed`
  - Context cancellation → context error
  - Non-JSON error response body handling
  - Request body encoding for POST/PUT methods
- **Acceptance criteria:**
  - [x] Tests cover all error code mappings (404, 409, 422, 429, 500, 503)
  - [x] Tests verify correct URL path construction
  - [x] Tests verify context cancellation
  - [x] All tests pass with `go test -race`
- **Risk notes:** None significant

### Step 10: Integration Tests — RemoteClient Against Real Server

- **Agent:** test-writer
- **File:** `internal/client/client_test.go`
- **Depends on:** Steps 3, 4, 5, 6
- **Description:** Integration tests that:
  - Start a real `server.Server` wrapping a mock `service.Service` (or a minimal stub)
  - Create a `RemoteClient` pointing at the test server
  - Exercise each sub-interface method and verify correct behavior
  - Test SSE event delivery: emit events from the mock service, verify they arrive on the client's Subscribe channel with correct types and payloads
  - Test error propagation: mock service returns `service.ErrNotFound`, verify client receives it
  - Test 202 async responses: verify operation IDs are returned correctly
  - Note: These tests DO import `internal/server` (test-only dependency) — this is acceptable since tests are not compiled into the binary
- **Acceptance criteria:**
  - [x] At least one integration test per sub-interface (6 sub-interfaces)
  - [x] SSE event delivery test covers at least 3 event types
  - [x] Error propagation test for ErrNotFound
  - [x] All tests pass with `go test -race`
  - [x] Tests use `t.Cleanup` to shut down server and client
- **Risk notes:**
  - The mock service needs to implement all 6 sub-interfaces — consider a `mockService` struct that embeds stubs and only overrides the methods under test
  - SSE tests need to wait for event delivery — use `select` with timeout to avoid hanging tests

### Step 11: Reconnection Tests

- **Agent:** test-writer
- **File:** `internal/client/reconnect_test.go`
- **Depends on:** Step 7
- **Description:** Tests for:
  - SSE disconnect triggers reconnect with backoff
  - Successful reconnect emits synthetic `progress.update` event
  - Context cancellation stops reconnect loop
  - Backoff timing (verify delays increase, cap at 30s)
  - Channel survives reconnect (same channel delivers events before and after)
  - Multiple rapid disconnects don't leak goroutines
- **Acceptance criteria:**
  - [x] Reconnect behavior verified with controlled server shutdown/restart
  - [x] Backoff timing verified (may use a clock interface or short durations for testing)
  - [x] No goroutine leaks (use `goleak` or manual verification)
  - [x] All tests pass with `go test -race`
- **Risk notes:**
  - Reconnect tests are inherently timing-sensitive — use short backoff durations in tests (e.g., 10ms base) and generous timeouts
  - May need a test helper that starts/stops the server to simulate disconnects

---

## Review Phase (Step 12)

### Step 12: Code Review

- **Agents:** code-reviewer, concurrency-reviewer, security-auditor
- **Depends on:** Steps 1–11
- **Description:** Review the complete `internal/client/` package for:
  - Correctness: all service methods correctly implemented, wire type conversions complete
  - Error handling: all error paths covered, no swallowed errors
  - Concurrency: no data races, proper mutex usage, goroutine lifecycle management
  - API contract: wire types match server output exactly
  - Code quality: follows project conventions, proper doc comments, no dead code
  - Dependency boundaries: no import of `internal/server` in non-test files
- **Acceptance criteria:**
  - [x] No blocking findings (3 blocking findings found and fixed during review)
  - [x] All suggestions addressed or documented as deferred (11 deferred: S1-S3, S5, S7-S13)

### Review Checkpoints

- **After Step 1:** Verify wire types match server types exactly (builder self-check against `internal/server/types.go`)
- **After Step 6:** Verify SSE event deserialization produces identical `service.Event` values to what `LocalService` emits (test-writer validates in Step 8)
- **After Step 7:** Concurrency reviewer should check reconnect goroutine lifecycle for leaks and races
- **After Step 11:** Security auditor should verify no sensitive data leaks in error messages or reconnect logging
- **After Step 12:** Final code review before merge

---

## Key Design Decisions

| Decision | Resolution |
|----------|-----------|
| **Package location** | `internal/client/` (new package) |
| **Wire type reuse** | Client defines its own wire types — no import of `internal/server` |
| **HTTP client** | `net/http` stdlib, no external library |
| **SSE parsing** | Reuses existing `internal/sse.Reader` |
| **Reconnect channel** | Single channel survives reconnects; goroutine replaces underlying SSE connection |
| **Typed errors** | `ErrConnectionFailed`, `ErrConflict`, `ErrRateLimited`, `ErrServerError`, `ErrServiceUnavailable` |
| **Channel buffer** | 256 (matches LocalService subscriber buffer) |
| **Backoff** | 1s → 30s cap, 10% jitter |

---

## Risks

| Risk | Mitigation |
|------|-----------|
| Wire type mismatch between client and server → silent data loss | Round-trip tests in Step 8 |
| Reconnect goroutine leaks on rapid connect/disconnect cycles | Reconnect tests in Step 11 |
| Timing-sensitive reconnect tests | Short backoff durations in tests (e.g., 10ms base) |

---

## Out of Scope

- `toasters serve` / `toasters --server` commands (Phase 3)
- Token auth (Phase 4), TLS (Phase 4)
- `Last-Event-ID` replay (future optimization)
- Modifying `cmd/root.go` or `event_consumer.go` (Phase 3)
- Connection health monitoring UI (future)
