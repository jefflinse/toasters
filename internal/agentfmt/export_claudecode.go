package agentfmt

import (
	"log/slog"

	"gopkg.in/yaml.v3"
)

// ExportClaudeCode converts an AgentDef into Claude Code format YAML frontmatter
// and body. Fields that have no Claude Code equivalent are dropped and returned
// as warnings. Claude Code uses camelCase field names.
func ExportClaudeCode(def *AgentDef) (string, string, []Warning) {
	if def == nil {
		return "", "", nil
	}

	var warnings []Warning

	out := make(map[string]any)

	// Identity.
	setIfNonEmpty(out, "name", def.Name)
	setIfNonEmpty(out, "description", def.Description)

	// Model — emit if non-empty.
	setIfNonEmpty(out, "model", def.Model)

	// Provider — "anthropic" is implicit in Claude Code, so don't emit it.
	// Non-anthropic providers are not supported.
	if def.Provider != "" && def.Provider != "anthropic" {
		warnings = append(warnings, Warning{
			Field:  "provider",
			Reason: "non-Anthropic provider not supported by Claude Code format",
		})
	}

	// Behavior.
	if def.MaxTurns != 0 {
		out["maxTurns"] = def.MaxTurns
	}
	if def.Temperature != nil {
		out["temperature"] = *def.Temperature
	}
	if def.TopP != nil {
		out["topP"] = *def.TopP
	}

	// Tools.
	if len(def.Tools) > 0 {
		out["tools"] = def.Tools
	}
	if len(def.DisallowedTools) > 0 {
		out["disallowedTools"] = def.DisallowedTools
	}

	// Permissions.
	setIfNonEmpty(out, "permissionMode", def.PermissionMode)

	// MCP servers — preserve as-is.
	if def.MCPServers != nil {
		out["mcpServers"] = def.MCPServers
	}

	// Memory.
	setIfNonEmpty(out, "memory", def.Memory)

	// Lifecycle.
	if len(def.Hooks) > 0 {
		out["hooks"] = def.Hooks
	}
	if def.Background {
		out["background"] = true
	}
	setIfNonEmpty(out, "isolation", def.Isolation)

	// UI/Display.
	setIfNonEmpty(out, "color", def.Color)

	// Lossy fields — emit warnings for non-zero values.
	if def.Mode != "" {
		warnings = append(warnings, Warning{
			Field:  "mode",
			Reason: "not supported by Claude Code format",
		})
	}
	if len(def.Skills) > 0 {
		warnings = append(warnings, Warning{
			Field:  "skills",
			Reason: "not supported by Claude Code format",
		})
	}
	if len(def.Permissions) > 0 {
		warnings = append(warnings, Warning{
			Field:  "permissions",
			Reason: "granular permissions not supported by Claude Code format; use permission_mode instead",
		})
	}
	if def.Hidden {
		warnings = append(warnings, Warning{
			Field:  "hidden",
			Reason: "not supported by Claude Code format",
		})
	}
	if def.Disabled {
		warnings = append(warnings, Warning{
			Field:  "disabled",
			Reason: "not supported by Claude Code format",
		})
	}
	if len(def.ModelOptions) > 0 {
		warnings = append(warnings, Warning{
			Field:  "model_options",
			Reason: "not supported by Claude Code format",
		})
	}

	fmYAML := marshalYAMLOrEmpty(out)
	return fmYAML, def.Body, warnings
}

// setIfNonEmpty adds a string value to the map if it is non-empty.
func setIfNonEmpty(m map[string]any, key, value string) {
	if value != "" {
		m[key] = value
	}
}

// marshalYAMLOrEmpty marshals a map to YAML, returning an empty string if the
// map is empty or marshaling fails.
func marshalYAMLOrEmpty(m map[string]any) string {
	if len(m) == 0 {
		return ""
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		slog.Warn("failed to marshal export YAML", "error", err)
		return ""
	}
	return string(data)
}
