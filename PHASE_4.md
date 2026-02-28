# Phase 4: Intelligence & Infrastructure — Roadmap

**Created:** 2026-02-25
**Status:** Future

---

## Objective

Make the system smarter and more capable — cost visibility, external service integration via OpenAPI bridges, multi-repo ecosystems, operator memory, job personas, and a server/client architecture split for resilience. This phase collects items deferred from Phase 3 alongside the original Phase 4 vision.

---

## Deliverables

### 4.1 — Cost Estimation

*Deferred from Phase 3.*

Token usage is already tracked per-session with model identity. The `CostUSD *float64` field exists in the DB schema but is never populated.

**What's needed:**
- A pricing table — `map[string]ModelPricing` keyed by model name, containing per-input-token and per-output-token rates. Could be hardcoded for known models, configurable in YAML for custom/local models, or both.
- A ~10-line cost calculation at session completion in `runtime.go` — `cost := pricing.Estimate(provider, model, tokensIn, tokensOut)`
- TUI display — extend the agent session panel to show estimated cost per session and a running total

**Open questions:**
- Where does pricing data live? Config YAML? Hardcoded table? Fetched from provider APIs?
- How to handle models with tiered pricing (e.g., cached vs. uncached input tokens)?
- Should we show cost in real-time (per-poll-cycle) or only at session completion?
- How to handle local models (LM Studio, Ollama) where cost is effectively $0?

---

### 4.2 — Ephemeral OpenAPI-to-MCP Bridges

*Deferred from Phase 3.*

**Effort:** 3–4 days
**Depends on:** 2.1 (MCP client)
**Unlocks:** Agents can query your actual backend services

**What to build:**

A `internal/mcp/openapi` package that:

1. **Parses OpenAPI v3.x specs** — Extract operations, parameters, request bodies, response schemas. Support both JSON and YAML specs, loaded from URL or file path.

2. **Converts operations to MCP tools** — Each operation becomes a tool. Tool name from `operationId` (or `method_path` fallback). Input schema from parameters + request body. Description from summary/description.

3. **Runs an ephemeral HTTP server** — Receives MCP `tools/call` requests, translates to HTTP requests against the actual backend, injects auth, returns the response.

4. **Lifecycle management** — Bridges are created on demand (when a job or ecosystem needs them) and torn down when no longer needed. Registered with the MCP manager as dynamic server entries.

**Scope limitations for v1:**
- JSON request/response bodies only (no multipart, no streaming)
- Path parameters, query parameters, and request body supported
- Auth: Bearer token, API key (header or query), Basic auth
- No OAuth flows (static credentials only)
- No webhooks or WebSocket endpoints

**Acceptance criteria:**
- Given an OpenAPI spec URL and credentials, a bridge MCP server starts and registers its tools.
- Agents can call the bridge tools and receive actual HTTP responses from the backend.
- Auth headers are injected correctly.
- The bridge shuts down cleanly when the job/ecosystem completes.
- Invalid or unsupported operations are skipped with warnings.

---

### 4.3 — Server/Client Architecture Split

**Effort:** 5–7 days
**Depends on:** Phases 1–3 (everything should be stable first)
**Unlocks:** Resilience, multiple clients, remote operation

**What to build:**

Extract the orchestration engine into a long-running server process. The TUI becomes a thin client.

1. **Server process** — Owns: SQLite database, agent runtime, MCP connections, job lifecycle, provider connections. Exposes a gRPC (or WebSocket) API for clients.

2. **Client API:**
   - `SubmitJob(request)` → job ID
   - `GetJob(id)` → job state
   - `ListJobs(filter)` → job list
   - `SubscribeJobEvents(id)` → stream of progress events
   - `SubscribeAllEvents()` → stream of all events (for TUI dashboard)
   - `SendMessage(message)` → operator response
   - `CancelJob(id)`
   - `GetActiveSessions()` → session snapshots

3. **TUI client** — Connects to the server, subscribes to events, renders state, sends commands. All business logic removed from the TUI.

4. **CLI client** — Simple command-line client for submitting jobs and checking status without the TUI.

