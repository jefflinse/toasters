package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// MCPCaller dispatches MCP tool calls. Implemented by *mcp.Manager.
// Defined here to avoid importing internal/mcp from internal/runtime.
type MCPCaller interface {
	Call(ctx context.Context, name string, args json.RawMessage) (string, error)
}

// CompositeTools wraps CoreTools and adds MCP tool dispatch.
type CompositeTools struct {
	core    *CoreTools
	mcp     MCPCaller
	mcpDefs []ToolDef // pre-converted MCP tool definitions
}

// NewCompositeTools creates a CompositeTools wrapping the given CoreTools.
func NewCompositeTools(core *CoreTools, mcp MCPCaller, mcpDefs []ToolDef) *CompositeTools {
	return &CompositeTools{core: core, mcp: mcp, mcpDefs: mcpDefs}
}

// Execute tries CoreTools first; if unknown tool and name contains "__", dispatches to MCP.
func (ct *CompositeTools) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	result, err := ct.core.Execute(ctx, name, args)
	if errors.Is(err, ErrUnknownTool) && strings.Contains(name, "__") {
		if ct.mcp != nil {
			return ct.mcp.Call(ctx, name, args)
		}
	}
	return result, err
}

// Definitions returns CoreTools definitions plus MCP tool definitions.
func (ct *CompositeTools) Definitions() []ToolDef {
	return append(ct.core.Definitions(), ct.mcpDefs...)
}
