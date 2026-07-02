package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

// validProviderID constrains provider IDs to filename-safe characters so the
// YAML path derived from an ID cannot escape the providers directory. IDs
// arrive from HTTP request bodies, so this is a security boundary.
var validProviderID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// ValidateProviderID returns an error if id is unsafe to use as a provider
// filename stem.
func ValidateProviderID(id string) error {
	if !validProviderID.MatchString(id) {
		return fmt.Errorf("invalid provider ID %q: must start with an alphanumeric character and contain only alphanumeric characters, dots, underscores, and hyphens", id)
	}
	return nil
}

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
	if err := ValidateProviderID(entry.ID); err != nil {
		return err
	}
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

// SetTopLevelValue updates (or creates) a top-level scalar key in config.yaml,
// preserving unrelated content. Unlike SetTopLevelScalar, the value may be
// any YAML-encodable scalar (bool, int, float, string), and the type tag is
// inferred so that round-tripping through viper preserves the field's Go
// type (a bool stays a bool, a float stays a float).
func SetTopLevelValue(configDir, key string, value any) error {
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

	if err := setMappingValueAny(rootMap, key, value); err != nil {
		return fmt.Errorf("encoding %s: %w", key, err)
	}

	out, err := yaml.Marshal(&root)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.WriteFile(configPath, out, 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}

// SetTopLevelScalar updates (or creates) a top-level scalar key in config.yaml,
// preserving unrelated content. Intended for simple runtime-editable settings
// like coarse_granularity and fine_granularity. The value is not validated
// here — callers should normalize first.
func SetTopLevelScalar(configDir, key, value string) error {
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

	setMappingValue(rootMap, key, value)

	out, err := yaml.Marshal(&root)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.WriteFile(configPath, out, 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}

// SetOperatorProvider updates the operator.provider field in config.yaml.
func SetOperatorProvider(configDir, providerID, model string) error {
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

	// Find or create the operator mapping.
	opNode := mappingValue(rootMap, "operator")
	if opNode == nil {
		opNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		rootMap.Content = append(rootMap.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "operator"},
			opNode,
		)
	}

	// Set the provider and model fields.
	setMappingValue(opNode, "provider", providerID)
	setMappingValue(opNode, "model", model)

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

// setMappingValueAny sets or adds a key-value pair in a mapping node, encoding
// value as the appropriate YAML scalar (bool, int, float, string). Returns an
// error if value can't be encoded.
func setMappingValueAny(node *yaml.Node, key string, value any) error {
	var encoded yaml.Node
	if err := encoded.Encode(value); err != nil {
		return err
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1] = &encoded
			return nil
		}
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&encoded,
	)
	return nil
}

// setMappingValue sets or adds a key-value pair in a mapping node.
func setMappingValue(node *yaml.Node, key, value string) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1].Value = value
			node.Content[i+1].Tag = "!!str"
			return
		}
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value},
	)
}

// UpdateProvider updates an existing provider YAML file in place, setting the
// entry's fields and preserving any keys the entry doesn't carry (model,
// concurrency, context_window, ...) so hand-edited values survive API-driven
// edits. Empty Endpoint/APIKey remove those keys, matching the previous
// whole-file-overwrite semantics. If the file doesn't exist, it is created
// (upsert behavior).
func UpdateProvider(configDir string, entry ProviderEntry) error {
	if err := ValidateProviderID(entry.ID); err != nil {
		return err
	}
	providersDir := filepath.Join(configDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		return fmt.Errorf("creating providers dir: %w", err)
	}

	filename := entry.ID + ".yaml"
	path := filepath.Join(providersDir, filename)

	root := &yaml.Node{
		Kind:    yaml.DocumentNode,
		Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}},
	}
	if data, err := os.ReadFile(path); err == nil {
		var existing yaml.Node
		if err := yaml.Unmarshal(data, &existing); err != nil {
			return fmt.Errorf("parsing provider file: %w", err)
		}
		if existing.Kind == yaml.DocumentNode && len(existing.Content) > 0 &&
			existing.Content[0].Kind == yaml.MappingNode {
			root = &existing
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading provider file: %w", err)
	}
	rootMap := root.Content[0]

	setMappingValue(rootMap, "id", entry.ID)
	setMappingValue(rootMap, "name", entry.Name)
	setMappingValue(rootMap, "type", entry.Type)
	for key, val := range map[string]string{"endpoint": entry.Endpoint, "api_key": entry.APIKey} {
		if val == "" {
			removeMappingKey(rootMap, key)
		} else {
			setMappingValue(rootMap, key, val)
		}
	}

	out, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("marshaling provider: %w", err)
	}

	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("writing provider file: %w", err)
	}

	return nil
}

// removeMappingKey deletes a key-value pair from a mapping node if present.
func removeMappingKey(node *yaml.Node, key string) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}
