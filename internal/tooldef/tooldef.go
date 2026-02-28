package tooldef

import "encoding/json"

// ToolDef defines a tool available to an agent.
type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON Schema
}
