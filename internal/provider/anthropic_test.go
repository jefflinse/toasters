package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// anthropicSSEServer creates a test server that responds with Anthropic-style SSE.
func anthropicSSEServer(t *testing.T, sseData string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprint(w, sseData)
		flusher.Flush()
	}))
}

func TestAnthropic_Name(t *testing.T) {
	t.Parallel()
	p := NewAnthropic("anthropic", "key")
	if p.Name() != "anthropic" {
		t.Errorf("Name() = %q, want %q", p.Name(), "anthropic")
	}
}

func TestAnthropic_Options(t *testing.T) {
	t.Parallel()

	p := NewAnthropic("test", "key",
		WithAnthropicBaseURL("https://custom.example.com/"),
		WithAnthropicVersion("2024-01-01"),
	)

	if p.baseURL != "https://custom.example.com" {
		t.Errorf("baseURL = %q, want %q", p.baseURL, "https://custom.example.com")
	}
	if p.version != "2024-01-01" {
		t.Errorf("version = %q, want %q", p.version, "2024-01-01")
	}
}

func TestAnthropic_ChatStream_TextStreaming(t *testing.T) {
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

	srv := anthropicSSEServer(t, sse)
	defer srv.Close()

	p := NewAnthropic("test", "key", WithAnthropicBaseURL(srv.URL))
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	events := collectEvents(ch)

	var text strings.Builder
	var gotDone bool
	var gotUsage *Usage

	for _, ev := range events {
		switch ev.Type {
		case EventText:
			text.WriteString(ev.Text)
		case EventUsage:
			gotUsage = ev.Usage
		case EventDone:
			gotDone = true
		case EventError:
			t.Fatalf("unexpected error: %v", ev.Error)
		}
	}

	if got := text.String(); got != "Hello world" {
		t.Errorf("text = %q, want %q", got, "Hello world")
	}
	if !gotDone {
		t.Error("never received EventDone")
	}
	if gotUsage == nil {
		t.Fatal("expected usage event")
	}
	if gotUsage.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", gotUsage.InputTokens)
	}
	if gotUsage.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d, want 5", gotUsage.OutputTokens)
	}
}

