# Toasters REST API Specification

**Version:** 1.0 (Phase 2)
**Status:** Draft
**Last Updated:** 2026-03-02

This document specifies the HTTP REST + SSE API for the Toasters server. It maps every method in the `internal/service.Service` interface to an HTTP endpoint. The server exposes this API; the `RemoteClient` consumes it as a drop-in replacement for `LocalService`.

---

## Table of Contents

1. [General Conventions](#1-general-conventions)
2. [Error Handling](#2-error-handling)
3. [Pagination](#3-pagination)
4. [Async Operations](#4-async-operations)
5. [SSE Event Stream](#5-sse-event-stream)
6. [Endpoints: Operator](#6-endpoints-operator)
7. [Endpoints: Skills](#7-endpoints-skills)
8. [Endpoints: Agents](#8-endpoints-agents)
9. [Endpoints: Teams](#9-endpoints-teams)
10. [Endpoints: Jobs](#10-endpoints-jobs)
11. [Endpoints: Sessions](#11-endpoints-sessions)
12. [Endpoints: System](#12-endpoints-system)
13. [Go Type Definitions](#13-go-type-definitions)
14. [Middleware Requirements](#14-middleware-requirements)
15. [Authentication](#15-authentication)
16. [Reconnect Protocol](#16-reconnect-protocol)

---

## 1. General Conventions

| Convention | Value |
|---|---|
| Base path | `/api/v1` |
| Content-Type (request) | `application/json` for all request bodies |
| Content-Type (response) | `application/json` for all response bodies |
| Content-Type (SSE) | `text/event-stream` |
| Character encoding | UTF-8 |
| Date format | RFC 3339 (`2026-03-02T12:00:00Z`) |
| ID format | Opaque strings (typically UUID v4 or slugified names) |
| HTTP method routing | Go 1.22+ `net/http.ServeMux` with `METHOD /path` patterns |
| Path parameters | `{name}` syntax (Go 1.22+ `http.Request.PathValue()`) |

### Naming Conventions

- Resource paths use **plural nouns**: `/skills`, `/agents`, `/teams`, `/jobs`, `/sessions`
- Action sub-resources use **verbs**: `/cancel`, `/promote`, `/respond`, `/generate`, `/detect-coordinator`
- Multi-word paths use **kebab-case**: `/detect-coordinator`, `/mcp/servers`
- Query parameters use **snake_case**: `?last_event_id=N`

### Request Rules

- All request bodies must be valid JSON. Malformed JSON returns `400 Bad Request`.
- Unknown JSON fields are silently ignored (forward compatibility).
- Empty request bodies are acceptable where no body is required.
- Path parameters are always required; missing parameters result in a `404` from the router.

### Response Rules

- All successful responses return JSON (except `204 No Content` and SSE streams).
- `201 Created` responses include a `Location` header with the URL of the created resource.
- `204 No Content` responses have no body.
- `202 Accepted` responses return an operation envelope (see [Async Operations](#4-async-operations)).
- Null JSON fields are omitted (`omitempty`) unless semantically meaningful.
- `SourcePath` fields are never included in JSON responses (tagged `json:"-"` in service DTOs).

---

## 2. Error Handling

All errors use a consistent envelope:

```json
{
  "error": {
    "code": "not_found",
    "message": "Job abc123 not found"
  }
}
```

### Error Codes

| Code | HTTP Status | When Used |
|---|---|---|
| `bad_request` | 400 | Malformed JSON, missing required fields, invalid field values |
| `not_found` | 404 | Resource does not exist (wraps `service.ErrNotFound`) |
| `conflict` | 409 | Resource already exists, invalid state transition (e.g., cancelling a completed job) |
| `unprocessable_entity` | 422 | Valid JSON but semantically invalid (e.g., system skill deletion, read-only team modification) |
| `too_many_requests` | 429 | Rate limit exceeded on async operations |
| `internal_error` | 500 | Unexpected server error |
| `service_unavailable` | 503 | Required component not configured (e.g., no LLM provider for generation) |

### Error Response Go Type

```go
type ErrorResponse struct {
    Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
    Code    string `json:"code"`
    Message string `json:"message"`
}
```

### Error Mapping from Service Layer

| Service Error | HTTP Status | Error Code |
|---|---|---|
| `service.ErrNotFound` (via `errors.Is`) | 404 | `not_found` |
| Validation failure (name empty, invalid ID) | 400 | `bad_request` |
| System/read-only resource modification | 422 | `unprocessable_entity` |
| Invalid state transition | 409 | `conflict` |
| Provider unreachable | 503 | `service_unavailable` |
| All other errors | 500 | `internal_error` |

### Internal Path Sanitization

Error messages MUST NOT leak absolute filesystem paths. The server handler layer strips or replaces internal paths before returning error messages to clients.

---

## 3. Pagination

All list endpoints support pagination via query parameters.

### Query Parameters

| Parameter | Type | Default | Max | Description |
|---|---|---|---|---|
| `limit` | int | 50 | 200 | Maximum number of items to return |
| `offset` | int | 0 | — | Number of items to skip |

### Response Envelope

All list endpoints return a paginated envelope:

```json
{
  "items": [...],
  "total": 142
}
```

| Field | Type | Description |
|---|---|---|
| `items` | array | The page of results (may be empty) |
| `total` | int | Total number of items matching the query (before pagination) |

### Validation

- `limit` < 0 or > 200 → `400 bad_request`
- `offset` < 0 → `400 bad_request`
- Non-integer values → `400 bad_request`

### Special Case: List All

`GET /api/v1/jobs?all=true` maps to `JobService.ListAll()` — returns all jobs with no limit. When `all=true` is set, `limit` and `offset` are ignored. The response still uses the paginated envelope (with `total` equal to `items` length).

### Go Types

```go
type PaginatedResponse[T any] struct {
    Items []T `json:"items"`
    Total int `json:"total"`
}

type PaginationParams struct {
    Limit  int // parsed from ?limit=, default 50, max 200
    Offset int // parsed from ?offset=, default 0
}
```

---

## 4. Async Operations

Several endpoints start long-running operations (LLM generation, team promotion, coordinator detection). These return immediately with `202 Accepted` and an operation ID. Results are delivered via SSE events.

### Response Format

```json
{
  "operation_id": "op_abc123"
}
```

### Lifecycle

1. Client calls an async endpoint → receives `202` with `operation_id`
2. Server processes the operation in a background goroutine
3. On success: server emits `operation.completed` SSE event with matching `operation_id`
4. On failure: server emits `operation.failed` SSE event with matching `operation_id`

### Async Endpoints

| Endpoint | Service Method | Operation Kind |
|---|---|---|
| `POST /api/v1/skills/generate` | `GenerateSkill` | `generate_skill` |
| `POST /api/v1/agents/generate` | `GenerateAgent` | `generate_agent` |
| `POST /api/v1/teams/generate` | `GenerateTeam` | `generate_team` |
| `POST /api/v1/teams/{id}/promote` | `PromoteTeam` | `promote_team` |
| `POST /api/v1/teams/{id}/detect-coordinator` | `DetectCoordinator` | `detect_coordinator` |

### Rate Limiting

Async operations are rate-limited to prevent unbounded goroutine creation. If the limit is exceeded, the server returns `429 Too Many Requests` with a `Retry-After` header (in seconds). Implementation: simple in-memory semaphore (e.g., 5 concurrent async operations).

---

## 5. SSE Event Stream

### Endpoint

```
GET /api/v1/events
```

Returns a `text/event-stream` response. The connection stays open indefinitely. Multiple concurrent clients are supported — each receives all events independently via fan-out broadcast.

### Wire Format

Each SSE event follows the [Server-Sent Events](https://html.spec.whatwg.org/multipage/server-sent-events.html) specification:

```
id: 42
event: operator.text
data: {"seq":42,"type":"operator.text","timestamp":"2026-03-02T12:00:00Z","turn_id":"turn_abc123","session_id":"","operation_id":"","payload":{"text":"Hello","reasoning":""}}

```

| SSE Field | Value |
|---|---|
| `id` | Sequence number (matches `seq` in the JSON envelope). Enables `Last-Event-ID` on reconnect (future optimization). |
| `event` | The `EventType` string (e.g., `operator.text`, `session.started`). Enables `EventSource.addEventListener()` in browsers. |
| `data` | JSON-encoded `Event` envelope (single line, no embedded newlines). |

A blank line terminates each event.

### JSON Event Envelope

```json
{
  "seq": 42,
  "type": "operator.text",
  "timestamp": "2026-03-02T12:00:00Z",
  "turn_id": "turn_abc123",
  "session_id": "",
  "operation_id": "",
  "payload": {
    "text": "Hello, how can I help?",
    "reasoning": ""
  }
}
```

| Field | Type | Description |
|---|---|---|
| `seq` | uint64 | Monotonically increasing sequence number per connection. Starts at 1. |
| `type` | string | Event type discriminator (see table below). |
| `timestamp` | string | RFC 3339 server timestamp. |
| `turn_id` | string | Correlates `operator.*` events to a `SendMessage` call. Empty for non-operator events. |
| `session_id` | string | Correlates `session.*` events to an agent session. Empty for non-session events. |
| `operation_id` | string | Correlates `operation.*` events to an async operation. Empty for non-operation events. |
| `payload` | object\|null | Typed payload. Structure depends on `type`. Null for `definitions.reloaded`. |

### Event Types

| Event Type | Payload Type | Correlation ID | Description |
|---|---|---|---|
| `operator.text` | `OperatorTextPayload` | `turn_id` | Streamed text tokens from the operator LLM (batched ~16ms). |
| `operator.done` | `OperatorDonePayload` | `turn_id` | Operator finished a turn. Client commits response buffer. |
| `operator.prompt` | `OperatorPromptPayload` | `turn_id` | Operator needs user input (ask_user). Client enters prompt mode. |
| `task.assigned` | `TaskAssignedPayload` | — | Operator assigned a task to a team. |
| `task.started` | `TaskStartedPayload` | — | Team began executing a task. |
| `task.completed` | `TaskCompletedPayload` | — | Task finished successfully. |
| `task.failed` | `TaskFailedPayload` | — | Task failed. |
| `blocker.reported` | `BlockerReportedPayload` | — | Agent reported a blocker needing user input. |
| `job.completed` | `JobCompletedPayload` | — | Entire job finished. |
| `progress.update` | `ProgressUpdatePayload` | — | Full progress state snapshot (replaces polling). |
| `session.started` | `SessionStartedPayload` | `session_id` | New agent session began. |
| `session.text` | `SessionTextPayload` | `session_id` | Text tokens from an agent session. |
| `session.tool_call` | `SessionToolCallPayload` | `session_id` | Agent invoked a tool. |
| `session.tool_result` | `SessionToolResultPayload` | `session_id` | Tool returned a result. |
| `session.done` | `SessionDonePayload` | `session_id` | Agent session completed. |
| `definitions.reloaded` | null | — | Definition files changed; client should refresh. |
| `operation.completed` | `OperationCompletedPayload` | `operation_id` | Async operation succeeded. |
| `operation.failed` | `OperationFailedPayload` | `operation_id` | Async operation failed. |
| `heartbeat` | `HeartbeatPayload` | — | Keepalive (every 15 seconds). |

### Payload Schemas

#### `OperatorTextPayload`
```json
{ "text": "string", "reasoning": "string" }
```

#### `OperatorDonePayload`
```json
{ "model_name": "string", "tokens_in": 0, "tokens_out": 0, "reasoning_tokens": 0 }
```

#### `OperatorPromptPayload`
```json
{
  "request_id": "string",
  "question": "string",
  "options": ["string"],
  "confirm_dispatch": false,
  "pending_dispatch": { "id": "string", "name": "string", "arguments": {} }
}
```
`options` and `pending_dispatch` may be null/omitted.

#### `TaskAssignedPayload`
```json
{ "task_id": "string", "job_id": "string", "team_id": "string", "title": "string" }
```

#### `TaskStartedPayload`
```json
{ "task_id": "string", "job_id": "string", "team_id": "string", "title": "string" }
```

#### `TaskCompletedPayload`
```json
{ "task_id": "string", "job_id": "string", "team_id": "string", "summary": "string", "recommendations": "string", "has_next_task": false }
```

#### `TaskFailedPayload`
```json
{ "task_id": "string", "job_id": "string", "team_id": "string", "error": "string" }
```

#### `BlockerReportedPayload`
```json
{ "task_id": "string", "team_id": "string", "agent_id": "string", "description": "string", "questions": ["string"] }
```

#### `JobCompletedPayload`
```json
{ "job_id": "string", "title": "string", "summary": "string" }
```

#### `ProgressUpdatePayload`
```json
{ "state": { /* ProgressState object */ } }
```
See [ProgressState](#progressstate) in Go Type Definitions.

#### `SessionStartedPayload`
```json
{ "session_id": "string", "agent_name": "string", "team_name": "string", "task": "string", "job_id": "string", "task_id": "string", "system_prompt": "string", "initial_message": "string" }
```

#### `SessionTextPayload`
```json
{ "text": "string" }
```

#### `SessionToolCallPayload`
```json
{ "tool_call": { "id": "string", "name": "string", "arguments": {} } }
```

#### `SessionToolResultPayload`
```json
{ "result": { "call_id": "string", "name": "string", "result": "string", "error": "string" } }
```

#### `SessionDonePayload`
```json
{ "agent_name": "string", "job_id": "string", "task_id": "string", "status": "string", "final_text": "string" }
```

#### `OperationCompletedPayload`
```json
{ "kind": "string", "result": { "operation_id": "string", "content": "string", "agent_names": ["string"], "error": "" } }
```

#### `OperationFailedPayload`
```json
{ "kind": "string", "error": "string" }
```

#### `HeartbeatPayload`
```json
{ "server_time": "2026-03-02T12:00:00Z" }
```

### Connection Management

- The server sends a `heartbeat` event every **15 seconds** to prevent proxy/load-balancer idle timeouts.
- The server MUST call `http.Flusher.Flush()` after writing each SSE event.
- On client disconnect, the server cleans up the subscriber goroutine.
- `Last-Event-ID` header support is deferred (future optimization). On reconnect, clients fetch full state via REST endpoints then re-subscribe.

### Query Parameters

| Parameter | Type | Description |
|---|---|---|
| `last_event_id` | uint64 | (Future) Resume from this sequence number. Currently ignored. |

---

## 6. Endpoints: Operator

### `POST /api/v1/operator/messages`

Send a user message to the operator. Returns immediately; the operator processes asynchronously and pushes events via SSE.

**Maps to:** `OperatorService.SendMessage(ctx, message) (turnID, error)`

**Request Body:**
```json
{
  "message": "Build me a REST API for managing toasters"
}
```

| Field | Type | Required | Constraints |
|---|---|---|---|
| `message` | string | yes | Non-empty, max 100,000 characters |

**Response:** `202 Accepted`
```json
{
  "turn_id": "turn_abc123"
}
```

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 400 | `bad_request` | Empty message, message too long, malformed JSON |
| 503 | `service_unavailable` | Operator not initialized |

**Notes:**
- The client should enter streaming state after receiving `202`.
- The client exits streaming state when it receives an `operator.done` SSE event with the matching `turn_id`.
- Subsequent `operator.text` events with the same `turn_id` carry the streamed response tokens.

---

### `POST /api/v1/operator/prompts/{requestId}/respond`

Respond to an active `ask_user` prompt from the operator.

**Maps to:** `OperatorService.RespondToPrompt(ctx, requestID, response)`

**Path Parameters:**
| Parameter | Description |
|---|---|
| `requestId` | The `request_id` from the `operator.prompt` SSE event |

**Request Body:**
```json
{
  "response": "Yes, proceed with the deployment"
}
```

| Field | Type | Required | Constraints |
|---|---|---|---|
| `response` | string | yes | Non-empty |

**Response:** `204 No Content`

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 400 | `bad_request` | Empty response, malformed JSON |
| 404 | `not_found` | No active prompt with this `requestId` |

---

### `GET /api/v1/operator/status`

Get the current operator state.

**Maps to:** `OperatorService.Status(ctx)`

**Response:** `200 OK`
```json
{
  "state": "idle",
  "current_turn_id": "",
  "model_name": "claude-sonnet-4-6"
}
```

| Field | Type | Description |
|---|---|---|
| `state` | string | One of: `idle`, `streaming`, `processing` |
| `current_turn_id` | string | Non-empty while a turn is in progress |
| `model_name` | string | The model the operator is using |

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 503 | `service_unavailable` | Operator not initialized |

---

### `GET /api/v1/operator/history`

Get the operator conversation history for the current session.

**Maps to:** `OperatorService.History(ctx)`

**Response:** `200 OK`
```json
{
  "items": [
    {
      "message": {
        "role": "user",
        "content": "Build me a REST API",
        "tool_calls": null,
        "tool_call_id": ""
      },
      "timestamp": "2026-03-02T12:00:00Z",
      "reasoning": "",
      "claude_meta": ""
    },
    {
      "message": {
        "role": "assistant",
        "content": "I'll help you build that...",
        "tool_calls": null,
        "tool_call_id": ""
      },
      "timestamp": "2026-03-02T12:00:05Z",
      "reasoning": "",
      "claude_meta": "operator · claude-sonnet-4-6"
    }
  ],
  "total": 2
}
```

**Notes:**
- Returns entries in chronological order (oldest first).
- Bounded to the most recent entries (server-side limit).
- Used by remote clients to hydrate the chat view on reconnect.
- Not paginated with `limit`/`offset` — the server returns the bounded history as a single page. The `total` field equals the `items` length.

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 503 | `service_unavailable` | Operator not initialized |

---

### `POST /api/v1/operator/blockers/{jobId}/{taskId}/respond`

Submit answers to a blocker reported by an agent.

**Maps to:** `OperatorService.RespondToBlocker(ctx, jobID, taskID, answers)`

**Path Parameters:**
| Parameter | Description |
|---|---|
| `jobId` | The job ID of the blocked task |
| `taskId` | The task ID of the blocked task |

**Request Body:**
```json
{
  "answers": [
    "Use PostgreSQL for the database",
    "Yes, include migration support"
  ]
}
```

| Field | Type | Required | Constraints |
|---|---|---|---|
| `answers` | string[] | yes | Non-empty array; each answer is a non-empty string |

**Response:** `204 No Content`

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 400 | `bad_request` | Empty answers array, empty individual answers, malformed JSON |
| 404 | `not_found` | Job or task not found |

---

## 7. Endpoints: Skills

### `GET /api/v1/skills`

List all skills.

**Maps to:** `DefinitionService.ListSkills(ctx)`

**Query Parameters:** Standard pagination (`limit`, `offset`).

**Response:** `200 OK`
```json
{
  "items": [
    {
      "id": "my-skill",
      "name": "My Skill",
      "description": "A reusable capability",
      "tools": ["tool_a", "tool_b"],
      "prompt": "You are skilled at...",
      "source": "user",
      "created_at": "2026-03-01T10:00:00Z",
      "updated_at": "2026-03-01T10:00:00Z"
    }
  ],
  "total": 15
}
```

**Notes:**
- Ordered by source (user first, then system) and then by name.
- `source_path` is never included in the response.

---

### `GET /api/v1/skills/{id}`

Get a single skill by ID.

**Maps to:** `DefinitionService.GetSkill(ctx, id)`

**Response:** `200 OK` — Single skill object (same schema as list items).

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 404 | `not_found` | Skill does not exist |

---

### `POST /api/v1/skills`

Create a new skill.

**Maps to:** `DefinitionService.CreateSkill(ctx, name)`

**Request Body:**
```json
{
  "name": "My New Skill"
}
```

| Field | Type | Required | Constraints |
|---|---|---|---|
| `name` | string | yes | Non-empty, no newlines (sanitized server-side) |

**Response:** `201 Created`
- Body: The created skill object.
- Header: `Location: /api/v1/skills/{id}`

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 400 | `bad_request` | Empty name, malformed JSON |
| 409 | `conflict` | Skill with this name already exists |

---

### `DELETE /api/v1/skills/{id}`

Delete a skill.

**Maps to:** `DefinitionService.DeleteSkill(ctx, id)`

**Response:** `204 No Content`

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 404 | `not_found` | Skill does not exist |
| 422 | `unprocessable_entity` | System skill cannot be deleted |

---

### `POST /api/v1/skills/generate`

Generate a skill definition using the LLM. Async operation.

**Maps to:** `DefinitionService.GenerateSkill(ctx, prompt)`

**Request Body:**
```json
{
  "prompt": "Create a skill for Kubernetes deployment management"
}
```

| Field | Type | Required | Constraints |
|---|---|---|---|
| `prompt` | string | yes | Non-empty, max 10,000 characters |

**Response:** `202 Accepted`
```json
{
  "operation_id": "op_abc123"
}
```

**Completion Event:** `operation.completed` with `kind: "generate_skill"`
**Failure Event:** `operation.failed` with `kind: "generate_skill"`

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 400 | `bad_request` | Empty prompt, malformed JSON |
| 429 | `too_many_requests` | Too many concurrent async operations |
| 503 | `service_unavailable` | LLM provider not configured |

---

## 8. Endpoints: Agents

### `GET /api/v1/agents`

List all agents.

**Maps to:** `DefinitionService.ListAgents(ctx)`

**Query Parameters:** Standard pagination (`limit`, `offset`).

**Response:** `200 OK`
```json
{
  "items": [
    {
      "id": "my-agent",
      "name": "My Agent",
      "description": "A specialized agent",
      "mode": "worker",
      "model": "claude-sonnet-4-6",
      "provider": "anthropic",
      "temperature": 0.7,
      "system_prompt": "You are...",
      "tools": ["read_file", "write_file"],
      "disallowed_tools": [],
      "skills": ["coding"],
      "permission_mode": "default",
      "max_turns": 25,
      "color": "#FF5733",
      "hidden": false,
      "disabled": false,
      "source": "user",
      "team_id": "",
      "created_at": "2026-03-01T10:00:00Z",
      "updated_at": "2026-03-01T10:00:00Z"
    }
  ],
  "total": 8
}
```

**Notes:**
- Ordered: shared agents alphabetically → team-local agents by "team/agent" → system agents alphabetically.
- `temperature` and `max_turns` are nullable (omitted when not set).
- `source_path` is never included.

---

### `GET /api/v1/agents/{id}`

Get a single agent by ID.

**Maps to:** `DefinitionService.GetAgent(ctx, id)`

**Response:** `200 OK` — Single agent object.

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 404 | `not_found` | Agent does not exist |

---

### `POST /api/v1/agents`

Create a new shared agent.

**Maps to:** `DefinitionService.CreateAgent(ctx, name)`

**Request Body:**
```json
{
  "name": "My New Agent"
}
```

| Field | Type | Required | Constraints |
|---|---|---|---|
| `name` | string | yes | Non-empty, no newlines |

**Response:** `201 Created`
- Body: The created agent object.
- Header: `Location: /api/v1/agents/{id}`

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 400 | `bad_request` | Empty name, malformed JSON |
| 409 | `conflict` | Agent with this name already exists |

---

### `DELETE /api/v1/agents/{id}`

Delete an agent.

**Maps to:** `DefinitionService.DeleteAgent(ctx, id)`

**Response:** `204 No Content`

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 404 | `not_found` | Agent does not exist |
| 422 | `unprocessable_entity` | System agent, team-local agent, or read-only agent cannot be deleted |

---

### `POST /api/v1/agents/{id}/skills`

Add a skill to an agent.

**Maps to:** `DefinitionService.AddSkillToAgent(ctx, agentID, skillName)`

**Path Parameters:**
| Parameter | Description |
|---|---|
| `id` | The agent ID |

**Request Body:**
```json
{
  "skill_name": "kubernetes-deploy"
}
```

| Field | Type | Required | Constraints |
|---|---|---|---|
| `skill_name` | string | yes | Non-empty; must reference an existing skill |

**Response:** `204 No Content`

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 400 | `bad_request` | Empty skill name, malformed JSON |
| 404 | `not_found` | Agent or skill does not exist |
| 422 | `unprocessable_entity` | Agent is read-only (system) or has no source path |

---

### `POST /api/v1/agents/generate`

Generate an agent definition using the LLM. Async operation.

**Maps to:** `DefinitionService.GenerateAgent(ctx, prompt)`

**Request Body:**
```json
{
  "prompt": "Create an agent specialized in database migrations"
}
```

| Field | Type | Required | Constraints |
|---|---|---|---|
| `prompt` | string | yes | Non-empty, max 10,000 characters |

**Response:** `202 Accepted`
```json
{
  "operation_id": "op_def456"
}
```

**Completion Event:** `operation.completed` with `kind: "generate_agent"`
**Failure Event:** `operation.failed` with `kind: "generate_agent"`

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 400 | `bad_request` | Empty prompt, malformed JSON |
| 429 | `too_many_requests` | Too many concurrent async operations |
| 503 | `service_unavailable` | LLM provider not configured |

---

## 9. Endpoints: Teams

### `GET /api/v1/teams`

List all non-system teams.

**Maps to:** `DefinitionService.ListTeams(ctx)`

**Query Parameters:** Standard pagination (`limit`, `offset`).

**Response:** `200 OK`
```json
{
  "items": [
    {
      "team": {
        "id": "backend-team",
        "name": "Backend Team",
        "description": "Handles backend services",
        "lead_agent": "agent-lead-id",
        "skills": ["coding", "testing"],
        "provider": "anthropic",
        "model": "claude-sonnet-4-6",
        "culture": "We write clean, tested code...",
        "source": "user",
        "is_auto": false,
        "created_at": "2026-03-01T10:00:00Z",
        "updated_at": "2026-03-01T10:00:00Z"
      },
      "coordinator": {
        "id": "agent-lead-id",
        "name": "Lead Agent",
        "...": "..."
      },
      "workers": [
        {
          "id": "agent-worker-1",
          "name": "Worker 1",
          "...": "..."
        }
      ]
    }
  ],
  "total": 3
}
```

**Notes:**
- System teams (source == "system") are excluded.
- `coordinator` is null if no lead agent is set or found.
- `workers` is an empty array if the team has no workers.
- `source_path` is never included in team or agent objects.

---

### `GET /api/v1/teams/{id}`

Get a single team with its members.

**Maps to:** `DefinitionService.GetTeam(ctx, id)`

**Response:** `200 OK` — Single `TeamView` object.

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 404 | `not_found` | Team does not exist |

---

### `POST /api/v1/teams`

Create a new team.

**Maps to:** `DefinitionService.CreateTeam(ctx, name)`

**Request Body:**
```json
{
  "name": "My New Team"
}
```

| Field | Type | Required | Constraints |
|---|---|---|---|
| `name` | string | yes | Non-empty, no newlines |

**Response:** `201 Created`
- Body: The created `TeamView` object.
- Header: `Location: /api/v1/teams/{id}`

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 400 | `bad_request` | Empty name, malformed JSON |
| 409 | `conflict` | Team with this name already exists |

---

### `DELETE /api/v1/teams/{id}`

Delete a team.

**Maps to:** `DefinitionService.DeleteTeam(ctx, id)`

**Response:** `204 No Content`

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 404 | `not_found` | Team does not exist |
| 422 | `unprocessable_entity` | System team or read-only auto-team cannot be deleted |

**Notes:**
- If the team is an auto-team, a dismiss marker is written to prevent re-creation on restart.

---

### `POST /api/v1/teams/{id}/agents`

Add an agent to a team.

**Maps to:** `DefinitionService.AddAgentToTeam(ctx, teamID, agentID)`

**Request Body:**
```json
{
  "agent_id": "my-agent-id"
}
```

| Field | Type | Required | Constraints |
|---|---|---|---|
| `agent_id` | string | yes | Non-empty; must reference an existing agent |

**Response:** `204 No Content`

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 400 | `bad_request` | Empty agent ID, malformed JSON |
| 404 | `not_found` | Team or agent does not exist |
| 422 | `unprocessable_entity` | Team is read-only or agent has no source path |

---

### `PUT /api/v1/teams/{id}/coordinator`

Set the coordinator for a team.

**Maps to:** `DefinitionService.SetCoordinator(ctx, teamID, agentName)`

**Request Body:**
```json
{
  "agent_name": "lead-agent"
}
```

| Field | Type | Required | Constraints |
|---|---|---|---|
| `agent_name` | string | yes | Non-empty; must be an agent in the team |

**Response:** `204 No Content`

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 400 | `bad_request` | Empty agent name, malformed JSON |
| 404 | `not_found` | Team does not exist or agent not found in team |
| 422 | `unprocessable_entity` | Team is read-only |

---

### `POST /api/v1/teams/{id}/promote`

Promote an auto-detected team to a fully managed team. Async operation.

**Maps to:** `DefinitionService.PromoteTeam(ctx, teamID)`

**Request Body:** None.

**Response:** `202 Accepted`
```json
{
  "operation_id": "op_ghi789"
}
```

**Completion Event:** `operation.completed` with `kind: "promote_team"`, `result.content` = team name.
**Failure Event:** `operation.failed` with `kind: "promote_team"`

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 404 | `not_found` | Team does not exist |
| 422 | `unprocessable_entity` | Team is not an auto-team (already managed) |
| 429 | `too_many_requests` | Too many concurrent async operations |

---

### `POST /api/v1/teams/generate`

Generate a team definition using the LLM. Async operation.

**Maps to:** `DefinitionService.GenerateTeam(ctx, prompt)`

**Request Body:**
```json
{
  "prompt": "Create a team for full-stack web development"
}
```

| Field | Type | Required | Constraints |
|---|---|---|---|
| `prompt` | string | yes | Non-empty, max 10,000 characters |

**Response:** `202 Accepted`
```json
{
  "operation_id": "op_jkl012"
}
```

**Completion Event:** `operation.completed` with `kind: "generate_team"`, `result.content` = team.md content, `result.agent_names` = agent names.
**Failure Event:** `operation.failed` with `kind: "generate_team"`

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 400 | `bad_request` | Empty prompt, malformed JSON |
| 429 | `too_many_requests` | Too many concurrent async operations |
| 503 | `service_unavailable` | LLM provider not configured |

---

### `POST /api/v1/teams/{id}/detect-coordinator`

Ask the LLM to pick the best coordinator for the team. Async operation.

**Maps to:** `DefinitionService.DetectCoordinator(ctx, teamID)`

**Request Body:** None.

**Response:** `202 Accepted`
```json
{
  "operation_id": "op_mno345"
}
```

**Completion Event:** `operation.completed` with `kind: "detect_coordinator"`, `result.content` = detected agent name (empty if no match).
**Failure Event:** `operation.failed` with `kind: "detect_coordinator"`

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 404 | `not_found` | Team does not exist |
| 429 | `too_many_requests` | Too many concurrent async operations |
| 503 | `service_unavailable` | LLM provider not configured |

---

## 10. Endpoints: Jobs

### `GET /api/v1/jobs`

List jobs with optional filtering and pagination.

**Maps to:** `JobService.List(ctx, filter)` and `JobService.ListAll(ctx)`

**Query Parameters:**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `limit` | int | 50 | Max items per page (max 200) |
| `offset` | int | 0 | Items to skip |
| `status` | string | — | Filter by job status (e.g., `active`, `completed`, `failed`) |
| `type` | string | — | Filter by job type (e.g., `bug_fix`, `new_feature`) |
| `all` | bool | false | If `true`, return all jobs (ignores `limit`/`offset`). Maps to `ListAll()`. |

**Response:** `200 OK`
```json
{
  "items": [
    {
      "id": "job_abc123",
      "title": "Build REST API",
      "description": "Create a REST API for the toasters service",
      "type": "new_feature",
      "status": "active",
      "workspace_dir": "/Users/jeff/toasters/job_abc123",
      "created_at": "2026-03-02T10:00:00Z",
      "updated_at": "2026-03-02T11:30:00Z",
      "metadata": null
    }
  ],
  "total": 5
}
```

**Notes:**
- Ordered by creation time, newest first.
- `status` filter values: `pending`, `setting_up`, `decomposing`, `active`, `paused`, `completed`, `failed`, `cancelled`.
- Invalid `status` value → `400 bad_request`.

---

### `GET /api/v1/jobs/{id}`

Get a job with its tasks and recent progress reports.

**Maps to:** `JobService.Get(ctx, id)`

**Response:** `200 OK`
```json
{
  "job": {
    "id": "job_abc123",
    "title": "Build REST API",
    "description": "...",
    "type": "new_feature",
    "status": "active",
    "workspace_dir": "/Users/jeff/toasters/job_abc123",
    "created_at": "2026-03-02T10:00:00Z",
    "updated_at": "2026-03-02T11:30:00Z",
    "metadata": null
  },
  "tasks": [
    {
      "id": "task_001",
      "job_id": "job_abc123",
      "title": "Design API schema",
      "status": "completed",
      "agent_id": "api-designer",
      "team_id": "backend-team",
      "parent_id": "",
      "sort_order": 1,
      "created_at": "2026-03-02T10:05:00Z",
      "updated_at": "2026-03-02T10:30:00Z",
      "summary": "API schema designed with 12 endpoints",
      "result_summary": "",
      "recommendations": "",
      "metadata": null
    }
  ],
  "progress": [
    {
      "id": 1,
      "job_id": "job_abc123",
      "task_id": "task_001",
      "agent_id": "api-designer",
      "status": "completed",
      "message": "Schema design complete",
      "created_at": "2026-03-02T10:30:00Z"
    }
  ]
}
```

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 404 | `not_found` | Job does not exist |

---

### `POST /api/v1/jobs/{id}/cancel`

Cancel a job.

**Maps to:** `JobService.Cancel(ctx, id)`

**Request Body:** None.

**Response:** `204 No Content`

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 404 | `not_found` | Job does not exist |
| 409 | `conflict` | Job is in a terminal state (`completed`, `failed`, `cancelled`) and cannot be cancelled |

---

## 11. Endpoints: Sessions

### `GET /api/v1/sessions`

List all active agent sessions.

**Maps to:** `SessionService.List(ctx)`

**Query Parameters:** Standard pagination (`limit`, `offset`).

**Response:** `200 OK`
```json
{
  "items": [
    {
      "id": "sess_abc123",
      "agent_id": "backend-worker",
      "team_name": "Backend Team",
      "job_id": "job_abc123",
      "task_id": "task_001",
      "status": "active",
      "model": "claude-sonnet-4-6",
      "provider": "anthropic",
      "start_time": "2026-03-02T10:05:00Z",
      "tokens_in": 15000,
      "tokens_out": 3200
    }
  ],
  "total": 2
}
```

**Notes:**
- Returns live snapshots with real-time token counts from the in-process runtime.
- Only active sessions are returned (completed sessions are not included).

---

### `GET /api/v1/sessions/{id}`

Get full session detail including output and activity history.

**Maps to:** `SessionService.Get(ctx, id)`

**Response:** `200 OK`
```json
{
  "snapshot": {
    "id": "sess_abc123",
    "agent_id": "backend-worker",
    "team_name": "Backend Team",
    "job_id": "job_abc123",
    "task_id": "task_001",
    "status": "active",
    "model": "claude-sonnet-4-6",
    "provider": "anthropic",
    "start_time": "2026-03-02T10:05:00Z",
    "tokens_in": 15000,
    "tokens_out": 3200
  },
  "system_prompt": "You are a backend developer...",
  "initial_message": "Implement the user authentication endpoint...",
  "output": "I'll start by creating the handler...\n",
  "activities": [
    {
      "label": "write: auth_handler.go",
      "tool_name": "write_file"
    }
  ],
  "agent_name": "Backend Worker",
  "team_name": "Backend Team",
  "task": "Implement user authentication"
}
```

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 404 | `not_found` | Session does not exist |

**Notes:**
- Used by the output modal and for reconnect hydration (clients call this on reconnect to rebuild session state).

---

### `POST /api/v1/sessions/{id}/cancel`

Cancel an agent session.

**Maps to:** `SessionService.Cancel(ctx, id)`

**Request Body:** None.

**Response:** `204 No Content`

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 404 | `not_found` | Session does not exist |
| 409 | `conflict` | Session is already complete |

---

## 12. Endpoints: System

### `GET /api/v1/health`

Health check endpoint.

**Maps to:** `SystemService.Health(ctx)`

**Response:** `200 OK`
```json
{
  "status": "ok",
  "version": "0.1.0",
  "uptime_seconds": 3600
}
```

| Field | Type | Description |
|---|---|---|
| `status` | string | `ok` or `degraded` |
| `version` | string | Application version |
| `uptime_seconds` | float64 | Seconds since the service started |

**Notes:**
- Always succeeds for a running server. Used for liveness probes.
- `uptime` is serialized as seconds (float64) rather than Go's `time.Duration` nanoseconds for client friendliness.

---

### `GET /api/v1/models`

List available LLM models.

**Maps to:** `SystemService.ListModels(ctx)`

**Response:** `200 OK`
```json
{
  "items": [
    {
      "id": "claude-sonnet-4-6",
      "name": "Claude Sonnet 4",
      "provider": "anthropic",
      "state": "loaded",
      "max_context_length": 200000,
      "loaded_context_length": 200000
    }
  ],
  "total": 3
}
```

**Errors:**
| Status | Code | Condition |
|---|---|---|
| 503 | `service_unavailable` | Provider unreachable |

---

### `GET /api/v1/mcp/servers`

List MCP server connection status and tools.

**Maps to:** `SystemService.ListMCPServers(ctx)`

**Response:** `200 OK`
```json
{
  "items": [
    {
      "name": "github",
      "transport": "stdio",
      "state": "connected",
      "error": "",
      "tool_count": 12,
      "tools": [
        {
          "namespaced_name": "github__create_issue",
          "original_name": "create_issue",
          "server_name": "github",
          "description": "Create a GitHub issue",
          "input_schema": {}
        }
      ]
    }
  ],
  "total": 2
}
```

---

### `GET /api/v1/progress`

Get the current full progress state snapshot.

**Maps to:** `SystemService.GetProgressState(ctx)`

**Response:** `200 OK`

<a name="progressstate"></a>

```json
{
  "jobs": [
    { "id": "...", "title": "...", "status": "active", "..." : "..." }
  ],
  "tasks": {
    "job_abc123": [
      { "id": "...", "title": "...", "status": "in_progress", "...": "..." }
    ]
  },
  "reports": {
    "job_abc123": [
      { "id": 1, "job_id": "...", "message": "Working on it...", "...": "..." }
    ]
  },
  "active_sessions": [
    { "id": "...", "agent_id": "...", "status": "active", "...": "..." }
  ],
  "live_snapshots": [
    { "id": "...", "agent_id": "...", "tokens_in": 15000, "...": "..." }
  ],
  "feed_entries": [
    { "id": 1, "entry_type": "task_started", "content": "...", "...": "..." }
  ]
}
```

**Notes:**
- Used by clients on connect/reconnect to hydrate their full state.
- For real-time updates after initial hydration, clients subscribe to the SSE event stream.
- `tasks` and `reports` are maps keyed by job ID.

---

## 13. Go Type Definitions

### HTTP Handler Types

```go
// ErrorResponse is the standard error envelope.
type ErrorResponse struct {
    Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
    Code    string `json:"code"`
    Message string `json:"message"`
}

// PaginatedResponse wraps any list response with pagination metadata.
type PaginatedResponse[T any] struct {
    Items []T `json:"items"`
    Total int `json:"total"`
}

// AsyncResponse is returned by all 202 Accepted endpoints.
type AsyncResponse struct {
    OperationID string `json:"operation_id"`
}

// TurnResponse is returned by POST /api/v1/operator/messages.
type TurnResponse struct {
    TurnID string `json:"turn_id"`
}
```

### Request Body Types

```go
// SendMessageRequest is the body for POST /api/v1/operator/messages.
type SendMessageRequest struct {
    Message string `json:"message"`
}

// RespondToPromptRequest is the body for POST /api/v1/operator/prompts/{requestId}/respond.
type RespondToPromptRequest struct {
    Response string `json:"response"`
}

// RespondToBlockerRequest is the body for POST /api/v1/operator/blockers/{jobId}/{taskId}/respond.
type RespondToBlockerRequest struct {
    Answers []string `json:"answers"`
}

// CreateSkillRequest is the body for POST /api/v1/skills.
type CreateSkillRequest struct {
    Name string `json:"name"`
}

// GenerateRequest is the body for POST /api/v1/{skills,agents,teams}/generate.
type GenerateRequest struct {
    Prompt string `json:"prompt"`
}

// CreateAgentRequest is the body for POST /api/v1/agents.
type CreateAgentRequest struct {
    Name string `json:"name"`
}

// AddSkillToAgentRequest is the body for POST /api/v1/agents/{id}/skills.
type AddSkillToAgentRequest struct {
    SkillName string `json:"skill_name"`
}

// CreateTeamRequest is the body for POST /api/v1/teams.
type CreateTeamRequest struct {
    Name string `json:"name"`
}

// AddAgentToTeamRequest is the body for POST /api/v1/teams/{id}/agents.
type AddAgentToTeamRequest struct {
    AgentID string `json:"agent_id"`
}

// SetCoordinatorRequest is the body for PUT /api/v1/teams/{id}/coordinator.
type SetCoordinatorRequest struct {
    AgentName string `json:"agent_name"`
}
```

### SSE Event Wire Type

```go
// SSEEvent is the JSON envelope written to the SSE data field.
// It mirrors service.Event but with JSON-friendly field names.
type SSEEvent struct {
    Seq         uint64    `json:"seq"`
    Type        string    `json:"type"`
    Timestamp   time.Time `json:"timestamp"`
    TurnID      string    `json:"turn_id,omitempty"`
    SessionID   string    `json:"session_id,omitempty"`
    OperationID string    `json:"operation_id,omitempty"`
    Payload     any       `json:"payload"`
}
```

### Health Response Type

```go
// HealthResponse is the body for GET /api/v1/health.
// Uptime is serialized as seconds for client friendliness.
type HealthResponse struct {
    Status        string  `json:"status"`
    Version       string  `json:"version"`
    UptimeSeconds float64 `json:"uptime_seconds"`
}
```

---

## 14. Middleware Requirements

The server should apply the following middleware in order (outermost first):

| Middleware | Description |
|---|---|
| **Recovery** | Catches panics, logs stack trace, returns `500 internal_error`. |
| **Request ID** | Generates a UUID v4 `X-Request-ID` header for every request. Propagates to `context.Context`. |
| **Logging** | Logs method, path, status code, duration, request ID via `slog`. |
| **CORS** | Allows `*` origin (single-user, local network). Headers: `Content-Type`, `X-Request-ID`. Methods: `GET`, `POST`, `PUT`, `DELETE`, `OPTIONS`. |
| **Content-Type** | Validates `Content-Type: application/json` on requests with bodies (`POST`, `PUT`, `PATCH`). Returns `415 Unsupported Media Type` otherwise. Skips `GET`, `DELETE`, `OPTIONS`. |
| **Auth** | Phase 4. Token validation on all endpoints including SSE. |

### Implementation Notes

- Middleware is composed as `http.Handler` wrappers (standard Go pattern).
- The SSE endpoint (`GET /api/v1/events`) skips the Content-Type validation middleware.
- The health endpoint (`GET /api/v1/health`) skips auth middleware (Phase 4) for liveness probes.

---

## 15. Authentication

**Deferred to Phase 4.** The API is unauthenticated in Phase 2.

Phase 4 design (for reference):
- Auto-generated bearer token stored at `~/.config/toasters/server.token` (0600 permissions).
- Clients send `Authorization: Bearer <token>` on every request including SSE.
- `--no-auth` flag for development.
- Health endpoint exempt from auth.

---

## 16. Reconnect Protocol

When a client reconnects (SSE connection dropped, client restarted, etc.), it must rebuild its full state before resuming event consumption:

### Steps

1. **Fetch operator status:** `GET /api/v1/operator/status` → determine if a turn is in progress.
2. **Fetch conversation history:** `GET /api/v1/operator/history` → hydrate the chat view.
3. **Fetch progress state:** `GET /api/v1/progress` → hydrate jobs, tasks, sessions, feed entries.
4. **Fetch active session details:** For each live session in the progress state, `GET /api/v1/sessions/{id}` → hydrate output buffers and activity history.
5. **Subscribe to SSE:** `GET /api/v1/events` → receive future events.

### Notes

- Steps 1–4 can be parallelized.
- There is a brief window between REST fetches and SSE subscription where events may be missed. This is acceptable for Phase 2 — the client will receive the next `progress.update` event which carries a full state snapshot.
- `Last-Event-ID` replay is deferred as a future optimization.

---

## Appendix A: Endpoint Summary

| Method | Path | Service Method | Status |
|---|---|---|---|
| `POST` | `/api/v1/operator/messages` | `SendMessage` | 202 |
| `POST` | `/api/v1/operator/prompts/{requestId}/respond` | `RespondToPrompt` | 204 |
| `GET` | `/api/v1/operator/status` | `Status` | 200 |
| `GET` | `/api/v1/operator/history` | `History` | 200 |
| `POST` | `/api/v1/operator/blockers/{jobId}/{taskId}/respond` | `RespondToBlocker` | 204 |
| `GET` | `/api/v1/skills` | `ListSkills` | 200 |
| `GET` | `/api/v1/skills/{id}` | `GetSkill` | 200 |
| `POST` | `/api/v1/skills` | `CreateSkill` | 201 |
| `DELETE` | `/api/v1/skills/{id}` | `DeleteSkill` | 204 |
| `POST` | `/api/v1/skills/generate` | `GenerateSkill` | 202 |
| `GET` | `/api/v1/agents` | `ListAgents` | 200 |
| `GET` | `/api/v1/agents/{id}` | `GetAgent` | 200 |
| `POST` | `/api/v1/agents` | `CreateAgent` | 201 |
| `DELETE` | `/api/v1/agents/{id}` | `DeleteAgent` | 204 |
| `POST` | `/api/v1/agents/{id}/skills` | `AddSkillToAgent` | 204 |
| `POST` | `/api/v1/agents/generate` | `GenerateAgent` | 202 |
| `GET` | `/api/v1/teams` | `ListTeams` | 200 |
| `GET` | `/api/v1/teams/{id}` | `GetTeam` | 200 |
| `POST` | `/api/v1/teams` | `CreateTeam` | 201 |
| `DELETE` | `/api/v1/teams/{id}` | `DeleteTeam` | 204 |
| `POST` | `/api/v1/teams/{id}/agents` | `AddAgentToTeam` | 204 |
| `PUT` | `/api/v1/teams/{id}/coordinator` | `SetCoordinator` | 204 |
| `POST` | `/api/v1/teams/{id}/promote` | `PromoteTeam` | 202 |
| `POST` | `/api/v1/teams/generate` | `GenerateTeam` | 202 |
| `POST` | `/api/v1/teams/{id}/detect-coordinator` | `DetectCoordinator` | 202 |
| `GET` | `/api/v1/jobs` | `List` / `ListAll` | 200 |
| `GET` | `/api/v1/jobs/{id}` | `Get` | 200 |
| `POST` | `/api/v1/jobs/{id}/cancel` | `Cancel` | 204 |
| `GET` | `/api/v1/sessions` | `List` | 200 |
| `GET` | `/api/v1/sessions/{id}` | `Get` | 200 |
| `POST` | `/api/v1/sessions/{id}/cancel` | `Cancel` | 204 |
| `GET` | `/api/v1/health` | `Health` | 200 |
| `GET` | `/api/v1/models` | `ListModels` | 200 |
| `GET` | `/api/v1/mcp/servers` | `ListMCPServers` | 200 |
| `GET` | `/api/v1/progress` | `GetProgressState` | 200 |
| `GET` | `/api/v1/events` | `Subscribe` (SSE) | 200 |

**Total: 36 endpoints** (35 REST + 1 SSE)

---

## Appendix B: Methods NOT Exposed Over HTTP

| Method/Function | Reason |
|---|---|
| `LocalService.ConfigDir()` | Client-side only; returns local filesystem path |
| `service.Slugify()` | Client-side utility; deterministic, no server state needed |

---

## Appendix C: Go 1.22+ ServeMux Route Registration

```go
mux := http.NewServeMux()

// Operator
mux.HandleFunc("POST /api/v1/operator/messages", h.sendMessage)
mux.HandleFunc("POST /api/v1/operator/prompts/{requestId}/respond", h.respondToPrompt)
mux.HandleFunc("GET /api/v1/operator/status", h.operatorStatus)
mux.HandleFunc("GET /api/v1/operator/history", h.operatorHistory)
mux.HandleFunc("POST /api/v1/operator/blockers/{jobId}/{taskId}/respond", h.respondToBlocker)

// Skills
mux.HandleFunc("GET /api/v1/skills", h.listSkills)
mux.HandleFunc("GET /api/v1/skills/{id}", h.getSkill)
mux.HandleFunc("POST /api/v1/skills", h.createSkill)
mux.HandleFunc("DELETE /api/v1/skills/{id}", h.deleteSkill)
mux.HandleFunc("POST /api/v1/skills/generate", h.generateSkill)

// Agents
mux.HandleFunc("GET /api/v1/agents", h.listAgents)
mux.HandleFunc("GET /api/v1/agents/{id}", h.getAgent)
mux.HandleFunc("POST /api/v1/agents", h.createAgent)
mux.HandleFunc("DELETE /api/v1/agents/{id}", h.deleteAgent)
mux.HandleFunc("POST /api/v1/agents/{id}/skills", h.addSkillToAgent)
mux.HandleFunc("POST /api/v1/agents/generate", h.generateAgent)

// Teams
mux.HandleFunc("GET /api/v1/teams", h.listTeams)
mux.HandleFunc("GET /api/v1/teams/{id}", h.getTeam)
mux.HandleFunc("POST /api/v1/teams", h.createTeam)
mux.HandleFunc("DELETE /api/v1/teams/{id}", h.deleteTeam)
mux.HandleFunc("POST /api/v1/teams/{id}/agents", h.addAgentToTeam)
mux.HandleFunc("PUT /api/v1/teams/{id}/coordinator", h.setCoordinator)
mux.HandleFunc("POST /api/v1/teams/{id}/promote", h.promoteTeam)
mux.HandleFunc("POST /api/v1/teams/generate", h.generateTeam)
mux.HandleFunc("POST /api/v1/teams/{id}/detect-coordinator", h.detectCoordinator)

// Jobs
mux.HandleFunc("GET /api/v1/jobs", h.listJobs)
mux.HandleFunc("GET /api/v1/jobs/{id}", h.getJob)
mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", h.cancelJob)

// Sessions
mux.HandleFunc("GET /api/v1/sessions", h.listSessions)
mux.HandleFunc("GET /api/v1/sessions/{id}", h.getSession)
mux.HandleFunc("POST /api/v1/sessions/{id}/cancel", h.cancelSession)

// System
mux.HandleFunc("GET /api/v1/health", h.health)
mux.HandleFunc("GET /api/v1/models", h.listModels)
mux.HandleFunc("GET /api/v1/mcp/servers", h.listMCPServers)
mux.HandleFunc("GET /api/v1/progress", h.getProgress)

// SSE
mux.HandleFunc("GET /api/v1/events", h.events)
```

**Notes:**
- Go 1.22+ `ServeMux` supports `METHOD /path` patterns and `{param}` path parameters natively.
- Path parameters are accessed via `r.PathValue("paramName")`.
- Route registration order does not matter — the most specific pattern wins.
- `POST /api/v1/skills/generate` does NOT conflict with `POST /api/v1/skills` because they are exact matches (no wildcard).
- `POST /api/v1/agents/generate` does NOT conflict with `POST /api/v1/agents/{id}/skills` because `generate` is a literal segment, not a wildcard match. However, `GET /api/v1/agents/generate` would conflict with `GET /api/v1/agents/{id}` — this is fine because we use `POST` for generate.
