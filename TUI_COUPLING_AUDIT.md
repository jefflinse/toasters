# TUI Coupling Audit Report

**Date:** 2026-02-28
**Reviewer:** code-reviewer
**Purpose:** Identify every coupling point between `internal/tui/` and internal packages that must be addressed when extracting business logic into `internal/service/`.
**Goal state:** The TUI imports ONLY the `service` package plus Bubble Tea/Lipgloss rendering libraries.

---

## 1. Every Import of Internal Packages

### model.go

| Package | What it's used for | Extraction difficulty |
|---------|-------------------|----------------------|
| `agentfmt` | `ParseBytes`, `DefTeam`, `*TeamDef` type assertion in `teamGeneratedMsg` handler; `DefSkill`, `*SkillDef` type assertion in `skillGeneratedMsg` handler | **Moderate** — move parsing into service; TUI receives pre-validated DTOs |
| `db` | `*db.Job` in `Model.jobs`, `progressState` fields, `db.Task`/`db.ProgressReport`/`db.AgentSession`/`db.FeedEntry` in `progressPollMsg`, `db.TaskStatusCompleted`/`Failed`/`Blocked` constants, `db.Task` map construction | **Complex** — pervasive; requires service-level DTO types for every db type |
| `loader` | `loader.Slugify` | **Trivial** — move to service; TUI never needs to slugify |
| `mcp` | `*mcp.Manager` in `Model.mcpManager`, `mcp.ServerConnected`/`ServerFailed` constants, `m.mcpManager.Servers()` | **Moderate** — replace with service method + service-level status enum |
| `operator` | `*operator.Operator` in `Model.operator`, `operator.Event` construction (via `sendMessage`/`notifyOperator`), `operator.EventType` constants and payload type assertions in `formatOperatorEvent` | **Complex** — operator event types deeply embedded in rendering logic |
| `provider` | `provider.Provider` in `Model.llmClient`, `provider.Message` in `ChatEntry`, `provider.ToolCall` in `promptModeState.pendingDispatch`, `provider.ModelInfo` in `ModelsMsg` | **Complex** — `provider.Message` is the core chat data type used everywhere |
| `runtime` | `*runtime.Runtime` in `Model.runtime`, `runtime.SessionSnapshot` in `progressState`, `runtime.SessionEventText`/`ToolCall`/`ToolResult` constants, `runtime.SessionEvent` embedded in `RuntimeSessionEventMsg` | **Complex** — session event type switches are in the Update() loop |

### messages.go

| Package | What it's used for | Extraction difficulty |
|---------|-------------------|----------------------|
| `db` | `*db.Job`, `*db.Task`, `*db.ProgressReport`, `*db.AgentSession`, `*db.FeedEntry` in `progressPollMsg`; `*db.Job` in `JobsReloadedMsg` | **Complex** — all progress data types must become service DTOs |
| `mcp` | `mcp.ServerStatus` in `MCPStatusMsg` | **Moderate** — replace with service-level status type |
| `operator` | `operator.Event` in `OperatorEventMsg` | **Moderate** — replace with service-level event type |
| `provider` | `provider.ModelInfo` in `ModelsMsg`; `provider.Message` in `ChatEntry` | **Complex** — `ChatEntry` is used in every rendering path |
| `runtime` | `runtime.SessionSnapshot` in `progressPollMsg`; `runtime.SessionEvent` in `RuntimeSessionEventMsg` | **Moderate** — replace with service-level session types |

### teams_modal.go

| Package | What it's used for | Extraction difficulty |
|---------|-------------------|----------------------|
| `agentfmt` | `ParseFile`, `DefAgent`, `*AgentDef` type assertions in promotion; `ParseTeam`; `*TeamDef` in `teamsModalState.selectedTeamDef`; `writeAgentFile`/`writeTeamFile` | **Complex** — ~400 lines of filesystem ops interleaved with agentfmt parsing |
| `config` | `config.Dir()` in `promoteReadOnlyAutoTeam` | **Trivial** — move to service |
| `db` | `*db.Agent` in `teamsModalState.pickerAgents`, `teamsModalState.generateAgents`; `*db.Agent` in `filterAgentsForTeam` | **Moderate** — replace with service DTOs |
| `loader` | `loader.Slugify` in `addAgentToTeam` | **Trivial** — move to service |
| `provider` | `provider.ChatCompletion` in `maybeAutoDetectCoordinator`; `provider.Message` construction | **Moderate** — becomes `service.Definitions().DetectCoordinator()` |

