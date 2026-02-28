# Enhanced Jobs Panel & Three-Panel Jobs Modal

## Objective

Enhance the TUI Jobs experience in two ways:

1. **Sidebar Jobs pane**: Nest team assignments under tasks in the job/task tree, adding one level of depth so the hierarchy reads Job > Task > Team.
2. **Fullscreen Jobs modal**: Transform the current two-panel layout into a three-panel operational dashboard: Jobs > Tasks > Agent Cards.

## Scope

### In Scope

- Team assignment nesting in the sidebar Jobs pane (left panel, top pane)
- Three-panel fullscreen Jobs modal with agent smart cards
- Reusable agent card renderer extracted from grid view
- Helper to find runtime sessions by task ID
- Unit tests for new helpers and rendering logic
- Code review of all changes

### Out of Scope

- Scrolling within the right panel (follow-up enhancement)
- Mouse interaction on agent cards
- Auto-refresh while the Jobs modal is open (close/reopen to refresh)
- Changes to the grid view (`Ctrl+G`) — it coexists unchanged
- Changes to the Agents pane (middle pane in left panel) — it stays as-is
- Database schema changes — no new tables or columns needed
- Agent card token usage bars (follow-up polish)

## Design Decisions

- **Right panel data source**: Live `runtimeSessions` map (not DB `AgentSession` records) — gives real-time activity feeds and tool call tracking.
- **Agent cards reuse grid styling**: Extracted from `renderRuntimeGridCell` so both views share the same visual language.
- **Team nesting in sidebar**: Team status mirrors task status (the team's work on the task IS the task status).
- **Three-panel proportions**: ~20% jobs list / ~30% task list / ~50% agent cards.
- **Focus cycling**: Tab cycles left > middle > right > left in the Jobs modal.

## Risks

| Risk | Mitigation |
|------|------------|
| Team lines in sidebar consume more vertical space | Jobs pane uses flexible height allocation ("whatever is left"), so it self-adjusts. The fullscreen modal provides the complete view. |
| `renderRuntimeGridCell` extraction must be regression-free | Grid view must render identically after refactoring. Verified by existing tests and visual inspection. |
| Right panel may be sparse for pending tasks (no agents yet) | Handled with placeholder text ("No agents assigned" / "Task pending"). |
| Many agents on a task could overflow the right panel | Clipped in v1; scrollable panels are a follow-up enhancement. |

## Plan

### Step 1: Add team nesting to sidebar Jobs pane

- **Agent**: builder
- **Files**: `internal/tui/panels.go`
- **Depends on**: nothing
- **Description**: In `renderLeftPanel`, modify the task rendering loop to add a nested team assignment line underneath each task that has a `TeamID`. The team line is indented one level deeper than the task (4 spaces total), uses a status icon matching the task status (e.g., `◈` for in-progress, `✓` for completed), and shows the team name. Remove the current inline `(teamID)` suffix from the task title.
- **Acceptance criteria**:
  - Tasks with a non-empty `TeamID` show a nested child line with the team name
  - Tasks without a `TeamID` show no extra line
  - The team line uses appropriate status indicators matching the task status
  - The tree visually reads as: Job > Task > Team (three levels of nesting)
  - The current inline `(teamID)` suffix on task titles is removed

### Step 2: Expand `jobsModalState` for three-panel layout

- **Agent**: builder
- **Files**: `internal/tui/jobs_modal.go`
- **Depends on**: nothing
- **Description**: Update the `jobsModalState` struct to support three panels. Change `focus` to cycle through 3 values: 0=jobs list, 1=tasks list, 2=agent detail. Add any fields needed for the right panel state (e.g., scroll offset for agent list).
- **Acceptance criteria**:
  - `jobsModalState` has the new fields
  - No behavioral changes yet (rendering and key handling updated in subsequent steps)

### Step 3: Update Jobs modal key handling for three-panel navigation

- **Agent**: builder
- **Files**: `internal/tui/jobs_modal.go`
- **Depends on**: Step 2
- **Description**: Update `updateJobsModal` to handle three-panel navigation:
  - **Tab**: Cycle focus 0 > 1 > 2 > 0
  - **Up/Down in focus=1 (tasks)**: Navigate task list, which triggers right panel to update
  - **Up/Down in focus=2 (agents)**: Scroll agent list if needed, or no-op for v1
  - **Ctrl+X**: Only available when focus=0 (jobs list)
  - When job selection changes (focus=0, up/down), reset `taskIdx` to 0
- **Acceptance criteria**:
  - Tab cycles through all three panels
  - Up/down navigation works correctly in each panel
  - Focus indicator (border style) correctly highlights the active panel
  - Selecting a different job resets the task selection

### Step 4: Extract reusable agent card renderer

- **Agent**: builder
- **Files**: `internal/tui/grid.go` (or new `internal/tui/agent_card.go`)
- **Depends on**: nothing
- **Description**: Extract the core rendering logic from `renderRuntimeGridCell` into a standalone function `renderAgentCard(rs *runtimeSlot, innerW, innerH int, focused bool, spinnerFrame int) string`. This returns the inner content without the cell border (the caller applies the border). The existing `renderRuntimeGridCell` calls this helper — zero regression to the grid view.
- **Acceptance criteria**:
  - `renderRuntimeGridCell` still works identically (regression-free)
  - The extracted function can be called from the Jobs modal to render agent cards
  - The function handles graceful degradation for small dimensions
  - `spinnerFrame` is passed as a parameter (not accessed via method receiver)

### Step 5: Build `runtimeSessionsForTask` helper

- **Agent**: builder
- **Files**: `internal/tui/helpers.go` or `internal/tui/jobs_modal.go`
- **Depends on**: nothing
- **Description**: Add a method `(m *Model) runtimeSessionsForTask(taskID string) []*runtimeSlot` that iterates `m.runtimeSessions` and returns all slots where `rs.taskID == taskID`, sorted with active sessions first, then completed, by start time.
- **Acceptance criteria**:
  - Returns all matching runtime sessions for a given task ID
  - Returns empty slice when no sessions match
  - Results are sorted: active first, then completed, by start time

### Step 6: Rewrite `renderJobsModal` as three-panel layout

- **Agent**: builder
- **Files**: `internal/tui/jobs_modal.go`
- **Depends on**: Steps 2, 3, 4, 5
- **Description**: Rewrite `renderJobsModal` to produce a three-panel layout:
  - **Left panel (~20% width)**: Job list with status icons (icon + title, selected job highlighted)
  - **Middle panel (~30% width)**: Job header (title, status, created date) at top, then task list with status indicators and team nesting (consistent with sidebar from Step 1). Selected task highlighted.
  - **Right panel (~50% width)**: Agent smart cards for the selected task. Uses `runtimeSessionsForTask` (Step 5) to get sessions, renders each using `renderAgentCard` (Step 4) stacked vertically. Empty state placeholder when no agents are assigned.
  - Panel focus indicated by `ModalFocusedPanel` vs `ModalPanelStyle` border styles
  - Footer key hints updated for three-panel navigation
- **Acceptance criteria**:
  - Three panels render side-by-side with correct proportions
  - Left panel shows job list with selection highlight
  - Middle panel shows tasks for selected job with status indicators and team nesting
  - Right panel shows agent smart cards for the selected task
  - Focus cycling (Tab) visually highlights the correct panel
  - Cancel job (Ctrl+X) still works from the jobs panel
  - Layout degrades gracefully at small terminal sizes
  - Empty states handled (no tasks, no agents, pending task)

### Step 7: Wire up task selection to right panel updates

- **Agent**: builder
- **Files**: `internal/tui/jobs_modal.go`
- **Depends on**: Steps 2, 3
- **Description**: Ensure changing the selected task (up/down in middle panel) causes the right panel to update with the correct agent sessions. Clamp `taskIdx` to valid bounds when the task list changes (e.g., when switching jobs).
- **Acceptance criteria**:
  - Changing the selected task refreshes the agent panel
  - `taskIdx` is clamped to valid bounds when the task list changes
  - No unnecessary DB queries on task navigation (sessions come from in-memory map)

### Step 8: Add/refine styles

- **Agent**: builder
- **Files**: `internal/tui/styles.go`, `internal/tui/panels.go`, `internal/tui/jobs_modal.go`
- **Depends on**: Steps 1, 6
- **Description**: Add any new styles needed for the team assignment line in the sidebar and the Jobs modal agent cards. Ensure no hardcoded color values in rendering code — all colors use named style variables. Review after Steps 1 and 6 to determine what's needed.
- **Acceptance criteria**:
  - All new visual elements have appropriate styling
  - Styles are consistent with the existing dark theme palette
  - No hardcoded color values in rendering code

### Step 9: Write tests

- **Agent**: test-writer
- **Files**: `internal/tui/*_test.go`
- **Depends on**: Steps 1, 4, 5
- **Description**: Write unit tests for new functions:
  - `runtimeSessionsForTask` — matching sessions, no matches, mixed active/completed sorting
  - `renderAgentCard` — non-empty output for various slot states (active, completed, with/without activities)
  - Team nesting line rendering in the sidebar (line appears when `TeamID` is set, absent when empty)
- **Acceptance criteria**:
  - All new helper functions have test coverage
  - Tests pass with `go test ./internal/tui/...`
  - Edge cases covered: empty task ID, nil activities, zero-width terminal

### Step 10: Code review

- **Agent**: code-reviewer
- **Files**: All changed files
- **Depends on**: Steps 1-9
- **Description**: Review all changes for correctness, consistency, performance (no unnecessary allocations in render path), regression (grid view, sidebar, existing tests), and code quality.
- **Acceptance criteria**:
  - All BLOCKING findings addressed
  - `go build ./...` succeeds
  - `go test ./...` passes
  - `golangci-lint run` reports 0 findings

## Review Checkpoints

1. **After Step 1**: Visual review of sidebar team nesting — indentation, icons, readability
2. **After Step 6**: Full visual review of three-panel Jobs modal — layout proportions, focus cycling, agent card rendering, empty states
3. **After Step 9**: Test coverage review
4. **Step 10**: Full code review before PR
