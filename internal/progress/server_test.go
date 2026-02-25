package progress

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// startTestServer starts the MCP server over in-process pipes and returns a
// cleanup function plus a function to send JSON-RPC requests and read responses.
func startTestServer(t *testing.T, store db.Store) (
	sendRequest func(req map[string]any) map[string]any,
	cleanup func(),
) {
	t.Helper()

	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()

	s := NewMCPServer(store)
	stdio := mcpserver.NewStdioServer(s)
	stdio.SetErrorLogger(log.New(io.Discard, "", 0))

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		_ = stdio.Listen(ctx, stdinReader, stdoutWriter)
		stdoutWriter.Close()
	}()

	scanner := bufio.NewScanner(stdoutReader)

	send := func(req map[string]any) map[string]any {
		t.Helper()
		data, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshaling request: %v", err)
		}
		if _, err := stdinWriter.Write(append(data, '\n')); err != nil {
			t.Fatalf("writing request: %v", err)
		}
		if !scanner.Scan() {
			t.Fatalf("reading response: %v", scanner.Err())
		}
		var resp map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshaling response: %v", err)
		}
		return resp
	}

	// Perform the MCP initialize handshake.
	send(map[string]any{
		"jsonrpc": "2.0",
		"id":      0,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"clientInfo": map[string]any{
				"name":    "test-client",
				"version": "1.0.0",
			},
		},
	})

	cleanup = func() {
		cancel()
		stdinWriter.Close()
		stdinReader.Close()
		stdoutReader.Close()
	}

	return send, cleanup
}

// callTool sends a tools/call request and returns the text content of the result.
func callTool(t *testing.T, send func(map[string]any) map[string]any, id int, toolName string, args map[string]any) (string, bool) {
	t.Helper()
	resp := send(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      toolName,
			"arguments": args,
		},
	})

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("response has no 'result' field: %v", resp)
	}

	isError, _ := result["isError"].(bool)

	contents, _ := result["content"].([]any)
	var sb strings.Builder
	for _, c := range contents {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := cm["text"].(string); ok {
			sb.WriteString(text)
		}
	}
	return sb.String(), isError
}

// --- NewMCPServer tests ---

func TestNewMCPServer_NotNil(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	s := NewMCPServer(store)
	if s == nil {
		t.Fatal("NewMCPServer returned nil")
	}
}

func TestNewMCPServer_HasSixTools(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	s := NewMCPServer(store)

	tools := s.ListTools()
	if len(tools) != 6 {
		t.Errorf("server has %d tools, want 6", len(tools))
	}
}

func TestNewMCPServer_ToolNames(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	s := NewMCPServer(store)

	wantTools := []string{
		"report_progress",
		"report_blocker",
		"update_task_status",
		"request_review",
		"query_job_context",
		"log_artifact",
	}

	tools := s.ListTools()
	for _, name := range wantTools {
		if _, ok := tools[name]; !ok {
			t.Errorf("tool %q not registered on MCP server", name)
		}
	}
}

// --- In-process stdio server tests ---

func TestMCPServer_ListTools(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	send, cleanup := startTestServer(t, store)
	defer cleanup()

	resp := send(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
		"params":  map[string]any{},
	})

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("response has no 'result': %v", resp)
	}

	toolsList, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("result has no 'tools' array: %v", result)
	}

	if len(toolsList) != 6 {
		t.Errorf("ListTools returned %d tools, want 6", len(toolsList))
	}

	// Collect tool names from the response.
	nameSet := make(map[string]bool)
	for _, item := range toolsList {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if name, ok := m["name"].(string); ok {
			nameSet[name] = true
		}
	}

	wantNames := []string{
		"report_progress",
		"report_blocker",
		"update_task_status",
		"request_review",
		"query_job_context",
		"log_artifact",
	}
	for _, name := range wantNames {
		if !nameSet[name] {
			t.Errorf("tool %q not in ListTools response", name)
		}
	}
}