### skills_modal.go

| Package | What it's used for | Extraction difficulty |
|---------|-------------------|----------------------|
| `agentfmt` | `ParseBytes` in `writeGeneratedSkillFile` | **Trivial** — move to service |
| `config` | `config.Dir()` in `createSkillFile`, `writeGeneratedSkillFile` | **Trivial** — move to service |
| `db` | `*db.Skill` in `skillsModalState.skills`; `m.store.ListSkills` | **Moderate** — replace with service DTOs |
| `loader` | `loader.Slugify` in `createSkillFile`, `writeGeneratedSkillFile` | **Trivial** — move to service |

### agents_modal.go

| Package | What it's used for | Extraction difficulty |
|---------|-------------------|----------------------|
| `agentfmt` | `ParseBytes` in `writeGeneratedAgentFile`; `ParseAgent` in `addSkillToAgent` | **Trivial** — move to service |
| `config` | `config.Dir()` in `createAgentFile`, `writeGeneratedAgentFile` | **Trivial** — move to service |
| `db` | `*db.Agent` in `agentsModalState.agents`; `*db.Skill` in `agentsModalState.pickerSkills`; `m.store.ListAgents`, `m.store.ListSkills` | **Moderate** — replace with service DTOs |
| `loader` | `loader.Slugify` in `createAgentFile`, `writeGeneratedAgentFile` | **Trivial** — move to service |

### mcp_modal.go

| Package | What it's used for | Extraction difficulty |
|---------|-------------------|----------------------|
| `mcp` | `mcp.ServerStatus` in `mcpModalState.servers`; `mcp.ServerConnected`/`ServerFailed` constants; `server.Config.Command`/`Args`/`URL`/`EnabledTools`; `server.Tools[].OriginalName` | **Moderate** — need service-level MCP status type with all display fields |

### team_view.go

| Package | What it's used for | Extraction difficulty |
|---------|-------------------|----------------------|
| `agentfmt` | `ParseFile`, `DefAgent`, `*AgentDef` in `SetCoordinator`; `ParseTeam`, `*TeamDef` in `SetCoordinator` and `writeTeamFileTo` | **Complex** — `SetCoordinator` is ~90 lines of filesystem + agentfmt ops |
| `config` | `config.Dir()` in `isSystemTeam` | **Trivial** — move to service |
| `db` | `*db.Team` in `TeamView.Team`; `*db.Agent` in `TeamView.Coordinator`/`Workers`; `db.Store` in `BuildTeamViews` and `reloadTeamsFromStore` | **Complex** — `TeamView` is the core team display type used everywhere |

### progress_poll.go

| Package | What it's used for | Extraction difficulty |
|---------|-------------------|----------------------|
| `db` | `db.Store` param in `progressPollCmd`; `db.JobFilter{}`; multiple store methods: `ListJobs`, `ListTasksForJob`, `GetRecentProgress`, `GetActiveSessions`, `ListRecentFeedEntries` | **Moderate** — entire function becomes unnecessary (replaced by SSE push) |
| `runtime` | `*runtime.Runtime` param in `progressPollCmd`; `rt.ActiveSessions()`; `runtime.SessionSnapshot` return type | **Moderate** — same: replaced by SSE push |

### streaming.go

| Package | What it's used for | Extraction difficulty |
|---------|-------------------|----------------------|
| `operator` | `operator.Event` construction, `operator.EventUserMessage`, `operator.UserMessagePayload` | **Moderate** — becomes `service.Operator().SendMessage()` |
| `provider` | `provider.Message` in `ChatEntry` construction; `client.Models()` in `fetchModels` | **Moderate** — `fetchModels` becomes `service.System().ListModels()` |

### llm_generate.go

