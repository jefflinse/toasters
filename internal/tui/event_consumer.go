// event_consumer.go: translates service.Event values from the unified event
// stream into Bubble Tea messages for the TUI update loop.
package tui

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// ConsumeServiceEvents subscribes to the service event stream and forwards
// events to the Bubble Tea program as typed TUI messages. It runs until ctx
// is cancelled or the event channel is closed.
//
// Call this in a goroutine after creating the tea.Program:
//
//	go tui.ConsumeServiceEvents(ctx, svc, &p)
func ConsumeServiceEvents(ctx context.Context, svc service.Service, prog *atomic.Pointer[tea.Program]) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("PANIC in service event consumer — TUI will stop receiving events",
				"panic", fmt.Sprintf("%v", r),
				"stack", string(debug.Stack()),
			)
		}
	}()

	ch := svc.Events().Subscribe(ctx)
	for ev := range ch {
		// Trace every event so a freeze investigation has timing data.
		// Filter out heartbeats and progress updates from the trace because
		// they are extremely high-frequency and would drown out everything
		// useful.
		if ev.Type != service.EventTypeHeartbeat && ev.Type != service.EventTypeProgressUpdate {
			slog.Debug("consume event", "type", ev.Type, "seq", ev.Seq, "session_id", ev.SessionID, "turn_id", ev.TurnID)
		}

		msg := translateEvent(ev)
		if msg == nil {
			continue
		}

		// p.Send is BLOCKING in Bubble Tea v2 — the program's message channel
		// is unbuffered. If the Update loop is slow, this stalls the consumer
		// goroutine and the SSE reader's channel fills up, dropping events.
		// Track how long Send takes so we can spot pathological cases in the
		// log without manually instrumenting the model.
		p := prog.Load()
		if p == nil {
			continue
		}
		start := time.Now()
		p.Send(msg)
		if d := time.Since(start); d > 100*time.Millisecond {
			slog.Warn("slow prog.Send blocking the event consumer",
				"event_type", ev.Type,
				"duration", d,
			)
		}
	}
	slog.Info("service event consumer exited (channel closed)")
}

