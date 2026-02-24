package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"

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
	SessionID         string `json:"session_id"`
}

// claudeInnerEvent is the inner event shape nested inside stream_event wrappers.
type claudeInnerEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
	ContentBlock struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"content_block"`
}

// claudeContentBlock is one element of an assistant message's content array.
type claudeContentBlock struct {
	Type     string `json:"type"`     // "text", "tool_use", or "thinking"
	ID       string `json:"id"`       // tool use ID (for type="tool_use")
	Name     string `json:"name"`     // for type="tool_use"
	Input    any    `json:"input"`    // for type="tool_use"
	Text     string `json:"text"`     // for type="text"
	Thinking string `json:"thinking"` // for type="thinking"
}

// claudeAssistantMessage is the message field inside a top-level "assistant" event.
type claudeAssistantMessage struct {
	Content []claudeContentBlock `json:"content"`
}

// claudeToolResultBlock is one element of a user message's content array,
// carrying the result of a tool call back to the model.
type claudeToolResultBlock struct {
	Type      string          `json:"type"`        // "tool_result"
	ToolUseID string          `json:"tool_use_id"` // matches the tool_use block ID
	Content   json.RawMessage `json:"content"`     // string or []content_block
}

// claudeUserMessage is the message field inside a top-level "user" event,
// typically carrying tool results from subagent calls.
type claudeUserMessage struct {
	Role    string                  `json:"role"`
	Content []claudeToolResultBlock `json:"content"`
}

// claudeUserOuterEvent is the top-level shape for type="user" events, which
// carry tool results back from subagent calls.
type claudeUserOuterEvent struct {
	Type    string            `json:"type"`
	Message claudeUserMessage `json:"message"`
}

// claudeOuterEvent is the top-level shape of a JSON line emitted by
// `claude --output-format stream-json`. Content deltas arrive wrapped in a
// "stream_event" envelope; terminal results arrive at the top level.
type claudeOuterEvent struct {
	Type    string                 `json:"type"`
	Event   claudeInnerEvent       `json:"event"`
	Message claudeAssistantMessage `json:"message"` // for type="assistant"
	Result  string                 `json:"result"`
	IsError bool                   `json:"is_error"`
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
		} else {
			args = append(args, "--dangerously-skip-permissions")
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
						SessionID:      init.SessionID,
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
				switch event.Event.Type {
				case "content_block_delta":
					// Content deltas are wrapped: {"type":"stream_event","event":{"type":"content_block_delta",...}}
					if event.Event.Delta.Type == "text_delta" && event.Event.Delta.Text != "" {
						ch <- llm.StreamResponse{Content: event.Event.Delta.Text}
					}
				case "content_block_start":
					if event.Event.ContentBlock.Type == "tool_use" {
						ch <- llm.StreamResponse{PendingTool: event.Event.ContentBlock.Name}
					}
				case "content_block_stop":
					ch <- llm.StreamResponse{ClearPendingTool: true}
				}
			case "assistant":
				// Handle thinking and tool_use blocks.
				for _, block := range event.Message.Content {
					switch block.Type {
					case "thinking":
						if block.Thinking != "" {
							ch <- llm.StreamResponse{Reasoning: block.Thinking}
						}
					case "tool_use":
						toolStr := fmt.Sprintf("[tool: %s]", block.Name)
						ch <- llm.StreamResponse{Content: "\n" + toolStr + "\n"}
					}
				}
			case "user":
				// User events carry tool results back from subagent calls.
				var userEvent claudeUserOuterEvent
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
							ch <- llm.StreamResponse{
								Content:        "\n[subagent result]\n" + text + "\n",
								SubagentResult: text,
							}
						}
					}
				}
			case "result":
				done = true
				if event.IsError {
					ch <- llm.StreamResponse{Error: fmt.Errorf("claude error: %s", event.Result)}
				} else {
					if event.Result != "" {
						ch <- llm.StreamResponse{Content: "\n[exit summary] " + event.Result}
					}
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
