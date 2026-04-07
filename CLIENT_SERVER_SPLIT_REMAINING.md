## Comprehensive Analysis: Client-Server Split Status

### ✅ What's Working Well

**TUI decoupling is complete.** Zero violations — the `internal/tui/` package imports exactly one internal package (`internal/service`). All model state uses service DTOs. All 24 service methods are called through the interface. This is a genuinely clean thin client.

**Operator event flow works end-to-end.** `OnText` → batcher → `BroadcastOperatorText()` → SSE → `ConsumeServiceEvents()` → `OperatorTextMsg` is fully wired for all three operator callbacks. Remote clients see streaming text, task lifecycle events (completed/failed/blocker), and job completion in real-time.

**Server and client infrastructure is solid.** Auth (bearer tokens, 0600 files, constant-time comparison), SSE with reconnection (exponential backoff 1s→30s), connection status events (`connection.lost`/`connection.restored`), and CORS/security headers are all implemented and tested.

**All 21 packages pass tests with `-race`.**

---

### 🔴 CRITICAL: Session Events Missing in Server Mode (P0)

This is the single biggest gap and explains the "unexpected TUI behaviors" you're seeing.

**What's wrong:** When running as `toasters serve`, the runtime's `OnSessionStarted` callback is set to a **no-op** (`serve.go:166`). This means:

- Agent sessions start, run, and complete with **zero events** reaching SSE clients
- A remote TUI (`toasters --server addr`) sees **no real-time agent output** — no text streaming, no tool calls, no session completion
- The 5 `session.*` event types (`session.started`, `session.text`, `session.tool_call`, `session.tool_result`, `session.done`) are fully defined, serialized, and deserialized but **never produced**

**Why:** In embedded mode (`toasters`), session events bypass the service event stream entirely — `cmd/root.go` wires `rt.OnSessionStarted` directly to `prog.Send(RuntimeSessionStartedMsg/EventMsg/DoneMsg)` via Go closures. The `event_consumer.go` explicitly returns `nil` for session events with a "Phase 2" comment acknowledging this is deferred.

**What needs to happen:** Wire the runtime's `OnSessionStarted` callback in `serve.go` to call a new `LocalService.BroadcastSessionStarted(sess)` method, which subscribes to `sess.Subscribe()` and broadcasts `session.text/tool_call/tool_result/done` events through the service event stream. The TUI event consumer already has handlers for these (currently returning `nil`) that need to be re-enabled.

**Impact if not fixed:** A remote TUI cannot see agent sessions at all — it only gets stale snapshots from the 500ms `progress.update` poll, and those disappear the instant a session completes.

---

### 🟡 HIGH: Progress is Still 500ms Polling, Not Push (P1)

Despite comments saying it "replaces the 500ms SQLite polling loop," `progress.update` **is** a 500ms polling loop. `LocalService.progressPollLoop()` queries SQLite for jobs, tasks, sessions, feed entries, and MCP status every 500ms and broadcasts the full snapshot.

**Problems:**
- Creates 2 SSE events/second per client even when nothing changes
- Full state snapshot is wasteful over HTTP (should be deltas)
- State changes from the operator, runtime, and MCP tools are only visible after the next poll tick

---

### 🟡 HIGH: Dead Event Types — Defined But Never Emitted (P1)

| Event Type | Status |
|---|---|
| `operator.prompt` | Never emitted — `ask_user` flow is unwired |
| `task.assigned` | Never emitted — `assignTask` creates a feed entry but no event |
| `session.started` | Never emitted through service stream |
| `session.text` | Never emitted through service stream |
| `session.tool_call` | Never emitted through service stream |
| `session.session.tool_result` | Never emitted through service stream |
| `session.done` | Never emitted through service stream |

---

### 🟡 MEDIUM: Stubs and Incomplete Implementations (P2)

| Item | Location | Status |
|---|---|---|
| Token counts always zero | `BroadcastOperatorDone(0, 0, 0)` | Hardcoded — `operator.Config.OnTurnDone` signature doesn't include tokens |
| Operator status hardcoded to idle | `OperatorService.Status()` | Always returns `OperatorStateIdle` |
| `SessionDetail.Activities` is nil | `localSessionService.Get()` | Never populated |
| Job creation has no event | `SystemTools.createJob` | Writes to DB, no event |
| MCP progress tools emit no events | `internal/progress/` | External agent status changes invisible until next poll |
| Duplicate heartbeats | `LocalService.heartbeatLoop` + `sse.go` | SSE clients get double heartbeats |

---

### 🟡 MEDIUM: `cmd/` Package Still Has Heavy Direct Wiring (P2)

Both `cmd/root.go` and `cmd/serve.go` import 10+ internal packages directly. This is the expected pattern for a wiring layer, but several direct bypasses exist:

| Bypass | What It Does |
|---|---|
| `rt.OnSessionStarted = closure` | Sets runtime callback directly (not through service) |
| `op.Start(opCtx)` | Starts operator directly (no `svc.Operator().Start()`) |
| `composer.Compose("operator", "system")` | Composes operator prompt directly |
| `generateTeamAwareness(provider)` | Calls `provider.ChatCompletion()` directly for LLM |
| `loader.NewWatcher(ldr, onChange)` | Passes loader directly to watcher |
| `prog.Send(TeamsReloadedMsg)` | File watcher sends directly to TUI, bypassing service events |

---

### 🟢 LOW: Cosmetic / Future Items (P3)

| Item | Notes |
|---|---|
| `operator.prompt` flow | Interactive prompts not functional — explicit Phase 2 |
| `cmd/awareness.go` imports `internal/provider` | Direct LLM call for team awareness generation |
| Client mode `sendClientModeAppReady` | Sends `TeamsReloadedMsg`/`AppReadyMsg` directly to `prog.Send()` |
| Double `stripCodeFences` | May still exist (was deduplicated in Phase 2 pre-work) |

---

### Recommended Priorities

| Priority | Item | Effort | Impact |
|---|---|---|---|
| **P0** | Wire session events through service stream (fix remote TUI) | 2-3 days | Remote TUI becomes functional |
| **P1** | Replace 500ms polling with push-on-change for progress | 2-3 days | Eliminates wasteful SSE traffic, real-time updates |
| **P1** | Emit `task.assigned` event from `assignTask` | 0.5 day | Remote clients can react to task assignments |
| **P2** | Thread token counts through `OnTurnDone` → `BroadcastOperatorDone` | 1 day | Accurate sidebar stats |
| **P2** | Implement real `OperatorService.Status()` | 0.5 day | Remote clients see operator state |
| **P2** | Move `generateTeamAwareness()` into service layer | 1 day | Eliminates direct provider bypass in cmd/ |
| **P3** | Wire `operator.prompt` / `ask_user` flow | 1-2 days | Interactive operator prompts |

---

### Summary

The client-server split has a **solid foundation** — the service interface is well-designed, the TUI is a genuinely thin client, the server/client infrastructure is production-quality, and auth is done right. But the **session event bridge is the critical missing piece** that makes the remote TUI unusable for its primary purpose (watching agent sessions in real-time). Everything else is incremental improvement on top of a working architecture.
