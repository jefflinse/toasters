package client

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

	"github.com/jefflinse/toasters/internal/llm"
)

// sseServer creates an httptest.Server that responds to POST /v1/chat/completions
// with the given SSE lines. Each line is flushed individually.
func sseServer(t *testing.T, lines []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		for _, line := range lines {
			_, _ = fmt.Fprint(w, line+"\n")
			flusher.Flush()
		}
	}))
}

// collectStream drains a StreamResponse channel into a slice.
func collectStream(ch <-chan llm.StreamResponse) []llm.StreamResponse {
	var results []llm.StreamResponse
	for resp := range ch {
		results = append(results, resp)
	}
	return results
}

// chunk builds a JSON SSE data line from a ChatCompletionChunk.
func chunk(c llm.ChatCompletionChunk) string {
	b, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	return "data: " + string(b)
}

func TestNewClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		model   string
		wantURL string
	}{
		{
			name:    "basic URL",
			baseURL: "http://localhost:1234",
			model:   "test-model",
			wantURL: "http://localhost:1234",
		},
		{
			name:    "trailing slash stripped",
			baseURL: "http://localhost:1234/",
			model:   "test-model",
			wantURL: "http://localhost:1234",
		},
		{
			name:    "multiple trailing slashes stripped",
			baseURL: "http://localhost:1234///",
			model:   "",
			wantURL: "http://localhost:1234",
		},
		{
			name:    "empty model",
			baseURL: "http://localhost:1234",
			model:   "",
			wantURL: "http://localhost:1234",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := NewClient(tt.baseURL, tt.model)
			if c.BaseURL() != tt.wantURL {
				t.Errorf("BaseURL() = %q, want %q", c.BaseURL(), tt.wantURL)
			}
			if c.model != tt.model {
				t.Errorf("model = %q, want %q", c.model, tt.model)
			}
		})
	}
}

func TestChatCompletionStream_BasicStreaming(t *testing.T) {
	t.Parallel()

	lines := []string{
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-1",
			Model: "test-model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{Role: "assistant", Content: "Hello"},
			}},
		}),
		"",
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-1",
			Model: "test-model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{Content: " world"},
			}},
		}),
		"",
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-1",
			Model: "test-model",
			Choices: []llm.Choice{{
				Delta:        llm.Delta{},
				FinishReason: "stop",
			}},
		}),
		"",
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-1",
			Model: "test-model",
			Usage: &llm.Usage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
		}),
		"",
		"data: [DONE]",
		"",
	}

	srv := sseServer(t, lines)
	defer srv.Close()

	c := NewClient(srv.URL, "test-model")
	ch := c.ChatCompletionStream(context.Background(), []llm.Message{
		{Role: "user", Content: "Hi"},
	}, 0)

	results := collectStream(ch)

	// Expect: "Hello", " world", and a final Done message.
	var content strings.Builder
	var gotDone bool
	var finalUsage *llm.Usage
	var finalModel string

	for _, r := range results {
		if r.Error != nil {
			t.Fatalf("unexpected error: %v", r.Error)
		}
		content.WriteString(r.Content)
		if r.Done {
			gotDone = true
			finalUsage = r.Usage
			finalModel = r.Model
		}
	}

	if got := content.String(); got != "Hello world" {
		t.Errorf("assembled content = %q, want %q", got, "Hello world")
	}
	if !gotDone {
		t.Error("never received Done=true")
	}
	if finalUsage == nil {
		t.Fatal("expected usage on final message, got nil")
	}
	if finalUsage.TotalTokens != 15 {
		t.Errorf("TotalTokens = %d, want 15", finalUsage.TotalTokens)
	}
	if finalModel != "test-model" {
		t.Errorf("Model = %q, want %q", finalModel, "test-model")
	}
}

