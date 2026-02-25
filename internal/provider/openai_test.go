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

// openAISSEServer creates a test server that responds with SSE lines.
func openAISSEServer(t *testing.T, lines []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
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
		for _, line := range lines {
			_, _ = fmt.Fprint(w, line+"\n")
			flusher.Flush()
		}
	}))
}

// openAIChunk builds a JSON SSE data line from an openAIChunk struct.
func makeOpenAIChunk(c openAIChunk) string {
	b, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	return "data: " + string(b)
}

// collectEvents drains a StreamEvent channel into a slice.
func collectEvents(ch <-chan StreamEvent) []StreamEvent {
	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	return events
}

func TestOpenAI_Name(t *testing.T) {
	t.Parallel()
	p := NewOpenAI("lmstudio", "http://localhost:1234", "", "model")
	if p.Name() != "lmstudio" {
		t.Errorf("Name() = %q, want %q", p.Name(), "lmstudio")
	}
}

func TestOpenAI_ChatStream_TextStreaming(t *testing.T) {
	t.Parallel()

	lines := []string{
		makeOpenAIChunk(openAIChunk{
			ID:    "chatcmpl-1",
			Model: "test-model",
			Choices: []openAIChoice{{
				Delta: openAIDelta{Content: "Hello"},
			}},
		}),
		"",
		makeOpenAIChunk(openAIChunk{
			ID:    "chatcmpl-1",
			Model: "test-model",
			Choices: []openAIChoice{{
				Delta: openAIDelta{Content: " world"},
			}},
		}),
		"",
		makeOpenAIChunk(openAIChunk{
			ID:    "chatcmpl-1",
			Model: "test-model",
			Choices: []openAIChoice{{
				Delta:        openAIDelta{},
				FinishReason: "stop",
			}},
		}),
		"",
		makeOpenAIChunk(openAIChunk{
			ID:    "chatcmpl-1",
			Model: "test-model",
			Usage: &openAIUsage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
		}),
		"",
		"data: [DONE]",
		"",
	}

	srv := openAISSEServer(t, lines)
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "", "test-model")
	ch, err := p.ChatStream(context.Background(), ChatRequest{
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

func TestOpenAI_ChatStream_ToolCalls(t *testing.T) {
	t.Parallel()

	lines := []string{
		makeOpenAIChunk(openAIChunk{
			ID:    "chatcmpl-2",
			Model: "tool-model",
			Choices: []openAIChoice{{
				Delta: openAIDelta{
					ToolCalls: []openAIToolCall{{
						Index: 0,
						ID:    "call_abc",
						Type:  "function",
						Function: openAIToolCallFunction{
							Name:      "read_file",
							Arguments: "",
						},
					}},
				},
			}},
		}),
		"",
		makeOpenAIChunk(openAIChunk{
			ID:    "chatcmpl-2",
			Model: "tool-model",
			Choices: []openAIChoice{{
				Delta: openAIDelta{
					ToolCalls: []openAIToolCall{{
						Index:    0,
						Function: openAIToolCallFunction{Arguments: `{"path":`},
					}},
				},
			}},
		}),
		"",
		makeOpenAIChunk(openAIChunk{
			ID:    "chatcmpl-2",
			Model: "tool-model",
			Choices: []openAIChoice{{
				Delta: openAIDelta{
					ToolCalls: []openAIToolCall{{
						Index:    0,
						Function: openAIToolCallFunction{Arguments: `"foo.txt"}`},
					}},
				},
			}},
		}),
		"",
		makeOpenAIChunk(openAIChunk{
			ID:    "chatcmpl-2",
			Model: "tool-model",
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

	p := NewOpenAI("test", srv.URL, "", "tool-model")
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "Read foo.txt"}},
		Tools: []Tool{{
			Name:        "read_file",
			Description: "Read a file",
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
	if tc.ID != "call_abc" {
		t.Errorf("ToolCall.ID = %q, want %q", tc.ID, "call_abc")
	}
	if tc.Name != "read_file" {
		t.Errorf("ToolCall.Name = %q, want %q", tc.Name, "read_file")
	}
	wantArgs := `{"path":"foo.txt"}`
	if string(tc.Arguments) != wantArgs {
		t.Errorf("ToolCall.Arguments = %q, want %q", string(tc.Arguments), wantArgs)
	}
}

func TestOpenAI_ChatStream_MultipleToolCalls(t *testing.T) {
	t.Parallel()

	lines := []string{
		makeOpenAIChunk(openAIChunk{
			Choices: []openAIChoice{{
				Delta: openAIDelta{
					ToolCalls: []openAIToolCall{
						{Index: 0, ID: "call_a", Type: "function", Function: openAIToolCallFunction{Name: "tool_a"}},
					},
				},
			}},
		}),
		"",
		makeOpenAIChunk(openAIChunk{
			Choices: []openAIChoice{{
				Delta: openAIDelta{
					ToolCalls: []openAIToolCall{
						{Index: 1, ID: "call_b", Type: "function", Function: openAIToolCallFunction{Name: "tool_b"}},
					},
				},
			}},
		}),
		"",
		makeOpenAIChunk(openAIChunk{
			Choices: []openAIChoice{{
				Delta: openAIDelta{
					ToolCalls: []openAIToolCall{
						{Index: 0, Function: openAIToolCallFunction{Arguments: `{"x":1}`}},
					},
				},
			}},
		}),
		"",
		makeOpenAIChunk(openAIChunk{
			Choices: []openAIChoice{{
				Delta: openAIDelta{
					ToolCalls: []openAIToolCall{
						{Index: 1, Function: openAIToolCallFunction{Arguments: `{"y":2}`}},
					},
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

	p := NewOpenAI("test", srv.URL, "", "")
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "Do stuff"}},
		Tools: []Tool{
			{Name: "tool_a"},
			{Name: "tool_b"},
		},
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

func TestOpenAI_ChatStream_ErrorStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
	}{
		{"bad request", http.StatusBadRequest},
		{"internal server error", http.StatusInternalServerError},
		{"unauthorized", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = fmt.Fprint(w, "error body")
			}))
			defer srv.Close()

			p := NewOpenAI("test", srv.URL, "", "model")
			ch, err := p.ChatStream(context.Background(), ChatRequest{
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
			if !strings.Contains(events[0].Error.Error(), fmt.Sprintf("unexpected status %d", tt.status)) {
				t.Errorf("error = %q, want it to contain status %d", events[0].Error, tt.status)
			}
		})
	}
}

func TestOpenAI_ChatStream_ContextCancellation(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		line := makeOpenAIChunk(openAIChunk{
			Choices: []openAIChoice{{
				Delta: openAIDelta{Content: "start"},
			}},
		})
		_, _ = fmt.Fprint(w, line+"\n\n")
		flusher.Flush()

		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	p := NewOpenAI("test", srv.URL, "", "model")
	ch, err := p.ChatStream(ctx, ChatRequest{
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	// Read first event.
	ev := <-ch
	if ev.Type != EventText || ev.Text != "start" {
		t.Fatalf("first event = %+v, want text 'start'", ev)
	}

	cancel()

	// Channel should close within a reasonable time.
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // success
			}
		case <-timer.C:
			t.Fatal("timed out waiting for channel to close")
		}
	}
}

func TestOpenAI_ChatStream_MalformedJSON(t *testing.T) {
	t.Parallel()

	lines := []string{
		makeOpenAIChunk(openAIChunk{
			Choices: []openAIChoice{{
				Delta: openAIDelta{Content: "ok"},
			}},
		}),
		"",
		"data: {this is not valid json!!!}",
		"",
	}

	srv := openAISSEServer(t, lines)
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "", "model")
	ch, err := p.ChatStream(context.Background(), ChatRequest{
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
			if !strings.Contains(ev.Error.Error(), "parsing chunk") {
				t.Errorf("error = %q, want it to contain 'parsing chunk'", ev.Error)
			}
		}
	}
	if !gotError {
		t.Error("expected an error event from malformed JSON")
	}
}

func TestOpenAI_ChatStream_APIKeyHeader(t *testing.T) {
	t.Parallel()

	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "sk-test-key", "model")
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	collectEvents(ch)

	if capturedAuth != "Bearer sk-test-key" {
		t.Errorf("Authorization = %q, want %q", capturedAuth, "Bearer sk-test-key")
	}
}

func TestOpenAI_ChatStream_SystemPrompt(t *testing.T) {
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
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		System:   "You are helpful.",
		Messages: []Message{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	collectEvents(ch)

	if len(capturedBody.Messages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(capturedBody.Messages))
	}
	if capturedBody.Messages[0].Role != "system" {
		t.Errorf("first message role = %q, want system", capturedBody.Messages[0].Role)
	}
	if capturedBody.Messages[0].Content != "You are helpful." {
		t.Errorf("system content = %q, want 'You are helpful.'", capturedBody.Messages[0].Content)
	}
}

func TestOpenAI_ChatStream_Temperature(t *testing.T) {
	t.Parallel()

	var capturedBody openAIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	temp := 0.7
	p := NewOpenAI("test", srv.URL, "", "model")
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Messages:    []Message{{Role: "user", Content: "test"}},
		Temperature: &temp,
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	collectEvents(ch)

	if capturedBody.Temperature == nil {
		t.Fatal("expected temperature to be set")
	}
	if *capturedBody.Temperature != 0.7 {
		t.Errorf("temperature = %f, want 0.7", *capturedBody.Temperature)
	}
}

func TestOpenAI_ChatStream_DefaultModel(t *testing.T) {
	t.Parallel()

	var capturedBody openAIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "", "default-model")
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	collectEvents(ch)

	if capturedBody.Model != "default-model" {
		t.Errorf("model = %q, want %q", capturedBody.Model, "default-model")
	}
}

func TestOpenAI_ChatStream_ModelOverride(t *testing.T) {
	t.Parallel()

	var capturedBody openAIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "", "default-model")
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Model:    "override-model",
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	collectEvents(ch)

	if capturedBody.Model != "override-model" {
		t.Errorf("model = %q, want %q", capturedBody.Model, "override-model")
	}
}

func TestOpenAI_ChatStream_WithoutDONE(t *testing.T) {
	t.Parallel()

	lines := []string{
		makeOpenAIChunk(openAIChunk{
			Choices: []openAIChoice{{
				Delta: openAIDelta{Content: "partial"},
			}},
		}),
		"",
		makeOpenAIChunk(openAIChunk{
			Choices: []openAIChoice{{
				Delta:        openAIDelta{},
				FinishReason: "stop",
			}},
		}),
		"",
	}

	srv := openAISSEServer(t, lines)
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "", "model")
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	events := collectEvents(ch)

	var gotDone bool
	for _, ev := range events {
		if ev.Type == EventDone {
			gotDone = true
		}
		if ev.Type == EventError {
			t.Fatalf("unexpected error: %v", ev.Error)
		}
	}
	if !gotDone {
		t.Error("expected EventDone when stream ends without [DONE]")
	}
}

