package agentfmt

// Format identifies the source format of a definition file.
type Format string

const (
	FormatToasters   Format = "toasters"
	FormatClaudeCode Format = "claude_code"
	FormatOpenCode   Format = "opencode"
)

// claudeCodeCamelFields are camelCase field names used by Claude Code agent
// definitions but not by toasters (which uses snake_case).
var claudeCodeCamelFields = map[string]bool{
	"maxTurns":        true,
	"disallowedTools": true,
	"mcpServers":      true,
	"permissionMode":  true,
	"modelOptions":    true,
	"topP":            true,
}

// openCodeFields are field names specific to OpenCode agent definitions.
var openCodeFields = map[string]bool{
	"steps":      true,
	"disable":    true,
	"permission": true, // singular, not "permissions"
}

// DetectFormat examines frontmatter fields and returns the likely source format.
//
// Heuristics:
//   - Has camelCase fields (maxTurns, disallowedTools, mcpServers, permissionMode) → ClaudeCode
//   - Has "steps" or "disable" or "permission" (singular) field → OpenCode
//   - Has "tools" as a map (tool_name: bool) rather than a list → OpenCode
//   - Otherwise → Toasters
func DetectFormat(frontmatter map[string]any) Format {
	for key := range frontmatter {
		if claudeCodeCamelFields[key] {
			return FormatClaudeCode
		}
	}

	for key := range frontmatter {
		if openCodeFields[key] {
			return FormatOpenCode
		}
	}

	// OpenCode uses "tools" as a map of tool_name→bool to enable/disable tools.
	// Toasters and Claude Code use "tools" as a list of strings.
	if v, ok := frontmatter["tools"]; ok {
		if _, isMap := v.(map[string]any); isMap {
			return FormatOpenCode
		}
	}

	return FormatToasters
}