func TestMCPServer_ReportProgress(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)

	// Create the job so the FK constraint is satisfied.
	createTestJob(t, ctx, store, "job-mcp-rp")

	send, cleanup := startTestServer(t, store)
	defer cleanup()

	text, isError := callTool(t, send, 2, "report_progress", map[string]any{
		"job_id":   "job-mcp-rp",
		"task_id":  "task-1",
		"agent_id": "agent-1",
		"status":   "in_progress",
		"message":  "making progress",
	})

	if isError {
		t.Fatalf("tool returned error: %s", text)
	}
	if text != "progress reported" {
		t.Errorf("result text = %q, want %q", text, "progress reported")
	}

	// Verify the report was persisted.
	reports, err := store.GetRecentProgress(ctx, "job-mcp-rp", 10)
	if err != nil {
		t.Fatalf("GetRecentProgress: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	if reports[0].Message != "making progress" {
		t.Errorf("Message = %q, want %q", reports[0].Message, "making progress")
	}
}

func TestMCPServer_ReportBlocker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)

	createTestJob(t, ctx, store, "job-mcp-rb")

	send, cleanup := startTestServer(t, store)
	defer cleanup()

	text, isError := callTool(t, send, 3, "report_blocker", map[string]any{
		"job_id":      "job-mcp-rb",
		"description": "cannot connect to database",
		"severity":    "high",
	})

	if isError {
		t.Fatalf("tool returned error: %s", text)
	}
	if text != "blocker reported" {
		t.Errorf("result text = %q, want %q", text, "blocker reported")
	}

	reports, err := store.GetRecentProgress(ctx, "job-mcp-rb", 10)
	if err != nil {
		t.Fatalf("GetRecentProgress: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	if reports[0].Status != "blocked" {
		t.Errorf("Status = %q, want %q", reports[0].Status, "blocked")
	}
}

func TestMCPServer_UpdateTaskStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)

	createTestJob(t, ctx, store, "job-mcp-uts")
	createTestTask(t, ctx, store, "job-mcp-uts", "task-mcp-uts")

	send, cleanup := startTestServer(t, store)
	defer cleanup()

	text, isError := callTool(t, send, 4, "update_task_status", map[string]any{
		"job_id":  "job-mcp-uts",
		"task_id": "task-mcp-uts",
		"status":  "completed",
		"summary": "done via MCP",
	})

	if isError {
		t.Fatalf("tool returned error: %s", text)
	}
	if text != "task status updated" {
		t.Errorf("result text = %q, want %q", text, "task status updated")
	}

	task, err := store.GetTask(ctx, "task-mcp-uts")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Status != db.TaskStatusCompleted {
		t.Errorf("Status = %q, want %q", task.Status, db.TaskStatusCompleted)
	}
}

func TestMCPServer_UpdateTaskStatus_InvalidStatus(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)

	send, cleanup := startTestServer(t, store)
	defer cleanup()

	text, isError := callTool(t, send, 5, "update_task_status", map[string]any{
		"job_id":  "job-1",
		"task_id": "task-1",
		"status":  "not_valid",
	})

	if !isError {
		t.Errorf("expected error result for invalid status, got: %s", text)
	}
}

func TestMCPServer_RequestReview(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)

	createTestJob(t, ctx, store, "job-mcp-rr")

	send, cleanup := startTestServer(t, store)
	defer cleanup()

	text, isError := callTool(t, send, 6, "request_review", map[string]any{
		"job_id":        "job-mcp-rr",
		"artifact_path": "/path/to/file.go",
		"notes":         "please check this",
	})

	if isError {
		t.Fatalf("tool returned error: %s", text)
	}
	if text != "review requested" {
		t.Errorf("result text = %q, want %q", text, "review requested")
	}

	artifacts, err := store.ListArtifactsForJob(ctx, "job-mcp-rr")
	if err != nil {
		t.Fatalf("ListArtifactsForJob: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}
	if artifacts[0].Type != "review_request" {
		t.Errorf("artifact Type = %q, want %q", artifacts[0].Type, "review_request")
	}
}

func TestMCPServer_QueryJobContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)

	createTestJob(t, ctx, store, "job-mcp-qjc")
	createTestTask(t, ctx, store, "job-mcp-qjc", "task-mcp-qjc")

	send, cleanup := startTestServer(t, store)
	defer cleanup()

	text, isError := callTool(t, send, 7, "query_job_context", map[string]any{
		"job_id": "job-mcp-qjc",
	})

	if isError {
		t.Fatalf("tool returned error: %s", text)
	}

	// Result should be valid JSON containing the job.
	var parsed jobContextResult
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v\nresult: %s", err, text)
	}
	if parsed.Job == nil {
		t.Fatal("job is nil in result")
	}
	if parsed.Job.ID != "job-mcp-qjc" {
		t.Errorf("job ID = %q, want %q", parsed.Job.ID, "job-mcp-qjc")
	}
	if len(parsed.Tasks) != 1 {
		t.Errorf("tasks count = %d, want 1", len(parsed.Tasks))
	}
}

