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
// Matching walks items newest-first: tool name + path match (preferring a
// still-pending item, but accepting a completed one since ordering isn't
// guaranteed), then a name-only fallback, then a synthesized standalone
// item mirroring completeTool's synthesize-on-miss path.
func (rs *runtimeSlot) attachFileChange(toolName, path, diff string, added, removed int, created, truncated bool) {
	set := func(it *outputItem) {
		it.fileDiff = diff
		it.diffAdded = added
		it.diffRemoved = removed
		it.diffCreated = created
		it.diffTruncated = truncated
	}

	// Pass 1: name + path match, preferring a pending item.
	var completedMatch *outputItem
	for i := len(rs.items) - 1; i >= 0; i-- {
		it := &rs.items[i]
		if it.kind != outputItemTool || it.toolName != toolName || toolArgPath(it.toolArgs) != path {
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

	// Pass 2: name-only fallback, preferring the newest pending item.
	var completedByName *outputItem
	for i := len(rs.items) - 1; i >= 0; i-- {
		it := &rs.items[i]
		if it.kind != outputItemTool || it.toolName != toolName {
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
