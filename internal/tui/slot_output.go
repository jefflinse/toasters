package tui

import (
	"encoding/json"
	"time"
)

// outputItemKind discriminates the renderable element types stored on a
// runtime slot. Text items accumulate streamed tokens; tool items wrap
// a single tool call's lifecycle (call → result).
type outputItemKind int

const (
	outputItemText outputItemKind = iota
	outputItemTool
)

// outputItem is one renderable element in a runtime slot's output
// stream. Replaces the old strings.Builder accumulator so the graph
// pane and the cockpit can render text and tool calls as distinct,
// stylized blocks (see renderOutputItems).
//
// text is a plain string rather than a strings.Builder because items
// are stored in a slice and slice growth copies elements; copying a
// non-zero strings.Builder panics on next use. Streamed text gets
// concatenated into the last text item, which is O(n²) over the run
// length but acceptable for chat-scale text bursts.
type outputItem struct {
	kind outputItemKind

	text string

	toolID     string
	toolName   string
	toolArgs   json.RawMessage
	toolResult string
	toolError  bool
	startedAt  time.Time
	endedAt    time.Time

	// File change attached to a write_file/edit_file tool item, delivered
	// by a session.file_change event (display-only; never LLM context).
	fileDiff      string
	diffAdded     int
	diffRemoved   int
	diffCreated   bool
	diffTruncated bool

	// Shell execution metadata attached to a shell tool item, delivered by
	// a session.shell_exec event (display-only; never LLM context).
	// hasShellExec distinguishes "no event yet" from a legitimate exit code
	// 0, which isn't representable as a zero-value sentinel.
	hasShellExec     bool
	shellExitCode    int
	shellDurationMs  int64
	shellOutputBytes int
	shellTruncated   bool
	shellTimedOut    bool

	// Worker-spawn metadata attached to a spawn_worker tool item, delivered
	// by a session.worker_spawn event (display-only; never LLM context).
	// hasWorkerSpawn distinguishes "no event yet" from a legitimate
	// zero-value spawnDepth.
	hasWorkerSpawn bool
	spawnRole      string
	spawnTask      string
	spawnJobID     string
	spawnDepth     int
	spawnFailed    bool
	spawnError     string

	// KB-note metadata attached to a job_note_write/job_notes_search tool
	// item, delivered by a session.kb event (display-only; never LLM
	// context). hasKBNote distinguishes "no event yet" from a legitimate
	// zero-value preview.
	hasKBNote bool
	kbScope   string
	kbOp      string
	kbSource  string
	kbPreview string
}

// appendText adds streamed text to the slot. Streamed tokens coalesce
// into the most recent text item so a long response is one block, not
// hundreds of one-token fragments. A tool call between text bursts
// starts a fresh text item on the next token.
func (rs *runtimeSlot) appendText(s string) {
	if s == "" {
		return
	}
	rs.contentVersion++
	if n := len(rs.items); n > 0 && rs.items[n-1].kind == outputItemText {
		rs.items[n-1].text += s
		return
	}
	rs.items = append(rs.items, outputItem{kind: outputItemText, text: s})
}

// startTool records a new in-flight tool call. Returns false when the
// tool already exists for this call ID — prevents double-appending if
// the runtime ever re-emits a start event.
func (rs *runtimeSlot) startTool(callID, name string, args json.RawMessage) bool {
	if rs.toolItemIdx == nil {
		rs.toolItemIdx = map[string]int{}
	}
	if _, ok := rs.toolItemIdx[callID]; ok {
		return false
	}
	rs.contentVersion++
	it := outputItem{
		kind:      outputItemTool,
		toolID:    callID,
		toolName:  name,
		toolArgs:  args,
		startedAt: time.Now(),
	}
	rs.items = append(rs.items, it)
	rs.toolItemIdx[callID] = len(rs.items) - 1
	return true
}

// completeTool fills in the result for a previously started tool call.
// If the matching start was missed (out-of-order delivery, slot reset),
// synthesize a completed item so the result still surfaces in the UI
// instead of being silently dropped.
func (rs *runtimeSlot) completeTool(callID, name, result string, isError bool) {
	if rs.toolItemIdx == nil {
		rs.toolItemIdx = map[string]int{}
	}
	rs.contentVersion++
	idx, ok := rs.toolItemIdx[callID]
	if !ok {
		now := time.Now()
		rs.items = append(rs.items, outputItem{
			kind:       outputItemTool,
			toolID:     callID,
			toolName:   name,
			toolResult: result,
			toolError:  isError,
			startedAt:  now,
			endedAt:    now,
		})
		return
	}
	rs.items[idx].toolResult = result
	rs.items[idx].toolError = isError
	rs.items[idx].endedAt = time.Now()
	delete(rs.toolItemIdx, callID)
}