| Package | What it's used for | Extraction difficulty |
|---------|-------------------|----------------------|
| `agentfmt` | `ParseBytes` for validation | **Trivial** — move to service |
| `db` | `*db.Agent` in `generateTeamCmd` param | **Trivial** — move to service |
| `provider` | `provider.Provider` param; `provider.ChatCompletion`; `provider.Message` construction | **Moderate** — all become service methods |

### panels.go

| Package | What it's used for | Extraction difficulty |
|---------|-------------------|----------------------|
| `db` | `db.JobStatusActive`/`Paused`/`Completed`/`SettingUp`/`Decomposing` constants; `db.TaskStatus` enum in `taskStatusIndicator`; `*db.Task` in `renderJobProgressSummary` | **Moderate** — need service-level status enums or string constants |
| `mcp` | `m.mcpManager.Servers()`; `mcp.ServerConnected`/`ServerFailed` constants | **Moderate** — replace with service method |

### helpers.go

| Package | What it's used for | Extraction difficulty |
|---------|-------------------|----------------------|
| `db` | `*db.Job` in `displayJobs`, `jobByID`; `db.JobStatus*` constants; `db.FeedEntryType` constants in `formatFeedEntry` | **Moderate** — need service-level enums |
| `operator` | `operator.Event` param in `formatOperatorEvent`; `operator.EventType` constants and payload type assertions: `EventTaskStarted`, `TaskStartedPayload`, `EventTaskCompleted`, `TaskCompletedPayload`, `EventTaskFailed`, `TaskFailedPayload`, `EventBlockerReported`, `BlockerReportedPayload`, `EventJobComplete`, `JobCompletePayload` | **Complex** — type switch on 5 operator event types with payload assertions |
| `provider` | `provider.Message` in `initMessages`; `provider.ToolCall{}` | **Moderate** — need service-level message type |

### blocker_modal.go

| Package | What it's used for | Extraction difficulty |
|---------|-------------------|----------------------|
| `db` | `*db.Job` param in `hasBlocker` | **Trivial** — only uses `j.ID` |

### jobs_modal.go

| Package | What it's used for | Extraction difficulty |
|---------|-------------------|----------------------|
| `db` | `*db.Job` in `jobsModalState.jobs`; `*db.Task`/`*db.ProgressReport` maps; `m.store.ListAllJobs`, `m.store.ListTasksForJob`, `m.store.GetRecentProgress`, `m.store.UpdateJobStatus`; `db.JobStatus*` constants; `db.TaskStatus*` in `taskStatusIndicator` calls | **Complex** — heavy store interaction + status enum switches |

### log_view.go

| Package | What it's used for | Extraction difficulty |
|---------|-------------------|----------------------|
| `config` | `config.Dir()` in `logFilePath` | **Trivial** — could be passed as a config value at init time |

### Files with NO internal package imports (pure rendering/UI):

`view.go`, `update.go`, `commands.go`, `styles.go`, `format.go`, `grid.go`, `prompt.go`, `messages_generate.go`

---

## 2. Hidden Dependencies

### Type assertions on internal types

| File | Type assertion | Risk |
|------|---------------|------|
| `model.go` | `agentfmt.ParseBytes(…, agentfmt.DefTeam)` → `parsed.(*agentfmt.TeamDef)` | Service must return validated team name, not raw parsed def |
| `model.go` | `agentfmt.ParseBytes(…, agentfmt.DefSkill)` → `parsed.(*agentfmt.SkillDef)` | Same pattern — extract name in service |
| `model.go` | `switch ev.Type { case runtime.SessionEventText, runtime.SessionEventToolCall, runtime.SessionEventToolResult }` | Service must define its own session event enum |
| `helpers.go` | `switch ev.Type { case operator.EventTaskStarted … }` + `ev.Payload.(operator.TaskStartedPayload)` etc. (5 payload types) | Service must define its own event types with typed payloads |
| `helpers.go` | `switch entry.EntryType { case db.FeedEntrySystemEvent … }` (8 feed entry types) | Service must define feed entry type enum |
| `teams_modal.go` | `agentfmt.ParseFile(path)` → `defType == agentfmt.DefAgent` → `def.(*agentfmt.AgentDef)` | Entire promotion flow moves to service |

### Constants from internal packages (~30 distinct values)