func TestChatCompletionStream_WithoutDONE(t *testing.T) {
	t.Parallel()

	// Server sends chunks then closes the connection without [DONE].
	lines := []string{
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-2",
			Model: "my-model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{Content: "partial"},
			}},
		}),
		"",
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-2",
			Model: "my-model",
			Choices: []llm.Choice{{
				Delta:        llm.Delta{},
				FinishReason: "stop",
			}},
		}),
		"",
		// No "data: [DONE]" — connection just closes.
	}

	srv := sseServer(t, lines)
	defer srv.Close()

	c := NewClient(srv.URL, "my-model")
	ch := c.ChatCompletionStream(context.Background(), []llm.Message{
		{Role: "user", Content: "Hi"},
	}, 0)

	results := collectStream(ch)

	var gotDone bool
	var content strings.Builder
	for _, r := range results {
		if r.Error != nil {
			t.Fatalf("unexpected error: %v", r.Error)
		}
		content.WriteString(r.Content)
		if r.Done {
			gotDone = true
		}
	}

	if !gotDone {
		t.Error("expected Done=true when stream ends without [DONE]")
	}
	if got := content.String(); got != "partial" {
		t.Errorf("content = %q, want %q", got, "partial")
	}
}

func TestChatCompletionStream_EmptyContentDeltas(t *testing.T) {
	t.Parallel()

	lines := []string{
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-3",
			Model: "test-model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{Role: "assistant", Content: ""},
			}},
		}),
		"",
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-3",
			Model: "test-model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{Content: "data"},
			}},
		}),
		"",
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-3",
			Model: "test-model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{Content: ""},
			}},
		}),
		"",
		"data: [DONE]",
		"",
	}

	srv := sseServer(t, lines)
	defer srv.Close()

	c := NewClient(srv.URL, "test-model")
	ch := c.ChatCompletionStream(context.Background(), []llm.Message{
		{Role: "user", Content: "test"},
	}, 0)

	results := collectStream(ch)

	// Only non-empty content should produce StreamResponse with Content set.
	var contentResponses int
	for _, r := range results {
		if r.Error != nil {
			t.Fatalf("unexpected error: %v", r.Error)
		}
		if r.Content != "" {
			contentResponses++
		}
	}

	if contentResponses != 1 {
		t.Errorf("expected 1 content response, got %d", contentResponses)
	}
}

func TestChatCompletionStream_ReasoningChunks(t *testing.T) {
	t.Parallel()

	lines := []string{
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-4",
			Model: "reasoning-model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{Reasoning: "Let me think..."},
			}},
		}),
		"",
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-4",
			Model: "reasoning-model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{Content: "The answer is 42."},
			}},
		}),
		"",
		"data: [DONE]",
		"",
	}

	srv := sseServer(t, lines)
	defer srv.Close()

	c := NewClient(srv.URL, "reasoning-model")
	ch := c.ChatCompletionStream(context.Background(), []llm.Message{
		{Role: "user", Content: "What is the meaning of life?"},
	}, 0)

	results := collectStream(ch)

	var gotReasoning, gotContent string
	for _, r := range results {
		if r.Error != nil {
			t.Fatalf("unexpected error: %v", r.Error)
		}
		if r.Reasoning != "" {
			gotReasoning += r.Reasoning
		}
		if r.Content != "" {
			gotContent += r.Content
		}
	}

	if gotReasoning != "Let me think..." {
		t.Errorf("Reasoning = %q, want %q", gotReasoning, "Let me think...")
	}
	if gotContent != "The answer is 42." {
		t.Errorf("Content = %q, want %q", gotContent, "The answer is 42.")
	}
}