func TestAnthropic_ChatStream_ToolUse(t *testing.T) {
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

	srv := anthropicSSEServer(t, sse)
	defer srv.Close()

	p := NewAnthropic("test", "key", WithAnthropicBaseURL(srv.URL))
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Model:    "claude-sonnet-4-20250514",
		Messages: []Message{{Role: "user", Content: "Weather?"}},
		Tools: []Tool{{
			Name:        "get_weather",
			Description: "Get weather",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	events := collectEvents(ch)

	var toolCalls []*ToolCall
	var gotDone bool
	for _, ev := range events {
		switch ev.Type {
		case EventToolCall:
			toolCalls = append(toolCalls, ev.ToolCall)
		case EventDone:
			gotDone = true
		case EventError:
			t.Fatalf("unexpected error: %v", ev.Error)
		}
	}

	if !gotDone {
		t.Error("never received EventDone")
	}
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}

	tc := toolCalls[0]
	if tc.ID != "toolu_01" {
		t.Errorf("ToolCall.ID = %q, want toolu_01", tc.ID)
	}
	if tc.Name != "get_weather" {
		t.Errorf("ToolCall.Name = %q, want get_weather", tc.Name)
	}
	if string(tc.Arguments) != `{"city":"NYC"}` {
		t.Errorf("ToolCall.Arguments = %q, want {\"city\":\"NYC\"}", string(tc.Arguments))
	}
}

func TestAnthropic_ChatStream_MultipleToolUse(t *testing.T) {
	t.Parallel()

	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"model":"test","usage":{"input_tokens":10,"output_tokens":0}}}`,
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

	srv := anthropicSSEServer(t, sse)
	defer srv.Close()

	p := NewAnthropic("test", "key", WithAnthropicBaseURL(srv.URL))
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: "user", Content: "Do stuff"}},
		Tools:    []Tool{{Name: "tool_a"}, {Name: "tool_b"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	events := collectEvents(ch)

	var toolCalls []*ToolCall
	for _, ev := range events {
		if ev.Type == EventToolCall {
			toolCalls = append(toolCalls, ev.ToolCall)
		}
		if ev.Type == EventError {
			t.Fatalf("unexpected error: %v", ev.Error)
		}
	}

	if len(toolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(toolCalls))
	}
	if toolCalls[0].Name != "tool_a" {
		t.Errorf("tool[0].Name = %q, want tool_a", toolCalls[0].Name)
	}
	if toolCalls[1].Name != "tool_b" {
		t.Errorf("tool[1].Name = %q, want tool_b", toolCalls[1].Name)
	}
}

func TestAnthropic_ChatStream_MixedTextAndToolUse(t *testing.T) {
	t.Parallel()

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

	srv := anthropicSSEServer(t, sse)
	defer srv.Close()

	p := NewAnthropic("test", "key", WithAnthropicBaseURL(srv.URL))
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: "user", Content: "test"}},
		Tools:    []Tool{{Name: "lookup"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	events := collectEvents(ch)

	var gotText bool
	var gotToolCall bool
	for _, ev := range events {
		if ev.Type == EventText && ev.Text == "Let me check." {
			gotText = true
		}
		if ev.Type == EventToolCall && ev.ToolCall.Name == "lookup" {
			gotToolCall = true
		}
		if ev.Type == EventError {
			t.Fatalf("unexpected error: %v", ev.Error)
		}
	}
	if !gotText {
		t.Error("expected text event 'Let me check.'")
	}
	if !gotToolCall {
		t.Error("expected tool call event for 'lookup'")
	}
}

func TestAnthropic_ChatStream_ErrorEvent(t *testing.T) {
	t.Parallel()

	sse := strings.Join([]string{
		`event: error`,
		`data: {"type":"error","error":{"type":"overloaded_error","message":"API is overloaded"}}`,
		``,
	}, "\n")

	srv := anthropicSSEServer(t, sse)
	defer srv.Close()

	p := NewAnthropic("test", "key", WithAnthropicBaseURL(srv.URL))
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	events := collectEvents(ch)

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != EventError {
		t.Fatalf("expected EventError, got %v", events[0].Type)
	}
	if !strings.Contains(events[0].Error.Error(), "overloaded_error") {
		t.Errorf("error = %v, want to contain 'overloaded_error'", events[0].Error)
	}
}

func TestAnthropic_ChatStream_ErrorStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"error":{"type":"rate_limit","message":"too many requests"}}`)
	}))
	defer srv.Close()

	p := NewAnthropic("test", "key", WithAnthropicBaseURL(srv.URL))
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	events := collectEvents(ch)

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != EventError {
		t.Fatalf("expected EventError, got %v", events[0].Type)
	}
	if !strings.Contains(events[0].Error.Error(), "429") {
		t.Errorf("error = %v, want to contain '429'", events[0].Error)
	}
}

func TestAnthropic_ChatStream_ContextCancellation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		sse := strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"model":"test","usage":{"input_tokens":1,"output_tokens":0}}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"start"}}`,
			``,
		}, "\n")
		_, _ = fmt.Fprint(w, sse)
		flusher.Flush()

		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	p := NewAnthropic("test", "key", WithAnthropicBaseURL(srv.URL))
	ch, err := p.ChatStream(ctx, ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	// Read first text event.
	ev := <-ch
	if ev.Type != EventText || ev.Text != "start" {
		t.Fatalf("first event = %+v, want text 'start'", ev)
	}

	cancel()

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timer.C:
			t.Fatal("timed out waiting for channel to close")
		}
	}
}

func TestAnthropic_ChatStream_APIKeyHeader(t *testing.T) {
	t.Parallel()

	var capturedAPIKey string
	var capturedVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAPIKey = r.Header.Get("x-api-key")
		capturedVersion = r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	p := NewAnthropic("test", "sk-ant-test", WithAnthropicBaseURL(srv.URL))
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	collectEvents(ch)

	if capturedAPIKey != "sk-ant-test" {
		t.Errorf("x-api-key = %q, want %q", capturedAPIKey, "sk-ant-test")
	}
	if capturedVersion != defaultAnthropicVersion {
		t.Errorf("anthropic-version = %q, want %q", capturedVersion, defaultAnthropicVersion)
	}
}

func TestAnthropic_ChatStream_SystemPrompt(t *testing.T) {
	t.Parallel()

	var capturedBody anthropicReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	p := NewAnthropic("test", "key", WithAnthropicBaseURL(srv.URL))
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Model:    "test",
		System:   "You are helpful.",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	collectEvents(ch)

	if capturedBody.System != "You are helpful." {
		t.Errorf("system = %q, want 'You are helpful.'", capturedBody.System)
	}
}

func TestAnthropic_ChatStream_DefaultMaxTokens(t *testing.T) {
	t.Parallel()

	var capturedBody anthropicReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	p := NewAnthropic("test", "key", WithAnthropicBaseURL(srv.URL))
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	collectEvents(ch)

	if capturedBody.MaxTokens != 4096 {
		t.Errorf("max_tokens = %d, want 4096", capturedBody.MaxTokens)
	}
}

func TestAnthropic_ChatStream_PingIgnored(t *testing.T) {
	t.Parallel()

	sse := strings.Join([]string{
		`event: ping`,
		`data: {}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	srv := anthropicSSEServer(t, sse)
	defer srv.Close()

	p := NewAnthropic("test", "key", WithAnthropicBaseURL(srv.URL))
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	events := collectEvents(ch)

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != EventDone {
		t.Errorf("expected EventDone, got %v", events[0].Type)
	}
}