| File | Constants used |
|------|---------------|
| `model.go` | `db.TaskStatusCompleted`, `db.TaskStatusFailed`, `db.TaskStatusBlocked` |
| `model.go` | `mcp.ServerConnected`, `mcp.ServerFailed` |
| `panels.go` | `db.JobStatusActive`, `Paused`, `Completed`, `SettingUp`, `Decomposing` |
| `panels.go` | `mcp.ServerConnected`, `mcp.ServerFailed` |
| `panels.go` | `db.TaskStatusPending`, `InProgress`, `Completed`, `Failed`, `Blocked`, `Cancelled` |
| `helpers.go` | `db.JobStatusCompleted`, `Failed`, `Cancelled` |
| `helpers.go` | `db.FeedEntrySystemEvent`, `ConsultationTrace`, `TaskStarted`, `TaskCompleted`, `TaskFailed`, `BlockerReported`, `JobComplete`, `UserMessage`, `OperatorMessage` |
| `jobs_modal.go` | `db.JobStatusActive`, `Pending`, `SettingUp`, `Decomposing`, `Completed`, `Failed`, `Paused` |
| `mcp_modal.go` | `mcp.ServerConnected`, `mcp.ServerFailed` |

**Gotcha:** ~30 distinct constant values from `db` and `mcp` used in rendering switches. The service must either re-export equivalent string/enum constants or the DTO types must carry display-ready strings.

### Struct fields referencing internal types

| File | Field | Type | Notes |
|------|-------|------|-------|
| `model.go` | `ModelConfig.Client` | `provider.Provider` | Interface — becomes `service.Service` |
| `model.go` | `ModelConfig.Store` | `db.Store` | Interface — removed entirely |
| `model.go` | `ModelConfig.Runtime` | `*runtime.Runtime` | Concrete — removed entirely |
| `model.go` | `ModelConfig.MCPManager` | `*mcp.Manager` | Concrete — removed entirely |
| `model.go` | `ModelConfig.Operator` | `*operator.Operator` | Concrete — removed entirely |
| `model.go` | `Model.llmClient` | `provider.Provider` | Used for `fetchModels` + generation |
| `model.go` | `Model.store` | `db.Store` | Used in 8+ methods |
| `model.go` | `Model.runtime` | `*runtime.Runtime` | Used in `progressPollCmd` |
| `model.go` | `Model.mcpManager` | `*mcp.Manager` | Used in `Init()` + sidebar |
| `model.go` | `Model.operator` | `*operator.Operator` | Used in `sendMessage`/`notifyOperator` |
| `model.go` | `Model.jobs` | `[]*db.Job` | Used in display + selection |
| `model.go` | `progressState.*` | `[]*db.Job`, `map[string][]*db.Task`, etc. | All progress data |
| `model.go` | `promptModeState.pendingDispatch` | `provider.ToolCall` | Dispatch confirmation flow |
| `messages.go` | `ChatEntry.Message` | `provider.Message` | Core chat data type |
| `team_view.go` | `TeamView.Team/Coordinator/Workers` | `*db.Team`, `*db.Agent` | Core team display type |

### Callbacks referencing internal types

| File | Callback | Types referenced |
|------|----------|-----------------|
| `cmd/root.go` | `notifySessionStarted` | `*runtime.Session` → `sess.Snapshot()` → `tui.RuntimeSessionStartedMsg` |
| `cmd/root.go` | `OnText` | `func(text string)` → `tui.OperatorTextMsg` — clean, string only |
| `cmd/root.go` | `OnEvent` | `func(event operator.Event)` → `tui.OperatorEventMsg` — carries `operator.Event` |
| `cmd/root.go` | `OnTurnDone` | `func()` → `tui.OperatorDoneMsg` — clean, no params |

---

## 3. Circular Dependency Risks — tea.Msg Types Carrying Server-Side Data

