// Command mcpserver is a minimal MCP server for testing.
// It exposes two tools: "greet" and "farewell".
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	mcptypes "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func main() {
	s := mcpserver.NewMCPServer("test-server", "1.0.0")

	s.AddTool(
		mcptypes.NewToolWithRawSchema("greet", "Greets someone", []byte(`{"type":"object","properties":{"name":{"type":"string"}}}`)),
		func(ctx context.Context, req mcptypes.CallToolRequest) (*mcptypes.CallToolResult, error) {
			return mcptypes.NewToolResultText("hello"), nil
		},
	)

	s.AddTool(
		mcptypes.NewToolWithRawSchema("farewell", "Says goodbye", []byte(`{"type":"object","properties":{}}`)),
		func(ctx context.Context, req mcptypes.CallToolRequest) (*mcptypes.CallToolResult, error) {
			return mcptypes.NewToolResultText("goodbye"), nil
		},
	)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	stdio := mcpserver.NewStdioServer(s)
	if err := stdio.Listen(ctx, os.Stdin, os.Stdout); err != nil {
		os.Exit(1)
	}
}
