package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"github.com/jefflinse/toasters/internal/runtime"
)

// BroadcastSessionPrompt emits a session.prompt event so the TUI can
// populate the system prompt and initial message on an existing session
// slot. Graph nodes call this once their prompt has been composed and
// before the LLM starts streaming.
func (s *LocalService) BroadcastSessionPrompt(sessionID, systemPrompt, initialMessage string) {
	if sessionID == "" {
		return
	}
	s.broadcast(Event{
		Type:      EventTypeSessionPrompt,
		SessionID: sessionID,
		Payload: SessionPromptPayload{
			SessionID:      sessionID,
			SystemPrompt:   systemPrompt,
			InitialMessage: initialMessage,
		},
	})
}

// BroadcastSessionMeta broadcasts a session.meta event carrying the model,
// provider, temperature, and thinking flag a node session is running with.
func (s *LocalService) BroadcastSessionMeta(sessionID, model, provider string, temperature float64, thinking bool) {
	if sessionID == "" {
		return
	}
	s.broadcast(Event{
		Type:      EventTypeSessionMeta,
		SessionID: sessionID,
		Payload: SessionMetaPayload{
			SessionID:   sessionID,
			Model:       model,
			Provider:    provider,
			Temperature: temperature,
			Thinking:    thinking,
		},
	})
}

// BroadcastSessionContextTokens broadcasts a session.context event carrying a
// node session's live context-window occupancy. Graph nodes emit it per
// round-trip so the fleet pane's context bar reflects real occupancy while the
// node runs (DB token counts only update at completion).
func (s *LocalService) BroadcastSessionContextTokens(sessionID string, contextTokens int64) {
	if sessionID == "" || contextTokens <= 0 {
		return
	}
	s.broadcast(Event{
		Type:      EventTypeSessionContext,
		SessionID: sessionID,
		Payload: SessionContextPayload{
			SessionID:     sessionID,
			ContextTokens: contextTokens,
		},
	})
}

// BroadcastSessionText broadcasts a session.text event for an arbitrary
// session id. Used by graph nodes (which synthesize session ids of the form
// "graph:<TaskID>:<Node>") to stream LLM text through the same pipeline as
// worker sessions.
func (s *LocalService) BroadcastSessionText(sessionID, text string) {
	if text == "" {
		return
	}
	s.broadcast(Event{
		Type:      EventTypeSessionText,
		SessionID: sessionID,
		Payload:   SessionTextPayload{Text: text},
	})
}

// BroadcastSessionToolCall emits a session.tool_call event for a graph
// node, reusing the same event type the runtime emits for worker
// sessions so the TUI renders graph activity identically.
func (s *LocalService) BroadcastSessionToolCall(sessionID, callID, name string, args json.RawMessage) {
	if name == "" {
		return
	}
	s.broadcast(Event{
		Type:      EventTypeSessionToolCall,
		SessionID: sessionID,
		Payload: SessionToolCallPayload{
			ToolCall: ToolCall{
				ID:        callID,
				Name:      name,
				Arguments: args,
			},
		},
	})
}

// BroadcastSessionReasoning emits a session.reasoning event for a
// graph node. Routes through its own event type so the TUI can style
// reasoning differently from plain output text.
func (s *LocalService) BroadcastSessionReasoning(sessionID, text string) {
	if text == "" {
		return
	}
	s.broadcast(Event{
		Type:      EventTypeSessionReasoning,
		SessionID: sessionID,
		Payload:   SessionReasoningPayload{Text: text},
	})
}

// BroadcastSessionToolResult emits a session.tool_result event for a
// graph node. CallID may be empty — mycelium's tool-result events do
// not carry the originating call id, and the TUI tolerates an empty
// CallID (it only uses the string for optional pairing).
func (s *LocalService) BroadcastSessionToolResult(sessionID, callID, name, result, errMsg string) {
	s.broadcast(Event{
		Type:      EventTypeSessionToolResult,
		SessionID: sessionID,
		Payload: SessionToolResultPayload{
			Result: ToolCallResult{
				CallID: callID,
				Name:   name,
				Result: result,
				Error:  errMsg,
			},
		},
	})
}

// BroadcastSessionFileChange emits a session.file_change event for a graph
// node's write_file/edit_file mutation. The TUI pairs it with the matching
// in-flight tool item by tool name + path.
func (s *LocalService) BroadcastSessionFileChange(sessionID string, fc runtime.FileChange) {
	s.broadcast(Event{
		Type:      EventTypeSessionFileChange,
		SessionID: sessionID,
		Payload: SessionFileChangePayload{
			ToolName:  fc.ToolName,
			Path:      fc.Path,
			Diff:      fc.Diff,
			Added:     fc.Added,
			Removed:   fc.Removed,
			Created:   fc.Created,
			Truncated: fc.Truncated,
		},
	})
}

