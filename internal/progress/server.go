package progress

import (
	"context"
	"fmt"
	"os"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// NewMCPServer creates an MCP server that exposes the 6 progress tools.
// The server uses the provided store for all database operations.
func NewMCPServer(store db.Store) *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer(
		"toasters",
		"1.0.0",
	)

	// report_progress
	s.AddTool(
		mcp.NewToolWithRawSchema(
			"report_progress",
			"Report progress on a task. Use this to keep the orchestrator informed of what you're doing.",
			[]byte(`{
				"type": "object",
				"properties": {
					"job_id":   {"type": "string", "description": "The job ID"},
					"task_id":  {"type": "string", "description": "The task ID (optional)"},
					"agent_id": {"type": "string", "description": "The agent ID (optional)"},
					"status":   {"type": "string", "description": "Current status: in_progress, completed, failed, blocked"},
					"message":  {"type": "string", "description": "What you are currently doing or have done"}
				},
				"required": ["job_id", "status", "message"]
			}`),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var params ReportProgressParams
			if err := req.BindArguments(&params); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
			}
			result, err := ReportProgress(ctx, store, params)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// report_blocker
	s.AddTool(
		mcp.NewToolWithRawSchema(
			"report_blocker",
			"Report that you are blocked and cannot proceed without help.",
			[]byte(`{
				"type": "object",
				"properties": {
					"job_id":      {"type": "string", "description": "The job ID"},
					"task_id":     {"type": "string", "description": "The task ID (optional)"},
					"agent_id":    {"type": "string", "description": "The agent ID (optional)"},
					"description": {"type": "string", "description": "What is blocking you"},
					"severity":    {"type": "string", "enum": ["low", "medium", "high"], "description": "Severity of the blocker"}
				},
				"required": ["job_id", "description", "severity"]
			}`),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var params ReportBlockerParams
			if err := req.BindArguments(&params); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid arguments: %v", err)), nil
			}
			result, err := ReportBlocker(ctx, store, params)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(result), nil
		},
	)

	// update_task_status
	s.AddTool(
		mcp.NewToolWithRawSchema(
			"update_task_status",
			"Update the status of a task in the job tracker.",
			[]byte(`{
				"type": "object",
				"properties": {
					"job_id":  {"type": "string", "description": "The job ID"},
					"task_id": {"type": "string", "description": "The task ID"},
					"status":  {"type": "string", "enum": ["pending", "in_progress", "completed", "failed", "blocked", "cancelled"], "description": "New task status"},
					"summary": {"type": "string", "description": "Optional summary of what was done"}
				},
				"required": ["job_id", "task_id", "status"]
			}`),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
	)

	// request_review
	s.AddTool(
		mcp.NewToolWithRawSchema(
			"request_review",
			"Request a review of an artifact you have produced.",
			[]byte(`{
				"type": "object",
				"properties": {
					"job_id":        {"type": "string", "description": "The job ID"},
					"task_id":       {"type": "string", "description": "The task ID (optional)"},
					"agent_id":      {"type": "string", "description": "The agent ID (optional)"},
					"artifact_path": {"type": "string", "description": "Path to the artifact to review"},
					"notes":         {"type": "string", "description": "Notes for the reviewer"}
				},
				"required": ["job_id", "artifact_path"]
			}`),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
	)

	// query_job_context
	s.AddTool(
		mcp.NewToolWithRawSchema(
			"query_job_context",
			"Query the current state of a job: overview, task statuses, recent progress, and artifacts.",
			[]byte(`{
				"type": "object",
				"properties": {
					"job_id": {"type": "string", "description": "The job ID to query"}
				},
				"required": ["job_id"]
			}`),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
	)

	// log_artifact
	s.AddTool(
		mcp.NewToolWithRawSchema(
			"log_artifact",
			"Log an artifact (file, report, etc.) produced during the job.",
			[]byte(`{
				"type": "object",
				"properties": {
					"job_id":  {"type": "string", "description": "The job ID"},
					"task_id": {"type": "string", "description": "The task ID (optional)"},
					"type":    {"type": "string", "description": "Artifact type: code, report, investigation, test_results, other"},
					"path":    {"type": "string", "description": "File path of the artifact"},
					"summary": {"type": "string", "description": "Brief description of the artifact"}
				},
				"required": ["job_id", "type", "path"]
			}`),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
	)

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
