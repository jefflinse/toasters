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
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/jefflinse/toasters/internal/llm"
)

// goos is the runtime OS, overridable in tests.
var goos = runtime.GOOS

const DefaultModel = "claude-sonnet-4-20250514"

const (
	apiBaseURL = "https://api.anthropic.com"
	tokenURL   = "https://platform.claude.com/v1/oauth/token"
	clientID   = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	keychainService = "Claude Code-credentials"
)

// formatAPIError extracts a human-readable error message from an Anthropic API
// error response body. Falls back to a truncated raw body if parsing fails.
func formatAPIError(statusCode int, body []byte) error {
	var apiErr struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error.Message != "" {
		return fmt.Errorf("anthropic API error (%d): %s", statusCode, apiErr.Error.Message)
	}
	// Fallback: truncate raw body to avoid dumping huge JSON into the TUI.
	s := string(body)
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return fmt.Errorf("anthropic API error (%d): %s", statusCode, s)
}

// Client is an Anthropic API client that satisfies llm.Provider.
type Client struct {
	model string
}

// NewClient creates a new Anthropic client. If model is empty, DefaultModel is used.
func NewClient(model string) *Client {
	if model == "" {
		model = DefaultModel
	}
	return &Client{model: model}
}

// Compile-time check that *Client satisfies llm.Provider.
var _ llm.Provider = (*Client)(nil)

// BaseURL returns the Anthropic API base URL.
func (c *Client) BaseURL() string {
	return apiBaseURL
}

// FetchModels returns a list of available Anthropic models with known context
// window sizes. The configured model is marked as "loaded" so the TUI picks
// it up. All current Claude models share a 200k token context window.
func (c *Client) FetchModels(_ context.Context) ([]llm.ModelInfo, error) {
	const claudeContextWindow = 200_000

	known := []llm.ModelInfo{
		{ID: "claude-sonnet-4-20250514", MaxContextLength: claudeContextWindow},
		{ID: "claude-haiku-4-20250414", MaxContextLength: claudeContextWindow},
		{ID: "claude-haiku-4-5-20251001", MaxContextLength: claudeContextWindow},
		{ID: "claude-opus-4-20250514", MaxContextLength: claudeContextWindow},
	}

	// Mark the configured model as loaded. If it's not in the known list
	// (e.g. a new model version), add it.
	found := false
	for i := range known {
		if known[i].ID == c.model {
			known[i].State = "loaded"
			found = true
		} else {
			known[i].State = "available"
		}
	}
	if !found && c.model != "" {
		known = append([]llm.ModelInfo{{
			ID:               c.model,
			State:            "loaded",
			MaxContextLength: claudeContextWindow,
		}}, known...)
	}

	return known, nil
}

// ChatCompletionStream sends messages to the Anthropic Messages API and returns
// a channel of streamed response chunks.
func (c *Client) ChatCompletionStream(ctx context.Context, messages []llm.Message, temperature float64) <-chan llm.StreamResponse {
	ch := make(chan llm.StreamResponse, 1)

	go func() {
		defer close(ch)

		system, msgs := convertMessages(messages)
		c.streamMessages(ctx, system, msgs, ch)
	}()

	return ch
}

// ChatCompletionStreamWithTools is like ChatCompletionStream but sends tool
// definitions to the Anthropic API, enabling native tool calling.
func (c *Client) ChatCompletionStreamWithTools(ctx context.Context, messages []llm.Message, tools []llm.Tool, temperature float64) <-chan llm.StreamResponse {
	ch := make(chan llm.StreamResponse, 1)

	go func() {
		defer close(ch)

		system, msgs := convertMessages(messages)
		aTools := convertTools(tools)
		c.streamMessagesWithTools(ctx, system, msgs, aTools, ch)
	}()

	return ch
}