// toolArgPath extracts the "path" argument from a tool call's raw JSON
// arguments, for matching a session.file_change event (tool name + path,
// no call ID) to the write_file/edit_file item that produced it. Parses
// leniently — malformed or missing args just yield "".
func toolArgPath(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var a map[string]any
	if err := json.Unmarshal(args, &a); err != nil {
		return ""
	}
	p, _ := a["path"].(string)
	return p
}

// attachFileChange pairs a session.file_change event with the tool item
// that produced it and copies over the diff fields. Event ordering is
// call → file_change → result: the notifier fires mid-execution, so the
// matching item is normally still pending. On a match, only the diff
// fields are set — endedAt and toolItemIdx are left untouched so the
// later tool_result still completes the same item via completeTool
// instead of finding it "already done" and synthesizing a duplicate.
//
// Matching walks items oldest-first: tool name + path match (preferring a
// still-pending item, but accepting a completed one since ordering isn't
// guaranteed), then a name-only fallback, then a synthesized standalone
// item mirroring completeTool's synthesize-on-miss path.
//
// Oldest-first (not newest-first) matters because mycelium fires ALL
// tool_call events for a turn up front, then executes sequentially — so two
// parallel calls to the same tool+path can both be pending at once, and
// their file_change notifications arrive in execution (= insertion) order.
// Newest-first matching would attach the first call's diff to the second
// call's item. An item that already carries a diff is skipped so a second
// notification for the same path can't clobber it before it completes.
func (rs *runtimeSlot) attachFileChange(toolName, path, diff string, added, removed int, created, truncated bool) {
	set := func(it *outputItem) {
		it.fileDiff = diff
		it.diffAdded = added
		it.diffRemoved = removed
		it.diffCreated = created
		it.diffTruncated = truncated
	}

	// Pass 1: name + path match, preferring the oldest pending item that
	// doesn't already carry a diff.
	var completedMatch *outputItem
	for i := 0; i < len(rs.items); i++ {
		it := &rs.items[i]
		if it.kind != outputItemTool || it.toolName != toolName || toolArgPath(it.toolArgs) != path {
			continue
		}
		if it.fileDiff != "" {
			continue
		}
		if it.endedAt.IsZero() {
			rs.contentVersion++
			set(it)
			return
		}
		if completedMatch == nil {
			completedMatch = it
		}
	}
	if completedMatch != nil {
		rs.contentVersion++
		set(completedMatch)
		return
	}

	// Pass 2: name-only fallback, preferring the oldest pending item that
	// doesn't already carry a diff.
	var completedByName *outputItem
	for i := 0; i < len(rs.items); i++ {
		it := &rs.items[i]
		if it.kind != outputItemTool || it.toolName != toolName {
			continue
		}
		if it.fileDiff != "" {
			continue
		}
		if it.endedAt.IsZero() {
			rs.contentVersion++
			set(it)
			return
		}
		if completedByName == nil {
			completedByName = it
		}
	}
	if completedByName != nil {
		rs.contentVersion++
		set(completedByName)
		return
	}

	// Pass 3: no matching tool item at all — synthesize a completed one so
	// the diff still surfaces instead of being silently dropped.
	rs.contentVersion++
	now := time.Now()
	it := outputItem{
		kind:      outputItemTool,
		toolName:  toolName,
		startedAt: now,
		endedAt:   now,
	}
	set(&it)
	rs.items = append(rs.items, it)
}