**Acceptance criteria:**
- Server starts independently and persists across TUI restarts.
- TUI connects to a running server and displays current state.
- Jobs survive TUI crashes.
- CLI client can submit jobs and query status.
- Server handles graceful shutdown (cancels agents, closes MCP connections, closes database).

---

### 4.4 — Ephemeral Ecosystems

**Effort:** 3–4 days
**Depends on:** 1.1 (SQLite), 1.3 (agent runtime), 2.1 (MCP client)
**Unlocks:** Multi-repo work

**What to build:**

1. **Ecosystem definition** — A job can declare "I need repos X, Y, Z." Stored in SQLite.

   ```yaml
   ecosystem:
     repos:
       - url: git@github.com:company/api-service.git
         role: api  # human-readable role label
       - url: git@github.com:company/frontend.git
         role: frontend
       - url: git@github.com:company/shared-contracts.git
         role: contracts
   ```

2. **Workspace setup** — Clone repos into a workspace directory (e.g. `~/.config/toasters/workspaces/{ecosystem-id}/`). Each repo gets its own subdirectory.

3. **Cross-repo context** — Agents receive context about the ecosystem: which repos exist, what role each plays, how they relate. This is injected into the agent's system prompt.

4. **Cleanup** — When a job completes, the workspace can be archived or deleted (configurable).

**Acceptance criteria:**
- A job can declare an ecosystem with multiple repos.
- Repos are cloned into a workspace directory.
- Agents receive ecosystem context and can work across repos.
- Workspaces are cleaned up when jobs complete.

---

### 4.5 — Long-Lived Ecosystems

**Effort:** 4–5 days
**Depends on:** 4.4 (ephemeral ecosystems), 4.2 (OpenAPI bridges)
**Unlocks:** Persistent knowledge of your engineering surface

**What to build:**

1. **Persistent ecosystem definitions** — Stored in SQLite. Define services, their repos, their APIs (OpenAPI specs), their relationships, and their MCP servers.

   ```yaml
   name: backend
   description: Company backend services
   services:
     - name: user-service
       repo: git@github.com:company/user-service.git
       api_spec: https://api.company.com/user/openapi.json
       description: User management, auth, profiles
       depends_on: [database, cache]
     - name: order-service
       repo: git@github.com:company/order-service.git
       api_spec: https://api.company.com/orders/openapi.json
       depends_on: [user-service, database, payment-gateway]
     - name: database
       type: infrastructure
       mcp_server: postgres  # references a configured MCP server
   ```

2. **On-demand loading** — Ecosystems are loaded into memory when needed (not all at startup). The operator can query ecosystem metadata without loading full repo clones.

3. **Knowledge queries** — The operator can ask "which services would be affected if I change the user ID format?" and get an answer based on the dependency graph and service descriptions.

4. **Auto-bridge setup** — When a job references an ecosystem, OpenAPI bridges are automatically spun up for the relevant services.

**Acceptance criteria:**
- Ecosystems can be defined and persisted.
- The operator can query ecosystem metadata.
- Dependency graphs are navigable.
- OpenAPI bridges are auto-created for ecosystem services.

---

### 4.6 — Operator Memory

**Effort:** 2–3 days
**Depends on:** 1.1 (SQLite)
**Unlocks:** The operator gets smarter over time

**What to build:**

1. **Memory storage** — A `memories` table in SQLite:

   ```sql
   CREATE TABLE memories (
       id          INTEGER PRIMARY KEY AUTOINCREMENT,
       type        TEXT NOT NULL,  -- job_outcome, team_performance, repo_quirk, pattern
       content     TEXT NOT NULL,  -- structured JSON
       relevance   TEXT,           -- tags for retrieval (e.g. "auth,security,user-service")
       created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
   );
   ```

2. **Memory capture** — When a job completes, distill key learnings:
   - Which team was assigned? Did it succeed?
   - Were there blockers? What resolved them?
   - Which repos were involved? Any quirks discovered?
   - How long did it take? What was the cost?

