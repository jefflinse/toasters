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

func TestIsLocalEndpoint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		endpoint string
		want     bool
	}{
		{"http://127.0.0.1:1234/v1", true},
		{"http://localhost:1234/v1", true},
		{"http://0.0.0.0:8080", true},
		{"http://[::1]:1234/v1", true},
		{"http://192.168.1.50:1234/v1", true},
		{"http://10.0.0.5:11434", true},
		{"http://my-rig.local:1234/v1", true},
		{"https://api.openai.com/v1", false},
		{"https://api.z.ai/api/coding/paas/v4", false},
		{"https://api.anthropic.com", false},
	}
	for _, c := range cases {
		if got := isLocalEndpoint(c.endpoint); got != c.want {
			t.Errorf("isLocalEndpoint(%q) = %v, want %v", c.endpoint, got, c.want)
		}
	}
}

// TestOpenAI_LocalSamplingDefaults verifies a local endpoint gets the
// anti-degeneration sampler fields (repetition penalty + DRY) in the body.
func TestOpenAI_LocalSamplingDefaults(t *testing.T) {
	t.Parallel()

	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &raw)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	// httptest serves on 127.0.0.1 → treated as local.
	p := NewOpenAI("local", srv.URL, "", "model")
	ch, err := p.ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream error: %v", err)
	}
	collectEvents(ch)

	for _, key := range []string{"repeat_penalty", "repeat_last_n", "dry_multiplier", "dry_base", "dry_allowed_length", "dry_penalty_last_n"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("local request body missing sampler field %q", key)
		}
	}
}

// TestOpenAI_CloudHasNoSamplerFields verifies a cloud endpoint sends none of the
// llama.cpp-only fields, which strict APIs would reject.
func TestOpenAI_CloudHasNoSamplerFields(t *testing.T) {
	t.Parallel()

	p := NewOpenAI("cloud", "https://api.openai.com/v1", "key", "gpt-x")
	body := openAIRequest{Model: "gpt-x"}
	body.samplingParams = p.sampling

	out, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"repeat_penalty", "dry_multiplier", "dry_base", "dry_allowed_length", "repeat_last_n", "dry_penalty_last_n"} {
		if strings.Contains(string(out), key) {
			t.Errorf("cloud request body should not contain %q; got %s", key, out)
		}
	}
}