func TestAnthropic_ChatStream_MalformedJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sse  string
	}{
		{
			name: "malformed message_start",
			sse:  "event: message_start\ndata: {not json}\n\n",
		},
		{
			name: "malformed content_block_delta",
			sse:  "event: content_block_delta\ndata: {broken\n\n",
		},
		{
			name: "malformed content_block_start",
			sse:  "event: content_block_start\ndata: !!!\n\n",
		},
		{
			name: "malformed message_delta",
			sse:  "event: message_delta\ndata: [invalid]\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := anthropicSSEServer(t, tt.sse)
			defer srv.Close()

			p := NewAnthropic("test", "key", WithAnthropicBaseURL(srv.URL))
			ch, err := p.ChatStream(context.Background(), ChatRequest{
				Model:    "test",
				Messages: []Message{{Role: "user", Content: "test"}},
			})
			if err != nil {
				t.Fatalf("ChatStream error: %v", err)
			}

			events := collectEvents(ch)

			var gotError bool
			for _, ev := range events {
				if ev.Type == EventError {
					gotError = true
				}
			}
			if !gotError {
				t.Error("expected an error event for malformed JSON")
			}
		})
	}
}

func TestAnthropic_ChatStream_EmptyStream(t *testing.T) {
	t.Parallel()

	srv := anthropicSSEServer(t, "")
	defer srv.Close()

	p := NewAnthropic("test", "key", WithAnthropicBaseURL(srv.URL))
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	events := collectEvents(ch)

	// Should get a Done event.
	var gotDone bool
	for _, ev := range events {
		if ev.Type == EventDone {
			gotDone = true
		}
	}
	if !gotDone {
		t.Error("expected EventDone for empty stream")
	}
}

func TestAnthropic_Models(t *testing.T) {
	t.Parallel()

	p := NewAnthropic("anthropic", "key")
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models error: %v", err)
	}

	if len(models) < 3 {
		t.Fatalf("expected at least 3 models, got %d", len(models))
	}

	// Verify all have the provider set.
	for _, m := range models {
		if m.Provider != "anthropic" {
			t.Errorf("model %q has Provider = %q, want anthropic", m.ID, m.Provider)
		}
	}
}

func TestAnthropic_ChatStream_KeychainFallback_BearerToken(t *testing.T) {
	t.Parallel()

	var capturedAuthHeader string
	var capturedBetaHeader string
	var capturedAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
		capturedBetaHeader = r.Header.Get("anthropic-beta")
		capturedAPIKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	// Empty apiKey → should use the injected authFunc (simulating Keychain fallback).
	p := NewAnthropic("test", "",
		WithAnthropicBaseURL(srv.URL),
		WithAnthropicAuthFunc(func() (*authHeaders, error) {
			return &authHeaders{
				header: "Authorization",
				value:  "Bearer test-oauth-token",
				extra: map[string]string{
					"anthropic-beta": "oauth-2025-04-20",
				},
			}, nil
		}),
	)

	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	collectEvents(ch)

	if capturedAuthHeader != "Bearer test-oauth-token" {
		t.Errorf("Authorization = %q, want %q", capturedAuthHeader, "Bearer test-oauth-token")
	}
	if capturedBetaHeader != "oauth-2025-04-20" {
		t.Errorf("anthropic-beta = %q, want %q", capturedBetaHeader, "oauth-2025-04-20")
	}
	if capturedAPIKey != "" {
		t.Errorf("x-api-key = %q, want empty (should not be set)", capturedAPIKey)
	}
}

