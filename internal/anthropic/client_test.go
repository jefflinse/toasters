package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/llm"
)

// These tests mutate the package-level `goos` variable, so they must NOT
// run in parallel with each other.

func TestReadKeychainBlob_NonDarwin(t *testing.T) {
	orig := goos
	defer func() { goos = orig }()
	goos = "linux"

	_, err := readKeychainBlob()
	if err == nil {
		t.Fatal("expected error on non-darwin platform, got nil")
	}

	const wantSubstr = "keychain access is only supported on macOS"
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error message %q does not contain %q", err.Error(), wantSubstr)
	}
}

func TestWriteKeychainBlob_NonDarwin(t *testing.T) {
	orig := goos
	defer func() { goos = orig }()
	goos = "linux"

	err := writeKeychainBlob(&keychainBlob{})
	if err == nil {
		t.Fatal("expected error on non-darwin platform, got nil")
	}

	const wantSubstr = "keychain access is only supported on macOS"
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error message %q does not contain %q", err.Error(), wantSubstr)
	}
}

func TestReadKeychainBlob_DarwinPassthrough(t *testing.T) {
	orig := goos
	defer func() { goos = orig }()
	goos = "darwin"

	_, err := readKeychainBlob()

	// On a real macOS host the function will attempt to call the `security`
	// binary. It may succeed (unlikely in CI) or fail with a Keychain-related
	// error. Either way, the platform guard must NOT have fired.
	//
	// On non-macOS hosts the `security` binary doesn't exist, so exec will
	// fail — but the error will be an exec error, not the platform guard.
	if err == nil {
		return // success path — nothing more to check
	}

	const guardSubstr = "only supported on macOS"
	if strings.Contains(err.Error(), guardSubstr) {
		t.Errorf("expected a non-guard error when goos=darwin, got platform guard error: %v", err)
	}

	if runtime.GOOS != "darwin" {
		t.Logf("non-macOS host: got expected exec error: %v", err)
	}
}

func TestWriteKeychainBlob_DarwinPassthrough(t *testing.T) {
	// NOTE: We do NOT actually call writeKeychainBlob on macOS because it
	// would overwrite real Claude Code credentials in the Keychain.
	// Instead, we only verify the platform guard fires on non-darwin.
	if runtime.GOOS == "darwin" {
		t.Skip("skipping write test on macOS to avoid overwriting real Keychain credentials")
	}

	orig := goos
	defer func() { goos = orig }()
	goos = "darwin"

	// On non-macOS hosts with goos="darwin", the function will try to run
	// the `security` binary which doesn't exist — producing an exec error,
	// not the platform guard error.
	err := writeKeychainBlob(&keychainBlob{})
	if err == nil {
		return
	}

	const guardSubstr = "only supported on macOS"
	if strings.Contains(err.Error(), guardSubstr) {
		t.Errorf("expected a non-guard error when goos=darwin, got platform guard error: %v", err)
	}

	t.Logf("non-macOS host: got expected exec error: %v", err)
}

// ---------------------------------------------------------------------------
// NewClient
// ---------------------------------------------------------------------------

func TestNewClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		model     string
		wantModel string
	}{
		{
			name:      "empty model uses default",
			model:     "",
			wantModel: DefaultModel,
		},
		{
			name:      "custom model is preserved",
			model:     "claude-opus-4-20250514",
			wantModel: "claude-opus-4-20250514",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := NewClient(tt.model)
			if c.model != tt.wantModel {
				t.Errorf("NewClient(%q).model = %q, want %q", tt.model, c.model, tt.wantModel)
			}
		})
	}
}

func TestClient_BaseURL(t *testing.T) {
	t.Parallel()
	c := NewClient("")
	if got := c.BaseURL(); got != apiBaseURL {
		t.Errorf("BaseURL() = %q, want %q", got, apiBaseURL)
	}
}

// ---------------------------------------------------------------------------
// FetchModels
// ---------------------------------------------------------------------------