// translateEvent converts a service.Event into a Bubble Tea message.
// Returns nil for events that should not be forwarded to the TUI.
func translateEvent(ev service.Event) tea.Msg {
	switch ev.Type {

	case service.EventTypeOperatorText:
		p, ok := ev.Payload.(service.OperatorTextPayload)
		if !ok {
			return nil
		}
		return OperatorTextMsg{Text: p.Text}

	case service.EventTypeOperatorDone:
		p, ok := ev.Payload.(service.OperatorDonePayload)
		if !ok {
			return OperatorDoneMsg{}
		}
		return OperatorDoneMsg{
			ModelName:       p.ModelName,
			TokensIn:        p.TokensIn,
			TokensOut:       p.TokensOut,
			ReasoningTokens: p.ReasoningTokens,
		}

	case service.EventTypeProgressUpdate:
		p, ok := ev.Payload.(service.ProgressUpdatePayload)
		if !ok {
			return nil
		}
		return progressPollMsg{
			Jobs:        p.State.Jobs,
			Tasks:       p.State.Tasks,
			Progress:    p.State.Reports,
			Sessions:    p.State.ActiveSessions,
			FeedEntries: p.State.FeedEntries,
			MCPServers:  p.State.MCPServers,
		}

	case service.EventTypeSessionStarted:
		p, ok := ev.Payload.(service.SessionStartedPayload)
		if !ok {
			return nil
		}
		return SessionStartedMsg{
			SessionID:      p.SessionID,
			WorkerName:     p.WorkerName,
			Task:           p.Task,
			JobID:          p.JobID,
			TaskID:         p.TaskID,
			SystemPrompt:   p.SystemPrompt,
			InitialMessage: p.InitialMessage,
		}

	case service.EventTypeSessionText:
		p, ok := ev.Payload.(service.SessionTextPayload)
		if !ok {
			return nil
		}
		return SessionTextMsg{
			SessionID: ev.SessionID,
			Text:      p.Text,
		}

	case service.EventTypeSessionReasoning:
		p, ok := ev.Payload.(service.SessionReasoningPayload)
		if !ok {
			return nil
		}
		return SessionReasoningMsg{
			SessionID: ev.SessionID,
			Text:      p.Text,
		}

	case service.EventTypeSessionToolCall:
		p, ok := ev.Payload.(service.SessionToolCallPayload)
		if !ok {
			return nil
		}
		return SessionToolCallMsg{
			SessionID: ev.SessionID,
			ToolID:    p.ToolCall.ID,
			ToolName:  p.ToolCall.Name,
			ToolInput: string(p.ToolCall.Arguments),
		}

	case service.EventTypeSessionToolResult:
		p, ok := ev.Payload.(service.SessionToolResultPayload)
		if !ok {
			return nil
		}
		return SessionToolResultMsg{
			SessionID:  ev.SessionID,
			CallID:     p.Result.CallID,
			ToolName:   p.Result.Name,
			ToolOutput: p.Result.Result,
			IsError:    p.Result.Error != "",
		}

	case service.EventTypeSessionDone:
		p, ok := ev.Payload.(service.SessionDonePayload)
		if !ok {
			return nil
		}
		return SessionDoneMsg{
			SessionID:  ev.SessionID,
			WorkerName: p.WorkerName,
			JobID:      p.JobID,
			TaskID:     p.TaskID,
			FinalText:  p.FinalText,
			Status:     p.Status,
		}

	case service.EventTypeGraphNodeStarted:
		p, ok := ev.Payload.(service.GraphNodeStartedPayload)
		if !ok {
			return nil
		}
		return GraphNodeStartedMsg{
			SessionID: "graph:" + p.TaskID + ":" + p.Node,
			Node:      p.Node,
			JobID:     p.JobID,
			TaskID:    p.TaskID,
		}

	case service.EventTypeGraphNodeCompleted:
		p, ok := ev.Payload.(service.GraphNodeCompletedPayload)
		if !ok {
			return nil
		}
		return GraphNodeDoneMsg{
			SessionID: "graph:" + p.TaskID + ":" + p.Node,
			Node:      p.Node,
			JobID:     p.JobID,
			TaskID:    p.TaskID,
			Status:    p.Status,
		}

	case service.EventTypeGraphCompleted, service.EventTypeGraphFailed:
		// Task-level graph events — the operator advances via task_completed
		// / task_failed. Nothing extra for the TUI to do here.
		return nil

	case service.EventTypeDefinitionsReloaded:
		return DefinitionsReloadedMsg{}

	case service.EventTypeOperationCompleted:
		p, ok := ev.Payload.(service.OperationCompletedPayload)
		if !ok {
			return nil
		}
		return handleOperationCompleted(p)

	case service.EventTypeOperationFailed:
		p, ok := ev.Payload.(service.OperationFailedPayload)
		if !ok {
			return nil
		}
		return handleOperationFailed(p)

	case service.EventTypeOperatorPrompt:
		p, ok := ev.Payload.(service.OperatorPromptPayload)
		if !ok {
			return nil
		}
		return OperatorPromptMsg{
			RequestID: p.RequestID,
			Question:  p.Question,
			Options:   p.Options,
		}

	case service.EventTypeJobCreated,
		service.EventTypeTaskCreated,
		service.EventTypeTaskAssigned,
		service.EventTypeTaskStarted,
		service.EventTypeTaskCompleted,
		service.EventTypeTaskFailed,
		service.EventTypeJobCompleted:
		// These are forwarded as OperatorEventMsg for feed display.
		return OperatorEventMsg{Event: ev}

	case service.EventTypeConnectionLost:
		p, ok := ev.Payload.(service.ConnectionLostPayload)
		if !ok {
			return nil
		}
		return ConnectionLostMsg{Error: p.Error}

	case service.EventTypeConnectionRestored:
		return ConnectionRestoredMsg{}

	case service.EventTypeHeartbeat:
		// Heartbeats are not forwarded to the TUI.
		return nil

	default:
		slog.Debug("unhandled service event type in consumer", "type", ev.Type)
		return nil
	}
}

// handleOperationCompleted translates an operation.completed event into the
// appropriate TUI message based on the operation kind.
func handleOperationCompleted(p service.OperationCompletedPayload) tea.Msg {
	switch p.Kind {
	case "generate_skill":
		return skillGeneratedMsg{content: p.Result.Content}
	default:
		slog.Debug("unhandled operation.completed kind", "kind", p.Kind)
		return nil
	}
}

// handleOperationFailed translates an operation.failed event into the
// appropriate TUI message based on the operation kind.
func handleOperationFailed(p service.OperationFailedPayload) tea.Msg {
	err := operationError(p.Error)
	switch p.Kind {
	case "generate_skill":
		return skillGeneratedMsg{err: err}
	default:
		slog.Debug("unhandled operation.failed kind", "kind", p.Kind)
		return nil
	}
}

// operationError wraps an error string as an error value.
type operationError string

func (e operationError) Error() string { return string(e) }
