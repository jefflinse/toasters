package provider

import (
	"strings"
	"testing"
)

func TestRegistry_RegisterAndGet(t *testing.T) {
	t.Parallel()

	r := NewRegistry()

	p1 := NewOpenAI("openai", "http://localhost:1234", "", "model")
	p2 := NewAnthropic("anthropic", "key")

	r.Register("openai", p1)
	r.Register("anthropic", p2)

	got, ok := r.Get("openai")
	if !ok {
		t.Fatal("expected to find 'openai' provider")
	}
	if got.Name() != "openai" {
		t.Errorf("Name() = %q, want openai", got.Name())
	}

	got, ok = r.Get("anthropic")
	if !ok {
		t.Fatal("expected to find 'anthropic' provider")
	}
	if got.Name() != "anthropic" {
		t.Errorf("Name() = %q, want anthropic", got.Name())
	}

	_, ok = r.Get("nonexistent")
	if ok {
		t.Error("expected 'nonexistent' to not be found")
	}
}

func TestRegistry_RegisterOverwrite(t *testing.T) {
	t.Parallel()

	r := NewRegistry()

	p1 := NewOpenAI("v1", "http://localhost:1234", "", "model-a")
	p2 := NewOpenAI("v2", "http://localhost:5678", "", "model-b")

	r.Register("provider", p1)
	r.Register("provider", p2)

	got, ok := r.Get("provider")
	if !ok {
		t.Fatal("expected to find 'provider'")
	}
	if got.Name() != "v2" {
		t.Errorf("Name() = %q, want v2 (overwritten)", got.Name())
	}
}

func TestRegistry_List(t *testing.T) {
	t.Parallel()

	r := NewRegistry()

	// Empty registry.
	if names := r.List(); len(names) != 0 {
		t.Errorf("expected empty list, got %v", names)
	}

	r.Register("charlie", NewOpenAI("charlie", "http://c", "", ""))
	r.Register("alpha", NewOpenAI("alpha", "http://a", "", ""))
	r.Register("bravo", NewOpenAI("bravo", "http://b", "", ""))

	names := r.List()
	if len(names) != 3 {
		t.Fatalf("expected 3 names, got %d", len(names))
	}

	// Should be sorted alphabetically.
	expected := []string{"alpha", "bravo", "charlie"}
	for i, name := range names {
		if name != expected[i] {
			t.Errorf("names[%d] = %q, want %q", i, name, expected[i])
		}
	}
}

func TestProviderConfig_Key(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  ProviderConfig
		want string
	}{
		{
			name: "ID takes precedence over Name",
			cfg:  ProviderConfig{ID: "my-id", Name: "My Name"},
			want: "my-id",
		},
		{
			name: "Name used when ID is empty",
			cfg:  ProviderConfig{Name: "My Name"},
			want: "My Name",
		},
		{
			name: "ID used when Name is empty",
			cfg:  ProviderConfig{ID: "my-id"},
			want: "my-id",
		},
		{
			name: "both empty returns empty",
			cfg:  ProviderConfig{},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.Key()
			if got != tt.want {
				t.Errorf("Key() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewFromConfig_OpenAI(t *testing.T) {
	t.Parallel()

	cfg := ProviderConfig{
		Name:     "lmstudio",
		Type:     "openai",
		Endpoint: "http://localhost:1234",
		APIKey:   "test-key",
		Model:    "test-model",
	}

	p, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig error: %v", err)
	}

	if p.Name() != "lmstudio" {
		t.Errorf("Name() = %q, want lmstudio", p.Name())
	}

	openai, ok := p.(*OpenAIProvider)
	if !ok {
		t.Fatalf("expected *OpenAIProvider, got %T", p)
	}
	if openai.defaultModel != "test-model" {
		t.Errorf("defaultModel = %q, want test-model", openai.defaultModel)
	}
}

func TestNewFromConfig_Anthropic(t *testing.T) {
	t.Parallel()

	cfg := ProviderConfig{
		Name:   "anthropic",
		Type:   "anthropic",
		APIKey: "sk-ant-test",
	}

	p, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig error: %v", err)
	}

	if p.Name() != "anthropic" {
		t.Errorf("Name() = %q, want anthropic", p.Name())
	}

	anth, ok := p.(*AnthropicProvider)
	if !ok {
		t.Fatalf("expected *AnthropicProvider, got %T", p)
	}
	if anth.apiKey != "sk-ant-test" {
		t.Errorf("apiKey = %q, want sk-ant-test", anth.apiKey)
	}
}

func TestNewFromConfig_AnthropicWithModel(t *testing.T) {
	t.Parallel()

	cfg := ProviderConfig{
		Name:   "anthropic",
		Type:   "anthropic",
		APIKey: "sk-ant-test",
		Model:  "claude-3-opus",
	}

	p, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig error: %v", err)
	}

	anth, ok := p.(*AnthropicProvider)
	if !ok {
		t.Fatalf("expected *AnthropicProvider, got %T", p)
	}
	if anth.defaultModel != "claude-3-opus" {
		t.Errorf("defaultModel = %q, want claude-3-opus", anth.defaultModel)
	}
}

func TestNewFromConfig_AnthropicWithEndpoint(t *testing.T) {
	t.Parallel()

	cfg := ProviderConfig{
		Name:     "anthropic-proxy",
		Type:     "anthropic",
		Endpoint: "https://proxy.example.com",
		APIKey:   "key",
	}

	p, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig error: %v", err)
	}

	anth, ok := p.(*AnthropicProvider)
	if !ok {
		t.Fatalf("expected *AnthropicProvider, got %T", p)
	}
	if anth.baseURL != "https://proxy.example.com" {
		t.Errorf("baseURL = %q, want https://proxy.example.com", anth.baseURL)
	}
}