// attachShellExec pairs a session.shell_exec event with the shell tool item
// that produced it. Shell calls carry no argument (like write_file/edit_file's
// path) to disambiguate concurrent in-flight calls, so matching is name-only:
// the oldest still-pending "shell" item that hasn't already been attached —
// oldest-first for the same reason as attachFileChange (mycelium fires every
// tool_call event for a turn before executing any of them sequentially, so
// notifications arrive in execution/insertion order).
func (rs *runtimeSlot) attachShellExec(exitCode int, durationMs int64, outputBytes int, truncated, timedOut bool) {
	set := func(it *outputItem) {
		it.hasShellExec = true
		it.shellExitCode = exitCode
		it.shellDurationMs = durationMs
		it.shellOutputBytes = outputBytes
		it.shellTruncated = truncated
		it.shellTimedOut = timedOut
	}

	var completed *outputItem
	for i := 0; i < len(rs.items); i++ {
		it := &rs.items[i]
		if it.kind != outputItemTool || it.toolName != "shell" || it.hasShellExec {
			continue
		}
		if it.endedAt.IsZero() {
			rs.contentVersion++
			set(it)
			return
		}
		if completed == nil {
			completed = it
		}
	}
	if completed != nil {
		rs.contentVersion++
		set(completed)
		return
	}

	// No matching tool item at all — synthesize a completed one so the
	// status still surfaces instead of being silently dropped.
	rs.contentVersion++
	now := time.Now()
	it := outputItem{
		kind:      outputItemTool,
		toolName:  "shell",
		startedAt: now,
		endedAt:   now,
	}
	set(&it)
	rs.items = append(rs.items, it)
}

// attachWorkerSpawn pairs a session.worker_spawn event with the spawn_worker
// tool item that produced it. Like shell, spawn_worker calls carry no
// argument that disambiguates concurrent in-flight calls, so matching is
// name-only: the oldest still-pending "spawn_worker" item that hasn't
// already been attached — oldest-first for the same reason as
// attachShellExec (mycelium fires every tool_call event for a turn before
// executing any of them sequentially, so notifications arrive in
// execution/insertion order).
func (rs *runtimeSlot) attachWorkerSpawn(role, task, jobID string, depth int, failed bool, errMsg string) {
	set := func(it *outputItem) {
		it.hasWorkerSpawn = true
		it.spawnRole = role
		it.spawnTask = task
		it.spawnJobID = jobID
		it.spawnDepth = depth
		it.spawnFailed = failed
		it.spawnError = errMsg
	}

	var completed *outputItem
	for i := 0; i < len(rs.items); i++ {
		it := &rs.items[i]
		if it.kind != outputItemTool || it.toolName != "spawn_worker" || it.hasWorkerSpawn {
			continue
		}
		if it.endedAt.IsZero() {
			rs.contentVersion++
			set(it)
			return
		}
		if completed == nil {
			completed = it
		}
	}
	if completed != nil {
		rs.contentVersion++
		set(completed)
		return
	}

	// No matching tool item at all — synthesize a completed one so the
	// status still surfaces instead of being silently dropped.
	rs.contentVersion++
	now := time.Now()
	it := outputItem{
		kind:      outputItemTool,
		toolName:  "spawn_worker",
		startedAt: now,
		endedAt:   now,
	}
	set(&it)
	rs.items = append(rs.items, it)
}

// attachKBNote pairs a session.kb event with the job_note_write/
// job_notes_search tool item that produced it. Like shell and spawn_worker,
// these calls carry no argument that disambiguates concurrent in-flight
// calls, so matching is name-only: the oldest still-pending item for the
// op's tool name that hasn't already been attached — oldest-first for the
// same reason as attachShellExec. Unlike shell/spawn_worker, the underlying
// tool result is already accurate (job_note_write/job_notes_search never
// misreport success/failure the way a nonzero shell exit does), so this is
// purely additive display metadata, not a correction.
func (rs *runtimeSlot) attachKBNote(scope, op, source, preview string) {
	toolName := "job_note_write"
	if op == "search" {
		toolName = "job_notes_search"
	}

	set := func(it *outputItem) {
		it.hasKBNote = true
		it.kbScope = scope
		it.kbOp = op
		it.kbSource = source
		it.kbPreview = preview
	}

	var completed *outputItem
	for i := 0; i < len(rs.items); i++ {
		it := &rs.items[i]
		if it.kind != outputItemTool || it.toolName != toolName || it.hasKBNote {
			continue
		}
		if it.endedAt.IsZero() {
			rs.contentVersion++
			set(it)
			return
		}
		if completed == nil {
			completed = it
		}
	}
	if completed != nil {
		rs.contentVersion++
		set(completed)
		return
	}

	// No matching tool item at all — synthesize a completed one so the
	// status still surfaces instead of being silently dropped.
	rs.contentVersion++
	now := time.Now()
	it := outputItem{
		kind:      outputItemTool,
		toolName:  toolName,
		startedAt: now,
		endedAt:   now,
	}
	set(&it)
	rs.items = append(rs.items, it)
}