// ChatCompletion sends a non-streaming request to the Anthropic Messages API
// and returns the text content of the response.
func (c *Client) ChatCompletion(ctx context.Context, msgs []llm.Message) (string, error) {
	creds, err := readKeychainCredentials()
	if err != nil {
		return "", fmt.Errorf("anthropic auth: %w", err)
	}

	system, messages := convertMessages(msgs)

	reqBody := anthropicRequest{
		Model:     c.model,
		MaxTokens: 8192,
		Stream:    false,
		Messages:  messages,
		System:    system,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("sending request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", formatAPIError(resp.StatusCode, respBody)
	}

	var result anthropicResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parsing response: %w", err)
	}

	// Extract text from content blocks.
	var parts []string
	for _, block := range result.Content {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}

	if len(parts) == 0 {
		return "", fmt.Errorf("no text content in response")
	}

	return strings.TrimSpace(strings.Join(parts, "\n")), nil
}

// StreamMessage reads OAuth credentials from the macOS Keychain and streams
// a message to the Anthropic Messages API, returning results on a channel.
// This is the original standalone function kept for backward compatibility
// with the /anthropic slash command.
func StreamMessage(ctx context.Context, model string, prompt string) <-chan llm.StreamResponse {
	ch := make(chan llm.StreamResponse, 1)

	go func() {
		defer close(ch)
		streamMessage(ctx, model, prompt, ch)
	}()

	return ch
}

// convertMessages splits llm.Message slices into an Anthropic system prompt
// and a messages array. Handles system, user, assistant (with optional tool_use),
// and tool (tool_result) messages.
func convertMessages(msgs []llm.Message) (string, []anthropicMessage) {
	var systemParts []string
	var out []anthropicMessage

	for i, m := range msgs {
		switch m.Role {
		case "system":
			if m.Content != "" {
				systemParts = append(systemParts, m.Content)
			}

		case "assistant":
			if len(m.ToolCalls) > 0 {
				// Assistant message with tool calls → content blocks.
				var blocks []any
				if m.Content != "" {
					blocks = append(blocks, map[string]any{
						"type": "text",
						"text": m.Content,
					})
				}
				for _, tc := range m.ToolCalls {
					// Parse the arguments JSON string into a raw value.
					var input any
					if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
						input = map[string]any{} // fallback to empty object
					}
					blocks = append(blocks, map[string]any{
						"type":  "tool_use",
						"id":    tc.ID,
						"name":  tc.Function.Name,
						"input": input,
					})
				}
				out = append(out, newBlockMessage("assistant", blocks))
			} else if m.Content != "" {
				// Skip display-only tool-call indicator messages (e.g. "⚙ calling job_list…")
				// that the TUI inserts between the assistant tool-call message and the tool
				// result. These would break the Anthropic API's strict message ordering.
				if i+1 < len(msgs) && msgs[i+1].Role == "tool" {
					continue
				}
				out = append(out, newTextMessage("assistant", m.Content))
			}

		case "tool":
			// Tool result → Anthropic uses role "user" with tool_result content blocks.
			// Batch all tool results that follow the same assistant tool_use message
			// into a single user message. We detect this by checking if the last
			// output message is already a user message (which would be a tool_result batch).
			block := map[string]any{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     m.Content,
			}
			if len(out) > 0 && out[len(out)-1].Role == "user" {
				// Check if the last output message contains tool_result blocks
				// by attempting to parse it as an array.
				var existing []any
				if err := json.Unmarshal(out[len(out)-1].Content, &existing); err == nil && len(existing) > 0 {
					// Verify the first element is a tool_result.
					if first, ok := existing[0].(map[string]any); ok && first["type"] == "tool_result" {
						existing = append(existing, block)
						b, _ := json.Marshal(existing)
						out[len(out)-1].Content = b
						continue
					}
				}
			}
			out = append(out, newBlockMessage("user", []any{block}))

		case "user":
			if m.Content != "" {
				out = append(out, newTextMessage("user", m.Content))
			}
		}
	}

	system := strings.Join(systemParts, "\n\n")
	return system, out
}

// convertTools converts llm.Tool definitions to Anthropic's tool format.
func convertTools(tools []llm.Tool) []anthropicTool {
	out := make([]anthropicTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		})
	}
	return out
}