func TestFetchModels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		model          string
		wantLoaded     string
		wantMinModels  int
		wantExtraFirst bool // true if model is not in the known list and should be prepended
	}{
		{
			name:          "known model is marked loaded",
			model:         "claude-sonnet-4-20250514",
			wantLoaded:    "claude-sonnet-4-20250514",
			wantMinModels: 4,
		},
		{
			name:          "another known model is marked loaded",
			model:         "claude-opus-4-20250514",
			wantLoaded:    "claude-opus-4-20250514",
			wantMinModels: 4,
		},
		{
			name:           "unknown model is prepended and marked loaded",
			model:          "claude-future-99-20300101",
			wantLoaded:     "claude-future-99-20300101",
			wantMinModels:  5,
			wantExtraFirst: true,
		},
		{
			name:          "default model",
			model:         "",
			wantLoaded:    DefaultModel,
			wantMinModels: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := NewClient(tt.model)
			models, err := c.FetchModels(context.Background())
			if err != nil {
				t.Fatalf("FetchModels() error = %v", err)
			}

			if len(models) < tt.wantMinModels {
				t.Errorf("FetchModels() returned %d models, want at least %d", len(models), tt.wantMinModels)
			}

			// Verify exactly one model is loaded.
			loadedCount := 0
			var loadedID string
			for _, m := range models {
				if m.State == "loaded" {
					loadedCount++
					loadedID = m.ID
				}
			}
			if loadedCount != 1 {
				t.Errorf("expected exactly 1 loaded model, got %d", loadedCount)
			}
			if loadedID != tt.wantLoaded {
				t.Errorf("loaded model = %q, want %q", loadedID, tt.wantLoaded)
			}

			// All non-loaded models should be "available".
			for _, m := range models {
				if m.State != "loaded" && m.State != "available" {
					t.Errorf("model %q has unexpected state %q", m.ID, m.State)
				}
			}

			// Unknown model should be first in the list.
			if tt.wantExtraFirst {
				if models[0].ID != tt.wantLoaded {
					t.Errorf("expected unknown model %q to be first, got %q", tt.wantLoaded, models[0].ID)
				}
			}

			// All models should have a context window.
			for _, m := range models {
				if m.MaxContextLength == 0 {
					t.Errorf("model %q has MaxContextLength = 0", m.ID)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// newTextMessage / newBlockMessage
// ---------------------------------------------------------------------------

func TestNewTextMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		role     string
		text     string
		wantRole string
		wantJSON string
	}{
		{
			name:     "user message",
			role:     "user",
			text:     "hello",
			wantRole: "user",
			wantJSON: `"hello"`,
		},
		{
			name:     "assistant message",
			role:     "assistant",
			text:     "world",
			wantRole: "assistant",
			wantJSON: `"world"`,
		},
		{
			name:     "empty text",
			role:     "user",
			text:     "",
			wantRole: "user",
			wantJSON: `""`,
		},
		{
			name:     "special characters",
			role:     "user",
			text:     `hello "world" \n`,
			wantRole: "user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			msg := newTextMessage(tt.role, tt.text)
			if msg.Role != tt.wantRole {
				t.Errorf("Role = %q, want %q", msg.Role, tt.wantRole)
			}

			// Verify content is valid JSON.
			var decoded string
			if err := json.Unmarshal(msg.Content, &decoded); err != nil {
				t.Fatalf("Content is not valid JSON string: %v", err)
			}
			if decoded != tt.text {
				t.Errorf("decoded content = %q, want %q", decoded, tt.text)
			}

			if tt.wantJSON != "" {
				if string(msg.Content) != tt.wantJSON {
					t.Errorf("Content = %s, want %s", msg.Content, tt.wantJSON)
				}
			}
		})
	}
}

func TestNewBlockMessage(t *testing.T) {
	t.Parallel()

	blocks := []any{
		map[string]any{"type": "text", "text": "hello"},
		map[string]any{"type": "tool_use", "id": "t1", "name": "fn"},
	}

	msg := newBlockMessage("assistant", blocks)
	if msg.Role != "assistant" {
		t.Errorf("Role = %q, want %q", msg.Role, "assistant")
	}

	var decoded []map[string]any
	if err := json.Unmarshal(msg.Content, &decoded); err != nil {
		t.Fatalf("Content is not valid JSON array: %v", err)
	}
	if len(decoded) != 2 {
		t.Fatalf("decoded %d blocks, want 2", len(decoded))
	}
	if decoded[0]["type"] != "text" {
		t.Errorf("block[0].type = %v, want text", decoded[0]["type"])
	}
	if decoded[1]["type"] != "tool_use" {
		t.Errorf("block[1].type = %v, want tool_use", decoded[1]["type"])
	}
}

func TestNewBlockMessage_Empty(t *testing.T) {
	t.Parallel()
	msg := newBlockMessage("user", []any{})
	var decoded []any
	if err := json.Unmarshal(msg.Content, &decoded); err != nil {
		t.Fatalf("Content is not valid JSON: %v", err)
	}
	if len(decoded) != 0 {
		t.Errorf("expected empty array, got %d elements", len(decoded))
	}
}

// ---------------------------------------------------------------------------
// formatAPIError
// ---------------------------------------------------------------------------

func TestFormatAPIError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		body       []byte
		wantSubstr string
	}{
		{
			name:       "valid JSON error body",
			statusCode: 400,
			body:       []byte(`{"error":{"type":"invalid_request_error","message":"max_tokens must be positive"}}`),
			wantSubstr: "max_tokens must be positive",
		},
		{
			name:       "valid JSON with status code",
			statusCode: 429,
			body:       []byte(`{"error":{"type":"rate_limit_error","message":"rate limited"}}`),
			wantSubstr: "429",
		},
		{
			name:       "invalid JSON falls back to raw body",
			statusCode: 500,
			body:       []byte(`not json at all`),
			wantSubstr: "not json at all",
		},
		{
			name:       "empty error message falls back to raw body",
			statusCode: 500,
			body:       []byte(`{"error":{"type":"server_error","message":""}}`),
			wantSubstr: "server_error",
		},
		{
			name:       "empty body",
			statusCode: 502,
			body:       []byte(``),
			wantSubstr: "502",
		},
		{
			name:       "long body is truncated",
			statusCode: 500,
			body:       []byte(strings.Repeat("x", 300)),
			wantSubstr: "...",
		},
		{
			name:       "body at exactly 200 chars is not truncated",
			statusCode: 500,
			body:       []byte(strings.Repeat("y", 200)),
			wantSubstr: strings.Repeat("y", 200),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := formatAPIError(tt.statusCode, tt.body)
			if err == nil {
				t.Fatal("expected non-nil error")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantSubstr)
			}
		})
	}
}

