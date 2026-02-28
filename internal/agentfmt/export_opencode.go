package agentfmt

// ExportOpenCode converts an AgentDef into OpenCode format YAML frontmatter
// and body. Fields that have no OpenCode equivalent are dropped and returned
// as warnings. OpenCode uses snake_case field names with some differences from
// the Toasters superset (e.g., "steps" instead of "max_turns").
//
// Exported for a planned agent definition export/conversion feature.
func ExportOpenCode(def *AgentDef) (string, string, []Warning) {
	if def == nil {
		return "", "", nil
	}

	var warnings []Warning

	out := make(map[string]any)

	// Identity.
	setIfNonEmpty(out, "name", def.Name)
	setIfNonEmpty(out, "description", def.Description)

	// Provider/model — OpenCode uses combined "provider/model" format.
	if def.Provider != "" || def.Model != "" {
		combined := combineProviderModel(def.Provider, def.Model)
		setIfNonEmpty(out, "provider", combined)
	}

	// Behavior: max_turns → steps.
	if def.MaxTurns != 0 {
		out["steps"] = def.MaxTurns
	}
	if def.Temperature != nil {
		out["temperature"] = *def.Temperature
	}
	if def.TopP != nil {
		out["top_p"] = *def.TopP
	}

	// Tools.
	if len(def.Tools) > 0 {
		out["tools"] = def.Tools
	}

	// disabled → disable.
	if def.Disabled {
		out["disable"] = true
	}

	// Permissions — if has "_mode" key, unwrap to string; otherwise use as map.
	if len(def.Permissions) > 0 {
		out["permission"] = unwrapPermissions(def.Permissions)
	}

	// UI/Display.
	setIfNonEmpty(out, "color", def.Color)
	if def.Hidden {
		out["hidden"] = true
	}

	// model_options — merge into top-level fields, skipping collisions.
	for k, v := range def.ModelOptions {
		if _, exists := out[k]; exists {
			warnings = append(warnings, Warning{
				Field:  "model_options." + k,
				Reason: "model_options key " + k + " conflicts with explicit field; skipped",
			})
			continue
		}
		out[k] = v
	}

	// Mode — pass through for OpenCode (it may use it).
	setIfNonEmpty(out, "mode", def.Mode)

	// Lossy fields — emit warnings for non-zero values.
	if len(def.Skills) > 0 {
		warnings = append(warnings, Warning{
			Field:  "skills",
			Reason: "not supported by OpenCode format",
		})
	}
	if def.MCPServers != nil {
		warnings = append(warnings, Warning{
			Field:  "mcp_servers",
			Reason: "not supported by OpenCode format",
		})
	}
	if def.PermissionMode != "" {
		warnings = append(warnings, Warning{
			Field:  "permission_mode",
			Reason: "not supported by OpenCode format; use permissions instead",
		})
	}
	if def.Memory != "" {
		warnings = append(warnings, Warning{
			Field:  "memory",
			Reason: "not supported by OpenCode format",
		})
	}
	if len(def.Hooks) > 0 {
		warnings = append(warnings, Warning{
			Field:  "hooks",
			Reason: "not supported by OpenCode format",
		})
	}
	if def.Background {
		warnings = append(warnings, Warning{
			Field:  "background",
			Reason: "not supported by OpenCode format",
		})
	}
	if def.Isolation != "" {
		warnings = append(warnings, Warning{
			Field:  "isolation",
			Reason: "not supported by OpenCode format",
		})
	}
	if len(def.DisallowedTools) > 0 {
		warnings = append(warnings, Warning{
			Field:  "disallowed_tools",
			Reason: "not supported by OpenCode format",
		})
	}

	fmYAML := marshalYAMLOrEmpty(out)
	return fmYAML, def.Body, warnings
}

// combineProviderModel joins provider and model into OpenCode's "provider/model"
// combined format. If only model is set, returns just the model. If only
// provider is set, returns just the provider.
func combineProviderModel(provider, model string) string {
	if provider == "" {
		return model
	}
	if model == "" {
		return provider
	}
	return provider + "/" + model
}

// unwrapPermissions converts a Toasters permissions map back to OpenCode format.
// If the map has a single "_mode" key with a string value, it's unwrapped to
// just that string. Otherwise the full map is returned.
func unwrapPermissions(perms map[string]any) any {
	if len(perms) == 1 {
		if mode, ok := perms["_mode"]; ok {
			if s, ok := mode.(string); ok {
				return s
			}
		}
	}
	return perms
}