| tea.Msg type | Internal types carried | Risk level |
|-------------|----------------------|------------|
| `OperatorTextMsg` | `string` only | **None** |
| `OperatorDoneMsg` | No fields | **None** |
| `OperatorEventMsg` | `operator.Event` (with typed payloads) | **High** |
| `RuntimeSessionStartedMsg` | All `string` fields | **None** |
| `RuntimeSessionEventMsg` | `runtime.SessionEvent` (with enum + `*provider.ToolCall`/`*provider.ToolResult` payloads) | **High** |
| `RuntimeSessionDoneMsg` | All `string` fields | **None** |
| `progressPollMsg` | `[]*db.Job`, `map[string][]*db.Task`, `map[string][]*db.ProgressReport`, `[]*db.AgentSession`, `[]runtime.SessionSnapshot`, `[]*db.FeedEntry` | **High** — replaced entirely by SSE push |
| `ModelsMsg` | `[]provider.ModelInfo` | **Moderate** |
| `TeamsReloadedMsg` | `[]TeamView` (contains `*db.Team`, `*db.Agent`) | **High** |
| `JobsReloadedMsg` | `[]*db.Job` | **Moderate** |
| `AppReadyMsg` | `string` fields only | **None** |
| `MCPStatusMsg` | `[]mcp.ServerStatus` | **Moderate** |
| `TeamsAutoDetectDoneMsg` | `string` fields only | **None** |
| `teamPromotedMsg` | `string` + `error` | **None** |
| `teamGeneratedMsg` | `string` + `[]string` + `error` | **None** |
| `skillGeneratedMsg` | `string` + `error` | **None** |
| `agentGeneratedMsg` | `string` + `error` | **None** |
| `blockerAnswersSubmittedMsg` | `*Blocker` (TUI-local type) | **None** |
| `DefinitionsReloadedMsg` | No fields | **None** |
| `editorFinishedMsg` | `error` only | **None** |

**Summary:** 6 of 19 message types carry internal types that need service-level equivalents. The `progressPollMsg` is the most complex (6 internal types) but is eliminated entirely by SSE push.

---

## 4. Tricky Extraction Patterns

### 4.1 Team promotion filesystem operations (~400 lines)

**File:** `teams_modal.go`
**Difficulty:** Complex

`promoteAutoTeam`, `promoteReadOnlyAutoTeam`, `promoteMarkerAutoTeam` perform heavy filesystem operations (glob, parse, mkdir, write, symlink removal) interleaved with `agentfmt` parsing and `config.Dir()` calls. These are pure business logic with zero rendering, but they're defined as package-level functions in the TUI package.

**Risk:** The `promoteAutoTeamCmd` wraps promotion as a `tea.Cmd` — the service method must be async and return results via the event stream.

### 4.2 teamGeneratedMsg handler (~100 lines)

**File:** `model.go`
**Difficulty:** Complex

~100 lines of business logic in the `Update()` method: parses generated content with `agentfmt.ParseBytes`, derives slug with `loader.Slugify`, creates directory structure with `os.MkdirAll`, writes `team.md`, copies agent files from store via `m.store.ListAgents`, and reloads the modal. This is the single worst case of business logic interleaved with TUI state updates.

**Risk:** Must split into: (1) service method that does all filesystem/store work and returns a result, (2) TUI handler that updates modal state from the result.

### 4.3 RuntimeSessionDoneMsg handler

**File:** `model.go`
**Difficulty:** Moderate

Business logic in the Update() loop: queries `m.store.GetTask()` to check if a task was already handled, then constructs a notification and routes it through the operator. The store query and operator notification are business logic; only the toast is UI.

**Risk:** The "was task already handled?" check must move to the service. The TUI should receive a pre-decided event ("notify operator" vs "skip").

### 4.4 maybeAutoDetectCoordinator

**File:** `teams_modal.go`
**Difficulty:** Moderate

Direct LLM call via `provider.ChatCompletion` inside a `tea.Cmd` goroutine. Constructs `provider.Message` slice, calls the LLM, and matches the result to agent names.

**Risk:** Becomes `service.Definitions().DetectCoordinator(teamID)` — straightforward but the async pattern (returns `tea.Cmd` → `TeamsAutoDetectDoneMsg`) must be preserved via the event stream.

### 4.5 formatOperatorEvent

**File:** `helpers.go`
**Difficulty:** Complex

Type switch on `operator.EventType` with 5 payload type assertions. This is rendering logic (produces styled strings) but depends deeply on operator event types.

**Risk:** The service must define its own event types. The TUI's `formatOperatorEvent` must switch on service event types instead. The mapping is 1:1 but touches 5 payload types.