// streamMessages is the core streaming implementation used by the Client methods.
func (c *Client) streamMessages(ctx context.Context, system string, messages []anthropicMessage, ch chan<- llm.StreamResponse) {
	creds, err := readKeychainCredentials()
	if err != nil {
		ch <- llm.StreamResponse{Error: fmt.Errorf("anthropic auth: %w", err)}
		return
	}

	reqBody := anthropicRequest{
		Model:     c.model,
		MaxTokens: 8192,
		Stream:    true,
		Messages:  messages,
		System:    system,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		ch <- llm.StreamResponse{Error: fmt.Errorf("marshaling request: %w", err)}
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBaseURL+"/v1/messages", bytes.NewReader(body))
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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		ch <- llm.StreamResponse{Error: formatAPIError(resp.StatusCode, respBody)}
		return
	}

	parseSSEStream(ctx, resp.Body, ch)
}

// streamMessagesWithTools is like streamMessages but includes tool definitions in the request.
func (c *Client) streamMessagesWithTools(ctx context.Context, system string, messages []anthropicMessage, tools []anthropicTool, ch chan<- llm.StreamResponse) {
	creds, err := readKeychainCredentials()
	if err != nil {
		ch <- llm.StreamResponse{Error: fmt.Errorf("anthropic auth: %w", err)}
		return
	}

	reqBody := anthropicRequest{
		Model:     c.model,
		MaxTokens: 8192,
		Stream:    true,
		Messages:  messages,
		System:    system,
		Tools:     tools,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		ch <- llm.StreamResponse{Error: fmt.Errorf("marshaling request: %w", err)}
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBaseURL+"/v1/messages", bytes.NewReader(body))
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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		ch <- llm.StreamResponse{Error: formatAPIError(resp.StatusCode, respBody)}
		return
	}

	parseSSEStream(ctx, resp.Body, ch)
}

// streamMessage is the original standalone streaming implementation.
// Kept for backward compatibility with StreamMessage().
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
			newTextMessage("user", prompt),
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		ch <- llm.StreamResponse{Error: fmt.Errorf("marshaling request: %w", err)}
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBaseURL+"/v1/messages", bytes.NewReader(body))
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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		ch <- llm.StreamResponse{Error: formatAPIError(resp.StatusCode, respBody)}
		return
	}

	parseSSEStream(ctx, resp.Body, ch)
}

// toolAccumulator tracks a tool_use content block being streamed.
type toolAccumulator struct {
	id       string
	name     string
	inputBuf strings.Builder // accumulated input_json_delta fragments
}

// parseSSEStream reads Anthropic SSE events from r and sends StreamResponse
// messages on ch. Handles text, tool_use, and tool result content blocks.
// Shared by both the Client methods and the standalone function.
func parseSSEStream(ctx context.Context, r io.Reader, ch chan<- llm.StreamResponse) {
	var (
		lastModel  string
		lastUsage  *llm.Usage
		eventType  string
		toolBlocks map[int]*toolAccumulator // index → accumulator
	)

	scanner := bufio.NewScanner(r)
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

		case "content_block_start":
			var ev contentBlockStartEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				ch <- llm.StreamResponse{Error: fmt.Errorf("parsing content_block_start: %w", err)}
				return
			}
			if ev.ContentBlock.Type == "tool_use" {
				if toolBlocks == nil {
					toolBlocks = make(map[int]*toolAccumulator)
				}
				toolBlocks[ev.Index] = &toolAccumulator{
					id:   ev.ContentBlock.ID,
					name: ev.ContentBlock.Name,
				}
			}

		case "content_block_delta":
			var ev contentBlockDeltaEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				ch <- llm.StreamResponse{Error: fmt.Errorf("parsing content_block_delta: %w", err)}
				return
			}
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					ch <- llm.StreamResponse{
						Content: ev.Delta.Text,
						Model:   lastModel,
					}
				}
			case "input_json_delta":
				if acc, ok := toolBlocks[ev.Index]; ok {
					acc.inputBuf.WriteString(ev.Delta.PartialJSON)
				}
			}

		case "content_block_stop":
			// Nothing special needed — tool blocks are emitted on message_delta.

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

			if ev.Delta.StopReason == "tool_use" && len(toolBlocks) > 0 {
				// Collect accumulated tool calls sorted by index.
				indices := make([]int, 0, len(toolBlocks))
				for idx := range toolBlocks {
					indices = append(indices, idx)
				}
				sort.Ints(indices)
				calls := make([]llm.ToolCall, 0, len(indices))
				for _, idx := range indices {
					acc := toolBlocks[idx]
					calls = append(calls, llm.ToolCall{
						Index: idx,
						ID:    acc.id,
						Type:  "function",
						Function: llm.ToolCallFunction{
							Name:      acc.name,
							Arguments: acc.inputBuf.String(),
						},
					})
				}
				ch <- llm.StreamResponse{
					ToolCalls: calls,
					Done:      true,
					Model:     lastModel,
					Usage:     lastUsage,
				}
				return
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

		case "ping":
			// Ignored.
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- llm.StreamResponse{Error: fmt.Errorf("reading stream: %w", err)}
		return
	}

	// Stream ended without message_stop — treat as done.
	ch <- llm.StreamResponse{Done: true, Model: lastModel, Usage: lastUsage}
}

