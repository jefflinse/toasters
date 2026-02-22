package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"

	"github.com/jefflinse/toasters/internal/llm"
)

// claudeInnerEvent is the inner event shape nested inside stream_event wrappers.
type claudeInnerEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

// claudeOuterEvent is the top-level shape of a JSON line emitted by
// `claude --output-format stream-json`. Content deltas arrive wrapped in a
// "stream_event" envelope; terminal results arrive at the top level.
type claudeOuterEvent struct {
	Type    string           `json:"type"`
	Event   claudeInnerEvent `json:"event"`
	Result  string           `json:"result"`
	IsError bool             `json:"is_error"`
}

// streamClaudeResponse launches the claude CLI as a subprocess and returns a
// channel that delivers streamed response chunks. The subprocess is started
// with exec.CommandContext so context cancellation kills it automatically.
// The channel is closed when the stream ends, either normally or due to an error.
func streamClaudeResponse(ctx context.Context, prompt string) <-chan llm.StreamResponse {
	ch := make(chan llm.StreamResponse, 64)

	go func() {
		defer close(ch)

		cmd := exec.CommandContext(ctx,
			"claude",
			"--print",
			"--output-format", "stream-json",
			"--include-partial-messages",
			prompt,
		)
		cmd.Stderr = io.Discard

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			ch <- llm.StreamResponse{Error: fmt.Errorf("opening claude stdout pipe: %w", err)}
			return
		}

		if err := cmd.Start(); err != nil {
			ch <- llm.StreamResponse{Error: fmt.Errorf("starting claude: %w", err)}
			return
		}

		done := false
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}

			var event claudeOuterEvent
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				// Malformed line — skip silently.
				continue
			}

			switch event.Type {
			case "stream_event":
				// Content deltas are wrapped: {"type":"stream_event","event":{"type":"content_block_delta",...}}
				if event.Event.Type == "content_block_delta" &&
					event.Event.Delta.Type == "text_delta" &&
					event.Event.Delta.Text != "" {
					ch <- llm.StreamResponse{Content: event.Event.Delta.Text}
				}
			case "result":
				done = true
				if event.IsError {
					ch <- llm.StreamResponse{Error: fmt.Errorf("claude error: %s", event.Result)}
				} else {
					ch <- llm.StreamResponse{Done: true}
				}
			}
		}

		// Wait for the process to exit so we don't leave zombies.
		_ = cmd.Wait()

		if !done {
			ch <- llm.StreamResponse{Done: true}
		}
	}()

	return ch
}