3. **Memory retrieval** — When the operator dispatches a new job, query for relevant memories based on job type, repos involved, and keywords. Inject the top N memories into the operator's system prompt.

4. **Memory decay** — Old memories are weighted less. Contradicted memories (e.g. "this team fails at X" followed by "this team succeeded at X") are reconciled.

**Acceptance criteria:**
- Job outcomes are automatically captured as memories.
- Relevant memories are retrieved and injected into the operator's context.
- The operator's dispatching decisions improve based on past experience.
- Memory storage grows bounded (old/irrelevant memories are pruned).

---

### 4.7 — Job Personas

**Effort:** 2–3 days
**Depends on:** 1.3 (agent runtime), 4.6 (operator memory)
**Unlocks:** Queryable job state without re-reading artifacts

**What to build:**

1. **Persona session** — Each active job gets a dedicated LLM session (lightweight, cheap model) that accumulates context as the job progresses. Fed with: job overview, task updates, progress reports, blocker alerts.

2. **Queryable** — The operator can ask a job persona: "what's your current status?", "what's blocking you?", "summarize what you've done so far." The persona answers from its accumulated context.

3. **Knowledge distillation** — When a job completes, the persona produces a structured summary that feeds into operator memory (4.6).

**Acceptance criteria:**
- Active jobs have a persona session that tracks progress.
- The operator can query personas and get informed answers.
- Completed job personas produce summaries for operator memory.
- Persona sessions use a cheap model (not the expensive agent model).

---

## Other Deferred Items

Items deferred from earlier phases that may be addressed in Phase 4 or later:

| Item | Origin | Notes |
|------|--------|-------|
| Event-driven TUI updates (replace polling) | Phase 3 | 500ms polling is sufficient for now |
| MCP server HTTP transport | Phase 3 | stdio is sufficient; HTTP needed only for non-Claude external agents |
| MCP resource/prompt support | Phase 3 | Only MCP tools are consumed; resources and prompts are future scope |
| Remove Claude CLI subprocess fallback | Phase 3 | Keep as-is in case it's still useful |
| Async `consult_agent` in operator event loop | Phase 3 review | `handleUserMessage` blocks the event loop during LLM calls (30-60s). Consider spawning consultations in goroutines |
| ~~Auto-team re-import on restart~~ | ~~Bug fix (2026-02-27)~~ | ✅ Fixed in pre-Phase 4 Wave 4 — `.dismissed/<name>` marker files persist dismiss state |
| ~~`workspace_dir` accepts any directory~~ | ~~Phase 3 review~~ | ✅ Fixed in pre-Phase 4 Wave 4 — `create_job` and `assign_task` validate workspace is under `$HOME` |

---

## Delivery Sequence (Tentative)

```
Wave 1 — Quick wins:
  4.1 (Cost estimation) ──────────►  done
  4.6 (Operator memory) ─────────►  done
       (can be parallel)

Wave 2 — External integration:
  4.2 (OpenAPI bridges) ─────────────────►  done
  4.4 (Ephemeral ecosystems) ────────────►  done
       (can be parallel)

Wave 3 — Advanced:
  4.5 (Long-lived ecosystems) ───────────►  done
       depends on 4.2, 4.4
  4.7 (Job personas) ────────────►  done
       depends on 4.6

Wave 4 — Architecture:
  4.3 (Server/client split) ─────────────────────►  done
       depends on everything being stable
```

---

## Dependency Graph

```
4.1 Cost Estimation          (standalone)
4.2 OpenAPI Bridges          (standalone) ──► 4.5 Long-Lived Ecosystems
4.3 Server/Client Split      (depends on Phases 1–3 stable)
4.4 Ephemeral Ecosystems     (standalone) ──► 4.5 Long-Lived Ecosystems
4.6 Operator Memory          (standalone) ──► 4.7 Job Personas
```

---

## Phase 4 Exit Criteria

Toasters runs as a persistent server. The TUI and CLI connect as clients. Jobs survive crashes. The operator dispatches multi-repo work across ecosystems, remembers past outcomes, and agents can query your backend services via OpenAPI bridges. Job personas provide queryable state. Cost is tracked and visible.
