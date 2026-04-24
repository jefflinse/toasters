package prompt

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Schema is a declarative output contract referenced by roles. Authors write
// these as YAML files under schemas/; roles reference one by name via their
// `output:` frontmatter field. The engine converts a Schema into JSON Schema
// at agent-run time so the provider's structured-output path enforces the
// shape of a node's terminal output.
type Schema struct {
	Name        string              `yaml:"name"`
	Description string              `yaml:"description"`
	Fields      map[string]FieldDef `yaml:"fields"`
}

// FieldDef describes a single field in a Schema. Simple fields only need
// Type + Description + Required. For arrays, set Type: "array" and declare
// Items. For nested objects, set Type: "object" and declare Properties.
type FieldDef struct {
	Type        string              `yaml:"type"`
	Description string              `yaml:"description"`
	Required    bool                `yaml:"required"`
	Items       *FieldDef           `yaml:"items,omitempty"`
	Properties  map[string]FieldDef `yaml:"properties,omitempty"`
}

// DefaultSchemaName is the schema used when a role omits `output:`. The
// loader is expected to have a schema registered under this name; when
// absent, the engine falls back to a minimal one-field summary schema so
// basic graphs still run.
const DefaultSchemaName = "summary"

// Schema returns the named schema, or nil if it is not loaded.
func (e *Engine) Schema(name string) *Schema {
	return e.schemas[name]
}

// Schemas returns all loaded schema names, sorted.
func (e *Engine) Schemas() []string {
	names := make([]string, 0, len(e.schemas))
	for name := range e.schemas {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SchemaJSON returns the JSON Schema bytes for the named schema, ready to
// hand to mycelium agent.Config.OutputSchema. Returns an error when the
// schema is not loaded.
func (e *Engine) SchemaJSON(name string) (json.RawMessage, error) {
	s := e.schemas[name]
	if s == nil {
		return nil, fmt.Errorf("schema %q not found", name)
	}
	return s.ToJSONSchema()
}

// ToJSONSchema converts a Schema into a JSON Schema document. Preserves
// field descriptions so the provider can forward them to the model as tool
// argument documentation.
func (s *Schema) ToJSONSchema() (json.RawMessage, error) {
	if len(s.Fields) == 0 {
		return nil, fmt.Errorf("schema %q has no fields", s.Name)
	}
	props, required := fieldsToProperties(s.Fields)
	doc := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		doc["required"] = required
	}
	return json.Marshal(doc)
}

// fieldsToProperties converts a Schema.Fields map into a JSON Schema
// properties object plus the required-name list. Handles nested objects
// (via Properties) and arrays (via Items) recursively.
func fieldsToProperties(fields map[string]FieldDef) (map[string]any, []string) {
	props := make(map[string]any, len(fields))
	var required []string
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		props[name] = fieldToSchema(fields[name])
		if fields[name].Required {
			required = append(required, name)
		}
	}
	return props, required
}

// fieldToSchema produces the JSON Schema fragment for a single field.
func fieldToSchema(f FieldDef) map[string]any {
	t := jsonType(f.Type)
	entry := map[string]any{"type": t}
	if f.Description != "" {
		entry["description"] = strings.TrimSpace(f.Description)
	}
	switch t {
	case "array":
		if f.Items != nil {
			entry["items"] = fieldToSchema(*f.Items)
		}
	case "object":
		if len(f.Properties) > 0 {
			nestedProps, nestedReq := fieldsToProperties(f.Properties)
			entry["properties"] = nestedProps
			if len(nestedReq) > 0 {
				entry["required"] = nestedReq
			}
		}
	}
	return entry
}

// jsonType normalizes short type names (bool, int, …) to their JSON Schema
// equivalents. Unknown types pass through verbatim, which lets authors use
// raw JSON Schema types like "integer" directly.
func jsonType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "bool", "boolean":
		return "boolean"
	case "int", "integer":
		return "integer"
	case "number", "float", "double":
		return "number"
	case "string", "text":
		return "string"
	case "array", "list":
		return "array"
	case "object", "map":
		return "object"
	case "":
		return "string"
	default:
		return t
	}
}

// loadSchemas loads all .yaml / .yml files from the schemas directory. The
// filename (sans extension) is the fallback key when no `name:` is set in
// the file.
func (e *Engine) loadSchemas(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("skipping unreadable schema file", "path", path, "error", err)
			continue
		}
		s := &Schema{}
		if err := yaml.Unmarshal(data, s); err != nil {
			slog.Warn("skipping unparseable schema file", "path", path, "error", err)
			continue
		}
		key := s.Name
		if key == "" {
			key = strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml")
			s.Name = key
		}
		e.schemas[key] = s
	}
	return nil
}
