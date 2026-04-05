package cmd

import (
	"testing"

	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/provider"
)

func TestResolveOperatorProvider_FromRegistry(t *testing.T) {
	registry := provider.NewRegistry()
	registeredProvider := provider.NewOpenAI("my-provider", "http://localhost:1234", "", "test-model")
	registry.Register("my-provider", registeredProvider)

	cfg := &config.Config{
		Operator: config.OperatorConfig{
			Provider: "my-provider",
		},
	}

	p, err := resolveOperatorProvider(cfg, registry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != registeredProvider {
		t.Error("expected to get the registered provider back")
	}
	if p.Name() != "my-provider" {
		t.Errorf("Name() = %q, want %q", p.Name(), "my-provider")
	}
}

func TestResolveOperatorProvider_RegistryMiss_ReturnsError(t *testing.T) {
	registry := provider.NewRegistry()

	cfg := &config.Config{
		Operator: config.OperatorConfig{
			Provider: "nonexistent",
			Model:    "test-model",
		},
	}

	p, err := resolveOperatorProvider(cfg, registry)
	if err == nil {
		t.Fatal("expected error for missing provider, got nil")
	}
	if p != nil {
		t.Error("expected nil provider on error")
	}
}

func TestResolveOperatorProvider_DefaultLMStudio(t *testing.T) {
	registry := provider.NewRegistry()
	registeredProvider := provider.NewOpenAI("lm-studio", "http://localhost:1234", "", "my-model")
	registry.Register("lm-studio", registeredProvider)

	// When Operator.Provider is empty, it should default to "lm-studio".
	cfg := &config.Config{
		Operator: config.OperatorConfig{
			Provider: "",
		},
	}

	p, err := resolveOperatorProvider(cfg, registry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != registeredProvider {
		t.Error("expected to get the lm-studio provider back when provider is empty")
	}
	if p.Name() != "lm-studio" {
		t.Errorf("Name() = %q, want %q", p.Name(), "lm-studio")
	}
}
