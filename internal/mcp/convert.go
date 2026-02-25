package mcp

import (
	"encoding/json"
	"log"

	"github.com/jefflinse/toasters/internal/llm"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

// ToLLMTools converts MCP tool definitions to llm.Tool format (operator format).
func ToLLMTools(tools []ToolInfo) []llm.Tool {
	result := make([]llm.Tool, 0, len(tools))
	for _, t := range tools {
		var params map[string]any
		if err := json.Unmarshal(t.InputSchema, &params); err != nil {
			log.Printf("mcp: failed to unmarshal schema for tool %q: %v (using empty schema)", t.NamespacedName, err)
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		result = append(result, llm.Tool{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        t.NamespacedName,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}
	return result
}

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

// ToRuntimeToolDefs converts MCP tool definitions to runtime.ToolDef format.
func ToRuntimeToolDefs(tools []ToolInfo) []runtime.ToolDef {
	result := make([]runtime.ToolDef, 0, len(tools))
	for _, t := range tools {
		result = append(result, runtime.ToolDef{
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
