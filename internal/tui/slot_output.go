package tui

import (
	"encoding/json"
	"fmt"
	"strings"
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
// pane can render text and tool calls as distinct, stylized blocks
// while legacy callers (output modal, grid mini view) get a plain-text
// rendering via outputText().
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

// outputText returns a plain-text rendering of the slot's output. Used
// by callers (output modal, grid mini view) that don't render styled
// blocks. The format matches the legacy strings.Builder output it
// replaced so existing UX is preserved.
func (rs *runtimeSlot) outputText() string {
	var b strings.Builder
	for i := range rs.items {
		it := &rs.items[i]
		switch it.kind {
		case outputItemText:
			b.WriteString(it.text)
		case outputItemTool:
			fmt.Fprintf(&b, "\n⚙ %s\n", it.toolName)
			if !it.endedAt.IsZero() && it.toolResult != "" {
				fmt.Fprintf(&b, "→ %s\n", it.toolResult)
			}
		}
	}
	return b.String()
}

