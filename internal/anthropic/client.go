package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/jefflinse/toasters/internal/llm"
)

const DefaultModel = "claude-sonnet-4-20250514"

const (
	tokenURL = "https://platform.claude.com/v1/oauth/token"
	clientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	keychainService = "Claude Code-credentials"
)

// keychainCredentials holds the OAuth token read from the macOS Keychain.
type keychainCredentials struct {
	AccessToken string
	ExpiresAt   int64 // unix millis
}

// keychainBlob is the full JSON structure stored in the Keychain.
// We preserve the entire blob so we can write it back after refresh.
type keychainBlob struct {
	ClaudeAiOauth keychainOauth `json:"claudeAiOauth"`
}

type keychainOauth struct {
	AccessToken      string   `json:"accessToken"`
	RefreshToken     string   `json:"refreshToken"`
	ExpiresAt        int64    `json:"expiresAt"`
	Scopes           []string `json:"scopes"`
	SubscriptionType string   `json:"subscriptionType"`
	RateLimitTier    string   `json:"rateLimitTier"`
}

// readKeychainBlob reads and parses the full credential blob from the macOS Keychain.
func readKeychainBlob() (*keychainBlob, error) {
	cmd := exec.Command("security", "find-generic-password", "-s", keychainService, "-w")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("reading keychain: %w (is Claude Code signed in?)", err)
	}

	raw := strings.TrimSpace(string(out))

	var blob keychainBlob
	if err := json.Unmarshal([]byte(raw), &blob); err != nil {
		return nil, fmt.Errorf("parsing keychain credentials: %w", err)
	}

	if blob.ClaudeAiOauth.AccessToken == "" {
		return nil, fmt.Errorf("no access token found in keychain credentials")
	}

	return &blob, nil
}

// writeKeychainBlob writes the credential blob back to the macOS Keychain,
// replacing the existing entry.
func writeKeychainBlob(blob *keychainBlob) error {
	data, err := json.Marshal(blob)
	if err != nil {
		return fmt.Errorf("marshaling keychain blob: %w", err)
	}

	// Find the account name from the existing entry.
	findCmd := exec.Command("security", "find-generic-password", "-s", keychainService)
	findOut, err := findCmd.Output()
	if err != nil {
		return fmt.Errorf("finding keychain entry: %w", err)
	}

	// Parse the account name from the output (line like: "acct"<blob>="username").
	account := ""
	for _, line := range strings.Split(string(findOut), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, `"acct"`) {
			// Extract value between the last pair of quotes.
			if idx := strings.LastIndex(line, `="`); idx != -1 {
				account = strings.TrimSuffix(line[idx+2:], `"`)
			}
		}
	}

	if account == "" {
		return fmt.Errorf("could not determine keychain account name")
	}

	// Delete the old entry and add the new one.
	// security doesn't have an "update" command — you delete and re-add.
	delCmd := exec.Command("security", "delete-generic-password", "-s", keychainService, "-a", account)
	_ = delCmd.Run() // ignore error if entry doesn't exist

	addCmd := exec.Command("security", "add-generic-password",
		"-s", keychainService,
		"-a", account,
		"-w", string(data),
	)
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("writing keychain entry: %w", err)
	}

	return nil
}

// refreshAccessToken uses the refresh token to obtain a new access token
// from the Anthropic OAuth token endpoint.
func refreshAccessToken(refreshToken string) (*tokenResponse, error) {
	form := fmt.Sprintf("grant_type=refresh_token&refresh_token=%s&client_id=%s", refreshToken, clientID)

	resp, err := http.Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(form))
	if err != nil {
		return nil, fmt.Errorf("token refresh request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}

	if tok.AccessToken == "" {
		return nil, fmt.Errorf("token refresh returned empty access token")
	}

	return &tok, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"` // seconds
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

// readKeychainCredentials reads the OAuth credentials from the macOS Keychain.
// If the access token is expired, it automatically refreshes it using the
// refresh token and writes the updated credentials back to the Keychain.
func readKeychainCredentials() (*keychainCredentials, error) {
	blob, err := readKeychainBlob()
	if err != nil {
		return nil, err
	}

	oauth := blob.ClaudeAiOauth

	// If the token is still valid, return it directly.
	if oauth.ExpiresAt == 0 || oauth.ExpiresAt > time.Now().UnixMilli() {
		return &keychainCredentials{
			AccessToken: oauth.AccessToken,
			ExpiresAt:   oauth.ExpiresAt,
		}, nil
	}

	// Token is expired — try to refresh.
	if oauth.RefreshToken == "" {
		return nil, fmt.Errorf("OAuth token expired and no refresh token available")
	}

	log.Printf("[anthropic] access token expired, refreshing...")

	tok, err := refreshAccessToken(oauth.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("refreshing expired token: %w", err)
	}

	// Update the blob with the new tokens.
	blob.ClaudeAiOauth.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		blob.ClaudeAiOauth.RefreshToken = tok.RefreshToken
	}
	blob.ClaudeAiOauth.ExpiresAt = time.Now().UnixMilli() + tok.ExpiresIn*1000

	// Write the updated credentials back to the Keychain.
	if err := writeKeychainBlob(blob); err != nil {
		// Log but don't fail — we still have a valid token for this request.
		log.Printf("[anthropic] warning: failed to write refreshed token to keychain: %v", err)
	} else {
		log.Printf("[anthropic] token refreshed successfully, expires at %s",
			time.UnixMilli(blob.ClaudeAiOauth.ExpiresAt).Format(time.RFC3339))
	}

	return &keychainCredentials{
		AccessToken: tok.AccessToken,
		ExpiresAt:   blob.ClaudeAiOauth.ExpiresAt,
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
