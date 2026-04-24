package progress

import (
	"context"
	"fmt"
	"os"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// toolHandler is a function that handles an MCP tool call.
type toolHandler func(ctx context.Context, store db.Store, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// toolHandlers maps tool names to their handler functions.
func toolHandlers() map[string]toolHandler {
	return map[string]toolHandler{
		"report_task_progress": func(ctx context.Context, store db.Store, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var params ReportTaskProgressParams
			if err := req.BindArguments(&params); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
			}
			result, err := ReportTaskProgress(ctx, store, params)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
		"update_task_status": func(ctx context.Context, store db.Store, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var params UpdateTaskStatusParams
			if err := req.BindArguments(&params); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
			}
			result, err := UpdateTaskStatus(ctx, store, params)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
		"request_review": func(ctx context.Context, store db.Store, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var params RequestReviewParams
			if err := req.BindArguments(&params); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
			}
			result, err := RequestReview(ctx, store, params)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
		"query_job_context": func(ctx context.Context, store db.Store, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var params QueryJobContextParams
			if err := req.BindArguments(&params); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
			}
			result, err := QueryJobContext(ctx, store, params)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
		"log_artifact": func(ctx context.Context, store db.Store, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var params LogArtifactParams
			if err := req.BindArguments(&params); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
			}
			result, err := LogArtifact(ctx, store, params)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	}
}

// NewMCPServer creates an MCP server that exposes the 6 progress tools.
// The server uses the provided store for all database operations.
// Tool schemas are sourced from ProgressToolDefs() to avoid duplication.
func NewMCPServer(store db.Store) *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer(
		"toasters",
		"1.0.0",
	)

	handlers := toolHandlers()

	for _, td := range ProgressToolDefs() {
		handler, ok := handlers[td.Name]
		if !ok {
			// Programming error: ProgressToolDefs has a tool with no handler.
			panic(fmt.Sprintf("no handler registered for progress tool %q", td.Name))
		}

		s.AddTool(
			mcp.NewToolWithRawSchema(td.Name, td.Description, td.Parameters),
			func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return handler(ctx, store, req)
			},
		)
	}

	return s
}

// StartStdioServer starts the MCP server on stdin/stdout.
// This is called by the `toasters mcp-server` subcommand.
func StartStdioServer(ctx context.Context, store db.Store) error {
	s := NewMCPServer(store)
	stdio := mcpserver.NewStdioServer(s)

	// Run until context is cancelled or stdin closes.
	if err := stdio.Listen(ctx, os.Stdin, os.Stdout); err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}
	return nil
}
