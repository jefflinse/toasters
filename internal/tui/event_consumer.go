// event_consumer.go: translates service.Event values from the unified event
// stream into Bubble Tea messages for the TUI update loop.
package tui

import (
	"context"
	"log/slog"
	"sync/atomic"

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
	ch := svc.Events().Subscribe(ctx)
	for ev := range ch {
		msg := translateEvent(ev)
		if msg != nil {
			if p := prog.Load(); p != nil {
				p.Send(msg)
			}
		}
	}
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
			Jobs:            p.State.Jobs,
			Tasks:           p.State.Tasks,
			Progress:        p.State.Reports,
			Sessions:        p.State.ActiveSessions,
			RuntimeSessions: p.State.LiveSnapshots,
			FeedEntries:     p.State.FeedEntries,
		}

	case service.EventTypeSessionStarted,
		service.EventTypeSessionText,
		service.EventTypeSessionToolCall,
		service.EventTypeSessionToolResult,
		service.EventTypeSessionDone:
		// Session events are delivered via the direct rt.OnSessionStarted callback
		// in cmd/root.go, not through the service event stream. When Phase 2 moves
		// session event broadcasting into LocalService, these handlers should be
		// re-enabled and the direct callback removed.
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

	case service.EventTypeTaskStarted,
		service.EventTypeTaskCompleted,
		service.EventTypeTaskFailed,
		service.EventTypeBlockerReported,
		service.EventTypeJobCompleted,
		service.EventTypeTaskAssigned:
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
	case "generate_agent":
		return agentGeneratedMsg{content: p.Result.Content}
	case "generate_team":
		return teamGeneratedMsg{content: p.Result.Content, agentNames: p.Result.AgentNames}
	case "promote_team":
		return teamPromotedMsg{teamName: p.Result.Content}
	case "detect_coordinator":
		// The service already called SetCoordinator; send TeamsAutoDetectDoneMsg
		// so the modal clears its autoDetecting flag and reloads.
		return TeamsAutoDetectDoneMsg{agentName: p.Result.Content}
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
	case "generate_agent":
		return agentGeneratedMsg{err: err}
	case "generate_team":
		return teamGeneratedMsg{err: err}
	case "promote_team":
		return teamPromotedMsg{err: err}
	case "detect_coordinator":
		return TeamsAutoDetectDoneMsg{err: err}
	default:
		slog.Debug("unhandled operation.failed kind", "kind", p.Kind)
		return nil
	}
}

// operationError wraps an error string as an error value.
type operationError string

func (e operationError) Error() string { return string(e) }