// ---- Keychain / OAuth helpers (unchanged) ----

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
	if goos != "darwin" {
		return nil, fmt.Errorf("keychain access is only supported on macOS; set ANTHROPIC_API_KEY environment variable instead")
	}

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
	if goos != "darwin" {
		return fmt.Errorf("keychain access is only supported on macOS; set ANTHROPIC_API_KEY environment variable instead")
	}

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
	defer func() { _ = resp.Body.Close() }()

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
	// Ignore write errors — we still have a valid token for this request.
	_ = writeKeychainBlob(blob)

	return &keychainCredentials{
		AccessToken: tok.AccessToken,
		ExpiresAt:   blob.ClaudeAiOauth.ExpiresAt,
	}, nil
}

// ReadKeychainAccessToken reads the OAuth access token from the macOS Keychain.
// If the token is expired, it automatically refreshes it. This is the exported
// entry point for other packages that need Keychain-based authentication.
func ReadKeychainAccessToken() (string, error) {
	creds, err := readKeychainCredentials()
	if err != nil {
		return "", err
	}
	return creds.AccessToken, nil
}

// ---- Anthropic API types ----

// anthropicRequest is the Anthropic Messages API request body.
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Stream    bool               `json:"stream"`
	Messages  []anthropicMessage `json:"messages"`
	System    string             `json:"system,omitempty"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

// anthropicTool is a tool definition in Anthropic's format.
type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema"`
}

// anthropicMessage is a message in the Anthropic Messages API.
// Content can be a plain string (for simple text) or an array of content blocks
// (for tool_use / tool_result). We use json.RawMessage to handle both.
type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// newTextMessage creates an anthropicMessage with a plain string content.
func newTextMessage(role, text string) anthropicMessage {
	b, _ := json.Marshal(text)
	return anthropicMessage{Role: role, Content: b}
}

// newBlockMessage creates an anthropicMessage with an array of content blocks.
func newBlockMessage(role string, blocks []any) anthropicMessage {
	b, _ := json.Marshal(blocks)
	return anthropicMessage{Role: role, Content: b}
}

// anthropicResponse is the non-streaming response from the Messages API.
type anthropicResponse struct {
	Content    []anthropicContentBlock `json:"content"`
	Model      string                  `json:"model"`
	StopReason string                  `json:"stop_reason"`
	Usage      apiUsage                `json:"usage"`
}

type anthropicContentBlock struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`
}

// SSE event types from the Anthropic streaming API.
type messageStartEvent struct {
	Type    string `json:"type"`
	Message struct {
		Model string   `json:"model"`
		Usage apiUsage `json:"usage"`
	} `json:"message"`
}

type contentBlockStartEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id,omitempty"`
		Name string `json:"name,omitempty"`
	} `json:"content_block"`
}

type contentBlockDeltaEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`
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