func TestFormatAPIError_LongBodyTruncation(t *testing.T) {
	t.Parallel()
	body := []byte(strings.Repeat("z", 300))
	err := formatAPIError(500, body)
	msg := err.Error()
	// The raw body portion should be 200 chars + "..."
	if !strings.HasSuffix(msg, "...") {
		t.Errorf("expected truncated message to end with '...', got %q", msg)
	}
	// Should NOT contain the full 300-char string.
	if strings.Contains(msg, strings.Repeat("z", 300)) {
		t.Error("expected body to be truncated, but full body is present")
	}
}

func TestFormatAPIError_200CharsNotTruncated(t *testing.T) {
	t.Parallel()
	body := []byte(strings.Repeat("a", 200))
	err := formatAPIError(500, body)
	msg := err.Error()
	if strings.HasSuffix(msg, "...") {
		t.Errorf("200-char body should not be truncated, but got %q", msg)
	}
}

// ---------------------------------------------------------------------------
// convertMessages
// ---------------------------------------------------------------------------

func TestConvertMessages_SystemExtraction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		msgs       []llm.Message
		wantSystem string
		wantLen    int
	}{
		{
			name: "single system message",
			msgs: []llm.Message{
				{Role: "system", Content: "You are a helpful assistant."},
			},
			wantSystem: "You are a helpful assistant.",
			wantLen:    0,
		},
		{
			name: "multiple system messages joined",
			msgs: []llm.Message{
				{Role: "system", Content: "Part one."},
				{Role: "system", Content: "Part two."},
			},
			wantSystem: "Part one.\n\nPart two.",
			wantLen:    0,
		},
		{
			name: "system with empty content is skipped",
			msgs: []llm.Message{
				{Role: "system", Content: ""},
				{Role: "system", Content: "Only this."},
			},
			wantSystem: "Only this.",
			wantLen:    0,
		},
		{
			name:       "no system messages",
			msgs:       []llm.Message{{Role: "user", Content: "hi"}},
			wantSystem: "",
			wantLen:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			system, out := convertMessages(tt.msgs)
			if system != tt.wantSystem {
				t.Errorf("system = %q, want %q", system, tt.wantSystem)
			}
			if len(out) != tt.wantLen {
				t.Errorf("len(messages) = %d, want %d", len(out), tt.wantLen)
			}
		})
	}
}

func TestConvertMessages_UserMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		msgs    []llm.Message
		wantLen int
	}{
		{
			name:    "single user message",
			msgs:    []llm.Message{{Role: "user", Content: "hello"}},
			wantLen: 1,
		},
		{
			name:    "empty user message is skipped",
			msgs:    []llm.Message{{Role: "user", Content: ""}},
			wantLen: 0,
		},
		{
			name: "multiple user messages",
			msgs: []llm.Message{
				{Role: "user", Content: "first"},
				{Role: "user", Content: "second"},
			},
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, out := convertMessages(tt.msgs)
			if len(out) != tt.wantLen {
				t.Fatalf("len(messages) = %d, want %d", len(out), tt.wantLen)
			}
			for _, m := range out {
				if m.Role != "user" {
					t.Errorf("message role = %q, want user", m.Role)
				}
				var text string
				if err := json.Unmarshal(m.Content, &text); err != nil {
					t.Errorf("content is not a JSON string: %v", err)
				}
			}
		})
	}
}

func TestConvertMessages_UserMessageContent(t *testing.T) {
	t.Parallel()
	msgs := []llm.Message{{Role: "user", Content: "hello world"}}
	_, out := convertMessages(msgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	var text string
	if err := json.Unmarshal(out[0].Content, &text); err != nil {
		t.Fatalf("content unmarshal error: %v", err)
	}
	if text != "hello world" {
		t.Errorf("content = %q, want %q", text, "hello world")
	}
}

func TestConvertMessages_AssistantPlainText(t *testing.T) {
	t.Parallel()
	msgs := []llm.Message{
		{Role: "assistant", Content: "I can help with that."},
	}
	_, out := convertMessages(msgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if out[0].Role != "assistant" {
		t.Errorf("role = %q, want assistant", out[0].Role)
	}
	var text string
	if err := json.Unmarshal(out[0].Content, &text); err != nil {
		t.Fatalf("content unmarshal error: %v", err)
	}
	if text != "I can help with that." {
		t.Errorf("content = %q, want %q", text, "I can help with that.")
	}
}

func TestConvertMessages_AssistantWithToolCalls(t *testing.T) {
	t.Parallel()
	msgs := []llm.Message{
		{
			Role:    "assistant",
			Content: "Let me check.",
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: llm.ToolCallFunction{
						Name:      "get_weather",
						Arguments: `{"city":"NYC"}`,
					},
				},
			},
		},
	}
	_, out := convertMessages(msgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if out[0].Role != "assistant" {
		t.Errorf("role = %q, want assistant", out[0].Role)
	}

	var blocks []map[string]any
	if err := json.Unmarshal(out[0].Content, &blocks); err != nil {
		t.Fatalf("content unmarshal error: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (text + tool_use), got %d", len(blocks))
	}

	// First block: text
	if blocks[0]["type"] != "text" {
		t.Errorf("block[0].type = %v, want text", blocks[0]["type"])
	}
	if blocks[0]["text"] != "Let me check." {
		t.Errorf("block[0].text = %v, want 'Let me check.'", blocks[0]["text"])
	}

	// Second block: tool_use
	if blocks[1]["type"] != "tool_use" {
		t.Errorf("block[1].type = %v, want tool_use", blocks[1]["type"])
	}
	if blocks[1]["id"] != "call_1" {
		t.Errorf("block[1].id = %v, want call_1", blocks[1]["id"])
	}
	if blocks[1]["name"] != "get_weather" {
		t.Errorf("block[1].name = %v, want get_weather", blocks[1]["name"])
	}

	// Verify input was parsed from JSON string.
	input, ok := blocks[1]["input"].(map[string]any)
	if !ok {
		t.Fatalf("block[1].input is not a map, got %T", blocks[1]["input"])
	}
	if input["city"] != "NYC" {
		t.Errorf("input.city = %v, want NYC", input["city"])
	}
}

func TestConvertMessages_AssistantToolCallsNoContent(t *testing.T) {
	t.Parallel()
	msgs := []llm.Message{
		{
			Role:    "assistant",
			Content: "", // no text content, just tool calls
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call_2",
					Type: "function",
					Function: llm.ToolCallFunction{
						Name:      "list_files",
						Arguments: `{}`,
					},
				},
			},
		},
	}
	_, out := convertMessages(msgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}

	var blocks []map[string]any
	if err := json.Unmarshal(out[0].Content, &blocks); err != nil {
		t.Fatalf("content unmarshal error: %v", err)
	}
	// Should only have tool_use block, no text block.
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (tool_use only), got %d", len(blocks))
	}
	if blocks[0]["type"] != "tool_use" {
		t.Errorf("block[0].type = %v, want tool_use", blocks[0]["type"])
	}
}

