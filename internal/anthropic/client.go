package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/jefflinse/toasters/internal/llm"
)

const DefaultModel = "claude-sonnet-4-20250514"

// keychainCredentials holds the OAuth token read from the macOS Keychain.
type keychainCredentials struct {
	AccessToken string
	ExpiresAt   int64 // unix millis
}

// readKeychainCredentials shells out to the macOS security CLI to extract
// the Claude Code OAuth access token from the Keychain.
func readKeychainCredentials() (*keychainCredentials, error) {
	cmd := exec.Command("security", "find-generic-password", "-s", "Claude Code-credentials", "-w")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("reading keychain: %w (is Claude Code signed in?)", err)
	}

	raw := strings.TrimSpace(string(out))

	// The keychain entry is a JSON blob. We need claudeAiOauth.accessToken and .expiresAt.
	var parsed struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"` // unix millis
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("parsing keychain credentials: %w", err)
	}

	if parsed.ClaudeAiOauth.AccessToken == "" {
		return nil, fmt.Errorf("no access token found in keychain credentials")
	}

	if parsed.ClaudeAiOauth.ExpiresAt > 0 && parsed.ClaudeAiOauth.ExpiresAt < time.Now().UnixMilli() {
		return nil, fmt.Errorf("OAuth token expired at %s", time.UnixMilli(parsed.ClaudeAiOauth.ExpiresAt).Format(time.RFC3339))
	}

	return &keychainCredentials{
		AccessToken: parsed.ClaudeAiOauth.AccessToken,
		ExpiresAt:   parsed.ClaudeAiOauth.ExpiresAt,
	}, nil
}

// anthropicRequest is the Anthropic Messages API request body.
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Stream    bool               `json:"stream"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// SSE event types from the Anthropic streaming API.
type messageStartEvent struct {
	Type    string `json:"type"`
	Message struct {
		Model string   `json:"model"`
		Usage apiUsage `json:"usage"`
	} `json:"message"`
}

type contentBlockDeltaEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

type messageDeltaEvent struct {
	Type  string `json:"type"`
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage apiUsage `json:"usage"`
}

type errorEvent struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type apiUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// StreamMessage reads OAuth credentials from the macOS Keychain and streams
// a message to the Anthropic Messages API, returning results on a channel.
func StreamMessage(ctx context.Context, model string, prompt string) <-chan llm.StreamResponse {
	ch := make(chan llm.StreamResponse, 1)

	go func() {
		defer close(ch)
		streamMessage(ctx, model, prompt, ch)
	}()

	return ch
}

func streamMessage(ctx context.Context, model string, prompt string, ch chan<- llm.StreamResponse) {
	creds, err := readKeychainCredentials()
	if err != nil {
		ch <- llm.StreamResponse{Error: fmt.Errorf("anthropic auth: %w", err)}
		return
	}

	reqBody := anthropicRequest{
		Model:     model,
		MaxTokens: 8192,
		Stream:    true,
		Messages: []anthropicMessage{
			{Role: "user", Content: prompt},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		ch <- llm.StreamResponse{Error: fmt.Errorf("marshaling request: %w", err)}
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		ch <- llm.StreamResponse{Error: fmt.Errorf("creating request: %w", err)}
		return
	}

	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		ch <- llm.StreamResponse{Error: fmt.Errorf("sending request: %w", err)}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		ch <- llm.StreamResponse{Error: fmt.Errorf("anthropic API status %d: %s", resp.StatusCode, string(respBody))}
		return
	}

	var (
		lastModel string
		lastUsage *llm.Usage
		eventType string
	)

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if ctx.Err() != nil {
			ch <- llm.StreamResponse{Error: ctx.Err()}
			return
		}

		line := scanner.Text()

		// Blank line = end of SSE event block.
		if line == "" {
			eventType = ""
			continue
		}

		// Capture the event type.
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}

		// We only process data lines.
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		switch eventType {
		case "message_start":
			var ev messageStartEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				ch <- llm.StreamResponse{Error: fmt.Errorf("parsing message_start: %w", err)}
				return
			}
			lastModel = ev.Message.Model
			lastUsage = &llm.Usage{
				PromptTokens: ev.Message.Usage.InputTokens,
			}

		case "content_block_delta":
			var ev contentBlockDeltaEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				ch <- llm.StreamResponse{Error: fmt.Errorf("parsing content_block_delta: %w", err)}
				return
			}
			if ev.Delta.Text != "" {
				ch <- llm.StreamResponse{
					Content: ev.Delta.Text,
					Model:   lastModel,
				}
			}

		case "message_delta":
			var ev messageDeltaEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				ch <- llm.StreamResponse{Error: fmt.Errorf("parsing message_delta: %w", err)}
				return
			}
			lastUsage = &llm.Usage{
				PromptTokens:     lastUsage.PromptTokens,
				CompletionTokens: ev.Usage.OutputTokens,
				TotalTokens:      lastUsage.PromptTokens + ev.Usage.OutputTokens,
			}
			ch <- llm.StreamResponse{
				Usage:      lastUsage,
				StopReason: ev.Delta.StopReason,
			}

		case "message_stop":
			ch <- llm.StreamResponse{
				Done:  true,
				Model: lastModel,
				Usage: lastUsage,
			}
			return

		case "error":
			var ev errorEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				ch <- llm.StreamResponse{Error: fmt.Errorf("parsing error event: %w", err)}
				return
			}
			ch <- llm.StreamResponse{Error: fmt.Errorf("anthropic API error: %s: %s", ev.Error.Type, ev.Error.Message)}
			return

		case "ping", "content_block_start", "content_block_stop":
			// Ignored event types.
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- llm.StreamResponse{Error: fmt.Errorf("reading stream: %w", err)}
		return
	}

	// Stream ended without message_stop — treat as done.
	ch <- llm.StreamResponse{Done: true, Model: lastModel, Usage: lastUsage}
}
