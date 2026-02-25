package provider

import (
	"fmt"
	"os"
	"sort"
	"sync"
)

// ProviderConfig defines configuration for a single provider.
type ProviderConfig struct {
	Name     string `yaml:"name" mapstructure:"name"`
	Type     string `yaml:"type" mapstructure:"type"` // "openai" or "anthropic"
	Endpoint string `yaml:"endpoint" mapstructure:"endpoint"`
	APIKey   string `yaml:"api_key" mapstructure:"api_key"`
	Model    string `yaml:"model" mapstructure:"model"`
}

// Registry maps provider names to Provider instances.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewRegistry creates an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

// Register adds a provider to the registry. If a provider with the same name
// already exists, it is replaced.
func (r *Registry) Register(name string, p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[name] = p
}

// Get returns the provider with the given name, or false if not found.
func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// List returns the names of all registered providers, sorted alphabetically.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// NewFromConfig creates a Provider from configuration.
// Supports ${ENV_VAR} expansion in the APIKey field.
func NewFromConfig(cfg ProviderConfig) (Provider, error) {
	apiKey := os.Expand(cfg.APIKey, os.Getenv)

	switch cfg.Type {
	case "openai":
		if cfg.Endpoint == "" {
			return nil, fmt.Errorf("openai provider %q requires an endpoint", cfg.Name)
		}
		return NewOpenAI(cfg.Name, cfg.Endpoint, apiKey, cfg.Model), nil

	case "anthropic":
		opts := []AnthropicOption{}
		if cfg.Endpoint != "" {
			opts = append(opts, WithAnthropicBaseURL(cfg.Endpoint))
		}
		return NewAnthropic(cfg.Name, apiKey, opts...), nil

	default:
		return nil, fmt.Errorf("unknown provider type %q for provider %q", cfg.Type, cfg.Name)
	}
}
