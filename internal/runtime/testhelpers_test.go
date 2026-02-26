package runtime

// toolNames extracts tool names from a slice of ToolDef for readable error messages.
func toolNames(defs []ToolDef) []string {
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	return names
}
