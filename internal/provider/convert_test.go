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

	"github.com/jefflinse/toasters/internal/llm"
)

func TestMessageFromLLM(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   llm.Message
		want Message
	}{
		{
			name: "simple user message",
			in:   llm.Message{Role: "user", Content: "hello"},
			want: Message{Role: "user", Content: "hello"},
		},
		{
			name: "assistant with tool calls",
			in: llm.Message{
				Role:    "assistant",
				Content: "Let me check.",
				ToolCalls: []llm.ToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: llm.ToolCallFunction{
						Name:      "read_file",
						Arguments: `{"path":"foo.txt"}`,
					},
				}},
			},
			want: Message{
				Role:    "assistant",
				Content: "Let me check.",
				ToolCalls: []ToolCall{{
					ID:        "call_1",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"path":"foo.txt"}`),
				}},
			},
		},
		{
			name: "tool result message",
			in:   llm.Message{Role: "tool", ToolCallID: "call_1", Content: "result"},
			want: Message{Role: "tool", ToolCallID: "call_1", Content: "result"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := MessageFromLLM(tt.in)
			if got.Role != tt.want.Role {
				t.Errorf("Role = %q, want %q", got.Role, tt.want.Role)
			}
			if got.Content != tt.want.Content {
				t.Errorf("Content = %q, want %q", got.Content, tt.want.Content)
			}
			if got.ToolCallID != tt.want.ToolCallID {
				t.Errorf("ToolCallID = %q, want %q", got.ToolCallID, tt.want.ToolCallID)
			}
			if len(got.ToolCalls) != len(tt.want.ToolCalls) {
				t.Fatalf("len(ToolCalls) = %d, want %d", len(got.ToolCalls), len(tt.want.ToolCalls))
			}
			for i := range got.ToolCalls {
				if got.ToolCalls[i].ID != tt.want.ToolCalls[i].ID {
					t.Errorf("ToolCalls[%d].ID = %q, want %q", i, got.ToolCalls[i].ID, tt.want.ToolCalls[i].ID)
				}
				if got.ToolCalls[i].Name != tt.want.ToolCalls[i].Name {
					t.Errorf("ToolCalls[%d].Name = %q, want %q", i, got.ToolCalls[i].Name, tt.want.ToolCalls[i].Name)
				}
			}
		})
	}
}

func TestMessageToLLM(t *testing.T) {
	t.Parallel()

	msg := Message{
		Role:    "assistant",
		Content: "text",
		ToolCalls: []ToolCall{{
			ID:        "call_1",
			Name:      "fn",
			Arguments: json.RawMessage(`{"x":1}`),
		}},
	}

	got := MessageToLLM(msg)
	if got.Role != "assistant" {
		t.Errorf("Role = %q, want assistant", got.Role)
	}
	if got.Content != "text" {
		t.Errorf("Content = %q, want text", got.Content)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(got.ToolCalls))
	}
	if got.ToolCalls[0].ID != "call_1" {
		t.Errorf("ToolCalls[0].ID = %q, want call_1", got.ToolCalls[0].ID)
	}
	if got.ToolCalls[0].Function.Name != "fn" {
		t.Errorf("ToolCalls[0].Function.Name = %q, want fn", got.ToolCalls[0].Function.Name)
	}
	if got.ToolCalls[0].Function.Arguments != `{"x":1}` {
		t.Errorf("ToolCalls[0].Function.Arguments = %q, want {\"x\":1}", got.ToolCalls[0].Function.Arguments)
	}
	if got.ToolCalls[0].Type != "function" {
		t.Errorf("ToolCalls[0].Type = %q, want function", got.ToolCalls[0].Type)
	}
}

func TestMessageRoundTrip(t *testing.T) {
	t.Parallel()

	original := llm.Message{
		Role:       "assistant",
		Content:    "hello",
		ToolCallID: "tc_1",
		ToolCalls: []llm.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "read_file",
				Arguments: `{"path":"test.txt"}`,
			},
		}},
	}

	converted := MessageFromLLM(original)
	roundTripped := MessageToLLM(converted)

	if roundTripped.Role != original.Role {
		t.Errorf("Role = %q, want %q", roundTripped.Role, original.Role)
	}
	if roundTripped.Content != original.Content {
		t.Errorf("Content = %q, want %q", roundTripped.Content, original.Content)
	}
	if roundTripped.ToolCallID != original.ToolCallID {
		t.Errorf("ToolCallID = %q, want %q", roundTripped.ToolCallID, original.ToolCallID)
	}
	if len(roundTripped.ToolCalls) != len(original.ToolCalls) {
		t.Fatalf("len(ToolCalls) = %d, want %d", len(roundTripped.ToolCalls), len(original.ToolCalls))
	}
	if roundTripped.ToolCalls[0].ID != original.ToolCalls[0].ID {
		t.Errorf("ToolCalls[0].ID = %q, want %q", roundTripped.ToolCalls[0].ID, original.ToolCalls[0].ID)
	}
	if roundTripped.ToolCalls[0].Function.Name != original.ToolCalls[0].Function.Name {
		t.Errorf("ToolCalls[0].Function.Name = %q, want %q", roundTripped.ToolCalls[0].Function.Name, original.ToolCalls[0].Function.Name)
	}
	if roundTripped.ToolCalls[0].Function.Arguments != original.ToolCalls[0].Function.Arguments {
		t.Errorf("ToolCalls[0].Function.Arguments = %q, want %q", roundTripped.ToolCalls[0].Function.Arguments, original.ToolCalls[0].Function.Arguments)
	}
}

func TestToolFromLLM(t *testing.T) {
	t.Parallel()

	in := llm.Tool{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "search",
			Description: "Search for things",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
				},
			},
		},
	}

	got := ToolFromLLM(in)
	if got.Name != "search" {
		t.Errorf("Name = %q, want search", got.Name)
	}
	if got.Description != "Search for things" {
		t.Errorf("Description = %q, want 'Search for things'", got.Description)
	}
	if got.Parameters == nil {
		t.Fatal("Parameters is nil")
	}

	// Verify parameters are valid JSON.
	var params map[string]any
	if err := json.Unmarshal(got.Parameters, &params); err != nil {
		t.Fatalf("Parameters unmarshal error: %v", err)
	}
	if params["type"] != "object" {
		t.Errorf("Parameters.type = %v, want object", params["type"])
	}
}

func TestToolToLLM(t *testing.T) {
	t.Parallel()

	in := Tool{
		Name:        "read_file",
		Description: "Read a file",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	}

	got := ToolToLLM(in)
	if got.Type != "function" {
		t.Errorf("Type = %q, want function", got.Type)
	}
	if got.Function.Name != "read_file" {
		t.Errorf("Function.Name = %q, want read_file", got.Function.Name)
	}
	if got.Function.Description != "Read a file" {
		t.Errorf("Function.Description = %q, want 'Read a file'", got.Function.Description)
	}
	if got.Function.Parameters == nil {
		t.Fatal("Function.Parameters is nil")
	}

	params, ok := got.Function.Parameters.(map[string]any)
	if !ok {
		t.Fatalf("Function.Parameters is %T, want map[string]any", got.Function.Parameters)
	}
	if params["type"] != "object" {
		t.Errorf("Parameters.type = %v, want object", params["type"])
	}
}

func TestToolRoundTrip(t *testing.T) {
	t.Parallel()

	original := llm.Tool{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "write_file",
			Description: "Write a file",
			Parameters:  map[string]any{"type": "object"},
		},
	}

	converted := ToolFromLLM(original)
	roundTripped := ToolToLLM(converted)

	if roundTripped.Type != "function" {
		t.Errorf("Type = %q, want function", roundTripped.Type)
	}
	if roundTripped.Function.Name != original.Function.Name {
		t.Errorf("Function.Name = %q, want %q", roundTripped.Function.Name, original.Function.Name)
	}
	if roundTripped.Function.Description != original.Function.Description {
		t.Errorf("Function.Description = %q, want %q", roundTripped.Function.Description, original.Function.Description)
	}
}

func TestToolCallFromLLM(t *testing.T) {
	t.Parallel()

	in := llm.ToolCall{
		ID:   "call_123",
		Type: "function",
		Function: llm.ToolCallFunction{
			Name:      "read_file",
			Arguments: `{"path":"test.txt"}`,
		},
	}

	got := ToolCallFromLLM(in)
	if got.ID != "call_123" {
		t.Errorf("ID = %q, want call_123", got.ID)
	}
	if got.Name != "read_file" {
		t.Errorf("Name = %q, want read_file", got.Name)
	}
	if string(got.Arguments) != `{"path":"test.txt"}` {
		t.Errorf("Arguments = %q, want {\"path\":\"test.txt\"}", string(got.Arguments))
	}
}

func TestToolCallToLLM(t *testing.T) {
	t.Parallel()

	in := ToolCall{
		ID:        "call_456",
		Name:      "write_file",
		Arguments: json.RawMessage(`{"path":"out.txt","content":"hello"}`),
	}

	got := ToolCallToLLM(in)
	if got.ID != "call_456" {
		t.Errorf("ID = %q, want call_456", got.ID)
	}
	if got.Type != "function" {
		t.Errorf("Type = %q, want function", got.Type)
	}
	if got.Function.Name != "write_file" {
		t.Errorf("Function.Name = %q, want write_file", got.Function.Name)
	}
	if got.Function.Arguments != `{"path":"out.txt","content":"hello"}` {
		t.Errorf("Function.Arguments = %q", got.Function.Arguments)
	}
}

func TestToolCallRoundTrip(t *testing.T) {
	t.Parallel()

	original := llm.ToolCall{
		ID:   "call_rt",
		Type: "function",
		Function: llm.ToolCallFunction{
			Name:      "search",
			Arguments: `{"query":"test"}`,
		},
	}

	converted := ToolCallFromLLM(original)
	roundTripped := ToolCallToLLM(converted)

	if roundTripped.ID != original.ID {
		t.Errorf("ID = %q, want %q", roundTripped.ID, original.ID)
	}
	if roundTripped.Function.Name != original.Function.Name {
		t.Errorf("Function.Name = %q, want %q", roundTripped.Function.Name, original.Function.Name)
	}
	if roundTripped.Function.Arguments != original.Function.Arguments {
		t.Errorf("Function.Arguments = %q, want %q", roundTripped.Function.Arguments, original.Function.Arguments)
	}
}

func TestToolFromLLM_NilParameters(t *testing.T) {
	t.Parallel()

	in := llm.Tool{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "no_params",
			Description: "A tool with no parameters",
			Parameters:  nil,
		},
	}

	got := ToolFromLLM(in)
	if got.Parameters != nil {
		t.Errorf("Parameters = %v, want nil", got.Parameters)
	}
}

func TestToolToLLM_EmptyParameters(t *testing.T) {
	t.Parallel()

	in := Tool{
		Name:        "no_params",
		Description: "A tool with no parameters",
		Parameters:  nil,
	}

	got := ToolToLLM(in)
	if got.Function.Parameters != nil {
		t.Errorf("Function.Parameters = %v, want nil", got.Function.Parameters)
	}
}

// TestLLMProviderAdapter_InterfaceCheck verifies compile-time interface satisfaction.
func TestLLMProviderAdapter_InterfaceCheck(t *testing.T) {
	t.Parallel()
	// This is a compile-time check via the var _ line in convert.go.
	// This test just documents that it exists.
	var _ llm.Provider = (*LLMProviderAdapter)(nil)
}

func TestLLMProviderAdapter_BaseURL(t *testing.T) {
	t.Parallel()

	p := NewOpenAI("test", "http://localhost:1234", "", "model")
	adapter := NewLLMProviderAdapter(p, "http://localhost:1234")

	if got := adapter.BaseURL(); got != "http://localhost:1234" {
		t.Errorf("BaseURL() = %q, want %q", got, "http://localhost:1234")
	}
}

func TestLLMProviderAdapter_ChatCompletionStream(t *testing.T) {
	t.Parallel()

	lines := []string{
		makeOpenAIChunk(openAIChunk{
			Choices: []openAIChoice{{
				Delta: openAIDelta{Content: "Hello"},
			}},
		}),
		"",
		makeOpenAIChunk(openAIChunk{
			Choices: []openAIChoice{{
				Delta: openAIDelta{Content: " world"},
			}},
		}),
		"",
		makeOpenAIChunk(openAIChunk{
			Usage: &openAIUsage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
		}),
		"",
		"data: [DONE]",
		"",
	}

	srv := openAISSEServer(t, lines)
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "", "model")
	adapter := NewLLMProviderAdapter(p, srv.URL)

	ch := adapter.ChatCompletionStream(context.Background(), []llm.Message{
		{Role: "user", Content: "Hi"},
	}, 0)

	var content strings.Builder
	var gotDone bool
	var finalUsage *llm.Usage

	for resp := range ch {
		if resp.Error != nil {
			t.Fatalf("unexpected error: %v", resp.Error)
		}
		content.WriteString(resp.Content)
		if resp.Done {
			gotDone = true
			finalUsage = resp.Usage
		}
	}

	if got := content.String(); got != "Hello world" {
		t.Errorf("content = %q, want %q", got, "Hello world")
	}
	if !gotDone {
		t.Error("never received Done=true")
	}
	if finalUsage == nil {
		t.Fatal("expected usage on final response")
	}
	if finalUsage.PromptTokens != 5 {
		t.Errorf("PromptTokens = %d, want 5", finalUsage.PromptTokens)
	}
}

func TestLLMProviderAdapter_ChatCompletionStreamWithTools(t *testing.T) {
	t.Parallel()

	lines := []string{
		makeOpenAIChunk(openAIChunk{
			Choices: []openAIChoice{{
				Delta: openAIDelta{
					ToolCalls: []openAIToolCall{{
						Index: 0,
						ID:    "call_1",
						Type:  "function",
						Function: openAIToolCallFunction{
							Name:      "read_file",
							Arguments: `{"path":"test.txt"}`,
						},
					}},
				},
			}},
		}),
		"",
		makeOpenAIChunk(openAIChunk{
			Choices: []openAIChoice{{
				Delta:        openAIDelta{},
				FinishReason: "tool_calls",
			}},
		}),
		"",
		"data: [DONE]",
		"",
	}

	srv := openAISSEServer(t, lines)
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "", "model")
	adapter := NewLLMProviderAdapter(p, srv.URL)

	tools := []llm.Tool{{
		Type: "function",
		Function: llm.ToolFunction{
			Name:        "read_file",
			Description: "Read a file",
			Parameters:  map[string]any{"type": "object"},
		},
	}}

	ch := adapter.ChatCompletionStreamWithTools(context.Background(), []llm.Message{
		{Role: "user", Content: "Read test.txt"},
	}, tools, 0)

	var gotToolCalls bool
	for resp := range ch {
		if resp.Error != nil {
			t.Fatalf("unexpected error: %v", resp.Error)
		}
		if resp.Done && len(resp.ToolCalls) > 0 {
			gotToolCalls = true
			if resp.ToolCalls[0].Function.Name != "read_file" {
				t.Errorf("ToolCalls[0].Function.Name = %q, want read_file", resp.ToolCalls[0].Function.Name)
			}
		}
	}

	if !gotToolCalls {
		t.Error("expected tool calls in response")
	}
}

func TestLLMProviderAdapter_ChatCompletion(t *testing.T) {
	t.Parallel()

	lines := []string{
		makeOpenAIChunk(openAIChunk{
			Choices: []openAIChoice{{
				Delta: openAIDelta{Content: "  Hello, world!  "},
			}},
		}),
		"",
		"data: [DONE]",
		"",
	}

	srv := openAISSEServer(t, lines)
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "", "model")
	adapter := NewLLMProviderAdapter(p, srv.URL)

	result, err := adapter.ChatCompletion(context.Background(), []llm.Message{
		{Role: "user", Content: "Hi"},
	})
	if err != nil {
		t.Fatalf("ChatCompletion error: %v", err)
	}

	if result != "Hello, world!" {
		t.Errorf("result = %q, want %q", result, "Hello, world!")
	}
}

func TestLLMProviderAdapter_ChatCompletion_Error(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, "server error")
	}))
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "", "model")
	adapter := NewLLMProviderAdapter(p, srv.URL)

	_, err := adapter.ChatCompletion(context.Background(), []llm.Message{
		{Role: "user", Content: "test"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLLMProviderAdapter_FetchModels(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": "model-a"},
					{"id": "model-b"},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "", "")
	adapter := NewLLMProviderAdapter(p, srv.URL)

	models, err := adapter.FetchModels(context.Background())
	if err != nil {
		t.Fatalf("FetchModels error: %v", err)
	}

	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "model-a" {
		t.Errorf("models[0].ID = %q, want model-a", models[0].ID)
	}
}

func TestLLMProviderAdapter_SystemMessageExtraction(t *testing.T) {
	t.Parallel()

	var capturedBody openAIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "", "model")
	adapter := NewLLMProviderAdapter(p, srv.URL)

	ch := adapter.ChatCompletionStream(context.Background(), []llm.Message{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "Hi"},
	}, 0)

	for range ch {
	}

	// The adapter should extract the system message and pass it as the System field.
	// The OpenAI provider then converts it back to a system message in the request.
	if len(capturedBody.Messages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(capturedBody.Messages))
	}
	if capturedBody.Messages[0].Role != "system" {
		t.Errorf("first message role = %q, want system", capturedBody.Messages[0].Role)
	}
}
