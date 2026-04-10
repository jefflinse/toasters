package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ProviderEntry is the data needed to add a provider to config.yaml.
type ProviderEntry struct {
	ID       string // required, unique
	Name     string // display name
	Type     string // "openai", "local", "anthropic"
	Endpoint string // API endpoint URL
	APIKey   string // API key or ${ENV_VAR} reference
}

// AddProvider appends a provider entry to config.yaml, preserving existing
// structure and comments. Returns an error if a provider with the same ID
// already exists.
func AddProvider(configDir string, entry ProviderEntry) error {
	configPath := filepath.Join(configDir, "config.yaml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}

	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return fmt.Errorf("config.yaml has unexpected structure")
	}
	rootMap := root.Content[0]
	if rootMap.Kind != yaml.MappingNode {
		return fmt.Errorf("config.yaml root is not a mapping")
	}

	// Find or create the providers sequence.
	providersNode := mappingValue(rootMap, "providers")
	if providersNode == nil {
		// No providers key — add one.
		providersNode = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		rootMap.Content = append(rootMap.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "providers"},
			providersNode,
		)
	}
	if providersNode.Kind != yaml.SequenceNode {
		return fmt.Errorf("providers key is not a sequence")
	}

	// Check for duplicate ID.
	for _, item := range providersNode.Content {
		if item.Kind == yaml.MappingNode {
			idNode := mappingValue(item, "id")
			if idNode != nil && idNode.Value == entry.ID {
				return fmt.Errorf("provider %q already exists in config", entry.ID)
			}
		}
	}

	// Build the new provider mapping node.
	provNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}

	appendScalar(provNode, "id", entry.ID)
	appendScalar(provNode, "name", entry.Name)
	appendScalar(provNode, "type", entry.Type)

	if entry.Endpoint != "" {
		appendScalar(provNode, "endpoint", entry.Endpoint)
	}
	if entry.APIKey != "" {
		appendScalar(provNode, "api_key", entry.APIKey)
	}

	providersNode.Content = append(providersNode.Content, provNode)

	out, err := yaml.Marshal(&root)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.WriteFile(configPath, out, 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}

// mappingValue returns the value node for a given key in a mapping node, or nil.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// appendScalar adds a key-value pair to a mapping node.
func appendScalar(node *yaml.Node, key, value string) {
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value},
	)
}