func TestChatCompletionStreamWithTools_SingleToolCall(t *testing.T) {
	t.Parallel()

	lines := []string{
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-5",
			Model: "tool-model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{
					ToolCalls: []llm.ToolCall{{
						Index: 0,
						ID:    "call_abc123",
						Type:  "function",
						Function: llm.ToolCallFunction{
							Name:      "read_file",
							Arguments: "",
						},
					}},
				},
			}},
		}),
		"",
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-5",
			Model: "tool-model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{
					ToolCalls: []llm.ToolCall{{
						Index: 0,
						Function: llm.ToolCallFunction{
							Arguments: `{"path":`,
						},
					}},
				},
			}},
		}),
		"",
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-5",
			Model: "tool-model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{
					ToolCalls: []llm.ToolCall{{
						Index: 0,
						Function: llm.ToolCallFunction{
							Arguments: `"foo.txt"}`,
						},
					}},
				},
			}},
		}),
		"",
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-5",
			Model: "tool-model",
			Choices: []llm.Choice{{
				Delta:        llm.Delta{},
				FinishReason: "tool_calls",
			}},
		}),
		"",
		"data: [DONE]",
		"",
	}

	srv := sseServer(t, lines)
	defer srv.Close()

	c := NewClient(srv.URL, "tool-model")
	tools := []llm.Tool{{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "read_file",
			Description: "Read a file",
			Parameters:  map[string]any{"type": "object"},
		},
	}}

	ch := c.ChatCompletionStreamWithTools(context.Background(), []llm.Message{
		{Role: "user", Content: "Read foo.txt"},
	}, tools, 0)

	results := collectStream(ch)

	// The finish_reason=tool_calls chunk should produce a Done response with ToolCalls.
	var toolCallResp *llm.StreamResponse
	for i := range results {
		if results[i].Error != nil {
			t.Fatalf("unexpected error: %v", results[i].Error)
		}
		if results[i].Done && len(results[i].ToolCalls) > 0 {
			toolCallResp = &results[i]
		}
	}

	if toolCallResp == nil {
		t.Fatal("expected a Done response with ToolCalls")
	}

	if len(toolCallResp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCallResp.ToolCalls))
	}

	tc := toolCallResp.ToolCalls[0]
	if tc.ID != "call_abc123" {
		t.Errorf("ToolCall.ID = %q, want %q", tc.ID, "call_abc123")
	}
	if tc.Function.Name != "read_file" {
		t.Errorf("ToolCall.Function.Name = %q, want %q", tc.Function.Name, "read_file")
	}
	wantArgs := `{"path":"foo.txt"}`
	if tc.Function.Arguments != wantArgs {
		t.Errorf("ToolCall.Function.Arguments = %q, want %q", tc.Function.Arguments, wantArgs)
	}
	if tc.Type != "function" {
		t.Errorf("ToolCall.Type = %q, want %q", tc.Type, "function")
	}
}

