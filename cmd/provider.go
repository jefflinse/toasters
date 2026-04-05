package cmd

import (
	"fmt"

	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/provider"
)

// resolveOperatorProvider determines the LLM provider for the operator by
// looking up the configured provider ID in the registry.
func resolveOperatorProvider(cfg *config.Config, registry *provider.Registry) (provider.Provider, error) {
	id := cfg.Operator.Provider
	if id == "" {
		id = "lm-studio"
	}

	if p, ok := registry.Get(id); ok {
		return p, nil
	}

	return nil, fmt.Errorf("operator provider %q not found in registry (available: %v)", id, registry.List())
}
