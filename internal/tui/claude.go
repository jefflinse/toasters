package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/jefflinse/toasters/internal/claude"
	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/provider"
)

// streamClaudeResponse launches the claude CLI as a subprocess and returns a
// channel that delivers streamed response chunks. The subprocess is started
// with exec.CommandContext so context cancellation kills it automatically.
// The channel is closed when the stream ends, either normally or due to an error.
func streamClaudeResponse(ctx context.Context, prompt string, claudeCfg config.ClaudeConfig) <-chan provider.StreamEvent {
	ch := make(chan provider.StreamEvent, 64)

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
		} else {
			slog.Warn("claude.permission_mode not configured, defaulting to plan")
			args = append(args, "--permission-mode", "plan")
		}
		args = append(args, prompt)

		cmd := exec.CommandContext(ctx, claudeCfg.Path, args...)
		cmd.Stderr = io.Discard

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			ch <- provider.StreamEvent{Type: provider.EventError, Error: fmt.Errorf("opening claude stdout pipe: %w", err)}
			return
		}

		if err := cmd.Start(); err != nil {
			ch <- provider.StreamEvent{Type: provider.EventError, Error: fmt.Errorf("starting claude: %w", err)}
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
				var init claude.InitEvent
				if err := json.Unmarshal([]byte(line), &init); err == nil &&
					init.Type == "system" && init.Subtype == "init" {
					ch <- provider.StreamEvent{Meta: &provider.ClaudeMeta{
						Model:          init.Model,
						PermissionMode: init.PermissionMode,
						Version:        init.ClaudeCodeVersion,
						SessionID:      init.SessionID,
					}}
					continue
				}
				// Not a system/init line — fall through to normal parsing below.
			}

			var event claude.OuterEvent
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				// Malformed line — skip silently.
				continue
			}

			switch event.Type {
			case "stream_event":
				switch event.Event.Type {
				case "content_block_delta":
					// Content deltas are wrapped: {"type":"stream_event","event":{"type":"content_block_delta",...}}
					if event.Event.Delta.Type == "text_delta" && event.Event.Delta.Text != "" {
						ch <- provider.StreamEvent{Type: provider.EventText, Text: event.Event.Delta.Text}
					}
				case "content_block_start":
					if event.Event.ContentBlock.Type == "tool_use" {
						ch <- provider.StreamEvent{PendingTool: event.Event.ContentBlock.Name}
					}
				case "content_block_stop":
					ch <- provider.StreamEvent{ClearPendingTool: true}
				}
			case "assistant":
				// Handle thinking and tool_use blocks.
				for _, block := range event.Message.Content {
					switch block.Type {
					case "thinking":
						if block.Thinking != "" {
							ch <- provider.StreamEvent{Type: provider.EventText, Reasoning: block.Thinking}
						}
					case "tool_use":
						toolStr := fmt.Sprintf("[tool: %s]", block.Name)
						ch <- provider.StreamEvent{Type: provider.EventText, Text: "\n" + toolStr + "\n"}
					}
				}
			case "user":
				// User events carry tool results back from subagent calls.
				var userEvent claude.UserOuterEvent
				if err := json.Unmarshal([]byte(line), &userEvent); err == nil {
					for _, block := range userEvent.Message.Content {
						if block.Type != "tool_result" {
							continue
						}
						// Content can be a plain string or an array of content blocks.
						var text string
						if err := json.Unmarshal(block.Content, &text); err != nil {
							// Try array of {type, text} blocks.
							var blocks []struct {
								Type string `json:"type"`
								Text string `json:"text"`
							}
							if err := json.Unmarshal(block.Content, &blocks); err == nil {
								var sb strings.Builder
								for _, b := range blocks {
									if b.Type == "text" && b.Text != "" {
										sb.WriteString(b.Text)
									}
								}
								text = sb.String()
							}
						}
						if text != "" {
							ch <- provider.StreamEvent{
								Type:           provider.EventText,
								Text:           "\n[subagent result]\n" + text + "\n",
								SubagentResult: text,
							}
						}
					}
				}
			case "result":
				done = true
				if event.IsError {
					ch <- provider.StreamEvent{Type: provider.EventError, Error: fmt.Errorf("claude error: %s", event.Result)}
				} else {
					if event.Result != "" {
						ch <- provider.StreamEvent{Type: provider.EventText, Text: "\n[exit summary] " + event.Result}
					}
					ch <- provider.StreamEvent{Type: provider.EventDone}
				}
			}
		}

		// Wait for the process to exit so we don't leave zombies.
		_ = cmd.Wait()

		if !done {
			ch <- provider.StreamEvent{Type: provider.EventDone}
		}
	}()

	return ch
}