func TestMCPServer_QueryJobContext_NotFound(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)

	send, cleanup := startTestServer(t, store)
	defer cleanup()

	text, isError := callTool(t, send, 8, "query_job_context", map[string]any{
		"job_id": "nonexistent-job-id",
	})

	if !isError {
		t.Errorf("expected error for nonexistent job, got: %s", text)
	}
}

func TestMCPServer_LogArtifact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)

	createTestJob(t, ctx, store, "job-mcp-la")

	send, cleanup := startTestServer(t, store)
	defer cleanup()

	text, isError := callTool(t, send, 9, "log_artifact", map[string]any{
		"job_id":  "job-mcp-la",
		"type":    "report",
		"path":    "/reports/final.md",
		"summary": "final report",
	})

	if isError {
		t.Fatalf("tool returned error: %s", text)
	}
	if text != "artifact logged" {
		t.Errorf("result text = %q, want %q", text, "artifact logged")
	}

	artifacts, err := store.ListArtifactsForJob(ctx, "job-mcp-la")
	if err != nil {
		t.Fatalf("ListArtifactsForJob: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}
	if artifacts[0].Path != "/reports/final.md" {
		t.Errorf("artifact Path = %q, want %q", artifacts[0].Path, "/reports/final.md")
	}
}

// --- StartStdioServer tests ---

func TestStartStdioServer_CancelledContext(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)

	// Create pipes so the server has something to read from.
	stdinReader, stdinWriter := io.Pipe()
	defer stdinWriter.Close()
	defer stdinReader.Close()

	stdoutReader, stdoutWriter := io.Pipe()
	defer stdoutReader.Close()
	defer stdoutWriter.Close()

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		s := NewMCPServer(store)
		stdio := mcpserver.NewStdioServer(s)
		stdio.SetErrorLogger(log.New(io.Discard, "", 0))
		errCh <- stdio.Listen(ctx, stdinReader, stdoutWriter)
	}()

	// Cancel the context — the server should stop.
	cancel()

	// Drain stdout so the server goroutine can write its final bytes.
	go func() { io.Copy(io.Discard, stdoutReader) }() //nolint:errcheck

	err := <-errCh
	// The server may return nil or context.Canceled — both are acceptable.
	if err != nil && err != context.Canceled && err != io.EOF {
		t.Errorf("unexpected error from Listen after cancel: %v", err)
	}
}

// --- mcptest-based integration test ---

func TestMCPServer_ViaTestHelper(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Build the real store and pre-populate it.
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := db.Open(path)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { store.Close() }) //nolint:errcheck

	createTestJob(t, ctx, store, "job-via-helper")

	// Wire up the MCP server's tools into the mcptest helper.
	mcpSrv := NewMCPServer(store)
	registeredTools := mcpSrv.ListTools()

	// Convert to []mcpserver.ServerTool for mcptest.
	var serverTools []mcpserver.ServerTool
	for _, st := range registeredTools {
		serverTools = append(serverTools, *st)
	}

	// Use the raw stdio approach since mcptest creates its own MCPServer internally.
	// We test via the in-process pipe approach instead.
	send, cleanup := startTestServer(t, store)
	defer cleanup()

	// Verify all 6 tools are listed.
	resp := send(map[string]any{
		"jsonrpc": "2.0",
		"id":      10,
		"method":  "tools/list",
		"params":  map[string]any{},
	})

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in response: %v", resp)
	}
	toolsList, _ := result["tools"].([]any)
	if len(toolsList) != 6 {
		t.Errorf("expected 6 tools, got %d", len(toolsList))
	}

	// Call report_progress and verify it persists.
	text, isError := callTool(t, send, 11, "report_progress", map[string]any{
		"job_id":  "job-via-helper",
		"status":  "in_progress",
		"message": "via helper test",
	})
	if isError {
		t.Fatalf("report_progress returned error: %s", text)
	}

	reports, err := store.GetRecentProgress(ctx, "job-via-helper", 10)
	if err != nil {
		t.Fatalf("GetRecentProgress: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}

	// Suppress unused import warning — mcp package is used via the type below.
	_ = mcp.LATEST_PROTOCOL_VERSION
}