func TestConvertMessages_AssistantToolCallInvalidJSON(t *testing.T) {
	t.Parallel()
	msgs := []llm.Message{
		{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{
					ID:   "call_bad",
					Type: "function",
					Function: llm.ToolCallFunction{
						Name:      "broken",
						Arguments: `not valid json`,
					},
				},
			},
		},
	}
	_, out := convertMessages(msgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}

	var blocks []map[string]any
	if err := json.Unmarshal(out[0].Content, &blocks); err != nil {
		t.Fatalf("content unmarshal error: %v", err)
	}
	// Should fallback to empty object for input.
	input, ok := blocks[0]["input"].(map[string]any)
	if !ok {
		t.Fatalf("expected input to be empty map, got %T: %v", blocks[0]["input"], blocks[0]["input"])
	}
	if len(input) != 0 {
		t.Errorf("expected empty input map, got %v", input)
	}
}

func TestConvertMessages_ToolResult(t *testing.T) {
	t.Parallel()
	msgs := []llm.Message{
		{Role: "tool", ToolCallID: "call_1", Content: "result data"},
	}
	_, out := convertMessages(msgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if out[0].Role != "user" {
		t.Errorf("role = %q, want user (tool results use user role in Anthropic)", out[0].Role)
	}

	var blocks []map[string]any
	if err := json.Unmarshal(out[0].Content, &blocks); err != nil {
		t.Fatalf("content unmarshal error: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0]["type"] != "tool_result" {
		t.Errorf("block.type = %v, want tool_result", blocks[0]["type"])
	}
	if blocks[0]["tool_use_id"] != "call_1" {
		t.Errorf("block.tool_use_id = %v, want call_1", blocks[0]["tool_use_id"])
	}
	if blocks[0]["content"] != "result data" {
		t.Errorf("block.content = %v, want 'result data'", blocks[0]["content"])
	}
}

func TestConvertMessages_ToolResultBatching(t *testing.T) {
	t.Parallel()
	// Multiple consecutive tool results should be batched into a single user message.
	msgs := []llm.Message{
		{Role: "tool", ToolCallID: "call_1", Content: "result 1"},
		{Role: "tool", ToolCallID: "call_2", Content: "result 2"},
		{Role: "tool", ToolCallID: "call_3", Content: "result 3"},
	}
	_, out := convertMessages(msgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 batched message, got %d", len(out))
	}
	if out[0].Role != "user" {
		t.Errorf("role = %q, want user", out[0].Role)
	}

	var blocks []map[string]any
	if err := json.Unmarshal(out[0].Content, &blocks); err != nil {
		t.Fatalf("content unmarshal error: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("expected 3 tool_result blocks, got %d", len(blocks))
	}
	for i, b := range blocks {
		if b["type"] != "tool_result" {
			t.Errorf("block[%d].type = %v, want tool_result", i, b["type"])
		}
	}
	if blocks[0]["tool_use_id"] != "call_1" {
		t.Errorf("block[0].tool_use_id = %v, want call_1", blocks[0]["tool_use_id"])
	}
	if blocks[2]["tool_use_id"] != "call_3" {
		t.Errorf("block[2].tool_use_id = %v, want call_3", blocks[2]["tool_use_id"])
	}
}

func TestConvertMessages_ToolResultNotBatchedAfterUserText(t *testing.T) {
	t.Parallel()
	// A tool result after a regular user message should NOT be batched into it.
	msgs := []llm.Message{
		{Role: "user", Content: "hello"},
		{Role: "tool", ToolCallID: "call_1", Content: "result"},
	}
	_, out := convertMessages(msgs)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
	// First should be a plain user text message.
	var text string
	if err := json.Unmarshal(out[0].Content, &text); err != nil {
		t.Fatalf("first message content unmarshal error: %v", err)
	}
	if text != "hello" {
		t.Errorf("first message content = %q, want hello", text)
	}
	// Second should be a user message with tool_result blocks.
	var blocks []map[string]any
	if err := json.Unmarshal(out[1].Content, &blocks); err != nil {
		t.Fatalf("second message content unmarshal error: %v", err)
	}
	if len(blocks) != 1 || blocks[0]["type"] != "tool_result" {
		t.Errorf("expected tool_result block, got %v", blocks)
	}
}

func TestConvertMessages_DisplayOnlyIndicatorSkipped(t *testing.T) {
	t.Parallel()
	// An assistant text message immediately before a tool message should be skipped
	// (it's a display-only indicator like "⚙ calling job_list…").
	msgs := []llm.Message{
		{Role: "assistant", Content: "⚙ calling job_list…"},
		{Role: "tool", ToolCallID: "call_1", Content: "result"},
	}
	_, out := convertMessages(msgs)
	// Should only have the tool result message, not the display-only assistant message.
	if len(out) != 1 {
		t.Fatalf("expected 1 message (tool result only), got %d", len(out))
	}
	if out[0].Role != "user" {
		t.Errorf("role = %q, want user", out[0].Role)
	}
}

func TestConvertMessages_AssistantTextNotSkippedWhenNotBeforeTool(t *testing.T) {
	t.Parallel()
	// An assistant text message NOT followed by a tool message should be kept.
	msgs := []llm.Message{
		{Role: "assistant", Content: "Here is my answer."},
		{Role: "user", Content: "Thanks!"},
	}
	_, out := convertMessages(msgs)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
	if out[0].Role != "assistant" {
		t.Errorf("first message role = %q, want assistant", out[0].Role)
	}
}

func TestConvertMessages_AssistantEmptyContentNoToolCalls(t *testing.T) {
	t.Parallel()
	// Assistant message with empty content and no tool calls should produce nothing.
	msgs := []llm.Message{
		{Role: "assistant", Content: ""},
	}
	_, out := convertMessages(msgs)
	if len(out) != 0 {
		t.Errorf("expected 0 messages for empty assistant, got %d", len(out))
	}
}

func TestConvertMessages_FullConversation(t *testing.T) {
	t.Parallel()
	// Full realistic conversation: system + user + assistant with tool call + tool result + assistant response.
	msgs := []llm.Message{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "What's the weather?"},
		{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{
					ID:   "tc_1",
					Type: "function",
					Function: llm.ToolCallFunction{
						Name:      "get_weather",
						Arguments: `{"city":"SF"}`,
					},
				},
			},
		},
		{Role: "tool", ToolCallID: "tc_1", Content: `{"temp":72}`},
		{Role: "assistant", Content: "It's 72°F in SF."},
	}

	system, out := convertMessages(msgs)
	if system != "You are helpful." {
		t.Errorf("system = %q, want 'You are helpful.'", system)
	}
	// Expected: user, assistant (tool_use), user (tool_result), assistant (text)
	if len(out) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(out))
	}
	if out[0].Role != "user" {
		t.Errorf("msg[0].role = %q, want user", out[0].Role)
	}
	if out[1].Role != "assistant" {
		t.Errorf("msg[1].role = %q, want assistant", out[1].Role)
	}
	if out[2].Role != "user" {
		t.Errorf("msg[2].role = %q, want user (tool_result)", out[2].Role)
	}
	if out[3].Role != "assistant" {
		t.Errorf("msg[3].role = %q, want assistant", out[3].Role)
	}
}

