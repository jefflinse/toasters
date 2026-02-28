package tooldef

import (
	"context"
	"encoding/json"
)

// ToolDef defines a tool available to an agent.
type ToolDef struct {
	Name        string
	Description string
	Parameters  json.RawMessage // JSON Schema
}

// MCPCaller dispatches MCP tool calls. Implemented by *mcp.Manager and
// *mcp.TruncatingCaller. Defined in this leaf package so both internal/mcp
// and internal/runtime can reference it without creating an import cycle.
type MCPCaller interface {
	Call(ctx context.Context, name string, args json.RawMessage) (string, error)
}
