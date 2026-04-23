package provider

import (
	"fmt"
	"slices"
	"sync"
)

// ProviderConfig defines configuration for a single provider.
type ProviderConfig struct {
	ID       string `yaml:"id" mapstructure:"id"`
	Name     string `yaml:"name" mapstructure:"name"`
	Type     string `yaml:"type" mapstructure:"type"` // "openai", "local", or "anthropic"
	Endpoint string `yaml:"endpoint" mapstructure:"endpoint"`
	APIKey   string `yaml:"api_key" mapstructure:"api_key"`
	Model    string `yaml:"model" mapstructure:"model"`

	// Concurrency caps in-flight chat calls against this provider. Zero
	// means "use the default" (1 — safe for a local LLM). Configure
	// higher for cloud providers that can serve parallel requests.
	// Isolating the operator from workers is done by configuring a
	// separate ProviderConfig with a distinct ID (so it gets its own
	// scheduler) rather than sharing this one.
	Concurrency int `yaml:"concurrency" mapstructure:"concurrency"`
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
	slices.Sort(names)
	return names
}

// Key returns the registry key for this provider config: ID if set, else Name.
func (c ProviderConfig) Key() string {
	if c.ID != "" {
		return c.ID
	}
	return c.Name
}

// NewFromConfig creates a Provider from configuration.
// API key expansion (${ENV_VAR}) is handled by config.Load(), not here.
func NewFromConfig(cfg ProviderConfig) (Provider, error) {
	// API key already expanded by config.Load().
	apiKey := cfg.APIKey

	switch cfg.Type {
	case "openai":
		if cfg.Endpoint == "" {
			return nil, fmt.Errorf("openai provider %q requires an endpoint", cfg.Key())
		}
		return NewOpenAI(cfg.Name, cfg.Endpoint, apiKey, cfg.Model), nil

	// "local" is sugar for an OpenAI-compatible local server (e.g. LM Studio, Ollama).
	// It defaults to http://localhost:1234 and never uses an API key.
	case "local":
		endpoint := cfg.Endpoint
		if endpoint == "" {
			endpoint = "http://localhost:1234"
		}
		return NewOpenAI(cfg.Name, endpoint, "", cfg.Model), nil

	case "anthropic":
		opts := []AnthropicOption{}
		if cfg.Endpoint != "" {
			opts = append(opts, WithAnthropicBaseURL(cfg.Endpoint))
		}
		if cfg.Model != "" {
			opts = append(opts, WithAnthropicModel(cfg.Model))
		}
		return NewAnthropic(cfg.Name, apiKey, opts...), nil

	default:
		return nil, fmt.Errorf("unknown provider type %q for provider %q", cfg.Type, cfg.Key())
	}
}