func TestConvertMessages_EmptyInput(t *testing.T) {
	t.Parallel()
	system, out := convertMessages(nil)
	if system != "" {
		t.Errorf("system = %q, want empty", system)
	}
	if len(out) != 0 {
		t.Errorf("expected 0 messages, got %d", len(out))
	}
}

func TestConvertMessages_SpecialCharacters(t *testing.T) {
	t.Parallel()
	msgs := []llm.Message{
		{Role: "user", Content: "Hello \"world\" <script>alert('xss')</script> \n\t"},
	}
	_, out := convertMessages(msgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	var text string
	if err := json.Unmarshal(out[0].Content, &text); err != nil {
		t.Fatalf("content unmarshal error: %v", err)
	}
	if text != msgs[0].Content {
		t.Errorf("content = %q, want %q", text, msgs[0].Content)
	}
}

func TestConvertMessages_MultipleToolCalls(t *testing.T) {
	t.Parallel()
	msgs := []llm.Message{
		{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{
					ID:   "tc_a",
					Type: "function",
					Function: llm.ToolCallFunction{
						Name:      "tool_a",
						Arguments: `{"x":1}`,
					},
				},
				{
					ID:   "tc_b",
					Type: "function",
					Function: llm.ToolCallFunction{
						Name:      "tool_b",
						Arguments: `{"y":2}`,
					},
				},
			},
		},
	}
	_, out := convertMessages(msgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}

	var blocks []map[string]any
	if err := json.Unmarshal(out[0].Content, &blocks); err != nil {
		t.Fatalf("content unmarshal error: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 tool_use blocks, got %d", len(blocks))
	}
	if blocks[0]["name"] != "tool_a" {
		t.Errorf("block[0].name = %v, want tool_a", blocks[0]["name"])
	}
	if blocks[1]["name"] != "tool_b" {
		t.Errorf("block[1].name = %v, want tool_b", blocks[1]["name"])
	}
}

// ---------------------------------------------------------------------------
// convertTools
// ---------------------------------------------------------------------------

func TestConvertTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		tools []llm.Tool
		want  int
	}{
		{
			name:  "empty tools",
			tools: nil,
			want:  0,
		},
		{
			name: "single tool",
			tools: []llm.Tool{
				{
					Type: "function",
					Function: llm.ToolFunction{
						Name:        "get_weather",
						Description: "Get weather for a city",
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"city": map[string]any{"type": "string"},
							},
						},
					},
				},
			},
			want: 1,
		},
		{
			name: "multiple tools",
			tools: []llm.Tool{
				{
					Type: "function",
					Function: llm.ToolFunction{
						Name:        "tool_a",
						Description: "First tool",
						Parameters:  map[string]any{"type": "object"},
					},
				},
				{
					Type: "function",
					Function: llm.ToolFunction{
						Name:        "tool_b",
						Description: "Second tool",
						Parameters:  map[string]any{"type": "object"},
					},
				},
			},
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := convertTools(tt.tools)
			if len(out) != tt.want {
				t.Fatalf("len(convertTools) = %d, want %d", len(out), tt.want)
			}
		})
	}
}

func TestConvertTools_FieldMapping(t *testing.T) {
	t.Parallel()
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string"},
		},
		"required": []string{"query"},
	}
	tools := []llm.Tool{
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "search",
				Description: "Search for something",
				Parameters:  params,
			},
		},
	}

	out := convertTools(tools)
	if len(out) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(out))
	}
	if out[0].Name != "search" {
		t.Errorf("Name = %q, want search", out[0].Name)
	}
	if out[0].Description != "Search for something" {
		t.Errorf("Description = %q, want 'Search for something'", out[0].Description)
	}

	// Verify InputSchema is the parameters map.
	schema, ok := out[0].InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("InputSchema is %T, want map[string]any", out[0].InputSchema)
	}
	if schema["type"] != "object" {
		t.Errorf("InputSchema.type = %v, want object", schema["type"])
	}
}

func TestConvertTools_JSONSerialization(t *testing.T) {
	t.Parallel()
	tools := []llm.Tool{
		{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "my_tool",
				Description: "A tool",
				Parameters:  map[string]any{"type": "object"},
			},
		},
	}
	out := convertTools(tools)
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}
	// Verify it produces valid JSON with expected fields.
	var decoded []map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("expected 1 tool in JSON, got %d", len(decoded))
	}
	if decoded[0]["name"] != "my_tool" {
		t.Errorf("JSON name = %v, want my_tool", decoded[0]["name"])
	}
	if decoded[0]["description"] != "A tool" {
		t.Errorf("JSON description = %v, want 'A tool'", decoded[0]["description"])
	}
	if _, ok := decoded[0]["input_schema"]; !ok {
		t.Error("expected input_schema key in JSON output")
	}
}

// ---------------------------------------------------------------------------
// parseSSEStream
// ---------------------------------------------------------------------------

// collectStream is a test helper that runs parseSSEStream and collects all responses.
func collectStream(t *testing.T, sseData string) []llm.StreamResponse {
	t.Helper()
	ctx := context.Background()
	ch := make(chan llm.StreamResponse, 100)
	r := strings.NewReader(sseData)

	go func() {
		defer close(ch)
		parseSSEStream(ctx, r, ch)
	}()

	var responses []llm.StreamResponse
	for resp := range ch {
		responses = append(responses, resp)
	}
	return responses
}

func TestParseSSEStream_TextDeltas(t *testing.T) {
	t.Parallel()

	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":10,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	responses := collectStream(t, sse)

	// Find text content responses.
	var texts []string
	for _, r := range responses {
		if r.Content != "" {
			texts = append(texts, r.Content)
		}
	}
	if len(texts) != 2 {
		t.Fatalf("expected 2 text deltas, got %d: %v", len(texts), texts)
	}
	if texts[0] != "Hello" {
		t.Errorf("text[0] = %q, want Hello", texts[0])
	}
	if texts[1] != " world" {
		t.Errorf("text[1] = %q, want ' world'", texts[1])
	}

	// Verify model is propagated.
	for _, r := range responses {
		if r.Content != "" && r.Model != "claude-sonnet-4-20250514" {
			t.Errorf("model = %q, want claude-sonnet-4-20250514", r.Model)
		}
	}

	// Verify Done is set on the last response.
	last := responses[len(responses)-1]
	if !last.Done {
		t.Error("expected last response to have Done=true")
	}
}

func TestParseSSEStream_ToolUse(t *testing.T) {
	t.Parallel()

	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":20,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"get_weather"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"NYC\"}"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":15}}`,
		``,
	}, "\n")

	responses := collectStream(t, sse)

	// Find the response with tool calls.
	var toolResp *llm.StreamResponse
	for i := range responses {
		if len(responses[i].ToolCalls) > 0 {
			toolResp = &responses[i]
			break
		}
	}
	if toolResp == nil {
		t.Fatal("expected a response with ToolCalls")
	}
	if !toolResp.Done {
		t.Error("expected tool call response to have Done=true")
	}
	if len(toolResp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolResp.ToolCalls))
	}

	tc := toolResp.ToolCalls[0]
	if tc.ID != "toolu_01" {
		t.Errorf("tool call ID = %q, want toolu_01", tc.ID)
	}
	if tc.Function.Name != "get_weather" {
		t.Errorf("tool call name = %q, want get_weather", tc.Function.Name)
	}
	if tc.Function.Arguments != `{"city":"NYC"}` {
		t.Errorf("tool call arguments = %q, want {\"city\":\"NYC\"}", tc.Function.Arguments)
	}
	if tc.Type != "function" {
		t.Errorf("tool call type = %q, want function", tc.Type)
	}
}