### 4.6 progressPollCmd

**File:** `progress_poll.go`
**Difficulty:** Moderate

Direct SQLite queries via `db.Store` + `runtime.Runtime.ActiveSessions()`. Runs every 500ms.

**Risk:** Eliminated entirely by SSE push. The service pushes `progress.update` events; the TUI subscribes. But the TUI must still maintain the same `progressState` struct — it just gets populated from events instead of polling.

### 4.7 SetCoordinator (~90 lines)

**File:** `team_view.go`
**Difficulty:** Complex

~90 lines of filesystem operations: globs agent files, parses each with `agentfmt.ParseFile`, rewrites `team.md` lead field, rewrites `mode:` in every agent file using temp-file-and-rename.

**Risk:** Must move verbatim to service. The `rewriteMode` helper is a pure string function that could stay in either package.

### 4.8 Jobs modal store interactions

**File:** `jobs_modal.go`
**Difficulty:** Moderate

`loadJobsForModal` and `loadJobDetail` make direct `m.store.*` calls. `updateJobsModal` calls `m.store.UpdateJobStatus` to cancel a job.

**Risk:** All become service methods. The cancel operation is the only write — `service.Jobs().Cancel(jobID)`.

---

## 5. Things That Should Stay in the TUI

| Category | Files/Functions | Reason |
|----------|----------------|--------|
| **Editor launching** | `skills_modal.go` (`openInEditor`) | OS-level process exec; disabled for remote clients |
| **Animation/spinner state** | `model.go` spinnerFrame, focusAnimFrames, loadingFrame | Pure rendering state |
| **Viewport scrolling** | `model.go` scroll state, `helpers.go` (`renderScrollbar`) | Bubble Tea viewport management |
| **Key handling** | `update.go`, `prompt.go`, all `update*Modal` methods | Input routing |
| **Layout calculations** | `panels.go` (width calculations), all `render*` methods | Terminal geometry |
| **Toast notifications** | `messages.go`, `helpers.go` | Pure UI chrome |
| **Slash command popup** | `commands.go`, `cmdPopupState` | Input autocomplete |
| **Grid rendering** | `grid.go` | Agent grid display |
| **Styles** | `styles.go` | Lipgloss style definitions |
| **Format utilities** | `format.go`, `helpers.go` (wrapText, truncateStr) | Text formatting |
| **Chat viewport content** | `updateViewportContent`, `resizeComponents` | Viewport management |
| **runtimeSlot accumulation** | `model.go`, `runtimeSessions` map | TUI-local display state for agent cards |
| **Log view** | `log_view.go` (entire file except `logFilePath`) | File tailing is a local-only feature |
| **Clipboard operations** | `model.go` (ctrl+v, ctrl+y) | OS clipboard access |

---

## 6. The cmd/root.go Wiring

### Current dependency construction sequence

1. `config.Load()` → `cfg`
2. `bootstrap.Run()` → first-run setup
3. `db.Open()` → `store db.Store`
4. `loader.New()` + `ldr.Load()` → loads definitions from files into DB
5. `compose.New()` → `composer *compose.Composer`
6. `provider.NewRegistry()` → `registry` with all configured providers
7. `runtime.New()` → `rt *runtime.Runtime`
8. `mcp.NewManager()` + `mcpManager.Connect()` → MCP server connections
9. `rt.SetMCPCaller()` → wires MCP tools into runtime
10. `provider.NewOpenAI()` or `provider.NewAnthropic()` → `client provider.Provider`
11. `composer.Compose("operator", "system")` → `operatorPrompt`
12. `operator.New()` → `op *operator.Operator` with callbacks
13. `tui.BuildTeamViews()` → `initialTeams`
14. `tui.NewModel(ModelConfig{…})` → TUI model with all deps
15. `tea.NewProgram()` → `prog`
16. Callback wiring: `rt.OnSessionStarted`, `op.OnText`, `op.OnEvent`, `op.OnTurnDone` all use `p.Load().Send(tui.*Msg)`
17. Background goroutine: generates team awareness + sends greeting
18. `loader.NewWatcher()` → fsnotify watcher that sends `DefinitionsReloadedMsg` + `TeamsReloadedMsg`

