// Streaming: message sending and model fetching for the operator-driven TUI.
package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/service"
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
		Message:   service.ChatMessage{Role: service.MessageRoleUser, Content: text},
		Timestamp: time.Now(),
	})
	m.stats.MessageCount++
	m.err = nil
	m.scroll.userScrolled = false
	m.scroll.hasNewMessages = false

	m.updateViewportContent()
	m.chatViewport.GotoBottom()

	if m.svc == nil {
		return nil
	}

	m.stream.streaming = true
	m.stream.currentResponse = ""
	m.stats.ResponseStart = time.Now()
	svc := m.svc
	return tea.Batch(
		func() tea.Msg {
			ctx := context.Background()
			_, err := svc.Operator().SendMessage(ctx, text)
			if err != nil {
				return OperatorDoneMsg{Err: err}
			}
			// SendMessage returns immediately — OperatorDoneMsg is fired by the
			// OnTurnDone callback wired in cmd/root.go when the turn completes.
			return nil
		},
		spinnerTick(),
	)
}

// fetchModels returns a command that fetches available models from the LLM server.
func (m Model) fetchModels() tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		models, err := svc.System().ListModels(ctx)
		return ModelsMsg{Models: models, Err: err}
	}
}