// BroadcastSessionStarted bridges a runtime session into the unified service
// event stream. It emits session.started immediately, then spawns a goroutine
// that subscribes to the session's events and re-emits them as session.text /
// session.tool_call / session.tool_result events. When the session terminates,
// it emits a final session.done event.
//
// This is the only path by which worker session activity reaches subscribers
// (TUI clients, SSE clients). It must be wired to runtime.Runtime.OnSessionStarted
// during server startup.
func (s *LocalService) BroadcastSessionStarted(sess *runtime.Session) {
	snap := sess.Snapshot()
	sessionID := snap.ID

	// Subscribe BEFORE emitting session.started so that any events the session
	// produces between SpawnWorker's OnSessionStarted invocation and the start
	// of Run() are captured. Subscribe is safe to call before Run() begins.
	events := sess.Subscribe()

	s.broadcast(Event{
		Type:      EventTypeSessionStarted,
		SessionID: sessionID,
		Payload: SessionStartedPayload{
			SessionID:      sessionID,
			WorkerName:     snap.WorkerID,
			Task:           sess.Task(),
			JobID:          snap.JobID,
			TaskID:         snap.TaskID,
			SystemPrompt:   sess.SystemPrompt(),
			InitialMessage: sess.InitialMessage(),
		},
	})

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic in session event forwarder",
					"session_id", sessionID,
					"panic", fmt.Sprintf("%v", r),
					"stack", string(debug.Stack()))
			}
		}()

		// Batch session.text events with a 16ms window. The runtime emits one
		// SessionEventText per token, which for a fast model can be 100+ events
		// per second. Each broadcast turns into a wire message, an SSE write,
		// and (on the client) a Bubble Tea Msg that has to traverse an
		// unbuffered prog.Send. Without batching, the TUI's main loop falls
		// behind and the Subscribe channel fills, dropping events and freezing
		// the UI. The operator already does this for its own text — see
		// cmd/batcher.go — and the same fix applies here.
		const textBatchWindow = 16 * time.Millisecond
		var textBuf strings.Builder
		var textTimer *time.Timer
		var textTimerCh <-chan time.Time
		flushText := func() {
			if textBuf.Len() == 0 {
				return
			}
			s.broadcast(Event{
				Type:      EventTypeSessionText,
				SessionID: sessionID,
				Payload:   SessionTextPayload{Text: textBuf.String()},
			})
			textBuf.Reset()
			if textTimer != nil {
				textTimer.Stop()
				textTimer = nil
				textTimerCh = nil
			}
		}
		armTimer := func() {
			if textTimer != nil {
				return
			}
			textTimer = time.NewTimer(textBatchWindow)
			textTimerCh = textTimer.C
		}

		for {
			select {
			case ev, ok := <-events:
				if !ok {
					// Subscribe channel closed — session has terminated. Flush
					// any pending text and emit session.done.
					flushText()
					finalSnap := sess.Snapshot()
					s.broadcast(Event{
						Type:      EventTypeSessionDone,
						SessionID: sessionID,
						Payload: SessionDonePayload{
							WorkerName: finalSnap.WorkerID,
							JobID:      finalSnap.JobID,
							TaskID:     finalSnap.TaskID,
							Status:     finalSnap.Status,
							FinalText:  sess.FinalText(),
						},
					})
					return
				}
				switch ev.Type {
				case runtime.SessionEventText:
					textBuf.WriteString(ev.Text)
					armTimer()
				case runtime.SessionEventToolCall:
					if ev.ToolCall == nil {
						continue
					}
					// Flush pending text before structural events so the
					// ordering observed by clients matches what the model
					// emitted.
					flushText()
					s.broadcast(Event{
						Type:      EventTypeSessionToolCall,
						SessionID: sessionID,
						Payload: SessionToolCallPayload{
							ToolCall: ToolCall{
								ID:        ev.ToolCall.ID,
								Name:      ev.ToolCall.Name,
								Arguments: ev.ToolCall.Arguments,
							},
						},
					})
				case runtime.SessionEventToolResult:
					if ev.ToolResult == nil {
						continue
					}
					flushText()
					s.broadcast(Event{
						Type:      EventTypeSessionToolResult,
						SessionID: sessionID,
						Payload: SessionToolResultPayload{
							Result: ToolCallResult{
								CallID: ev.ToolResult.CallID,
								Name:   ev.ToolResult.Name,
								Result: ev.ToolResult.Result,
								Error:  ev.ToolResult.Error,
							},
						},
					})
				case runtime.SessionEventFileChange:
					if ev.FileChange == nil {
						continue
					}
					flushText()
					s.BroadcastSessionFileChange(sessionID, *ev.FileChange)
				case runtime.SessionEventCompaction:
					if ev.Compaction == nil {
						continue
					}
					flushText()
					s.broadcast(Event{
						Type:      EventTypeSessionCompaction,
						SessionID: sessionID,
						Payload: SessionCompactionPayload{
							SessionID:            sessionID,
							Tier:                 ev.Compaction.Tier,
							BeforeTokens:         ev.Compaction.BeforeTokens,
							EstimatedAfterTokens: ev.Compaction.EstimatedAfterTokens,
						},
					})
				}

			case <-textTimerCh:
				flushText()
			}
		}
	}()
}