### Target state after extraction

```go
func runTUI(cmd *cobra.Command, _ []string) error {
    // Steps 1-12 collapse into:
    svc, err := service.NewLocal(service.LocalConfig{...})

    // Step 14 becomes:
    m := tui.NewModel(tui.ModelConfig{
        Service: svc,
    })

    // Steps 16-18 become:
    // A single goroutine consuming svc.Events().Subscribe(ctx)
    // and translating service events to tea.Msg values
}
```

### Specific risks in the wiring

1. **`atomic.Pointer[tea.Program]` pattern:** The `p.Store(prog)` / `p.Load()` pattern for safe callback→TUI messaging must be preserved. In the service world, the TUI starts a goroutine that reads from `svc.Events().Subscribe(ctx)` and calls `prog.Send()` — same pattern, cleaner.

2. **`notifySessionStarted` closure:** Wires `runtime.Session.Subscribe()` into the TUI. In the service world, the `LocalService` does this internally and emits unified events.

3. **`textBatcher`:** Lives server-side per the design decision. The `LocalService` batches operator text tokens before emitting events.

4. **`generateTeamAwareness`:** Called in a background goroutine. Must become a service method or remain in `cmd/` as a startup helper.

5. **`loader.NewWatcher` callback:** Calls `tui.BuildTeamViews()` which queries the DB. In the service world, the watcher is internal to the service; it emits `definitions.reloaded` events.

6. **`tui.BuildTeamViews()` called from `cmd/root.go`:** This function is defined in the TUI package but queries the DB. It must move to the service package. The TUI should receive pre-built team view DTOs.

---

## 7. Service-Level Types Required

The service must define equivalents for all of these internal types:

### Data types
- `db.Job`, `db.Task`, `db.Agent`, `db.Skill`, `db.Team`
- `db.ProgressReport`, `db.AgentSession`, `db.FeedEntry`
- `provider.Message`, `provider.ToolCall`, `provider.ModelInfo`
- `runtime.SessionSnapshot`, `runtime.SessionEvent`
- `operator.Event` (with 5 payload types)
- `mcp.ServerStatus`
- `agentfmt.TeamDef` (for `selectedTeamDef` display)
- `TeamView` (currently in TUI package, must move to service)

### Enum/constant types (~30 values)
- `db.JobStatus*` (7 values): Active, Pending, SettingUp, Decomposing, Completed, Failed, Paused
- `db.TaskStatus*` (6 values): Pending, InProgress, Completed, Failed, Blocked, Cancelled
- `db.FeedEntryType` (9 values): SystemEvent, ConsultationTrace, TaskStarted, TaskCompleted, TaskFailed, BlockerReported, JobComplete, UserMessage, OperatorMessage
- `runtime.SessionEventType` (3 values): Text, ToolCall, ToolResult
- `operator.EventType` (6+ values): TaskStarted, TaskCompleted, TaskFailed, BlockerReported, JobComplete, UserMessage
- `mcp.ServerState` (2 values): Connected, Failed

---

## 8. Extraction Priority

### Must extract (business logic in TUI):

1. **Team promotion** (~400 lines) — `promoteAutoTeam`, `promoteReadOnlyAutoTeam`, `promoteMarkerAutoTeam`
2. **SetCoordinator** (~90 lines) — filesystem + agentfmt ops
3. **teamGeneratedMsg handler** (~100 lines) — filesystem + store ops in Update()
4. **All CRUD operations** — `createSkillFile`, `createAgentFile`, `writeGenerated*File`, `addAgentToTeam`, `addSkillToAgent`
5. **All LLM generation** — `generateSkillCmd`, `generateAgentCmd`, `generateTeamCmd`, `maybeAutoDetectCoordinator`
6. **Progress polling** — replaced entirely by SSE push
7. **Store queries** — `reloadSkillsForModal`, `reloadAgentsForModal`, `reloadTeamsFromStore`, `loadJobsForModal`, `loadJobDetail`
8. **Operator interaction** — `sendMessage`, `notifyOperator`
9. **Model fetching** — `fetchModels`
10. **Job cancellation** — `m.store.UpdateJobStatus` in jobs modal
