package agentfmt

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ImportOpenCode converts OpenCode agent frontmatter into an AgentDef.
// It reads OpenCode-specific fields (steps, disable, permission) and maps them
// to Toasters equivalents. Provider/model strings in "provider/model" format
// are split into separate fields.
func ImportOpenCode(fmYAML string, body string, defaultName string) (*AgentDef, error) {
	var raw map[string]any
	if fmYAML != "" {
		if err := yaml.Unmarshal([]byte(fmYAML), &raw); err != nil {
			return nil, fmt.Errorf("unmarshaling opencode frontmatter: %w", err)
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

	// Provider/model — OpenCode may use "provider/model" combined format.
	provider := mapString(raw, "provider")
	model := mapString(raw, "model")
	def.Provider, def.Model = resolveOpenCodeProviderModel(provider, model)

	// Behavior: "steps" → max_turns.
	def.MaxTurns = mapInt(raw, "steps")
	def.Temperature = mapFloat64Ptr(raw, "temperature")
	def.TopP = mapFloat64Ptr(raw, "top_p")

	// Mode — OpenCode uses "subagent"/"primary"; pass through as-is since
	// Toasters' internal/agents package also uses "primary" for coordinators.
	def.Mode = mapString(raw, "mode")

	// Tools — OpenCode supports both a list of enabled tool names and a map of
	// tool_name→bool to selectively enable/disable tools.
	//   List form:  tools: [read_file, bash]  → Tools
	//   Map form:   tools: {write: false}      → false entries → DisallowedTools
	def.Tools, def.DisallowedTools = resolveOpenCodeTools(raw)

	// "disable" (bool) → disabled.
	def.Disabled = mapBool(raw, "disable")

	// "permission" (singular) → permissions map.
	def.Permissions = resolveOpenCodePermission(raw)

	// UI/Display.
	def.Color = NormalizeColor(mapString(raw, "color"))
	def.Hidden = mapBool(raw, "hidden")

	// Collect model_options from explicit field or unrecognized keys.
	def.ModelOptions = collectOpenCodeModelOptions(raw)

	def.Body = body
	return def, nil
}

// resolveOpenCodeTools handles OpenCode's two forms of the "tools" field:
//   - List form: tools: [read_file, bash] → all entries go into the enabled tools list
//   - Map form:  tools: {write: false, edit: false} → true entries → Tools, false entries → DisallowedTools
//
// Returns (tools, disallowedTools).
func resolveOpenCodeTools(raw map[string]any) ([]string, []string) {
	v, ok := raw["tools"]
	if !ok {
		return nil, nil
	}

	switch t := v.(type) {
	case []any:
		// List form — same as mapStringSlice.
		result := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		if len(result) == 0 {
			return nil, nil
		}
		return result, nil

	case map[string]any:
		// Map form — split by bool value.
		var enabled, disabled []string
		for name, val := range t {
			b, ok := val.(bool)
			if !ok {
				continue
			}
			if b {
				enabled = append(enabled, name)
			} else {
				disabled = append(disabled, name)
			}
		}
		if len(enabled) == 0 {
			enabled = nil
		}
		if len(disabled) == 0 {
			disabled = nil
		}
		return enabled, disabled

	default:
		return nil, nil
	}
}

// resolveOpenCodeProviderModel handles OpenCode's combined "provider/model"
// format. If provider contains a "/", it's split into provider and model
// (the model part overrides any separate model field). If provider has no "/",
// both are returned as-is.
func resolveOpenCodeProviderModel(provider, model string) (string, string) {
	if provider == "" {
		return "", model
	}

	if idx := strings.Index(provider, "/"); idx >= 0 {
		p := provider[:idx]
		m := provider[idx+1:]
		// The combined format takes precedence over a separate model field.
		if m != "" {
			return p, m
		}
		return p, model
	}

	return provider, model
}

// resolveOpenCodePermission converts OpenCode's singular "permission" field
// into a Toasters permissions map.
//   - string value → {"_mode": value}
//   - map value → used as-is
//   - absent/other → nil
func resolveOpenCodePermission(raw map[string]any) map[string]any {
	v, ok := raw["permission"]
	if !ok {
		return nil
	}

	switch p := v.(type) {
	case string:
		return map[string]any{"_mode": p}
	case map[string]any:
		return p
	default:
		return nil
	}
}

// openCodeKnownFields are fields that ImportOpenCode handles explicitly and
// should not be collected into model_options.
var openCodeKnownFields = map[string]bool{
	"name":        true,
	"description": true,
	"provider":    true,
	"model":       true,
	"steps":       true,
	"temperature": true,
	"top_p":       true,
	"tools":       true,
	"disable":     true,
	"permission":  true,
	"color":       true,
	"hidden":      true,
	"mode":        true,
}

// collectOpenCodeModelOptions merges the explicit "model_options" field with
// any unrecognized frontmatter keys.
func collectOpenCodeModelOptions(raw map[string]any) map[string]any {
	var opts map[string]any

	// Start with explicit model_options if present.
	if v, ok := raw["model_options"]; ok {
		if m, ok := v.(map[string]any); ok {
			opts = make(map[string]any, len(m))
			for k, v := range m {
				opts[k] = v
			}
		}
	}

	// Collect unrecognized keys (excluding model_options itself).
	for k, v := range raw {
		if k == "model_options" {
			continue
		}
		if openCodeKnownFields[k] {
			continue
		}
		if opts == nil {
			opts = make(map[string]any)
		}
		opts[k] = v
	}

	return opts
}