func TestParseSSEStream_MultipleToolUse(t *testing.T) {
	t.Parallel()

	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":10,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_a","name":"tool_a"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_b","name":"tool_b"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"k\":\"v\"}"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":1}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}`,
		``,
	}, "\n")

	responses := collectStream(t, sse)

	var toolResp *llm.StreamResponse
	for i := range responses {
		if len(responses[i].ToolCalls) > 0 {
			toolResp = &responses[i]
			break
		}
	}
	if toolResp == nil {
		t.Fatal("expected a response with ToolCalls")
	}
	if len(toolResp.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(toolResp.ToolCalls))
	}

	// Verify sorted by index.
	if toolResp.ToolCalls[0].Index != 0 || toolResp.ToolCalls[1].Index != 1 {
		t.Errorf("tool calls not sorted by index: %v, %v", toolResp.ToolCalls[0].Index, toolResp.ToolCalls[1].Index)
	}
	if toolResp.ToolCalls[0].Function.Name != "tool_a" {
		t.Errorf("first tool = %q, want tool_a", toolResp.ToolCalls[0].Function.Name)
	}
	if toolResp.ToolCalls[1].Function.Name != "tool_b" {
		t.Errorf("second tool = %q, want tool_b", toolResp.ToolCalls[1].Function.Name)
	}
}

func TestParseSSEStream_ErrorEvent(t *testing.T) {
	t.Parallel()

	sse := strings.Join([]string{
		`event: error`,
		`data: {"type":"error","error":{"type":"overloaded_error","message":"API is overloaded"}}`,
		``,
	}, "\n")

	responses := collectStream(t, sse)
	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	if responses[0].Error == nil {
		t.Fatal("expected error response")
	}
	if !strings.Contains(responses[0].Error.Error(), "overloaded_error") {
		t.Errorf("error = %v, want to contain 'overloaded_error'", responses[0].Error)
	}
	if !strings.Contains(responses[0].Error.Error(), "API is overloaded") {
		t.Errorf("error = %v, want to contain 'API is overloaded'", responses[0].Error)
	}
}

func TestParseSSEStream_ContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	// Provide a stream that would normally produce output.
	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"model":"test","usage":{"input_tokens":1,"output_tokens":0}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"should not appear"}}`,
		``,
	}, "\n")

	ch := make(chan llm.StreamResponse, 100)
	r := strings.NewReader(sse)

	go func() {
		defer close(ch)
		parseSSEStream(ctx, r, ch)
	}()

	var responses []llm.StreamResponse
	for resp := range ch {
		responses = append(responses, resp)
	}

	// Should get a context error.
	foundCtxErr := false
	for _, r := range responses {
		if r.Error != nil && r.Error == context.Canceled {
			foundCtxErr = true
		}
	}
	if !foundCtxErr {
		t.Error("expected context.Canceled error in responses")
	}
}

func TestParseSSEStream_EmptyStream(t *testing.T) {
	t.Parallel()

	responses := collectStream(t, "")
	// Empty stream should produce a Done response (stream ended without message_stop).
	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	if !responses[0].Done {
		t.Error("expected Done=true for empty stream")
	}
}

func TestParseSSEStream_PingIgnored(t *testing.T) {
	t.Parallel()

	sse := strings.Join([]string{
		`event: ping`,
		`data: {}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	responses := collectStream(t, sse)
	// Should only get the message_stop Done response, ping is ignored.
	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	if !responses[0].Done {
		t.Error("expected Done=true")
	}
}

func TestParseSSEStream_UsageTracking(t *testing.T) {
	t.Parallel()

	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"model":"claude-sonnet-4-20250514","usage":{"input_tokens":42,"output_tokens":0}}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":17}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	responses := collectStream(t, sse)

	// Find the final Done response — it should carry usage.
	var final *llm.StreamResponse
	for i := range responses {
		if responses[i].Done {
			final = &responses[i]
			break
		}
	}
	if final == nil {
		t.Fatal("expected a Done response")
	}
	if final.Usage == nil {
		t.Fatal("expected Usage on final response")
	}
	if final.Usage.PromptTokens != 42 {
		t.Errorf("PromptTokens = %d, want 42", final.Usage.PromptTokens)
	}
	if final.Usage.CompletionTokens != 17 {
		t.Errorf("CompletionTokens = %d, want 17", final.Usage.CompletionTokens)
	}
	if final.Usage.TotalTokens != 59 {
		t.Errorf("TotalTokens = %d, want 59", final.Usage.TotalTokens)
	}
}

func TestParseSSEStream_StopReason(t *testing.T) {
	t.Parallel()

	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"model":"test","usage":{"input_tokens":1,"output_tokens":0}}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	responses := collectStream(t, sse)

	// Find the message_delta response (not the Done one).
	foundStopReason := false
	for _, r := range responses {
		if r.StopReason == "end_turn" {
			foundStopReason = true
		}
	}
	if !foundStopReason {
		t.Error("expected a response with StopReason='end_turn'")
	}
}

