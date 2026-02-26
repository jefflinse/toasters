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

	return FormatToasters
}
