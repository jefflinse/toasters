// Streaming: LLM stream lifecycle including start, send, chunk processing, and completion draining.
package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/anthropic"
	"github.com/jefflinse/toasters/internal/llm"
)

// streamStartedMsg carries the channel back to the model after the stream begins.
type streamStartedMsg struct {
	ch <-chan llm.StreamResponse
}

func (m *Model) drainPendingCompletions() ([]llm.Message, bool) {
	if len(m.chat.pendingCompletions) == 0 {
		return m.messagesFromEntries(), false
	}
	for _, pc := range m.chat.pendingCompletions {
		m.appendEntry(ChatEntry{
			Message:   llm.Message{Role: "user", Content: pc.notification},
			Timestamp: time.Now(),
		})
	}
	m.chat.pendingCompletions = nil
	return m.messagesFromEntries(), true
}

// startStream begins a new LLM stream with the current messages and available tools.
// It sets m.stream.streaming = true and m.stats.ResponseStart.
func (m *Model) startStream(msgs []llm.Message) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.stream.cancelStream = cancel
	m.stream.streaming = true
	m.stream.currentResponse = ""
	m.stream.currentReasoning = ""
	m.stats.ResponseStart = time.Now()

	var temperature float64

	client := m.llmClient
	tools := m.toolExec.Tools
	return tea.Batch(
		func() tea.Msg {
			ch := client.ChatCompletionStreamWithTools(ctx, msgs, tools, temperature)
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
		Message:   llm.Message{Role: "user", Content: text},
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
		Message:   llm.Message{Role: "user", Content: "/claude " + prompt},
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
		Message:   llm.Message{Role: "user", Content: "/anthropic " + prompt},
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

	client := anthropic.NewClient(anthropic.DefaultModel)
	ch := client.ChatCompletionStream(ctx, []llm.Message{{Role: "user", Content: prompt}}, 0)
	return tea.Batch(
		func() tea.Msg {
			return streamStartedMsg{ch: ch}
		},
		spinnerTick(),
	)
}

// waitForChunk reads one item from the stream channel and returns the appropriate Msg.
func waitForChunk(ch <-chan llm.StreamResponse) tea.Cmd {
	return func() tea.Msg {
		resp, ok := <-ch
		if !ok {
			return StreamDoneMsg{}
		}
		if resp.Error != nil {
			return StreamErrMsg{Err: resp.Error}
		}
		if resp.Meta != nil {
			return claudeMetaMsg{
				Model:          resp.Meta.Model,
				PermissionMode: resp.Meta.PermissionMode,
				Version:        resp.Meta.Version,
				SessionID:      resp.Meta.SessionID,
			}
		}
		if len(resp.ToolCalls) > 0 {
			return ToolCallMsg{Calls: resp.ToolCalls}
		}
		if resp.Done {
			return StreamDoneMsg{Model: resp.Model, Usage: resp.Usage}
		}
		return StreamChunkMsg{Content: resp.Content, Reasoning: resp.Reasoning}
	}
}

// fetchModels returns a command that fetches available models from the LLM server.
func (m Model) fetchModels() tea.Cmd {
	client := m.llmClient
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		models, err := client.FetchModels(ctx)
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