func TestParseSSEStream_MalformedJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sse  string
	}{
		{
			name: "malformed message_start",
			sse: strings.Join([]string{
				`event: message_start`,
				`data: {not json}`,
				``,
			}, "\n"),
		},
		{
			name: "malformed content_block_delta",
			sse: strings.Join([]string{
				`event: content_block_delta`,
				`data: {broken`,
				``,
			}, "\n"),
		},
		{
			name: "malformed content_block_start",
			sse: strings.Join([]string{
				`event: content_block_start`,
				`data: !!!`,
				``,
			}, "\n"),
		},
		{
			name: "malformed message_delta",
			sse: strings.Join([]string{
				`event: message_delta`,
				`data: [invalid]`,
				``,
			}, "\n"),
		},
		{
			name: "malformed error event",
			sse: strings.Join([]string{
				`event: error`,
				`data: not-json`,
				``,
			}, "\n"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			responses := collectStream(t, tt.sse)
			if len(responses) == 0 {
				t.Fatal("expected at least one response")
			}
			// Should get an error response.
			foundErr := false
			for _, r := range responses {
				if r.Error != nil {
					foundErr = true
				}
			}
			if !foundErr {
				t.Error("expected an error response for malformed JSON")
			}
		})
	}
}

func TestParseSSEStream_EmptyTextDeltaIgnored(t *testing.T) {
	t.Parallel()

	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"model":"test","usage":{"input_tokens":1,"output_tokens":0}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	responses := collectStream(t, sse)
	for _, r := range responses {
		if r.Content != "" {
			t.Errorf("expected no content responses for empty text delta, got %q", r.Content)
		}
	}
}

func TestParseSSEStream_NonDataLinesIgnored(t *testing.T) {
	t.Parallel()

	sse := strings.Join([]string{
		`: this is a comment`,
		`retry: 5000`,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	responses := collectStream(t, sse)
	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	if !responses[0].Done {
		t.Error("expected Done=true")
	}
}

func TestParseSSEStream_TextThenToolUse(t *testing.T) {
	t.Parallel()

	// Simulate assistant producing text then deciding to use a tool.
	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"model":"test","usage":{"input_tokens":5,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me check."}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_99","name":"lookup"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"test\"}"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":1}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`,
		``,
	}, "\n")

	responses := collectStream(t, sse)

	// Should have text content and tool calls.
	var gotText bool
	var gotToolCalls bool
	for _, r := range responses {
		if r.Content == "Let me check." {
			gotText = true
		}
		if len(r.ToolCalls) > 0 {
			gotToolCalls = true
			if r.ToolCalls[0].Function.Name != "lookup" {
				t.Errorf("tool name = %q, want lookup", r.ToolCalls[0].Function.Name)
			}
		}
	}
	if !gotText {
		t.Error("expected text content 'Let me check.'")
	}
	if !gotToolCalls {
		t.Error("expected tool calls")
	}
}

// errReader is an io.Reader that returns an error after reading some data.
type errReader struct {
	data string
	pos  int
	err  error
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	if r.pos >= len(r.data) {
		return n, r.err
	}
	return n, nil
}

func TestParseSSEStream_ScannerError(t *testing.T) {
	t.Parallel()

	// Provide partial data followed by a read error.
	reader := &errReader{
		data: "event: ping\ndata: {}\n\n",
		err:  fmt.Errorf("connection reset"),
	}

	ctx := context.Background()
	ch := make(chan llm.StreamResponse, 100)

	go func() {
		defer close(ch)
		parseSSEStream(ctx, reader, ch)
	}()

	var responses []llm.StreamResponse
	for resp := range ch {
		responses = append(responses, resp)
	}

	// Should get an error about reading stream.
	foundReadErr := false
	for _, r := range responses {
		if r.Error != nil && strings.Contains(r.Error.Error(), "reading stream") {
			foundReadErr = true
		}
	}
	if !foundReadErr {
		// The scanner may not surface the error if it completed scanning all lines.
		// In that case, we should get a Done response (stream ended without message_stop).
		foundDone := false
		for _, r := range responses {
			if r.Done {
				foundDone = true
			}
		}
		if !foundDone {
			t.Error("expected either a reading stream error or a Done response")
		}
	}
}

// errAfterReader returns data successfully, then returns an error on the next read.
type errAfterReader struct {
	r     io.Reader
	done  bool
	ioErr error
}

func (e *errAfterReader) Read(p []byte) (int, error) {
	if e.done {
		return 0, e.ioErr
	}
	n, err := e.r.Read(p)
	if err == io.EOF {
		e.done = true
		return n, e.ioErr
	}
	return n, err
}

func TestParseSSEStream_ScannerReadError(t *testing.T) {
	t.Parallel()

	// Use a reader that returns an error instead of EOF.
	// We need to make the scanner encounter the error, so we use a reader
	// that has no trailing newline (so scanner keeps trying to read more).
	underlying := strings.NewReader("event: ping\ndata: {}\n\nsome incomplete line without newline")
	reader := &errAfterReader{
		r:     underlying,
		ioErr: fmt.Errorf("network timeout"),
	}

	ctx := context.Background()
	ch := make(chan llm.StreamResponse, 100)

	go func() {
		defer close(ch)
		parseSSEStream(ctx, reader, ch)
	}()

	var responses []llm.StreamResponse
	for resp := range ch {
		responses = append(responses, resp)
	}

	// We should get at least one response.
	if len(responses) == 0 {
		t.Fatal("expected at least one response")
	}

	// Check if we got the scanner error.
	foundReadErr := false
	for _, r := range responses {
		if r.Error != nil && strings.Contains(r.Error.Error(), "reading stream") {
			foundReadErr = true
		}
	}
	if !foundReadErr {
		// If the scanner didn't surface the error, we should at least get a Done.
		foundDone := false
		for _, r := range responses {
			if r.Done {
				foundDone = true
			}
		}
		if !foundDone {
			t.Error("expected either a reading stream error or a Done response")
		}
	}
}
