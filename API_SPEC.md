# Toasters REST + SSE API Specification

> **Generated from `internal/server/server.go` route table on 2026-06-28. Keep in sync with `registerRoutes`.**

**Status:** Implemented (server: `internal/server/`, client: `internal/client/`)

This document specifies the HTTP REST + SSE API for the Toasters server. Every documented endpoint corresponds to a real `mux.HandleFunc(...)` line in `registerRoutes` (`internal/server/server.go`), and every registered route is documented here. The server exposes this API; `RemoteClient` (`internal/client/`) consumes it as a drop-in replacement for the in-process `LocalService`.

The authoritative sources, if this document ever drifts:

- `internal/server/server.go` — `registerRoutes` (the route table)
- `internal/server/handlers.go` — handler implementations (params, bodies)
- `internal/server/types.go` — wire request/response types and SSE payload types
- `internal/server/helpers.go` — `mapServiceError` (error → HTTP status)
- `internal/service/events.go` — SSE event types and payloads
- `internal/server/middleware.go` + `internal/auth/` — auth and middleware

---

## Table of Contents

1. [General Conventions](#1-general-conventions)
2. [Authentication](#2-authentication)
3. [Error Handling](#3-error-handling)
4. [Pagination](#4-pagination)
5. [Async Operations](#5-async-operations)
6. [Endpoints: Operator](#6-endpoints-operator)
7. [Endpoints: Skills](#7-endpoints-skills)
8. [Endpoints: Graphs](#8-endpoints-graphs)
9. [Endpoints: Jobs](#9-endpoints-jobs)
10. [Endpoints: Tasks](#10-endpoints-tasks)
11. [Endpoints: Sessions](#11-endpoints-sessions)
12. [Endpoints: System](#12-endpoints-system)
13. [SSE Event Stream](#13-sse-event-stream)
14. [Middleware](#14-middleware)

---

## 1. General Conventions

| Convention | Value |
|---|---|
| Default bind address | `127.0.0.1:8421` (loopback only; `--addr` to change) |
| Base path | `/api/v1` |
| Content-Type (request) | `application/json` for all request bodies |
| Content-Type (response) | `application/json`, except `204 No Content` and SSE |
| Content-Type (SSE) | `text/event-stream` |
| Character encoding | UTF-8 |
| Date format | RFC 3339 (`2026-06-28T12:00:00Z`) |
| ID format | Opaque strings (UUID v4 or slugified names) |
| HTTP method routing | Go 1.22+ `net/http.ServeMux` with `METHOD /path/{param}` patterns |
| Path parameters | `{name}` syntax via `http.Request.PathValue()` |
| Query parameters | snake_case (`?status=...`, `?limit=...`, `?offset=...`, `?all=true`) |

### Request rules

- Request bodies must be valid JSON. Malformed JSON returns `400 Bad Request` with code `bad_request`.
- Request bodies are capped at 1 MiB (`http.MaxBytesReader`); oversized bodies fail decoding.
- Unknown JSON fields are silently ignored (forward compatibility).
- Path parameters are always required; a missing parameter results in a `404` from the router.

### Response rules

- Successful responses return JSON, except `204 No Content` and the SSE stream.
- `201 Created` responses include a `Location` header for the created resource where applicable.
- `202 Accepted` responses return an async envelope (see [Async Operations](#5-async-operations)).
- Fields tagged `omitempty` are omitted when empty/zero.
- Filesystem paths are never leaked: `WorkspaceDir` and similar fields are tagged `json:"-"` on service DTOs.

---

## 2. Authentication

Implemented by `authMiddleware` (`internal/server/middleware.go`) and the token loader in `internal/auth/token.go`.

- **Scheme:** Bearer token in the `Authorization` header: `Authorization: Bearer <token>`.
- **Token source:** `~/.config/toasters/server.token` (file mode `0600`). The token is generated/loaded on server start.
- **Comparison:** constant-time (`crypto/subtle.ConstantTimeCompare`) to prevent timing attacks.
- **Exempt route:** `GET /api/v1/health` is always reachable without a token (supports liveness probes).
- **Disabled mode:** `toasters serve --no-auth` runs with auth disabled (every request passes through). Dev-only. The server **refuses** to start with `--no-auth` on a non-loopback bind address, and warns when binding to a non-loopback address even with auth on (the token is sniffable over plain HTTP).
- **Failure:** a missing or invalid token on a protected route returns `401 Unauthorized` with code `unauthorized` and message `invalid or missing bearer token`.

CORS is restricted to localhost origins (`localhost`, `127.0.0.1`, `::1`). Requests with no `Origin` header (curl, same-origin, SSE) pass through unconditionally.

---

## 3. Error Handling

All error responses use a single envelope:

```json
{
  "error": {
    "code": "not_found",
    "message": "human-readable description"
  }
}
```

Service-layer errors map to HTTP status codes via `mapServiceError` (`internal/server/helpers.go`), using `errors.Is` against package sentinels in `internal/service/errors.go` (no string matching, except one legacy `invalid provider ID` substring fallback originating in `internal/config`).

| Service sentinel | HTTP status | `code` |
|---|---|---|
| `service.ErrNotFound` | `404 Not Found` | `not_found` |
| `service.ErrConflict` | `409 Conflict` | `conflict` |
| `service.ErrInvalid` | `422 Unprocessable Entity` | `unprocessable_entity` |
| `service.ErrBusy` | `429 Too Many Requests` | `too_many_requests` |
| `service.ErrUnavailable` | `503 Service Unavailable` | `service_unavailable` |
| (any other error) | `500 Internal Server Error` | `internal_error` |

Additional codes produced directly by handlers/middleware (not via `mapServiceError`):

| Status | `code` | When |
|---|---|---|
| `400 Bad Request` | `bad_request` | validation failure, missing required field, malformed JSON, invalid query param |
| `401 Unauthorized` | `unauthorized` | missing/invalid bearer token |
| `429 Too Many Requests` | `too_many_requests` | SSE connection limit (max 10) exceeded |
| `502 Bad Gateway` | `provider_error` | `GET /providers/{id}/models` when the provider is unreachable/misconfigured |
| `500 Internal Server Error` | `internal_error` | SSE flushing not supported by the response writer |

Notes:
- `500` responses log the real error server-side and return a generic `internal server error` message; all other error messages are sanitized via `service.SanitizeErrorMessage` before being returned.
- When an error maps to `too_many_requests` on `POST /skills/generate`, a `Retry-After: 5` header is set.

---

## 4. Pagination

List endpoints that paginate accept two query parameters (`parsePagination` in `helpers.go`):

| Param | Type | Default | Constraints |
|---|---|---|---|
| `limit` | int | `50` | `0`–`200` inclusive |
| `offset` | int | `0` | `>= 0` |

Out-of-range values return `400 bad_request`. Paginated responses use the envelope:

```json
{
  "items": [ ... ],
  "total": 123
}
```

`total` is the full unpaginated count; `items` is the current page. Some list endpoints (operator history, blockers, models, MCP servers, catalog) return all items with `total = len(items)` and do not slice by `limit`/`offset`.

---

## 5. Async Operations

Long-running operations return `202 Accepted` immediately with an operation handle. Completion is delivered later over the [SSE stream](#13-sse-event-stream) as `operation.completed` / `operation.failed` events, correlated by `operation_id`.

```json
{ "operation_id": "op-abc123" }
```

`POST /api/v1/operator/messages` is a special async case: it returns a **turn** handle instead, correlated to `operator.text` / `operator.done` events by `turn_id`:

```json
{ "turn_id": "turn-abc123" }
```

---

## 6. Endpoints: Operator

### POST /api/v1/operator/messages

Send a user message to the operator. Processing is asynchronous; text streams back over SSE (`operator.text`, `operator.done`) keyed by the returned `turn_id`.

- **Request body:**
  ```json
  { "message": "string (required, non-empty, <= 100000 bytes)" }
  ```
- **Response:** `202 Accepted`
  ```json
  { "turn_id": "string" }
  ```
- **Status codes:** `202`, `400` (empty or too-long message), `429` (operator busy → `service.ErrBusy`), `503`.

### POST /api/v1/operator/prompts/{requestId}/respond

Answer a pending blocker (an `ask_user` request raised by the operator or a graph node via `rhizome.Interrupt`). Unblocks the waiting caller.

- **Path params:** `requestId` — the `request_id` from the `blocker.added` event / `GET /operator/blockers`.
- **Request body:**
  ```json
  { "response": "string (required, non-empty, <= 50000 bytes)" }
  ```
- **Response:** `204 No Content`
- **Status codes:** `204`, `400` (missing `requestId`, empty/too-long response), `404` (no such pending request).

### GET /api/v1/operator/status

Current operator state.

- **Response:** `200 OK`
  ```json
  {
    "state": "string",
    "current_turn_id": "string",
    "model_name": "string",
    "endpoint": "string"
  }
  ```

### GET /api/v1/operator/history

Full operator chat transcript.

- **Response:** `200 OK` — `PaginatedResponse<ChatEntry>` (all entries; `total = len(items)`).
  ```json
  {
    "items": [
      {
        "message": {
          "role": "string",
          "content": "string",
          "tool_calls": [ { "id": "string", "name": "string", "arguments": { } } ],
          "tool_call_id": "string"
        },
        "timestamp": "2026-06-28T12:00:00Z",
        "reasoning": "string",
        "claude_meta": "string"
      }
    ],
    "total": 0
  }
  ```

### GET /api/v1/operator/blockers

List currently pending blockers (ask_user requests awaiting a human response).

- **Response:** `200 OK` — `PaginatedResponse<Blocker>` (all blockers; `total = len(items)`).
  ```json
  {
    "items": [
      {
        "request_id": "string",
        "source": "string (empty=operator, e.g. \"graph:investigate\")",
        "job_id": "string",
        "task_id": "string",
        "questions": [ { "question": "string", "options": ["string"] } ],
        "created_at": "2026-06-28T12:00:00Z"
      }
    ],
    "total": 0
  }
  ```

### PUT /api/v1/operator/provider

Set the provider and model the operator runs on. (Registered in the System block of the route table, but operates on operator config.)

- **Request body:**
  ```json
  { "provider_id": "string", "model": "string" }
  ```
- **Response:** `200 OK`
  ```json
  { "status": "ok" }
  ```
- **Status codes:** `200`, `400`, `404`, `422`.

---

## 7. Endpoints: Skills

### GET /api/v1/skills

List skill definitions.

- **Query params:** `limit`, `offset` (paginated).
- **Response:** `200 OK` — `PaginatedResponse<Skill>`.
  ```json
  {
    "items": [
      {
        "id": "string",
        "name": "string",
        "description": "string",
        "tools": ["string"],
        "prompt": "string",
        "source": "string",
        "created_at": "2026-06-28T12:00:00Z",
        "updated_at": "2026-06-28T12:00:00Z"
      }
    ],
    "total": 0
  }
  ```

### GET /api/v1/skills/{id}

Get a single skill by ID.

- **Path params:** `id`.
- **Response:** `200 OK` — a `Skill` object (shape as above).
- **Status codes:** `200`, `404`.

### POST /api/v1/skills

Create a new (empty) skill with the given name.

- **Request body:**
  ```json
  { "name": "string (required, non-empty)" }
  ```
- **Response:** `201 Created` — the created `Skill`; `Location: /api/v1/skills/{id}` header set.
- **Status codes:** `201`, `400`, `409` (name conflict), `422`.

### DELETE /api/v1/skills/{id}

Delete a skill.

- **Path params:** `id`.
- **Response:** `204 No Content`.
- **Status codes:** `204`, `404`.

### POST /api/v1/skills/generate

Generate a skill from a natural-language prompt (async, LLM-backed).

- **Request body:**
  ```json
  { "prompt": "string (required, non-empty, <= 10000 bytes)" }
  ```
- **Response:** `202 Accepted` — `{ "operation_id": "string" }`. Result arrives via `operation.completed` / `operation.failed` SSE events.
- **Status codes:** `202`, `400`, `429` (sets `Retry-After: 5`), `503`.

---

## 8. Endpoints: Graphs

Graphs are declarative rhizome graph definitions. They are read-only over the API.

### GET /api/v1/graphs

List graph definitions.

- **Query params:** `limit`, `offset` (paginated).
- **Response:** `200 OK` — `PaginatedResponse<GraphDefinition>`.
  ```json
  {
    "items": [
      {
        "id": "string",
        "name": "string",
        "description": "string",
        "tags": ["string"],
        "entry": "string",
        "exit": "string",
        "nodes": ["string"],
        "edges": [ { "from": "string", "to": "string", "kind": "string", "label": "string" } ]
      }
    ],
    "total": 0
  }
  ```

### GET /api/v1/graphs/{id}

Get a single graph definition.

- **Path params:** `id`.
- **Response:** `200 OK` — a `GraphDefinition` (shape as above).
- **Status codes:** `200`, `404`.

---

## 9. Endpoints: Jobs

### GET /api/v1/jobs

List jobs, with optional filtering.

- **Query params:**
  - `all=true` — return every job (bypasses pagination and filters; `total = len(items)`).
  - `status` — one of: `pending`, `setting_up`, `decomposing`, `active`, `paused`, `completed`, `failed`, `cancelled`. Invalid values return `400`.
  - `type` — free-form job type string.
  - `limit`, `offset` — pagination (ignored when `all=true`).
- **Response:** `200 OK` — `PaginatedResponse<Job>`.
  ```json
  {
    "items": [
      {
        "id": "string",
        "title": "string",
        "description": "string",
        "type": "string",
        "status": "string",
        "created_at": "2026-06-28T12:00:00Z",
        "updated_at": "2026-06-28T12:00:00Z",
        "metadata": { }
      }
    ],
    "total": 0
  }
  ```
  (`total` is computed by re-running the filter unpaginated.)

### GET /api/v1/jobs/{id}

Get a job with its tasks and progress reports.

- **Path params:** `id`.
- **Response:** `200 OK` — `JobDetail`:
  ```json
  {
    "job": { /* Job, as above */ },
    "tasks": [
      {
        "id": "string",
        "job_id": "string",
        "title": "string",
        "status": "string",
        "worker_id": "string",
        "graph_id": "string",
        "parent_id": "string",
        "sort_order": 0,
        "created_at": "2026-06-28T12:00:00Z",
        "updated_at": "2026-06-28T12:00:00Z",
        "summary": "string",
        "result_summary": "string",
        "recommendations": "string",
        "metadata": { }
      }
    ],
    "progress": [
      {
        "id": 0,
        "job_id": "string",
        "task_id": "string",
        "worker_id": "string",
        "status": "string",
        "message": "string",
        "created_at": "2026-06-28T12:00:00Z"
      }
    ]
  }
  ```
- **Status codes:** `200`, `404`.

### POST /api/v1/jobs/{id}/cancel

Cancel a job.

- **Path params:** `id`.
- **Response:** `204 No Content`.
- **Status codes:** `204`, `404`, `409`/`422` (invalid state).

---

## 10. Endpoints: Tasks

### POST /api/v1/tasks/{id}/retry

Retry a failed task.

- **Path params:** `id`.
- **Response:** `204 No Content`.
- **Status codes:** `204`, `404`, `409`/`422` (task not in a retryable state).

---

## 11. Endpoints: Sessions

Sessions are worker (and operator) LLM conversation runs.

### GET /api/v1/sessions

List session snapshots.

- **Query params:** `limit`, `offset` (paginated).
- **Response:** `200 OK` — `PaginatedResponse<SessionSnapshot>`.
  ```json
  {
    "items": [
      {
        "id": "string",
        "worker_id": "string",
        "job_id": "string",
        "task_id": "string",
        "status": "string",
        "model": "string",
        "provider": "string",
        "start_time": "2026-06-28T12:00:00Z",
        "tokens_in": 0,
        "tokens_out": 0
      }
    ],
    "total": 0
  }
  ```

### GET /api/v1/sessions/{id}

Get a session with its prompt, output, and activity.

- **Path params:** `id`.
- **Response:** `200 OK` — `SessionDetail`:
  ```json
  {
    "snapshot": { /* SessionSnapshot, as above */ },
    "system_prompt": "string",
    "initial_message": "string",
    "output": "string",
    "activities": [ { "label": "string", "tool_name": "string" } ],
    "worker_name": "string",
    "task": "string"
  }
  ```
- **Status codes:** `200`, `404`.

### POST /api/v1/sessions/{id}/cancel

Cancel a running session.

- **Path params:** `id`.
- **Response:** `204 No Content`.
- **Status codes:** `204`, `404`, `409`/`422`.

---

## 12. Endpoints: System

### GET /api/v1/health

Liveness/health check. **Exempt from authentication.**

- **Response:** `200 OK`
  ```json
  { "status": "string", "version": "string", "uptime_seconds": 0.0 }
  ```

### GET /api/v1/logs

Fetch the server log buffer.

- **Response:** `200 OK`
  ```json
  { "content": "string" }
  ```

### GET /api/v1/models

List models available to the operator/runtime.

- **Response:** `200 OK` — `PaginatedResponse<ModelInfo>` (all models; `total = len(items)`).
  ```json
  {
    "items": [
      {
        "id": "string",
        "name": "string",
        "provider": "string",
        "state": "string",
        "max_context_length": 0,
        "loaded_context_length": 0
      }
    ],
    "total": 0
  }
  ```

### GET /api/v1/catalog

List the provider/model catalog (known providers and their models).

- **Response:** `200 OK` — `PaginatedResponse<CatalogProvider>`.
  ```json
  {
    "items": [
      {
        "id": "string",
        "name": "string",
        "api": "string",
        "doc": "string",
        "env": ["string"],
        "models": [
          {
            "id": "string",
            "name": "string",
            "family": "string",
            "tool_call": false,
            "reasoning": false,
            "structured_output": false,
            "open_weights": false,
            "context_limit": 0,
            "output_limit": 0,
            "input_cost": 0.0,
            "output_cost": 0.0
          }
        ]
      }
    ],
    "total": 0
  }
  ```

### POST /api/v1/providers

Add (configure) a provider.

- **Request body:**
  ```json
  {
    "id": "string",
    "name": "string",
    "type": "string",
    "endpoint": "string",
    "api_key": "string"
  }
  ```
- **Response:** `201 Created` — `{ "status": "ok" }`.
- **Status codes:** `201`, `400`, `422` (invalid provider ID), `409`.

### PUT /api/v1/providers

Update an existing provider. Same request body as `POST /api/v1/providers`.

- **Response:** `200 OK` — `{ "status": "ok" }`.
- **Status codes:** `200`, `400`, `404`, `422`.

### GET /api/v1/providers/configured

List the IDs of all configured providers.

- **Response:** `200 OK` — a JSON array of strings:
  ```json
  ["anthropic", "openai", "lmstudio"]
  ```

### GET /api/v1/providers/{id}/models

List the models offered by a specific configured provider (queries the provider live).

- **Path params:** `id`.
- **Response:** `200 OK` — `PaginatedResponse<ModelInfo>` (shape as `GET /models`).
- **Status codes:** `200`, `502` (`provider_error`) when the provider is unreachable or misconfigured.

### GET /api/v1/mcp/servers

List configured MCP servers and their tools.

- **Response:** `200 OK` — `PaginatedResponse<MCPServerStatus>`.
  ```json
  {
    "items": [
      {
        "name": "string",
        "transport": "string",
        "state": "string",
        "error": "string",
        "tool_count": 0,
        "tools": [
          {
            "namespaced_name": "string",
            "original_name": "string",
            "server_name": "string",
            "description": "string",
            "input_schema": { }
          }
        ]
      }
    ],
    "total": 0
  }
  ```

### GET /api/v1/progress

Snapshot of the full orchestration progress state (the same payload pushed via the `progress.update` SSE event).

- **Response:** `200 OK` — `ProgressState`:
  ```json
  {
    "jobs": [ /* Job[] */ ],
    "tasks": { "<job_id>": [ /* Task[] */ ] },
    "reports": { "<job_id>": [ /* ProgressReport[] */ ] },
    "active_sessions": [
      {
        "id": "string",
        "worker_id": "string",
        "job_id": "string",
        "task_id": "string",
        "status": "string",
        "model": "string",
        "provider": "string",
        "tokens_in": 0,
        "tokens_out": 0,
        "started_at": "2026-06-28T12:00:00Z",
        "ended_at": "2026-06-28T12:00:00Z",
        "cost_usd": 0.0
      }
    ],
    "live_snapshots": [ /* SessionSnapshot[] */ ],
    "active_graph_nodes": [
      { "session_id": "string", "job_id": "string", "task_id": "string", "node": "string", "started_at": "2026-06-28T12:00:00Z" }
    ],
    "feed_entries": [
      { "id": 0, "job_id": "string", "entry_type": "string", "content": "string", "metadata": { }, "created_at": "2026-06-28T12:00:00Z" }
    ]
  }
  ```

### GET /api/v1/settings

Get server settings.

- **Response:** `200 OK` — a `service.Settings` object (serialized as-is).

### PUT /api/v1/settings

Update server settings.

- **Request body:** a `service.Settings` object.
- **Response:** `200 OK` — `{ "status": "ok" }`.
- **Status codes:** `200`, `400` (validation failure — settings validation errors are user-actionable and returned as `bad_request`).

---

## 13. SSE Event Stream

### GET /api/v1/events

Server-Sent Events stream of the unified service event feed. A single connection receives every event type below.

- **Response headers:** `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`, `X-Accel-Buffering: no`.
- **Connection limit:** max 10 concurrent SSE connections; exceeding it returns `429 too_many_requests`.
- **Heartbeats:** a `heartbeat` event is emitted every 15 seconds to keep the connection alive through proxies.
- **Resume:** clients may send a `Last-Event-ID` header (the numeric `seq` of the last event they saw). The server replays buffered events newer than that seq from an in-memory ring, then continues live. Events older than the ring are not replayed — clients should resync full state via REST after a long disconnect.

### Wire format

Each event is written in standard SSE framing:

```
id: <seq>
event: <type>
data: <json-envelope>

```

The `data` line is a JSON envelope:

```json
{
  "seq": 0,
  "type": "string",
  "timestamp": "2026-06-28T12:00:00Z",
  "turn_id": "string",
  "session_id": "string",
  "operation_id": "string",
  "payload": { }
}
```

- `seq` — monotonically increasing global sequence number (used for dedupe and `Last-Event-ID` resume). A `seq` of `0` denotes a synthetic/test event delivered unconditionally.
- `turn_id` — set on operator events (correlates to the `POST /operator/messages` turn). Omitted otherwise.
- `session_id` — set on `session.*` events. Omitted otherwise.
- `operation_id` — set on `operation.*` events. Omitted otherwise.
- `payload` — shape depends on `type` (see below). `null` for `definitions.reloaded`.

### Event types and payloads

| `type` | Payload fields | Notes |
|---|---|---|
| `operator.text` | `text`, `reasoning` | Batched operator text tokens. Carries `turn_id`. |
| `operator.tool_call` | `name`, `args`, `result`, `is_error` | Operator invoked one of its tools. |
| `operator.done` | `model_name`, `tokens_in`, `tokens_out`, `reasoning_tokens` | Operator finished a turn. Carries `turn_id`. |
| `blocker.added` | `request_id`, `source`, `job_id`, `task_id`, `questions[]` (`question`, `options[]`), `created_at` | An `ask_user` request is awaiting a human response. |
| `blocker.resolved` | `request_id` | A pending blocker was answered or cancelled. |
| `job.created` | `job_id`, `title`, `description` | |
| `task.created` | `task_id`, `job_id`, `title`, `graph_id` | `graph_id` may be empty. |
| `task.assigned` | `task_id`, `job_id`, `graph_id`, `title` | |
| `task.started` | `task_id`, `job_id`, `graph_id`, `title` | |
| `task.completed` | `task_id`, `job_id`, `graph_id`, `summary`, `recommendations`, `has_next_task` | |
| `task.failed` | `task_id`, `job_id`, `graph_id`, `error` | |
| `job.completed` | `job_id`, `title`, `summary`, `status`, `workspace`, `started_at`, `ended_at`, `tasks_total`, `tasks_completed`, `tasks_failed`, `tokens_in`, `tokens_out`, `cost_usd`, `files_touched[]` (`path`, `size`, `is_new`), `files_touched_extra` | Fires when all tasks reach a terminal state; `status` may be `failed`. |
| `progress.update` | `state` (full `ProgressState`, see `GET /progress`) | Replaces the legacy SQLite polling loop. |
| `session.started` | `session_id`, `worker_name`, `task`, `job_id`, `task_id`, `system_prompt`, `initial_message` | Carries `session_id`. |
| `session.text` | `text` | Worker text tokens. Carries `session_id`. |
| `session.reasoning` | `text` | Worker chain-of-thought chunk. Carries `session_id`. |
| `session.tool_call` | `tool_call` (`id`, `name`, `arguments`) | Carries `session_id`. |
| `session.tool_result` | `result` (`call_id`, `name`, `result`, `error`) | Carries `session_id`. |
| `session.done` | `worker_name`, `job_id`, `task_id`, `status`, `final_text` | `status` is `completed`/`failed`/`cancelled`. Carries `session_id`. |
| `session.prompt` | `session_id`, `system_prompt`, `initial_message` | Fills prompt fields on an existing slot (graph-node sessions). |
| `session.meta` | `session_id`, `model`, `provider`, `temperature`, `thinking` | Resolved model/sampling settings for a session. |
| `definitions.reloaded` | (none — `payload` is `null`) | Definition files changed on disk and were reloaded. |
| `operation.completed` | `kind`, `result` (`operation_id`, `content`, `error`) | Async operation finished. Carries `operation_id`. |
| `operation.failed` | `kind`, `error` | Async operation failed. Carries `operation_id`. |
| `heartbeat` | `server_time` | Keepalive, every 15s. |
| `graph.node_started` | `job_id`, `task_id`, `node` | A rhizome graph node began. |
| `graph.node_completed` | `job_id`, `task_id`, `node`, `status` | A rhizome graph node finished. |
| `graph.completed` | `job_id`, `task_id`, `summary` | A rhizome graph finished successfully. |
| `graph.failed` | `job_id`, `task_id`, `error` | A rhizome graph execution failed. |

**Client-only events** (synthesized by `RemoteClient`, never sent by the server, but defined in `service.EventType` for the unified stream):

| `type` | Payload fields | Notes |
|---|---|---|
| `connection.lost` | `error` | SSE connection to the server dropped. |
| `connection.restored` | (empty object) | SSE connection re-established. |

---

## 14. Middleware

The middleware stack (applied in `Server.Start`, outermost first) is:

`recovery → request ID → auth → logging → CORS → security headers → content-type`

- **Recovery** — recovers from handler panics, returns `500`.
- **Request ID** — sets/propagates `X-Request-ID` (validated: <= 64 chars, alphanumeric + `-_.`); included in logs and surfaced on responses.
- **Auth** — bearer-token enforcement (see [Authentication](#2-authentication)); exempts `GET /api/v1/health`.
- **Logging** — structured per-request logging (method, path, status, duration, request ID).
- **CORS** — localhost origins only; handles `OPTIONS` preflight with `204`.
- **Security headers** — defensive response headers.
- **Content-Type** — enforces/normalizes `application/json` handling.

Server timeouts: `ReadHeaderTimeout` 10s, `WriteTimeout` 30s, `IdleTimeout` 120s. Each individual SSE event write has a rolling 30s deadline.

---

## Endpoint summary

33 routes registered in `registerRoutes`:

- **Operator (6):** `POST /operator/messages`, `POST /operator/prompts/{requestId}/respond`, `GET /operator/status`, `GET /operator/history`, `GET /operator/blockers`, `PUT /operator/provider`
- **Skills (5):** `GET /skills`, `GET /skills/{id}`, `POST /skills`, `DELETE /skills/{id}`, `POST /skills/generate`
- **Graphs (2):** `GET /graphs`, `GET /graphs/{id}`
- **Jobs (3):** `GET /jobs`, `GET /jobs/{id}`, `POST /jobs/{id}/cancel`
- **Tasks (1):** `POST /tasks/{id}/retry`
- **Sessions (3):** `GET /sessions`, `GET /sessions/{id}`, `POST /sessions/{id}/cancel`
- **System (12):** `GET /health`, `GET /logs`, `GET /models`, `GET /catalog`, `POST /providers`, `PUT /providers`, `GET /providers/configured`, `GET /providers/{id}/models`, `GET /mcp/servers`, `GET /progress`, `GET /settings`, `PUT /settings`
- **SSE (1):** `GET /events`