func TestOpenAI_Models_LMStudioEndpoint(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v0/models" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": "model-a"},
					{"id": "model-b"},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := NewOpenAI("lmstudio", srv.URL, "", "")
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models error: %v", err)
	}

	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "model-a" {
		t.Errorf("models[0].ID = %q, want model-a", models[0].ID)
	}
	if models[0].Provider != "lmstudio" {
		t.Errorf("models[0].Provider = %q, want lmstudio", models[0].Provider)
	}
}

func TestOpenAI_Models_FallbackToOpenAI(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/models":
			w.WriteHeader(http.StatusNotFound)
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": "gpt-4"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := NewOpenAI("openai", srv.URL, "", "")
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models error: %v", err)
	}

	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].ID != "gpt-4" {
		t.Errorf("models[0].ID = %q, want gpt-4", models[0].ID)
	}
}

func TestOpenAI_Models_BothFail(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "", "")
	_, err := p.Models(context.Background())
	if err == nil {
		t.Fatal("expected error when both endpoints fail")
	}
}

func TestOpenAI_ChatStream_SSEWithoutSpace(t *testing.T) {
	t.Parallel()

	chunkJSON, _ := json.Marshal(openAIChunk{
		Choices: []openAIChoice{{
			Delta: openAIDelta{Content: "works"},
		}},
	})

	lines := []string{
		"data:" + string(chunkJSON),
		"",
		"data:[DONE]",
		"",
	}

	srv := openAISSEServer(t, lines)
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "", "model")
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}

	events := collectEvents(ch)

	var text string
	for _, ev := range events {
		if ev.Type == EventText {
			text += ev.Text
		}
		if ev.Type == EventError {
			t.Fatalf("unexpected error: %v", ev.Error)
		}
	}
	if text != "works" {
		t.Errorf("text = %q, want %q", text, "works")
	}
}
