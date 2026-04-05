package agentfmt

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// claudeModelAliases maps Claude Code short model aliases to full model IDs.
var claudeModelAliases = map[string]string{
	"sonnet": "claude-sonnet-4-20250514",
	"opus":   "claude-opus-4-20250514",
	"haiku":  "claude-haiku-3-5-20241022",
}

// ImportClaudeCode converts Claude Code agent frontmatter into an AgentDef.
// It reads camelCase fields from the raw YAML and maps them to the Toasters
// snake_case equivalents. Model aliases (sonnet, opus, haiku) are expanded to
// full model IDs with provider set to "anthropic".
func ImportClaudeCode(fmYAML string, body string, defaultName string) (*AgentDef, error) {
	var raw map[string]any
	if fmYAML != "" {
		if err := yaml.Unmarshal([]byte(fmYAML), &raw); err != nil {
			return nil, fmt.Errorf("unmarshaling claude code frontmatter: %w", err)
		}
	}
	if raw == nil {
		raw = make(map[string]any)
	}

	def := &AgentDef{}

	// Identity fields.
	def.Name = mapString(raw, "name")
	if def.Name == "" {
		def.Name = defaultName
	}
	def.Description = mapString(raw, "description")

	// Model + provider.
	model := mapString(raw, "model")
	def.Model, def.Provider = resolveClaudeModel(model)

	// Behavior.
	def.Mode = mapString(raw, "mode")
	def.MaxTurns = mapInt(raw, "maxTurns")
	def.Temperature = mapFloat64Ptr(raw, "temperature")
	def.TopP = mapFloat64Ptr(raw, "topP")

	// Tools.
	def.Tools = mapStringSlice(raw, "tools")
	def.DisallowedTools = mapStringSlice(raw, "disallowedTools")

	// Permissions.
	def.PermissionMode = mapString(raw, "permissionMode")

	// MCP servers — preserve as-is (list or map).
	if v, ok := raw["mcpServers"]; ok {
		def.MCPServers = v
	}

	// Model options.
	def.ModelOptions = mapStringAnyMap(raw, "modelOptions")

	// Pass-through fields.
	def.Memory = mapString(raw, "memory")
	def.Hooks = mapStringAnyMap(raw, "hooks")
	def.Background = mapBool(raw, "background")
	def.Isolation = mapString(raw, "isolation")

	// UI/Display.
	def.Color = NormalizeColor(mapString(raw, "color"))

	def.Body = body
	return def, nil
}

// resolveClaudeModel expands Claude Code model aliases and determines the
// provider. Returns (model, provider).
func resolveClaudeModel(model string) (string, string) {
	if model == "" {
		return "", ""
	}

	// Check short aliases first.
	if fullID, ok := claudeModelAliases[model]; ok {
		return fullID, "anthropic"
	}

	// Any model containing "claude" → anthropic provider.
	if strings.Contains(strings.ToLower(model), "claude") {
		return model, "anthropic"
	}

	// Other models — keep as-is, no provider inference.
	return model, ""
}

// mapString extracts a string value from a map, returning "" if missing or wrong type.
func mapString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// mapInt extracts an int value from a map. YAML numbers may unmarshal as int or
// float64, so both are handled.
func mapInt(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}

// mapFloat64Ptr extracts a *float64 from a map. Returns nil if the key is absent.
func mapFloat64Ptr(m map[string]any, key string) *float64 {
	v, ok := m[key]
	if !ok {
		return nil
	}
	switch n := v.(type) {
	case float64:
		return &n
	case int:
		f := float64(n)
		return &f
	default:
		return nil
	}
}

// mapBool extracts a bool value from a map.
func mapBool(m map[string]any, key string) bool {
	v, ok := m[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	if !ok {
		return false
	}
	return b
}

// mapStringSlice extracts a []string from a map. YAML lists unmarshal as []any,
// so each element is asserted to string.
func mapStringSlice(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok {
		return nil
	}
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// mapStringAnyMap extracts a map[string]any from a map.
func mapStringAnyMap(m map[string]any, key string) map[string]any {
	v, ok := m[key]
	if !ok {
		return nil
	}
	result, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return result
}
