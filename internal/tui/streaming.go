// Streaming: LLM stream lifecycle including start, send, chunk processing, and completion draining.
package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/provider"
)

// streamStartedMsg carries the channel back to the model after the stream begins.
type streamStartedMsg struct {
	ch <-chan provider.StreamEvent
}

func (m *Model) drainPendingCompletions() ([]provider.Message, bool) {
	if len(m.chat.pendingCompletions) == 0 {
		return m.messagesFromEntries(), false
	}
	for _, pc := range m.chat.pendingCompletions {
		m.appendEntry(ChatEntry{
			Message:   provider.Message{Role: "user", Content: pc.notification},
			Timestamp: time.Now(),
		})
	}
	m.chat.pendingCompletions = nil
	return m.messagesFromEntries(), true
}

// startStream begins a new LLM stream with the current messages and available tools.
// It sets m.stream.streaming = true and m.stats.ResponseStart.
func (m *Model) startStream(msgs []provider.Message) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.stream.cancelStream = cancel
	m.stream.streaming = true
	m.stream.currentResponse = ""
	m.stream.currentReasoning = ""
	m.stats.ResponseStart = time.Now()

	client := m.llmClient
	tools := m.toolExec.Tools

	// Build the ChatRequest: extract system messages, pass tools.
	req := provider.ChatRequest{}
	var systemParts []string
	for _, msg := range msgs {
		if msg.Role == "system" {
			if msg.Content != "" {
				systemParts = append(systemParts, msg.Content)
			}
			continue
		}
		req.Messages = append(req.Messages, msg)
	}
	if len(systemParts) > 0 {
		req.System = strings.Join(systemParts, "\n\n")
	}
	for _, t := range tools {
		req.Tools = append(req.Tools, t)
	}

	return tea.Batch(
		func() tea.Msg {
			ch, err := client.ChatStream(ctx, req)
			if err != nil {
				// Return error as a stream event channel with one error event.
				errCh := make(chan provider.StreamEvent, 1)
				errCh <- provider.StreamEvent{Type: provider.EventError, Error: err}
				close(errCh)
				return streamStartedMsg{ch: errCh}
			}
			return streamStartedMsg{ch: ch}
		},
		spinnerTick(), // re-arm spinner animation for streaming cursor
	)
}

// sendMessage takes the current input, appends it to history, and starts streaming.
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

	return m.startStream(m.messagesFromEntries())
}

// sendClaudeMessage appends the user prompt to history and starts a subprocess
// stream via the claude CLI, reusing the same streaming pipeline as sendMessage.
func (m *Model) sendClaudeMessage(prompt string) tea.Cmd {
	m.input.Blur()
	m.cmdPopup.filteredCmds = nil
	m.cmdPopup.selectedIdx = 0

	m.appendEntry(ChatEntry{
		Message:   provider.Message{Role: "user", Content: "/claude " + prompt},
		Timestamp: time.Now(),
	})
	m.stats.MessageCount++
	m.stream.streaming = true
	m.stream.currentResponse = ""
	m.stream.currentReasoning = ""
	m.err = nil
	m.scroll.userScrolled = false
	m.scroll.hasNewMessages = false
	m.stats.ResponseStart = time.Now()

	m.updateViewportContent()
	m.chatViewport.GotoBottom()

	ctx, cancel := context.WithCancel(context.Background())
	m.stream.cancelStream = cancel

	ch := streamClaudeResponse(ctx, prompt, m.claudeCfg)
	return tea.Batch(
		func() tea.Msg {
			return streamStartedMsg{ch: ch}
		},
		spinnerTick(), // re-arm spinner animation for streaming cursor
	)
}

// sendAnthropicMessage appends the user prompt to history and starts a direct
// Anthropic API stream using OAuth credentials from the macOS Keychain.
func (m *Model) sendAnthropicMessage(prompt string) tea.Cmd {
	m.input.Blur()
	m.cmdPopup.filteredCmds = nil
	m.cmdPopup.selectedIdx = 0

	m.appendEntry(ChatEntry{
		Message:   provider.Message{Role: "user", Content: "/anthropic " + prompt},
		Timestamp: time.Now(),
	})
	m.stats.MessageCount++
	m.stream.streaming = true
	m.stream.currentResponse = ""
	m.stream.currentReasoning = ""
	m.err = nil
	m.scroll.userScrolled = false
	m.scroll.hasNewMessages = false
	m.stats.ResponseStart = time.Now()

	m.updateViewportContent()
	m.chatViewport.GotoBottom()

	ctx, cancel := context.WithCancel(context.Background())
	m.stream.cancelStream = cancel

	client := provider.NewAnthropic("anthropic", "")
	ch, err := client.ChatStream(ctx, provider.ChatRequest{
		Messages: []provider.Message{{Role: "user", Content: prompt}},
	})
	if err != nil {
		errCh := make(chan provider.StreamEvent, 1)
		errCh <- provider.StreamEvent{Type: provider.EventError, Error: err}
		close(errCh)
		ch = errCh
	}
	return tea.Batch(
		func() tea.Msg {
			return streamStartedMsg{ch: ch}
		},
		spinnerTick(),
	)
}

// waitForChunk reads one item from the stream channel and returns the appropriate Msg.
func waitForChunk(ch <-chan provider.StreamEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return StreamDoneMsg{}
		}
		switch ev.Type {
		case provider.EventError:
			return StreamErrMsg{Err: ev.Error}
		case provider.EventToolCall:
			if ev.ToolCall != nil {
				return ToolCallMsg{Calls: []provider.ToolCall{*ev.ToolCall}}
			}
			return StreamChunkMsg{}
		case provider.EventUsage:
			if ev.Usage != nil {
				return StreamDoneMsg{
					Model: ev.Model,
					Usage: ev.Usage,
				}
			}
			return StreamChunkMsg{}
		case provider.EventDone:
			return StreamDoneMsg{Model: ev.Model, Usage: ev.Usage}
		case provider.EventText:
			return StreamChunkMsg{Content: ev.Text, Reasoning: ev.Reasoning}
		}
		// Gateway-specific events.
		if ev.Meta != nil {
			return claudeMetaMsg{
				Model:          ev.Meta.Model,
				PermissionMode: ev.Meta.PermissionMode,
				Version:        ev.Meta.Version,
				SessionID:      ev.Meta.SessionID,
			}
		}
		return StreamChunkMsg{Content: ev.Text, Reasoning: ev.Reasoning}
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

// formatClaudeMeta returns a short byline string for a claudeMetaMsg.
func formatClaudeMeta(msg claudeMetaMsg) string {
	s := msg.Model + " · " + msg.PermissionMode + " mode"
	if msg.Version != "" {
		s += " · claude v" + msg.Version
	}
	if msg.SessionID != "" {
		short := msg.SessionID
		if len(short) > 8 {
			short = short[:8]
		}
		s += " · session: " + short
	}
	return s
}
