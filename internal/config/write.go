package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ProviderEntry is the data needed to write a provider YAML file.
type ProviderEntry struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	Type     string `yaml:"type"`
	Endpoint string `yaml:"endpoint,omitempty"`
	APIKey   string `yaml:"api_key,omitempty"`
}

// AddProvider writes a provider YAML file to the providers/ directory.
// The filename is derived from the ID. Returns an error if a file for
// this provider already exists.
func AddProvider(configDir string, entry ProviderEntry) error {
	providersDir := filepath.Join(configDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		return fmt.Errorf("creating providers dir: %w", err)
	}

	filename := entry.ID + ".yaml"
	path := filepath.Join(providersDir, filename)

	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("provider %q already exists at %s", entry.ID, path)
	}

	data, err := yaml.Marshal(&entry)
	if err != nil {
		return fmt.Errorf("marshaling provider: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing provider file: %w", err)
	}

	return nil
}
