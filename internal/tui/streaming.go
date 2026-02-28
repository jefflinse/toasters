// Streaming: message sending and model fetching for the operator-driven TUI.
package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/operator"
	"github.com/jefflinse/toasters/internal/provider"
)

// sendMessage takes the current input, appends it to history, and routes it
// through the operator event loop.
func (m *Model) sendMessage() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return nil
	}

	m.input.Reset()
	m.input.Blur()
	m.cmdPopup.show = false
	m.cmdPopup.filteredCmds = nil
	m.cmdPopup.selectedIdx = 0

	m.appendEntry(ChatEntry{
		Message:   provider.Message{Role: "user", Content: text},
		Timestamp: time.Now(),
	})
	m.stats.MessageCount++
	m.err = nil
	m.scroll.userScrolled = false
	m.scroll.hasNewMessages = false

	m.updateViewportContent()
	m.chatViewport.GotoBottom()

	if m.operator == nil {
		return nil
	}

	m.stream.streaming = true
	m.stream.currentResponse = ""
	m.stats.ResponseStart = time.Now()
	op := m.operator
	return tea.Batch(
		func() tea.Msg {
			ctx := context.Background()
			_ = op.Send(ctx, operator.Event{
				Type:    operator.EventUserMessage,
				Payload: operator.UserMessagePayload{Text: text},
			})
			// Send returns immediately — OperatorDoneMsg is fired by the
			// OnTurnDone callback wired in cmd/root.go when the turn completes.
			return nil
		},
		spinnerTick(),
	)
}

// notifyOperator sends a notification message to the operator. If the operator
// is currently streaming, the notification is sent asynchronously (the operator
// will queue it). Returns a tea.Cmd that performs the send.
func (m *Model) notifyOperator(notification string) tea.Cmd {
	if m.operator == nil {
		return nil
	}
	op := m.operator
	return func() tea.Msg {
		ctx := context.Background()
		_ = op.Send(ctx, operator.Event{
			Type:    operator.EventUserMessage,
			Payload: operator.UserMessagePayload{Text: notification},
		})
		return nil
	}
}

// fetchModels returns a command that fetches available models from the LLM server.
func (m Model) fetchModels() tea.Cmd {
	client := m.llmClient
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		models, err := client.Models(ctx)
		return ModelsMsg{Models: models, Err: err}
	}
}