func TestNewFromConfig_UnknownType(t *testing.T) {
	t.Parallel()

	cfg := ProviderConfig{
		Name: "bad",
		Type: "unknown",
	}

	_, err := NewFromConfig(cfg)
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
	if got := err.Error(); !strings.Contains(got, "unknown provider type") {
		t.Errorf("error = %q, want it to contain 'unknown provider type'", got)
	}
}

func TestNewFromConfig_OpenAIMissingEndpoint(t *testing.T) {
	t.Parallel()

	cfg := ProviderConfig{
		Name: "bad",
		Type: "openai",
	}

	_, err := NewFromConfig(cfg)
	if err == nil {
		t.Fatal("expected error for missing endpoint")
	}
	if got := err.Error(); !strings.Contains(got, "requires an endpoint") {
		t.Errorf("error = %q, want it to contain 'requires an endpoint'", got)
	}
}

func TestNewFromConfig_EnvVarExpansion(t *testing.T) {
	// Env var expansion is done by config.Load(), not NewFromConfig.
	// NewFromConfig uses the API key as-is (already expanded by config.Load).
	t.Setenv("TEST_PROVIDER_KEY", "expanded-key-value")

	cfg := ProviderConfig{
		Name:   "test",
		Type:   "anthropic",
		APIKey: "expanded-key-value", // simulates post-expansion value from config.Load()
	}

	p, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig error: %v", err)
	}

	anth, ok := p.(*AnthropicProvider)
	if !ok {
		t.Fatalf("expected *AnthropicProvider, got %T", p)
	}
	if anth.apiKey != "expanded-key-value" {
		t.Errorf("apiKey = %q, want expanded-key-value", anth.apiKey)
	}
}

func TestNewFromConfig_EnvVarExpansion_Unset(t *testing.T) {
	t.Parallel()

	// After config.Load() expansion, an unset env var becomes "".
	cfg := ProviderConfig{
		Name:   "test",
		Type:   "anthropic",
		APIKey: "", // simulates post-expansion value for unset env var
	}

	p, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig error: %v", err)
	}

	anth := p.(*AnthropicProvider)
	if anth.apiKey != "" {
		t.Errorf("apiKey = %q, want empty (unset env var)", anth.apiKey)
	}
}

func TestNewFromConfig_LiteralEnvVarNotExpanded(t *testing.T) {
	// Verify that NewFromConfig does NOT expand ${VAR} — expansion is config.Load()'s job.
	t.Setenv("LITERAL_VAR_TEST", "should-not-appear")

	cfg := ProviderConfig{
		Name:   "test",
		Type:   "anthropic",
		APIKey: "${LITERAL_VAR_TEST}",
	}

	p, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig error: %v", err)
	}

	anth := p.(*AnthropicProvider)
	if anth.apiKey != "${LITERAL_VAR_TEST}" {
		t.Errorf("apiKey = %q, want literal ${LITERAL_VAR_TEST} (no expansion)", anth.apiKey)
	}
}

func TestNewFromConfig_Local(t *testing.T) {
	t.Parallel()

	cfg := ProviderConfig{
		Name: "Local",
		Type: "local",
	}

	p, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig error: %v", err)
	}

	if p.Name() != "Local" {
		t.Errorf("Name() = %q, want Local", p.Name())
	}

	openai, ok := p.(*OpenAIProvider)
	if !ok {
		t.Fatalf("expected *OpenAIProvider, got %T", p)
	}
	if openai.endpoint != "http://localhost:1234" {
		t.Errorf("endpoint = %q, want http://localhost:1234", openai.endpoint)
	}
	if openai.apiKey != "" {
		t.Errorf("apiKey = %q, want empty (local providers never use API keys)", openai.apiKey)
	}
}

func TestNewFromConfig_LocalCustomEndpoint(t *testing.T) {
	t.Parallel()

	cfg := ProviderConfig{
		Name:     "MyLocal",
		Type:     "local",
		Endpoint: "http://custom:9999",
	}

	p, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig error: %v", err)
	}

	openai, ok := p.(*OpenAIProvider)
	if !ok {
		t.Fatalf("expected *OpenAIProvider, got %T", p)
	}
	if openai.endpoint != "http://custom:9999" {
		t.Errorf("endpoint = %q, want http://custom:9999", openai.endpoint)
	}
}

func TestNewFromConfig_LocalAPIKeyIgnored(t *testing.T) {
	t.Parallel()

	cfg := ProviderConfig{
		Name:   "Local",
		Type:   "local",
		APIKey: "this-should-be-ignored",
	}

	p, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig error: %v", err)
	}

	openai, ok := p.(*OpenAIProvider)
	if !ok {
		t.Fatalf("expected *OpenAIProvider, got %T", p)
	}
	if openai.apiKey != "" {
		t.Errorf("apiKey = %q, want empty (local providers ignore API keys)", openai.apiKey)
	}
}

func TestNewFromConfig_LocalWithModel(t *testing.T) {
	t.Parallel()

	cfg := ProviderConfig{
		Name:  "Local",
		Type:  "local",
		Model: "llama-3.2",
	}

	p, err := NewFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFromConfig error: %v", err)
	}

	openai, ok := p.(*OpenAIProvider)
	if !ok {
		t.Fatalf("expected *OpenAIProvider, got %T", p)
	}
	if openai.defaultModel != "llama-3.2" {
		t.Errorf("defaultModel = %q, want llama-3.2", openai.defaultModel)
	}
}
