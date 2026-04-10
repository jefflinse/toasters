package cmd

import (
	"log/slog"

	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/provider"
)

// resolveOperatorProvider determines the LLM provider for the operator by
// looking up the configured provider ID in the registry. Returns (nil, nil)
// if no provider is configured or the configured provider is not found —
// the server will start with the operator disabled.
func resolveOperatorProvider(cfg *config.Config, registry *provider.Registry) (provider.Provider, error) {
	id := cfg.Operator.Provider
	if id == "" {
		// No operator provider configured.
		slog.Warn("no operator provider configured; operator will be disabled")
		return nil, nil
	}

	if p, ok := registry.Get(id); ok {
		return p, nil
	}

	slog.Warn("operator provider not found in registry; operator will be disabled",
		"provider", id, "available", registry.List())
	return nil, nil
}
