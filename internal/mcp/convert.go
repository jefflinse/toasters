package mcp

import (
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/tooldef"
)

// ToProviderTools converts MCP tool definitions to provider.Tool format.
func ToProviderTools(tools []ToolInfo) []provider.Tool {
	result := make([]provider.Tool, 0, len(tools))
	for _, t := range tools {
		result = append(result, provider.Tool{
			Name:        t.NamespacedName,
			Description: t.Description,
			Parameters:  t.InputSchema,
		})
	}
	return result
}

// ToRuntimeToolDefs converts MCP tool definitions to tooldef.ToolDef format.
func ToRuntimeToolDefs(tools []ToolInfo) []tooldef.ToolDef {
	result := make([]tooldef.ToolDef, 0, len(tools))
	for _, t := range tools {
		result = append(result, tooldef.ToolDef{
			Name:        t.NamespacedName,
			Description: t.Description,
			Parameters:  t.InputSchema,
		})
	}
	return result
}

// FilterTools filters tools by whitelist. If whitelist is empty, all tools are returned.
// Filtering is by OriginalName (not namespaced name).
func FilterTools(tools []ToolInfo, whitelist []string) []ToolInfo {
	if len(whitelist) == 0 {
		return tools
	}
	allowed := make(map[string]bool, len(whitelist))
	for _, name := range whitelist {
		allowed[name] = true
	}
	result := make([]ToolInfo, 0, len(tools))
	for _, t := range tools {
		if allowed[t.OriginalName] {
			result = append(result, t)
		}
	}
	return result
}