func TestChatCompletionStreamWithTools_MultipleToolCalls(t *testing.T) {
	t.Parallel()

	lines := []string{
		// First tool call starts.
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-6",
			Model: "tool-model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{
					ToolCalls: []llm.ToolCall{{
						Index: 0,
						ID:    "call_aaa",
						Type:  "function",
						Function: llm.ToolCallFunction{
							Name:      "read_file",
							Arguments: "",
						},
					}},
				},
			}},
		}),
		"",
		// Second tool call starts.
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-6",
			Model: "tool-model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{
					ToolCalls: []llm.ToolCall{{
						Index: 1,
						ID:    "call_bbb",
						Type:  "function",
						Function: llm.ToolCallFunction{
							Name:      "write_file",
							Arguments: "",
						},
					}},
				},
			}},
		}),
		"",
		// Arguments for first tool call.
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-6",
			Model: "tool-model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{
					ToolCalls: []llm.ToolCall{{
						Index: 0,
						Function: llm.ToolCallFunction{
							Arguments: `{"path":"a.txt"}`,
						},
					}},
				},
			}},
		}),
		"",
		// Arguments for second tool call.
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-6",
			Model: "tool-model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{
					ToolCalls: []llm.ToolCall{{
						Index: 1,
						Function: llm.ToolCallFunction{
							Arguments: `{"path":"b.txt","content":"hi"}`,
						},
					}},
				},
			}},
		}),
		"",
		// Finish.
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-6",
			Model: "tool-model",
			Choices: []llm.Choice{{
				Delta:        llm.Delta{},
				FinishReason: "tool_calls",
			}},
		}),
		"",
		"data: [DONE]",
		"",
	}

	srv := sseServer(t, lines)
	defer srv.Close()

	c := NewClient(srv.URL, "tool-model")
	tools := []llm.Tool{
		{Type: "function", Function: llm.ToolFunction{Name: "read_file"}},
		{Type: "function", Function: llm.ToolFunction{Name: "write_file"}},
	}

	ch := c.ChatCompletionStreamWithTools(context.Background(), []llm.Message{
		{Role: "user", Content: "Do stuff"},
	}, tools, 0)

	results := collectStream(ch)

	var toolCallResp *llm.StreamResponse
	for i := range results {
		if results[i].Error != nil {
			t.Fatalf("unexpected error: %v", results[i].Error)
		}
		if results[i].Done && len(results[i].ToolCalls) > 0 {
			toolCallResp = &results[i]
		}
	}

	if toolCallResp == nil {
		t.Fatal("expected a Done response with ToolCalls")
	}

	if len(toolCallResp.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(toolCallResp.ToolCalls))
	}

	// Verify sorted by index.
	tc0 := toolCallResp.ToolCalls[0]
	tc1 := toolCallResp.ToolCalls[1]

	if tc0.ID != "call_aaa" || tc0.Function.Name != "read_file" {
		t.Errorf("tool call 0: ID=%q Name=%q, want call_aaa/read_file", tc0.ID, tc0.Function.Name)
	}
	if tc0.Function.Arguments != `{"path":"a.txt"}` {
		t.Errorf("tool call 0 args = %q, want %q", tc0.Function.Arguments, `{"path":"a.txt"}`)
	}

	if tc1.ID != "call_bbb" || tc1.Function.Name != "write_file" {
		t.Errorf("tool call 1: ID=%q Name=%q, want call_bbb/write_file", tc1.ID, tc1.Function.Name)
	}
	if tc1.Function.Arguments != `{"path":"b.txt","content":"hi"}` {
		t.Errorf("tool call 1 args = %q, want %q", tc1.Function.Arguments, `{"path":"b.txt","content":"hi"}`)
	}
}

func TestChatCompletionStreamWithTools_ArgumentsConcatenation(t *testing.T) {
	t.Parallel()

	// Arguments arrive in many small chunks.
	lines := []string{
		chunk(llm.ChatCompletionChunk{
			Choices: []llm.Choice{{
				Delta: llm.Delta{
					ToolCalls: []llm.ToolCall{{
						Index: 0,
						ID:    "call_xyz",
						Type:  "function",
						Function: llm.ToolCallFunction{
							Name:      "search",
							Arguments: "",
						},
					}},
				},
			}},
		}),
		"",
		chunk(llm.ChatCompletionChunk{
			Choices: []llm.Choice{{
				Delta: llm.Delta{
					ToolCalls: []llm.ToolCall{{
						Index:    0,
						Function: llm.ToolCallFunction{Arguments: `{`},
					}},
				},
			}},
		}),
		"",
		chunk(llm.ChatCompletionChunk{
			Choices: []llm.Choice{{
				Delta: llm.Delta{
					ToolCalls: []llm.ToolCall{{
						Index:    0,
						Function: llm.ToolCallFunction{Arguments: `"query"`},
					}},
				},
			}},
		}),
		"",
		chunk(llm.ChatCompletionChunk{
			Choices: []llm.Choice{{
				Delta: llm.Delta{
					ToolCalls: []llm.ToolCall{{
						Index:    0,
						Function: llm.ToolCallFunction{Arguments: `:"hello"`},
					}},
				},
			}},
		}),
		"",
		chunk(llm.ChatCompletionChunk{
			Choices: []llm.Choice{{
				Delta: llm.Delta{
					ToolCalls: []llm.ToolCall{{
						Index:    0,
						Function: llm.ToolCallFunction{Arguments: `}`},
					}},
				},
			}},
		}),
		"",
		chunk(llm.ChatCompletionChunk{
			Choices: []llm.Choice{{
				Delta:        llm.Delta{},
				FinishReason: "tool_calls",
			}},
		}),
		"",
		"data: [DONE]",
		"",
	}

	srv := sseServer(t, lines)
	defer srv.Close()

	c := NewClient(srv.URL, "")
	tools := []llm.Tool{{Type: "function", Function: llm.ToolFunction{Name: "search"}}}

	ch := c.ChatCompletionStreamWithTools(context.Background(), []llm.Message{
		{Role: "user", Content: "search"},
	}, tools, 0)

	results := collectStream(ch)

	var toolCallResp *llm.StreamResponse
	for i := range results {
		if results[i].Error != nil {
			t.Fatalf("unexpected error: %v", results[i].Error)
		}
		if results[i].Done && len(results[i].ToolCalls) > 0 {
			toolCallResp = &results[i]
		}
	}

	if toolCallResp == nil {
		t.Fatal("expected Done response with ToolCalls")
	}

	wantArgs := `{"query":"hello"}`
	if got := toolCallResp.ToolCalls[0].Function.Arguments; got != wantArgs {
		t.Errorf("concatenated arguments = %q, want %q", got, wantArgs)
	}
}