func TestAnthropic_ChatStream_APIKeyOverridesKeychain(t *testing.T) {
	t.Parallel()

	var capturedAuthHeader string
	var capturedAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
		capturedAPIKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	// Non-empty apiKey → should use x-api-key, not Bearer.
	p := NewAnthropic("test", "sk-ant-explicit-key", WithAnthropicBaseURL(srv.URL))

	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	collectEvents(ch)

	if capturedAPIKey != "sk-ant-explicit-key" {
		t.Errorf("x-api-key = %q, want %q", capturedAPIKey, "sk-ant-explicit-key")
	}
	if capturedAuthHeader != "" {
		t.Errorf("Authorization = %q, want empty (should not be set when apiKey is provided)", capturedAuthHeader)
	}
}

func TestAnthropic_ChatStream_AuthFuncError(t *testing.T) {
	t.Parallel()

	p := NewAnthropic("test", "",
		WithAnthropicAuthFunc(func() (*authHeaders, error) {
			return nil, fmt.Errorf("keychain unavailable")
		}),
	)

	_, err := p.ChatStream(context.Background(), ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err == nil {
		t.Fatal("expected error from ChatStream when authFunc fails")
	}
	if !strings.Contains(err.Error(), "keychain unavailable") {
		t.Errorf("error = %v, want to contain 'keychain unavailable'", err)
	}
	if !strings.Contains(err.Error(), "resolving credentials") {
		t.Errorf("error = %v, want to contain 'resolving credentials'", err)
	}
}

func TestAnthropic_WithAnthropicAuthFunc_OverridesDefault(t *testing.T) {
	t.Parallel()

	var capturedAuthHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	// Even with an apiKey set, WithAnthropicAuthFunc should override.
	p := NewAnthropic("test", "sk-ant-should-be-ignored",
		WithAnthropicBaseURL(srv.URL),
		WithAnthropicAuthFunc(func() (*authHeaders, error) {
			return &authHeaders{
				header: "Authorization",
				value:  "Bearer custom-token",
			}, nil
		}),
	)

	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Model:    "test",
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	collectEvents(ch)

	if capturedAuthHeader != "Bearer custom-token" {
		t.Errorf("Authorization = %q, want %q", capturedAuthHeader, "Bearer custom-token")
	}
}

func TestAnthropic_ChatStream_ToolResultMessages(t *testing.T) {
	t.Parallel()

	var capturedBody anthropicReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	p := NewAnthropic("test", "key", WithAnthropicBaseURL(srv.URL))
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Model: "test",
		Messages: []Message{
			{Role: "user", Content: "What's the weather?"},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID:        "toolu_1",
					Name:      "get_weather",
					Arguments: json.RawMessage(`{"city":"NYC"}`),
				}},
			},
			{Role: "tool", ToolCallID: "toolu_1", Content: `{"temp":72}`},
		},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	collectEvents(ch)

	// Verify the messages were converted correctly.
	if len(capturedBody.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(capturedBody.Messages))
	}

	// First: user
	if capturedBody.Messages[0].Role != "user" {
		t.Errorf("msg[0].role = %q, want user", capturedBody.Messages[0].Role)
	}

	// Second: assistant with tool_use blocks
	if capturedBody.Messages[1].Role != "assistant" {
		t.Errorf("msg[1].role = %q, want assistant", capturedBody.Messages[1].Role)
	}
	var assistantBlocks []map[string]any
	if err := json.Unmarshal(capturedBody.Messages[1].Content, &assistantBlocks); err != nil {
		t.Fatalf("msg[1] content unmarshal error: %v", err)
	}
	if len(assistantBlocks) != 1 {
		t.Fatalf("expected 1 assistant block, got %d", len(assistantBlocks))
	}
	if assistantBlocks[0]["type"] != "tool_use" {
		t.Errorf("assistant block type = %v, want tool_use", assistantBlocks[0]["type"])
	}

	// Third: user with tool_result blocks
	if capturedBody.Messages[2].Role != "user" {
		t.Errorf("msg[2].role = %q, want user", capturedBody.Messages[2].Role)
	}
	var toolBlocks []map[string]any
	if err := json.Unmarshal(capturedBody.Messages[2].Content, &toolBlocks); err != nil {
		t.Fatalf("msg[2] content unmarshal error: %v", err)
	}
	if len(toolBlocks) != 1 {
		t.Fatalf("expected 1 tool_result block, got %d", len(toolBlocks))
	}
	if toolBlocks[0]["type"] != "tool_result" {
		t.Errorf("tool block type = %v, want tool_result", toolBlocks[0]["type"])
	}
}
