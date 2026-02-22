package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"

	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/llm"
)

// claudeInitEvent is the first line emitted by `claude --output-format stream-json`.
// It carries model and permission metadata for the session.
type claudeInitEvent struct {
	Type              string `json:"type"`
	Subtype           string `json:"subtype"`
	Model             string `json:"model"`
	PermissionMode    string `json:"permissionMode"`
	ClaudeCodeVersion string `json:"claude_code_version"`
}

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
func streamClaudeResponse(ctx context.Context, prompt string, claudeCfg config.ClaudeConfig) <-chan llm.StreamResponse {
	ch := make(chan llm.StreamResponse, 64)

	go func() {
		defer close(ch)

		args := []string{
			"--print",
			"--output-format", "stream-json",
			"--include-partial-messages",
		}
		if claudeCfg.DefaultModel != "" {
			args = append(args, "--model", claudeCfg.DefaultModel)
		}
		if claudeCfg.PermissionMode != "" {
			args = append(args, "--permission-mode", claudeCfg.PermissionMode)
		}
		args = append(args, prompt)

		cmd := exec.CommandContext(ctx, claudeCfg.Path, args...)
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
		firstLine := true
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}

			// The very first non-empty line is always the system/init event.
			// Parse it separately and emit a Meta response, then move on.
			if firstLine {
				firstLine = false
				var init claudeInitEvent
				if err := json.Unmarshal([]byte(line), &init); err == nil &&
					init.Type == "system" && init.Subtype == "init" {
					ch <- llm.StreamResponse{Meta: &llm.ClaudeMeta{
						Model:          init.Model,
						PermissionMode: init.PermissionMode,
						Version:        init.ClaudeCodeVersion,
					}}
					continue
				}
				// Not a system/init line — fall through to normal parsing below.
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