func TestChatCompletionStream_UsageReporting(t *testing.T) {
	t.Parallel()

	lines := []string{
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-u",
			Model: "usage-model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{Content: "ok"},
			}},
		}),
		"",
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-u",
			Model: "usage-model",
			Choices: []llm.Choice{{
				Delta:        llm.Delta{},
				FinishReason: "stop",
			}},
		}),
		"",
		// Usage-only chunk (no choices).
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-u",
			Model: "usage-model",
			Usage: &llm.Usage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
			},
		}),
		"",
		"data: [DONE]",
		"",
	}

	srv := sseServer(t, lines)
	defer srv.Close()

	c := NewClient(srv.URL, "usage-model")
	ch := c.ChatCompletionStream(context.Background(), []llm.Message{
		{Role: "user", Content: "test"},
	}, 0)

	results := collectStream(ch)

	var finalResp *llm.StreamResponse
	for i := range results {
		if results[i].Done {
			finalResp = &results[i]
		}
	}

	if finalResp == nil {
		t.Fatal("expected Done response")
	}
	if finalResp.Usage == nil {
		t.Fatal("expected usage on final response")
	}
	if finalResp.Usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100", finalResp.Usage.PromptTokens)
	}
	if finalResp.Usage.CompletionTokens != 50 {
		t.Errorf("CompletionTokens = %d, want 50", finalResp.Usage.CompletionTokens)
	}
	if finalResp.Usage.TotalTokens != 150 {
		t.Errorf("TotalTokens = %d, want 150", finalResp.Usage.TotalTokens)
	}
}

func TestChatCompletionStream_NonOKStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
	}{
		{"bad request", http.StatusBadRequest},
		{"internal server error", http.StatusInternalServerError},
		{"unauthorized", http.StatusUnauthorized},
		{"rate limited", http.StatusTooManyRequests},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = fmt.Fprint(w, "error body")
			}))
			defer srv.Close()

			c := NewClient(srv.URL, "model")
			ch := c.ChatCompletionStream(context.Background(), []llm.Message{
				{Role: "user", Content: "test"},
			}, 0)

			results := collectStream(ch)

			if len(results) != 1 {
				t.Fatalf("expected 1 response, got %d", len(results))
			}
			if results[0].Error == nil {
				t.Fatal("expected error response")
			}
			if !strings.Contains(results[0].Error.Error(), fmt.Sprintf("unexpected status %d", tt.statusCode)) {
				t.Errorf("error = %q, want it to contain status %d", results[0].Error, tt.statusCode)
			}
		})
	}
}

