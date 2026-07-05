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
)

// TestOpenAIProvider_Embed_ReturnsInInputOrder verifies that Embed reorders
// the response's data[] entries by their index field rather than assuming
// server response order matches request order.
func TestOpenAIProvider_Embed_ReturnsInInputOrder(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Respond with index 1 before index 0 — out of input order.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 1, "embedding": []float32{4, 5, 6}},
				{"index": 0, "embedding": []float32{1, 2, 3}},
			},
		})
	}))
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "", "embed-model")
	vecs, err := p.Embed(context.Background(), "", []string{"first", "second"})
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(vecs))
	}
	if got, want := vecs[0], []float32{1, 2, 3}; !floatsEqual(got, want) {
		t.Errorf("vecs[0] = %v, want %v", got, want)
	}
	if got, want := vecs[1], []float32{4, 5, 6}; !floatsEqual(got, want) {
		t.Errorf("vecs[1] = %v, want %v", got, want)
	}
}

func floatsEqual(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestOpenAIProvider_Embed_SendsFloatEncoding verifies the request body
// carries encoding_format=float explicitly (some OpenAI-compatible servers
// default to base64-encoded floats otherwise) along with the right model and
// input.
func TestOpenAIProvider_Embed_SendsFloatEncoding(t *testing.T) {
	t.Parallel()

	var captured openAIEmbeddingsRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"encoding_format":"float"`) {
			t.Errorf("request body missing encoding_format:float, got %s", body)
		}
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 0, "embedding": []float32{0.1, 0.2}},
			},
		})
	}))
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "", "default-embed-model")
	_, err := p.Embed(context.Background(), "explicit-model", []string{"hello"})
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}

	if captured.Model != "explicit-model" {
		t.Errorf("Model = %q, want explicit-model", captured.Model)
	}
	if len(captured.Input) != 1 || captured.Input[0] != "hello" {
		t.Errorf("Input = %v, want [hello]", captured.Input)
	}
	if captured.EncodingFormat != "float" {
		t.Errorf("EncodingFormat = %q, want float", captured.EncodingFormat)
	}
}

// TestOpenAIProvider_Embed_DefaultModel verifies an empty model argument
// falls back to the provider's configured default model.
func TestOpenAIProvider_Embed_DefaultModel(t *testing.T) {
	t.Parallel()

	var captured openAIEmbeddingsRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"index": 0, "embedding": []float32{1}}},
		})
	}))
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "", "default-embed-model")
	_, err := p.Embed(context.Background(), "", []string{"hi"})
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if captured.Model != "default-embed-model" {
		t.Errorf("Model = %q, want default-embed-model", captured.Model)
	}
}

// TestOpenAIProvider_Embed_Non200 verifies a non-200 response surfaces as an
// error including the status code.
func TestOpenAIProvider_Embed_Non200(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, "boom")
	}))
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "", "model")
	_, err := p.Embed(context.Background(), "model", []string{"hi"})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want it to mention status 500", err)
	}
}

// TestOpenAIProvider_Embed_MismatchedCount verifies a response with fewer
// embeddings than inputs errors instead of silently returning nils.
func TestOpenAIProvider_Embed_MismatchedCount(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 0, "embedding": []float32{1, 2}},
			},
		})
	}))
	defer srv.Close()

	p := NewOpenAI("test", srv.URL, "", "model")
	_, err := p.Embed(context.Background(), "model", []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error when response has fewer embeddings than inputs")
	}
}

// TestEmbeddingsURL mirrors TestModelsURL / TestChatCompletionsURL: same
// endpoint-normalization heuristics, different suffix.
func TestEmbeddingsURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		endpoint string
		want     string
	}{
		{"bare host", "http://localhost:1234", "http://localhost:1234/v1/embeddings"},
		{"trailing slash trimmed", "http://localhost:1234/", "http://localhost:1234/v1/embeddings"},
		{"endpoint already has /v1", "http://localhost:1234/v1", "http://localhost:1234/v1/embeddings"},
		{"z.ai custom version path", "https://api.z.ai/api/coding/paas/v4", "https://api.z.ai/api/coding/paas/v4/embeddings"},
		{"already /embeddings passed through", "https://example.com/v1/embeddings", "https://example.com/v1/embeddings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := embeddingsURL(tt.endpoint)
			if got != tt.want {
				t.Errorf("embeddingsURL(%q) = %q, want %q", tt.endpoint, got, tt.want)
			}
		})
	}
}

// fakeChatOnlyProvider implements Provider but not EmbeddingProvider.
type fakeChatOnlyProvider struct{}

func (fakeChatOnlyProvider) Name() string { return "fake-chat-only" }
func (fakeChatOnlyProvider) ChatStream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent)
	close(ch)
	return ch, nil
}
func (fakeChatOnlyProvider) Models(ctx context.Context) ([]ModelInfo, error) {
	return nil, nil
}

var _ Provider = fakeChatOnlyProvider{}

// TestScheduler_Embed_UnsupportedInner verifies wrapping a chat-only Provider
// returns a clean error rather than panicking on the type assertion.
func TestScheduler_Embed_UnsupportedInner(t *testing.T) {
	t.Parallel()

	s := NewScheduler(fakeChatOnlyProvider{}, 1)
	_, err := s.Embed(context.Background(), "model", []string{"hi"})
	if err == nil {
		t.Fatal("expected error for provider without embedding support")
	}
	if !strings.Contains(err.Error(), "does not support embeddings") {
		t.Errorf("error = %q, want it to mention unsupported embeddings", err)
	}
}

// TestScheduler_Embed_ProxiesToInner verifies the Scheduler forwards Embed to
// an inner EmbeddingProvider and returns its vectors unchanged.
func TestScheduler_Embed_ProxiesToInner(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 0, "embedding": []float32{7, 8, 9}},
			},
		})
	}))
	defer srv.Close()

	inner := NewOpenAI("test", srv.URL, "", "embed-model")
	s := NewScheduler(inner, 1)

	vecs, err := s.Embed(context.Background(), "embed-model", []string{"hi"})
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if len(vecs) != 1 || !floatsEqual(vecs[0], []float32{7, 8, 9}) {
		t.Errorf("vecs = %v, want [[7 8 9]]", vecs)
	}
}
