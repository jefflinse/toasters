package progress

import (
	"encoding/json"

	"github.com/jefflinse/toasters/internal/tooldef"
)

// ToolDef is an alias for tooldef.ToolDef, the shared tool definition type.
type ToolDef = tooldef.ToolDef

// ProgressToolDefs returns the tool definitions for the 6 progress tools.
func ProgressToolDefs() []ToolDef {
	return []ToolDef{
		{
			Name:        "report_task_progress",
			Description: "Report progress on a task. Use this to keep the orchestrator informed of what you're doing.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"job_id":   {"type": "string", "description": "The job ID"},
					"task_id":  {"type": "string", "description": "The task ID (optional)"},
					"agent_id": {"type": "string", "description": "The agent ID (optional, auto-filled from session context)"},
					"status":   {"type": "string", "description": "Current status: in_progress, completed, failed, blocked"},
					"message":  {"type": "string", "description": "What you are currently doing or have done"}
				},
				"required": ["job_id", "status", "message"]
			}`),
		},
		{
			Name:        "report_blocker",
			Description: "Report that you are blocked and cannot proceed without help.",
			Parameters: json.RawMessage(`{
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
		},
		{
			Name:        "update_task_status",
			Description: "Update the status of a task in the job tracker.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"job_id":  {"type": "string", "description": "The job ID"},
					"task_id": {"type": "string", "description": "The task ID"},
					"status":  {"type": "string", "enum": ["pending", "in_progress", "completed", "failed", "blocked", "cancelled"], "description": "New task status"},
					"summary": {"type": "string", "description": "Optional summary of what was done"}
				},
				"required": ["job_id", "task_id", "status"]
			}`),
		},
		{
			Name:        "request_review",
			Description: "Request a review of an artifact you have produced.",
			Parameters: json.RawMessage(`{
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
		},
		{
			Name:        "query_job_context",
			Description: "Query the current state of a job: overview, task statuses, recent progress, and artifacts.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"job_id": {"type": "string", "description": "The job ID to query"}
				},
				"required": ["job_id"]
			}`),
		},
		{
			Name:        "log_artifact",
			Description: "Log an artifact (file, report, etc.) produced during the job.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"job_id":  {"type": "string", "description": "The job ID"},
					"task_id": {"type": "string", "description": "The task ID (optional)"},
					"type": {
						"type": "string",
						"enum": ["code", "report", "investigation", "test_results", "other"],
						"description": "Artifact type"
					},
					"path":    {"type": "string", "description": "File path of the artifact"},
					"summary": {"type": "string", "description": "Brief description of the artifact"}
				},
				"required": ["job_id", "type", "path"]
			}`),
		},
	}
}