func TestChatCompletionStream_MalformedJSON(t *testing.T) {
	t.Parallel()

	lines := []string{
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-bad",
			Model: "model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{Content: "ok"},
			}},
		}),
		"",
		"data: {this is not valid json!!!}",
		"",
		"data: [DONE]",
		"",
	}

	srv := sseServer(t, lines)
	defer srv.Close()

	c := NewClient(srv.URL, "model")
	ch := c.ChatCompletionStream(context.Background(), []llm.Message{
		{Role: "user", Content: "test"},
	}, 0)

	results := collectStream(ch)

	var gotError bool
	for _, r := range results {
		if r.Error != nil {
			gotError = true
			if !strings.Contains(r.Error.Error(), "parsing chunk") {
				t.Errorf("error = %q, want it to contain 'parsing chunk'", r.Error)
			}
		}
	}

	if !gotError {
		t.Error("expected an error from malformed JSON")
	}
}

func TestChatCompletionStream_ContextCancellation(t *testing.T) {
	t.Parallel()

	// Server that sends one chunk, then blocks until the request context is done.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		line := chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-ctx",
			Model: "model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{Content: "start"},
			}},
		})
		_, _ = fmt.Fprint(w, line+"\n\n")
		flusher.Flush()

		// Block until the client cancels.
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := NewClient(srv.URL, "model")
	ch := c.ChatCompletionStream(ctx, []llm.Message{
		{Role: "user", Content: "test"},
	}, 0)

	// Read the first content chunk.
	resp := <-ch
	if resp.Content != "start" {
		t.Fatalf("first chunk Content = %q, want %q", resp.Content, "start")
	}

	// Cancel the context.
	cancel()

	// The channel should close within a reasonable time.
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	var drained bool
	for !drained {
		select {
		case _, ok := <-ch:
			if !ok {
				drained = true
			}
		case <-timer.C:
			t.Fatal("timed out waiting for channel to close after context cancellation")
		}
	}
}

func TestChatCompletionStream_TemperatureSet(t *testing.T) {
	t.Parallel()

	// Verify that temperature > 0 is sent in the request body.
	var capturedBody llm.ChatRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "model")
	ch := c.ChatCompletionStream(context.Background(), []llm.Message{
		{Role: "user", Content: "test"},
	}, 0.7)

	collectStream(ch)

	if capturedBody.Temperature == nil {
		t.Fatal("expected temperature to be set")
	}
	if *capturedBody.Temperature != 0.7 {
		t.Errorf("temperature = %f, want 0.7", *capturedBody.Temperature)
	}
}

func TestChatCompletionStream_ZeroTemperatureOmitted(t *testing.T) {
	t.Parallel()

	var capturedBody llm.ChatRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "model")
	ch := c.ChatCompletionStream(context.Background(), []llm.Message{
		{Role: "user", Content: "test"},
	}, 0)

	collectStream(ch)

	if capturedBody.Temperature != nil {
		t.Errorf("expected temperature to be nil for 0, got %v", *capturedBody.Temperature)
	}
}

func TestChatCompletion_Success(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Verify request body.
		var reqBody struct {
			Model    string        `json:"model"`
			Messages []llm.Message `json:"messages"`
			Stream   bool          `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if reqBody.Stream {
			http.Error(w, "expected stream=false", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"content": "  Hello, world!  ",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-model")
	result, err := c.ChatCompletion(context.Background(), []llm.Message{
		{Role: "user", Content: "Hi"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Content should be trimmed.
	if result != "Hello, world!" {
		t.Errorf("result = %q, want %q", result, "Hello, world!")
	}
}

func TestChatCompletion_NonOKStatus(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, "internal error")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "model")
	_, err := c.ChatCompletion(context.Background(), []llm.Message{
		{Role: "user", Content: "test"},
	})

	if err == nil {
		t.Fatal("expected error for non-200 status")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Errorf("error = %q, want it to contain 'status 500'", err)
	}
}

func TestChatCompletion_NoChoices(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "model")
	_, err := c.ChatCompletion(context.Background(), []llm.Message{
		{Role: "user", Content: "test"},
	})

	if err == nil {
		t.Fatal("expected error for no choices")
	}
	if !strings.Contains(err.Error(), "no choices") {
		t.Errorf("error = %q, want it to contain 'no choices'", err)
	}
}

func TestChatCompletion_MalformedJSON(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "not json at all")
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "model")
	_, err := c.ChatCompletion(context.Background(), []llm.Message{
		{Role: "user", Content: "test"},
	})

	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "decoding response") {
		t.Errorf("error = %q, want it to contain 'decoding response'", err)
	}
}

func TestChatCompletion_ContextCancellation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until context is cancelled.
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	c := NewClient(srv.URL, "model")
	_, err := c.ChatCompletion(ctx, []llm.Message{
		{Role: "user", Content: "test"},
	})

	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestFetchModels_LMStudioEndpoint(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v0/models" {
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{
				"data": []map[string]any{
					{
						"id":                    "model-a",
						"state":                 "loaded",
						"max_context_length":    8192,
						"loaded_context_length": 4096,
					},
					{
						"id":                    "model-b",
						"state":                 "not-loaded",
						"max_context_length":    16384,
						"loaded_context_length": 0,
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	models, err := c.FetchModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}

	if models[0].ID != "model-a" {
		t.Errorf("models[0].ID = %q, want %q", models[0].ID, "model-a")
	}
	if models[0].State != "loaded" {
		t.Errorf("models[0].State = %q, want %q", models[0].State, "loaded")
	}
	if models[0].MaxContextLength != 8192 {
		t.Errorf("models[0].MaxContextLength = %d, want 8192", models[0].MaxContextLength)
	}
	if models[0].LoadedContextLength != 4096 {
		t.Errorf("models[0].LoadedContextLength = %d, want 4096", models[0].LoadedContextLength)
	}

	if models[1].ID != "model-b" {
		t.Errorf("models[1].ID = %q, want %q", models[1].ID, "model-b")
	}
	if models[1].State != "not-loaded" {
		t.Errorf("models[1].State = %q, want %q", models[1].State, "not-loaded")
	}
}

func TestFetchModels_FallbackToOpenAI(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/models":
			// LM Studio endpoint fails.
			w.WriteHeader(http.StatusNotFound)
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{
				"data": []map[string]any{
					{"id": "gpt-4"},
					{"id": "gpt-3.5-turbo"},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	models, err := c.FetchModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}

	if models[0].ID != "gpt-4" {
		t.Errorf("models[0].ID = %q, want %q", models[0].ID, "gpt-4")
	}
	if models[1].ID != "gpt-3.5-turbo" {
		t.Errorf("models[1].ID = %q, want %q", models[1].ID, "gpt-3.5-turbo")
	}

	// OpenAI fallback should not have rich metadata.
	if models[0].State != "" {
		t.Errorf("models[0].State = %q, want empty", models[0].State)
	}
}

func TestFetchModels_BothEndpointsFail(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.FetchModels(context.Background())
	if err == nil {
		t.Fatal("expected error when both endpoints fail")
	}
}

func TestChatCompletionStream_SSEWithoutSpace(t *testing.T) {
	t.Parallel()

	// Test that "data:{json}" (no space after colon) is handled.
	chunkJSON, _ := json.Marshal(llm.ChatCompletionChunk{
		ID:    "chatcmpl-nospace",
		Model: "model",
		Choices: []llm.Choice{{
			Delta: llm.Delta{Content: "works"},
		}},
	})

	lines := []string{
		"data:" + string(chunkJSON), // No space after "data:"
		"",
		"data:[DONE]", // No space after "data:"
		"",
	}

	srv := sseServer(t, lines)
	defer srv.Close()

	c := NewClient(srv.URL, "model")
	ch := c.ChatCompletionStream(context.Background(), []llm.Message{
		{Role: "user", Content: "test"},
	}, 0)

	results := collectStream(ch)

	var content string
	var gotDone bool
	for _, r := range results {
		if r.Error != nil {
			t.Fatalf("unexpected error: %v", r.Error)
		}
		content += r.Content
		if r.Done {
			gotDone = true
		}
	}

	if content != "works" {
		t.Errorf("content = %q, want %q", content, "works")
	}
	if !gotDone {
		t.Error("expected Done=true")
	}
}

func TestChatCompletionStream_NonDataLinesIgnored(t *testing.T) {
	t.Parallel()

	// SSE can include event:, id:, retry: lines — they should be ignored.
	lines := []string{
		"event: message",
		"id: 1",
		"retry: 5000",
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-ignore",
			Model: "model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{Content: "hello"},
			}},
		}),
		"",
		": this is a comment",
		"data: [DONE]",
		"",
	}

	srv := sseServer(t, lines)
	defer srv.Close()

	c := NewClient(srv.URL, "model")
	ch := c.ChatCompletionStream(context.Background(), []llm.Message{
		{Role: "user", Content: "test"},
	}, 0)

	results := collectStream(ch)

	var content string
	for _, r := range results {
		if r.Error != nil {
			t.Fatalf("unexpected error: %v", r.Error)
		}
		content += r.Content
	}

	if content != "hello" {
		t.Errorf("content = %q, want %q", content, "hello")
	}
}

func TestChatCompletionStreamWithTools_NoToolCallsWithoutToolsDefined(t *testing.T) {
	t.Parallel()

	// When streaming without tools, tool_calls in delta should NOT be accumulated
	// (hasTools is false because reqBody.Tools is empty).
	lines := []string{
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-notool",
			Model: "model",
			Choices: []llm.Choice{{
				Delta: llm.Delta{Content: "just text"},
			}},
		}),
		"",
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-notool",
			Model: "model",
			Choices: []llm.Choice{{
				Delta:        llm.Delta{},
				FinishReason: "stop",
			}},
		}),
		"",
		"data: [DONE]",
		"",
	}

	srv := sseServer(t, lines)
	defer srv.Close()

	c := NewClient(srv.URL, "model")
	// Use ChatCompletionStream (no tools).
	ch := c.ChatCompletionStream(context.Background(), []llm.Message{
		{Role: "user", Content: "test"},
	}, 0)

	results := collectStream(ch)

	for _, r := range results {
		if r.Error != nil {
			t.Fatalf("unexpected error: %v", r.Error)
		}
		if len(r.ToolCalls) > 0 {
			t.Error("expected no tool calls when tools are not defined")
		}
	}
}

func TestChatCompletionStream_ModelPropagation(t *testing.T) {
	t.Parallel()

	// Verify that the model from chunks is propagated to StreamResponse.
	lines := []string{
		chunk(llm.ChatCompletionChunk{
			ID:    "chatcmpl-model",
			Model: "specific-model-v2",
			Choices: []llm.Choice{{
				Delta: llm.Delta{Content: "hi"},
			}},
		}),
		"",
		"data: [DONE]",
		"",
	}

	srv := sseServer(t, lines)
	defer srv.Close()

	c := NewClient(srv.URL, "")
	ch := c.ChatCompletionStream(context.Background(), []llm.Message{
		{Role: "user", Content: "test"},
	}, 0)

	results := collectStream(ch)

	// Content chunk should have the model.
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	if results[0].Model != "specific-model-v2" {
		t.Errorf("content chunk Model = %q, want %q", results[0].Model, "specific-model-v2")
	}

	// Done chunk should also have the model (lastModel).
	doneResp := results[len(results)-1]
	if !doneResp.Done {
		t.Fatal("last response should be Done")
	}
	if doneResp.Model != "specific-model-v2" {
		t.Errorf("done chunk Model = %q, want %q", doneResp.Model, "specific-model-v2")
	}
}
