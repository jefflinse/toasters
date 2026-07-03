package client

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/server"
	"github.com/jefflinse/toasters/internal/service"
)

// testTime is a fixed timestamp used across all tests for consistency.
var testTime = time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// Wire → service round-trip tests
// ---------------------------------------------------------------------------

func TestWireJobToService(t *testing.T) {
	t.Parallel()

	w := wireJob{
		ID:          "job-1",
		Title:       "Fix bug",
		Description: "Fix the login bug",
		Type:        "bug_fix",
		Status:      "active",
		CreatedAt:   testTime,
		UpdatedAt:   testTime.Add(time.Hour),
		Metadata:    json.RawMessage(`{"priority":"high"}`),
	}

	data, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded wireJob
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := wireJobToService(decoded)

	if got.ID != "job-1" {
		t.Errorf("ID = %q, want %q", got.ID, "job-1")
	}
	if got.Title != "Fix bug" {
		t.Errorf("Title = %q, want %q", got.Title, "Fix bug")
	}
	if got.Description != "Fix the login bug" {
		t.Errorf("Description = %q, want %q", got.Description, "Fix the login bug")
	}
	if got.Type != "bug_fix" {
		t.Errorf("Type = %q, want %q", got.Type, "bug_fix")
	}
	if got.Status != service.JobStatus("active") {
		t.Errorf("Status = %q, want %q", got.Status, "active")
	}
	if !got.CreatedAt.Equal(testTime) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, testTime)
	}
	if !got.UpdatedAt.Equal(testTime.Add(time.Hour)) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, testTime.Add(time.Hour))
	}
	if string(got.Metadata) != `{"priority":"high"}` {
		t.Errorf("Metadata = %s, want %s", got.Metadata, `{"priority":"high"}`)
	}
}

func TestWireTaskToService(t *testing.T) {
	t.Parallel()

	meta := json.RawMessage(`{"key":"val"}`)
	w := wireTask{
		ID:              "task-1",
		JobID:           "job-1",
		Title:           "Write tests",
		Status:          "in_progress",
		WorkerID:        "agent-1",
		GraphID:         "team-1",
		ParentID:        "task-0",
		SortOrder:       3,
		CreatedAt:       testTime,
		UpdatedAt:       testTime.Add(2 * time.Hour),
		Summary:         "Tests written",
		ResultSummary:   "All pass",
		Recommendations: "Add more edge cases",
		Metadata:        meta,
	}

	data, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded wireTask
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := wireTaskToService(decoded)

	if got.ID != "task-1" {
		t.Errorf("ID = %q, want %q", got.ID, "task-1")
	}
	if got.JobID != "job-1" {
		t.Errorf("JobID = %q, want %q", got.JobID, "job-1")
	}
	if got.Status != service.TaskStatus("in_progress") {
		t.Errorf("Status = %q, want %q", got.Status, "in_progress")
	}
	if got.WorkerID != "agent-1" {
		t.Errorf("WorkerID = %q, want %q", got.WorkerID, "agent-1")
	}
	if got.GraphID != "team-1" {
		t.Errorf("GraphID = %q, want %q", got.GraphID, "team-1")
	}
	if got.ParentID != "task-0" {
		t.Errorf("ParentID = %q, want %q", got.ParentID, "task-0")
	}
	if got.SortOrder != 3 {
		t.Errorf("SortOrder = %d, want %d", got.SortOrder, 3)
	}
	if got.Summary != "Tests written" {
		t.Errorf("Summary = %q, want %q", got.Summary, "Tests written")
	}
	if got.ResultSummary != "All pass" {
		t.Errorf("ResultSummary = %q, want %q", got.ResultSummary, "All pass")
	}
	if got.Recommendations != "Add more edge cases" {
		t.Errorf("Recommendations = %q, want %q", got.Recommendations, "Add more edge cases")
	}
	if string(got.Metadata) != `{"key":"val"}` {
		t.Errorf("Metadata = %s, want %s", got.Metadata, `{"key":"val"}`)
	}
}

func TestWireTaskToService_OptionalFieldsEmpty(t *testing.T) {
	t.Parallel()

	w := wireTask{
		ID:        "task-2",
		JobID:     "job-2",
		Title:     "Minimal task",
		Status:    "pending",
		CreatedAt: testTime,
		UpdatedAt: testTime,
	}

	got := wireTaskToService(w)

	if got.WorkerID != "" {
		t.Errorf("WorkerID = %q, want empty", got.WorkerID)
	}
	if got.GraphID != "" {
		t.Errorf("GraphID = %q, want empty", got.GraphID)
	}
	if got.Metadata != nil {
		t.Errorf("Metadata = %v, want nil", got.Metadata)
	}
}

func TestWireSkillToService(t *testing.T) {
	t.Parallel()

	w := wireSkill{
		ID:          "skill-1",
		Name:        "code-review",
		Description: "Reviews code for quality",
		Tools:       []string{"read_file", "grep"},
		Prompt:      "You are a code reviewer.",
		Source:      "user",
		CreatedAt:   testTime,
		UpdatedAt:   testTime,
	}

	data, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded wireSkill
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := wireSkillToService(decoded)

	if got.ID != "skill-1" {
		t.Errorf("ID = %q, want %q", got.ID, "skill-1")
	}
	if got.Name != "code-review" {
		t.Errorf("Name = %q, want %q", got.Name, "code-review")
	}
	if got.Description != "Reviews code for quality" {
		t.Errorf("Description = %q, want %q", got.Description, "Reviews code for quality")
	}
	if len(got.Tools) != 2 || got.Tools[0] != "read_file" || got.Tools[1] != "grep" {
		t.Errorf("Tools = %v, want [read_file grep]", got.Tools)
	}
	if got.Prompt != "You are a code reviewer." {
		t.Errorf("Prompt = %q, want %q", got.Prompt, "You are a code reviewer.")
	}
	if got.Source != "user" {
		t.Errorf("Source = %q, want %q", got.Source, "user")
	}
}

func TestWireSessionSnapshotToService(t *testing.T) {
	t.Parallel()

	w := wireSessionSnapshot{
		ID:            "sess-1",
		WorkerID:      "agent-1",
		JobID:         "job-1",
		TaskID:        "task-1",
		Status:        "active",
		Model:         "claude-sonnet-4-6",
		Provider:      "anthropic",
		StartTime:     testTime,
		TokensIn:      1500,
		TokensOut:     3200,
		ContextWindow: 200000,
	}

	data, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded wireSessionSnapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := wireSessionSnapshotToService(decoded)

	if got.ID != "sess-1" {
		t.Errorf("ID = %q, want %q", got.ID, "sess-1")
	}
	if got.WorkerID != "agent-1" {
		t.Errorf("WorkerID = %q, want %q", got.WorkerID, "agent-1")
	}
	if got.TokensIn != 1500 {
		t.Errorf("TokensIn = %d, want 1500", got.TokensIn)
	}
	if got.TokensOut != 3200 {
		t.Errorf("TokensOut = %d, want 3200", got.TokensOut)
	}
	if got.Status != "active" {
		t.Errorf("Status = %q, want %q", got.Status, "active")
	}
	if !got.StartTime.Equal(testTime) {
		t.Errorf("StartTime = %v, want %v", got.StartTime, testTime)
	}
	if got.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", got.ContextWindow)
	}
}

// TestWireSessionSnapshot_OldPayload verifies a payload from a server that
// predates context_window decodes with the field at 0 ("unknown"), which is
// the pre-existing behavior.
func TestWireSessionSnapshot_OldPayload(t *testing.T) {
	t.Parallel()

	old := `{"id":"sess-1","worker_id":"w1","status":"active","start_time":"2025-01-01T00:00:00Z","tokens_in":1,"tokens_out":2}`
	var decoded wireSessionSnapshot
	if err := json.Unmarshal([]byte(old), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := wireSessionSnapshotToService(decoded)
	if got.ContextWindow != 0 {
		t.Errorf("ContextWindow = %d, want 0 for old payload", got.ContextWindow)
	}
}

func TestWireSessionDetailToService(t *testing.T) {
	t.Parallel()

	w := wireSessionDetail{
		Snapshot: wireSessionSnapshot{
			ID:        "sess-1",
			WorkerID:  "agent-1",
			Status:    "completed",
			StartTime: testTime,
			TokensIn:  100,
			TokensOut: 200,
		},
		SystemPrompt:   "You are a test writer.",
		InitialMessage: "Write tests for foo.go",
		Output:         "Here are the tests...",
		Activities: []wireActivityItem{
			{Label: "read: foo.go", ToolName: "read_file"},
			{Label: "write: foo_test.go", ToolName: "write_file"},
		},
		WorkerName: "test-writer",
		Task:       "Write unit tests",
	}

	data, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded wireSessionDetail
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := wireSessionDetailToService(decoded)

	if got.Snapshot.ID != "sess-1" {
		t.Errorf("Snapshot.ID = %q, want %q", got.Snapshot.ID, "sess-1")
	}
	if got.SystemPrompt != "You are a test writer." {
		t.Errorf("SystemPrompt = %q, want %q", got.SystemPrompt, "You are a test writer.")
	}
	if got.InitialMessage != "Write tests for foo.go" {
		t.Errorf("InitialMessage = %q, want %q", got.InitialMessage, "Write tests for foo.go")
	}
	if got.Output != "Here are the tests..." {
		t.Errorf("Output = %q, want %q", got.Output, "Here are the tests...")
	}
	if len(got.Activities) != 2 {
		t.Fatalf("Activities len = %d, want 2", len(got.Activities))
	}
	if got.Activities[0].Label != "read: foo.go" {
		t.Errorf("Activities[0].Label = %q, want %q", got.Activities[0].Label, "read: foo.go")
	}
	if got.Activities[0].ToolName != "read_file" {
		t.Errorf("Activities[0].ToolName = %q, want %q", got.Activities[0].ToolName, "read_file")
	}
	if got.Activities[1].Label != "write: foo_test.go" {
		t.Errorf("Activities[1].Label = %q, want %q", got.Activities[1].Label, "write: foo_test.go")
	}
	if got.WorkerName != "test-writer" {
		t.Errorf("WorkerName = %q, want %q", got.WorkerName, "test-writer")
	}
	if got.Task != "Write unit tests" {
		t.Errorf("Task = %q, want %q", got.Task, "Write unit tests")
	}
}

func TestWireModelInfoToService(t *testing.T) {
	t.Parallel()

	w := wireModelInfo{
		ID:                  "model-1",
		Name:                "claude-sonnet-4-6",
		Provider:            "anthropic",
		State:               "loaded",
		MaxContextLength:    200000,
		LoadedContextLength: 128000,
	}

	data, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded wireModelInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := wireModelInfoToService(decoded)

	if got.ID != "model-1" {
		t.Errorf("ID = %q, want %q", got.ID, "model-1")
	}
	if got.Name != "claude-sonnet-4-6" {
		t.Errorf("Name = %q, want %q", got.Name, "claude-sonnet-4-6")
	}
	if got.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", got.Provider, "anthropic")
	}
	if got.State != "loaded" {
		t.Errorf("State = %q, want %q", got.State, "loaded")
	}
	if got.MaxContextLength != 200000 {
		t.Errorf("MaxContextLength = %d, want 200000", got.MaxContextLength)
	}
	if got.LoadedContextLength != 128000 {
		t.Errorf("LoadedContextLength = %d, want 128000", got.LoadedContextLength)
	}
}

func TestWireMCPServerStatusToService(t *testing.T) {
	t.Parallel()

	w := wireMCPServerStatus{
		Name:      "github",
		Transport: "stdio",
		State:     "connected",
		Error:     "",
		ToolCount: 2,
		Tools: []wireMCPToolInfo{
			{
				NamespacedName: "github__list_repos",
				OriginalName:   "list_repos",
				ServerName:     "github",
				Description:    "Lists repositories",
				InputSchema:    json.RawMessage(`{"type":"object"}`),
			},
			{
				NamespacedName: "github__create_pr",
				OriginalName:   "create_pr",
				ServerName:     "github",
				Description:    "Creates a pull request",
				InputSchema:    json.RawMessage(`{"type":"object"}`),
			},
		},
	}

	data, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded wireMCPServerStatus
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := wireMCPServerStatusToService(decoded)

	if got.Name != "github" {
		t.Errorf("Name = %q, want %q", got.Name, "github")
	}
	if got.Transport != "stdio" {
		t.Errorf("Transport = %q, want %q", got.Transport, "stdio")
	}
	if got.State != service.MCPServerState("connected") {
		t.Errorf("State = %q, want %q", got.State, "connected")
	}
	if got.Error != "" {
		t.Errorf("Error = %q, want empty", got.Error)
	}
	if got.ToolCount != 2 {
		t.Errorf("ToolCount = %d, want 2", got.ToolCount)
	}
	if len(got.Tools) != 2 {
		t.Fatalf("Tools len = %d, want 2", len(got.Tools))
	}
	if got.Tools[0].NamespacedName != "github__list_repos" {
		t.Errorf("Tools[0].NamespacedName = %q, want %q", got.Tools[0].NamespacedName, "github__list_repos")
	}
	if got.Tools[0].OriginalName != "list_repos" {
		t.Errorf("Tools[0].OriginalName = %q, want %q", got.Tools[0].OriginalName, "list_repos")
	}
	if got.Tools[1].Description != "Creates a pull request" {
		t.Errorf("Tools[1].Description = %q, want %q", got.Tools[1].Description, "Creates a pull request")
	}
	if string(got.Tools[0].InputSchema) != `{"type":"object"}` {
		t.Errorf("Tools[0].InputSchema = %s, want %s", got.Tools[0].InputSchema, `{"type":"object"}`)
	}
}

func TestWireChatEntryToService(t *testing.T) {
	t.Parallel()

	w := wireChatEntry{
		Message: wireChatMessage{
			Role:    "assistant",
			Content: "Here is the answer.",
			ToolCalls: []wireToolCall{
				{
					ID:        "tc-1",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"path":"main.go"}`),
				},
			},
			ToolCallID: "",
		},
		Timestamp:  testTime,
		Reasoning:  "I need to read the file first.",
		ClaudeMeta: "operator · claude-sonnet-4-6",
	}

	data, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded wireChatEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := wireChatEntryToService(decoded)

	if got.Message.Role != service.MessageRole("assistant") {
		t.Errorf("Message.Role = %q, want %q", got.Message.Role, "assistant")
	}
	if got.Message.Content != "Here is the answer." {
		t.Errorf("Message.Content = %q, want %q", got.Message.Content, "Here is the answer.")
	}
	if len(got.Message.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(got.Message.ToolCalls))
	}
	if got.Message.ToolCalls[0].ID != "tc-1" {
		t.Errorf("ToolCalls[0].ID = %q, want %q", got.Message.ToolCalls[0].ID, "tc-1")
	}
	if got.Message.ToolCalls[0].Name != "read_file" {
		t.Errorf("ToolCalls[0].Name = %q, want %q", got.Message.ToolCalls[0].Name, "read_file")
	}
	if string(got.Message.ToolCalls[0].Arguments) != `{"path":"main.go"}` {
		t.Errorf("ToolCalls[0].Arguments = %s, want %s", got.Message.ToolCalls[0].Arguments, `{"path":"main.go"}`)
	}
	if !got.Timestamp.Equal(testTime) {
		t.Errorf("Timestamp = %v, want %v", got.Timestamp, testTime)
	}
	if got.Reasoning != "I need to read the file first." {
		t.Errorf("Reasoning = %q, want %q", got.Reasoning, "I need to read the file first.")
	}
	if got.ClaudeMeta != "operator · claude-sonnet-4-6" {
		t.Errorf("ClaudeMeta = %q, want %q", got.ClaudeMeta, "operator · claude-sonnet-4-6")
	}
}

func TestWireProgressStateToService(t *testing.T) {
	t.Parallel()

	w := wireProgressState{
		Jobs: []wireJob{
			{ID: "job-1", Title: "Job 1", Status: "active", CreatedAt: testTime, UpdatedAt: testTime},
		},
		Tasks: map[string][]wireTask{
			"job-1": {
				{ID: "task-1", JobID: "job-1", Title: "Task 1", Status: "pending", CreatedAt: testTime, UpdatedAt: testTime},
			},
		},
		Reports: map[string][]wireProgressReport{
			"job-1": {
				{ID: 1, JobID: "job-1", Status: "in_progress", Message: "Working on it", CreatedAt: testTime},
			},
		},
		ActiveSessions: []wireWorkerSession{
			{ID: "sess-1", WorkerID: "agent-1", Status: "active", StartedAt: testTime, TokensIn: 100, TokensOut: 200},
		},
		LiveSnapshots: []wireSessionSnapshot{
			{ID: "snap-1", WorkerID: "agent-1", Status: "active", StartTime: testTime, TokensIn: 50, TokensOut: 75},
		},
		GraphNodes: []wireGraphNode{
			{SessionID: "graph:task-1:plan", JobID: "job-1", TaskID: "task-1", Node: "plan", StartedAt: testTime},
		},
		FeedEntries: []wireFeedEntry{
			{ID: 1, EntryType: "system_event", Content: "Job started", CreatedAt: testTime},
		},
	}

	data, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded wireProgressState
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := wireProgressStateToService(decoded)

	if len(got.Jobs) != 1 || got.Jobs[0].ID != "job-1" {
		t.Errorf("Jobs = %v, want 1 job with ID job-1", got.Jobs)
	}
	if tasks, ok := got.Tasks["job-1"]; !ok || len(tasks) != 1 || tasks[0].ID != "task-1" {
		t.Errorf("Tasks[job-1] = %v, want 1 task with ID task-1", got.Tasks["job-1"])
	}
	if reports, ok := got.Reports["job-1"]; !ok || len(reports) != 1 || reports[0].Message != "Working on it" {
		t.Errorf("Reports[job-1] = %v, want 1 report", got.Reports["job-1"])
	}
	if len(got.ActiveSessions) != 1 || got.ActiveSessions[0].ID != "sess-1" {
		t.Errorf("ActiveSessions = %v, want 1 session", got.ActiveSessions)
	}
	if len(got.LiveSnapshots) != 1 || got.LiveSnapshots[0].ID != "snap-1" {
		t.Errorf("LiveSnapshots = %v, want 1 snapshot", got.LiveSnapshots)
	}
	if len(got.ActiveGraphNodes) != 1 || got.ActiveGraphNodes[0].SessionID != "graph:task-1:plan" {
		t.Errorf("ActiveGraphNodes = %v, want 1 graph node", got.ActiveGraphNodes)
	}
	if len(got.ActiveGraphNodes) == 1 && got.ActiveGraphNodes[0].Node != "plan" {
		t.Errorf("graph node Node = %q, want plan", got.ActiveGraphNodes[0].Node)
	}
	if len(got.FeedEntries) != 1 || got.FeedEntries[0].Content != "Job started" {
		t.Errorf("FeedEntries = %v, want 1 entry", got.FeedEntries)
	}
}

func TestWireProgressStateToService_EmptyMaps(t *testing.T) {
	t.Parallel()

	w := wireProgressState{
		Jobs:           nil,
		Tasks:          nil,
		Reports:        nil,
		ActiveSessions: nil,
		LiveSnapshots:  nil,
		FeedEntries:    nil,
	}

	got := wireProgressStateToService(w)

	if got.Jobs == nil {
		t.Error("Jobs should be non-nil empty slice")
	}
	if len(got.Jobs) != 0 {
		t.Errorf("Jobs len = %d, want 0", len(got.Jobs))
	}
	if got.Tasks == nil {
		t.Error("Tasks should be non-nil empty map")
	}
	if got.Reports == nil {
		t.Error("Reports should be non-nil empty map")
	}
	if got.ActiveSessions == nil {
		t.Error("ActiveSessions should be non-nil empty slice")
	}
	if got.LiveSnapshots == nil {
		t.Error("LiveSnapshots should be non-nil empty slice")
	}
	if got.FeedEntries == nil {
		t.Error("FeedEntries should be non-nil empty slice")
	}
}

func TestWireWorkerSessionToService(t *testing.T) {
	t.Parallel()

	endedAt := testTime.Add(5 * time.Minute)
	cost := 0.0042
	w := wireWorkerSession{
		ID:        "sess-1",
		WorkerID:  "agent-1",
		JobID:     "job-1",
		TaskID:    "task-1",
		Status:    "completed",
		Model:     "claude-sonnet-4-6",
		Provider:  "anthropic",
		TokensIn:  1500,
		TokensOut: 3200,
		StartedAt: testTime,
		EndedAt:   &endedAt,
		CostUSD:   &cost,
	}

	data, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded wireWorkerSession
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := wireWorkerSessionToService(decoded)

	if got.ID != "sess-1" {
		t.Errorf("ID = %q, want %q", got.ID, "sess-1")
	}
	if got.Status != service.SessionStatus("completed") {
		t.Errorf("Status = %q, want %q", got.Status, "completed")
	}
	if got.TokensIn != 1500 {
		t.Errorf("TokensIn = %d, want 1500", got.TokensIn)
	}
	if got.TokensOut != 3200 {
		t.Errorf("TokensOut = %d, want 3200", got.TokensOut)
	}
	if got.EndedAt == nil {
		t.Fatal("EndedAt is nil, want non-nil")
	}
	if !got.EndedAt.Equal(endedAt) {
		t.Errorf("EndedAt = %v, want %v", *got.EndedAt, endedAt)
	}
	if got.CostUSD == nil {
		t.Fatal("CostUSD is nil, want non-nil")
	}
	if *got.CostUSD != 0.0042 {
		t.Errorf("CostUSD = %f, want 0.0042", *got.CostUSD)
	}
	if !got.StartedAt.Equal(testTime) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, testTime)
	}
}

func TestWireWorkerSessionToService_NilOptionalFields(t *testing.T) {
	t.Parallel()

	w := wireWorkerSession{
		ID:        "sess-2",
		WorkerID:  "agent-2",
		Status:    "active",
		StartedAt: testTime,
	}

	got := wireWorkerSessionToService(w)

	if got.EndedAt != nil {
		t.Errorf("EndedAt = %v, want nil", got.EndedAt)
	}
	if got.CostUSD != nil {
		t.Errorf("CostUSD = %v, want nil", got.CostUSD)
	}
}

func TestWireFeedEntryToService(t *testing.T) {
	t.Parallel()

	w := wireFeedEntry{
		ID:        42,
		JobID:     "job-1",
		EntryType: "task_completed",
		Content:   "Task finished successfully",
		Metadata:  json.RawMessage(`{"duration_ms":1234}`),
		CreatedAt: testTime,
	}

	data, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded wireFeedEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := wireFeedEntryToService(decoded)

	if got.ID != 42 {
		t.Errorf("ID = %d, want 42", got.ID)
	}
	if got.JobID != "job-1" {
		t.Errorf("JobID = %q, want %q", got.JobID, "job-1")
	}
	if got.EntryType != service.FeedEntryType("task_completed") {
		t.Errorf("EntryType = %q, want %q", got.EntryType, "task_completed")
	}
	if got.Content != "Task finished successfully" {
		t.Errorf("Content = %q, want %q", got.Content, "Task finished successfully")
	}
	if string(got.Metadata) != `{"duration_ms":1234}` {
		t.Errorf("Metadata = %s, want %s", got.Metadata, `{"duration_ms":1234}`)
	}
	if !got.CreatedAt.Equal(testTime) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, testTime)
	}
}

// ---------------------------------------------------------------------------
// ParseSSEPayload tests — all 19 event types
// ---------------------------------------------------------------------------

func TestParseSSEPayload_OperatorText(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"text":"Hello world","reasoning":"thinking..."}`)
	payload, err := ParseSSEPayload("operator.text", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.OperatorTextPayload)
	if !ok {
		t.Fatalf("payload type = %T, want OperatorTextPayload", payload)
	}
	if p.Text != "Hello world" {
		t.Errorf("Text = %q, want %q", p.Text, "Hello world")
	}
	if p.Reasoning != "thinking..." {
		t.Errorf("Reasoning = %q, want %q", p.Reasoning, "thinking...")
	}
}

func TestParseSSEPayload_OperatorDone(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"model_name":"claude-sonnet-4-6","tokens_in":100,"tokens_out":200,"reasoning_tokens":50,"context_tokens":4096}`)
	payload, err := ParseSSEPayload("operator.done", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.OperatorDonePayload)
	if !ok {
		t.Fatalf("payload type = %T, want OperatorDonePayload", payload)
	}
	if p.ModelName != "claude-sonnet-4-6" {
		t.Errorf("ModelName = %q, want %q", p.ModelName, "claude-sonnet-4-6")
	}
	if p.TokensIn != 100 {
		t.Errorf("TokensIn = %d, want 100", p.TokensIn)
	}
	if p.TokensOut != 200 {
		t.Errorf("TokensOut = %d, want 200", p.TokensOut)
	}
	if p.ReasoningTokens != 50 {
		t.Errorf("ReasoningTokens = %d, want 50", p.ReasoningTokens)
	}
	if p.ContextTokens != 4096 {
		t.Errorf("ContextTokens = %d, want 4096", p.ContextTokens)
	}
}

func TestParseSSEPayload_SessionContext(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"session_id":"graph:task-1:implement","context_tokens":8192}`)
	payload, err := ParseSSEPayload("session.context", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.SessionContextPayload)
	if !ok {
		t.Fatalf("payload type = %T, want SessionContextPayload", payload)
	}
	if p.SessionID != "graph:task-1:implement" {
		t.Errorf("SessionID = %q, want %q", p.SessionID, "graph:task-1:implement")
	}
	if p.ContextTokens != 8192 {
		t.Errorf("ContextTokens = %d, want 8192", p.ContextTokens)
	}
}

func TestParseSSEPayload_BlockerAdded(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"request_id":"req-1",
		"source":"graph:investigate",
		"job_id":"job-1",
		"task_id":"task-1",
		"questions":[{"question":"What should I do?","options":["yes","no"]}],
		"created_at":"2026-06-06T00:00:00Z"
	}`)
	payload, err := ParseSSEPayload("blocker.added", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.Blocker)
	if !ok {
		t.Fatalf("payload type = %T, want service.Blocker", payload)
	}
	if p.RequestID != "req-1" {
		t.Errorf("RequestID = %q, want %q", p.RequestID, "req-1")
	}
	if p.JobID != "job-1" || p.TaskID != "task-1" {
		t.Errorf("job/task = %q/%q, want job-1/task-1", p.JobID, p.TaskID)
	}
	if len(p.Questions) != 1 || p.Questions[0].Question != "What should I do?" {
		t.Errorf("Questions = %v, want one question", p.Questions)
	}
	if len(p.Questions) == 1 && (len(p.Questions[0].Options) != 2 || p.Questions[0].Options[0] != "yes") {
		t.Errorf("Options = %v, want [yes no]", p.Questions[0].Options)
	}
	if p.Source != "graph:investigate" {
		t.Errorf("Source = %q, want %q", p.Source, "graph:investigate")
	}
}

func TestParseSSEPayload_BlockerResolved(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"request_id":"req-1"}`)
	payload, err := ParseSSEPayload("blocker.resolved", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p, ok := payload.(service.BlockerResolvedPayload)
	if !ok {
		t.Fatalf("payload type = %T, want service.BlockerResolvedPayload", payload)
	}
	if p.RequestID != "req-1" {
		t.Errorf("RequestID = %q, want %q", p.RequestID, "req-1")
	}
}

func TestParseSSEPayload_TaskAssigned(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"task_id":"task-1","job_id":"job-1","graph_id":"team-1","title":"Write tests"}`)
	payload, err := ParseSSEPayload("task.assigned", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.TaskAssignedPayload)
	if !ok {
		t.Fatalf("payload type = %T, want TaskAssignedPayload", payload)
	}
	if p.TaskID != "task-1" {
		t.Errorf("TaskID = %q, want %q", p.TaskID, "task-1")
	}
	if p.JobID != "job-1" {
		t.Errorf("JobID = %q, want %q", p.JobID, "job-1")
	}
	if p.GraphID != "team-1" {
		t.Errorf("GraphID = %q, want %q", p.GraphID, "team-1")
	}
	if p.Title != "Write tests" {
		t.Errorf("Title = %q, want %q", p.Title, "Write tests")
	}
}

func TestParseSSEPayload_TaskStarted(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"task_id":"task-1","job_id":"job-1","graph_id":"team-1","title":"Write tests"}`)
	payload, err := ParseSSEPayload("task.started", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.TaskStartedPayload)
	if !ok {
		t.Fatalf("payload type = %T, want TaskStartedPayload", payload)
	}
	if p.TaskID != "task-1" {
		t.Errorf("TaskID = %q, want %q", p.TaskID, "task-1")
	}
	if p.JobID != "job-1" {
		t.Errorf("JobID = %q, want %q", p.JobID, "job-1")
	}
	if p.GraphID != "team-1" {
		t.Errorf("GraphID = %q, want %q", p.GraphID, "team-1")
	}
	if p.Title != "Write tests" {
		t.Errorf("Title = %q, want %q", p.Title, "Write tests")
	}
}

func TestParseSSEPayload_TaskCompleted(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"task_id":"task-1","job_id":"job-1","graph_id":"team-1","summary":"All done","recommendations":"Ship it","has_next_task":true}`)
	payload, err := ParseSSEPayload("task.completed", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.TaskCompletedPayload)
	if !ok {
		t.Fatalf("payload type = %T, want TaskCompletedPayload", payload)
	}
	if p.TaskID != "task-1" {
		t.Errorf("TaskID = %q, want %q", p.TaskID, "task-1")
	}
	if p.Summary != "All done" {
		t.Errorf("Summary = %q, want %q", p.Summary, "All done")
	}
	if p.Recommendations != "Ship it" {
		t.Errorf("Recommendations = %q, want %q", p.Recommendations, "Ship it")
	}
	if !p.HasNextTask {
		t.Error("HasNextTask = false, want true")
	}
}

func TestParseSSEPayload_TaskFailed(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"task_id":"task-1","job_id":"job-1","graph_id":"team-1","error":"compilation failed"}`)
	payload, err := ParseSSEPayload("task.failed", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.TaskFailedPayload)
	if !ok {
		t.Fatalf("payload type = %T, want TaskFailedPayload", payload)
	}
	if p.TaskID != "task-1" {
		t.Errorf("TaskID = %q, want %q", p.TaskID, "task-1")
	}
	if p.Error != "compilation failed" {
		t.Errorf("Error = %q, want %q", p.Error, "compilation failed")
	}
}

func TestParseSSEPayload_JobCompleted(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"job_id":"job-1","title":"Fix bug","summary":"Bug fixed and tests added"}`)
	payload, err := ParseSSEPayload("job.completed", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.JobCompletedPayload)
	if !ok {
		t.Fatalf("payload type = %T, want JobCompletedPayload", payload)
	}
	if p.JobID != "job-1" {
		t.Errorf("JobID = %q, want %q", p.JobID, "job-1")
	}
	if p.Title != "Fix bug" {
		t.Errorf("Title = %q, want %q", p.Title, "Fix bug")
	}
	if p.Summary != "Bug fixed and tests added" {
		t.Errorf("Summary = %q, want %q", p.Summary, "Bug fixed and tests added")
	}
}

func TestParseSSEPayload_ProgressUpdate(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"state":{
			"jobs":[{"id":"job-1","title":"J1","status":"active","created_at":"2026-03-02T12:00:00Z","updated_at":"2026-03-02T12:00:00Z"}],
			"tasks":{"job-1":[{"id":"task-1","job_id":"job-1","title":"T1","status":"pending","created_at":"2026-03-02T12:00:00Z","updated_at":"2026-03-02T12:00:00Z"}]},
			"reports":{},
			"active_sessions":[],
			"live_snapshots":[],
			"feed_entries":[]
		}
	}`)
	payload, err := ParseSSEPayload("progress.update", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.ProgressUpdatePayload)
	if !ok {
		t.Fatalf("payload type = %T, want ProgressUpdatePayload", payload)
	}
	if len(p.State.Jobs) != 1 {
		t.Fatalf("State.Jobs len = %d, want 1", len(p.State.Jobs))
	}
	if p.State.Jobs[0].ID != "job-1" {
		t.Errorf("State.Jobs[0].ID = %q, want %q", p.State.Jobs[0].ID, "job-1")
	}
	if tasks, ok := p.State.Tasks["job-1"]; !ok || len(tasks) != 1 {
		t.Errorf("State.Tasks[job-1] len = %d, want 1", len(p.State.Tasks["job-1"]))
	}
}

func TestParseSSEPayload_SessionStarted(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"session_id":"sess-1",
		"worker_name":"test-writer",
		"task":"Write unit tests",
		"job_id":"job-1",
		"task_id":"task-1",
		"system_prompt":"You write tests.",
		"initial_message":"Write tests for foo.go"
	}`)
	payload, err := ParseSSEPayload("session.started", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.SessionStartedPayload)
	if !ok {
		t.Fatalf("payload type = %T, want SessionStartedPayload", payload)
	}
	if p.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", p.SessionID, "sess-1")
	}
	if p.WorkerName != "test-writer" {
		t.Errorf("WorkerName = %q, want %q", p.WorkerName, "test-writer")
	}
	if p.Task != "Write unit tests" {
		t.Errorf("Task = %q, want %q", p.Task, "Write unit tests")
	}
	if p.SystemPrompt != "You write tests." {
		t.Errorf("SystemPrompt = %q, want %q", p.SystemPrompt, "You write tests.")
	}
	if p.InitialMessage != "Write tests for foo.go" {
		t.Errorf("InitialMessage = %q, want %q", p.InitialMessage, "Write tests for foo.go")
	}
}

func TestParseSSEPayload_SessionText(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"text":"func TestFoo(t *testing.T) {"}`)
	payload, err := ParseSSEPayload("session.text", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.SessionTextPayload)
	if !ok {
		t.Fatalf("payload type = %T, want SessionTextPayload", payload)
	}
	if p.Text != "func TestFoo(t *testing.T) {" {
		t.Errorf("Text = %q, want %q", p.Text, "func TestFoo(t *testing.T) {")
	}
}

func TestParseSSEPayload_SessionToolCall(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"tool_call":{"id":"tc-1","name":"read_file","arguments":{"path":"main.go"}}}`)
	payload, err := ParseSSEPayload("session.tool_call", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.SessionToolCallPayload)
	if !ok {
		t.Fatalf("payload type = %T, want SessionToolCallPayload", payload)
	}
	if p.ToolCall.ID != "tc-1" {
		t.Errorf("ToolCall.ID = %q, want %q", p.ToolCall.ID, "tc-1")
	}
	if p.ToolCall.Name != "read_file" {
		t.Errorf("ToolCall.Name = %q, want %q", p.ToolCall.Name, "read_file")
	}
}

func TestParseSSEPayload_SessionToolResult(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"result":{"call_id":"tc-1","name":"read_file","result":"file contents here","error":""}}`)
	payload, err := ParseSSEPayload("session.tool_result", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.SessionToolResultPayload)
	if !ok {
		t.Fatalf("payload type = %T, want SessionToolResultPayload", payload)
	}
	if p.Result.CallID != "tc-1" {
		t.Errorf("Result.CallID = %q, want %q", p.Result.CallID, "tc-1")
	}
	if p.Result.Name != "read_file" {
		t.Errorf("Result.Name = %q, want %q", p.Result.Name, "read_file")
	}
	if p.Result.Result != "file contents here" {
		t.Errorf("Result.Result = %q, want %q", p.Result.Result, "file contents here")
	}
	if p.Result.Error != "" {
		t.Errorf("Result.Error = %q, want empty", p.Result.Error)
	}
}

func TestParseSSEPayload_SessionFileChange(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"tool_name":"edit_file","path":"internal/foo.go","diff":"@@ -1 +1 @@\n-old\n+new\n","added":1,"removed":1,"created":true,"truncated":true}`)
	payload, err := ParseSSEPayload("session.file_change", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.SessionFileChangePayload)
	if !ok {
		t.Fatalf("payload type = %T, want SessionFileChangePayload", payload)
	}
	if p.ToolName != "edit_file" {
		t.Errorf("ToolName = %q, want %q", p.ToolName, "edit_file")
	}
	if p.Path != "internal/foo.go" {
		t.Errorf("Path = %q, want %q", p.Path, "internal/foo.go")
	}
	if p.Diff != "@@ -1 +1 @@\n-old\n+new\n" {
		t.Errorf("Diff = %q, unexpected", p.Diff)
	}
	if p.Added != 1 {
		t.Errorf("Added = %d, want 1", p.Added)
	}
	if p.Removed != 1 {
		t.Errorf("Removed = %d, want 1", p.Removed)
	}
	if !p.Created {
		t.Errorf("Created = false, want true")
	}
	if !p.Truncated {
		t.Errorf("Truncated = false, want true")
	}
}

func TestParseSSEPayload_SessionShellExec(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"command":"go test ./...","exit_code":2,"duration_ms":1234,"output_bytes":9000,"truncated":true,"timed_out":false}`)
	payload, err := ParseSSEPayload("session.shell_exec", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.SessionShellExecPayload)
	if !ok {
		t.Fatalf("payload type = %T, want SessionShellExecPayload", payload)
	}
	if p.Command != "go test ./..." {
		t.Errorf("Command = %q, want %q", p.Command, "go test ./...")
	}
	if p.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2", p.ExitCode)
	}
	if p.DurationMs != 1234 {
		t.Errorf("DurationMs = %d, want 1234", p.DurationMs)
	}
	if p.OutputBytes != 9000 {
		t.Errorf("OutputBytes = %d, want 9000", p.OutputBytes)
	}
	if !p.Truncated {
		t.Errorf("Truncated = false, want true")
	}
	if p.TimedOut {
		t.Errorf("TimedOut = true, want false")
	}
}

func TestParseSSEPayload_SessionWorkerSpawn(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"role":"coder","task":"implement the thing","job_id":"job-1","depth":2,"failed":true,"error":"role not found"}`)
	payload, err := ParseSSEPayload("session.worker_spawn", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.SessionWorkerSpawnPayload)
	if !ok {
		t.Fatalf("payload type = %T, want SessionWorkerSpawnPayload", payload)
	}
	if p.Role != "coder" {
		t.Errorf("Role = %q, want %q", p.Role, "coder")
	}
	if p.Task != "implement the thing" {
		t.Errorf("Task = %q, want %q", p.Task, "implement the thing")
	}
	if p.JobID != "job-1" {
		t.Errorf("JobID = %q, want %q", p.JobID, "job-1")
	}
	if p.Depth != 2 {
		t.Errorf("Depth = %d, want 2", p.Depth)
	}
	if !p.Failed {
		t.Errorf("Failed = false, want true")
	}
	if p.Error != "role not found" {
		t.Errorf("Error = %q, want %q", p.Error, "role not found")
	}
}

func TestParseSSEPayload_SessionDone(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"worker_name":"test-writer","job_id":"job-1","task_id":"task-1","status":"completed","final_text":"All tests pass."}`)
	payload, err := ParseSSEPayload("session.done", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.SessionDonePayload)
	if !ok {
		t.Fatalf("payload type = %T, want SessionDonePayload", payload)
	}
	if p.WorkerName != "test-writer" {
		t.Errorf("WorkerName = %q, want %q", p.WorkerName, "test-writer")
	}
	if p.JobID != "job-1" {
		t.Errorf("JobID = %q, want %q", p.JobID, "job-1")
	}
	if p.TaskID != "task-1" {
		t.Errorf("TaskID = %q, want %q", p.TaskID, "task-1")
	}
	if p.Status != "completed" {
		t.Errorf("Status = %q, want %q", p.Status, "completed")
	}
	if p.FinalText != "All tests pass." {
		t.Errorf("FinalText = %q, want %q", p.FinalText, "All tests pass.")
	}
}

func TestParseSSEPayload_DefinitionsReloaded(t *testing.T) {
	t.Parallel()

	// definitions.reloaded has no payload.
	payload, err := ParseSSEPayload("definitions.reloaded", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if payload != nil {
		t.Errorf("payload = %v, want nil", payload)
	}

	// Also test with explicit null.
	payload2, err := ParseSSEPayload("definitions.reloaded", json.RawMessage(`null`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if payload2 != nil {
		t.Errorf("payload = %v, want nil", payload2)
	}
}

func TestParseSSEPayload_OperationCompleted(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"kind":"generate_skill",
		"result":{
			"operation_id":"op-1",
			"content":"---\nname: test-skill\n---\nYou are a skill.",
			"error":""
		}
	}`)
	payload, err := ParseSSEPayload("operation.completed", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.OperationCompletedPayload)
	if !ok {
		t.Fatalf("payload type = %T, want OperationCompletedPayload", payload)
	}
	if p.Kind != "generate_skill" {
		t.Errorf("Kind = %q, want %q", p.Kind, "generate_skill")
	}
	if p.Result.OperationID != "op-1" {
		t.Errorf("Result.OperationID = %q, want %q", p.Result.OperationID, "op-1")
	}
	if p.Result.Content == "" {
		t.Error("Result.Content is empty, want non-empty")
	}
}

func TestParseSSEPayload_OperationFailed(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"kind":"generate_team","error":"LLM rate limited"}`)
	payload, err := ParseSSEPayload("operation.failed", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.OperationFailedPayload)
	if !ok {
		t.Fatalf("payload type = %T, want OperationFailedPayload", payload)
	}
	if p.Kind != "generate_team" {
		t.Errorf("Kind = %q, want %q", p.Kind, "generate_team")
	}
	if p.Error != "LLM rate limited" {
		t.Errorf("Error = %q, want %q", p.Error, "LLM rate limited")
	}
}

func TestParseSSEPayload_Heartbeat(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"server_time":"2026-03-02T12:00:00Z"}`)
	payload, err := ParseSSEPayload("heartbeat", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p, ok := payload.(service.HeartbeatPayload)
	if !ok {
		t.Fatalf("payload type = %T, want HeartbeatPayload", payload)
	}
	if !p.ServerTime.Equal(testTime) {
		t.Errorf("ServerTime = %v, want %v", p.ServerTime, testTime)
	}
}

// ---------------------------------------------------------------------------
// ParseSSEPayload edge cases
// ---------------------------------------------------------------------------

func TestParseSSEPayload_UnknownEventType(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"foo":"bar"}`)
	payload, err := ParseSSEPayload("unknown.event.type", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if payload != nil {
		t.Errorf("payload = %v, want nil for unknown event type", payload)
	}
}

func TestParseSSEPayload_NullPayload(t *testing.T) {
	t.Parallel()

	payload, err := ParseSSEPayload("operator.text", json.RawMessage(`null`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if payload != nil {
		t.Errorf("payload = %v, want nil for null payload", payload)
	}
}

func TestParseSSEPayload_EmptyRaw(t *testing.T) {
	t.Parallel()

	payload, err := ParseSSEPayload("operator.text", json.RawMessage{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if payload != nil {
		t.Errorf("payload = %v, want nil for empty raw", payload)
	}
}

func TestParseSSEPayload_NilRaw(t *testing.T) {
	t.Parallel()

	payload, err := ParseSSEPayload("operator.text", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if payload != nil {
		t.Errorf("payload = %v, want nil for nil raw", payload)
	}
}

func TestParseSSEPayload_InvalidJSON(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{invalid json}`)
	_, err := ParseSSEPayload("operator.text", raw)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

// ---------------------------------------------------------------------------
// Table-driven ParseSSEPayload test covering all 19 types
// ---------------------------------------------------------------------------

func TestParseSSEPayload_AllEventTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		eventType string
		raw       json.RawMessage
		wantType  string // expected Go type name
		wantNil   bool   // if true, expect nil payload
	}{
		{
			name:      "operator.text",
			eventType: "operator.text",
			raw:       json.RawMessage(`{"text":"hi"}`),
			wantType:  "service.OperatorTextPayload",
		},
		{
			name:      "operator.done",
			eventType: "operator.done",
			raw:       json.RawMessage(`{"model_name":"m","tokens_in":1,"tokens_out":2,"reasoning_tokens":0}`),
			wantType:  "service.OperatorDonePayload",
		},
		{
			name:      "blocker.added",
			eventType: "blocker.added",
			raw:       json.RawMessage(`{"request_id":"r","questions":[{"question":"q"}]}`),
			wantType:  "service.Blocker",
		},
		{
			name:      "blocker.resolved",
			eventType: "blocker.resolved",
			raw:       json.RawMessage(`{"request_id":"r"}`),
			wantType:  "service.BlockerResolvedPayload",
		},
		{
			name:      "task.assigned",
			eventType: "task.assigned",
			raw:       json.RawMessage(`{"task_id":"t","job_id":"j","graph_id":"tm","title":"T"}`),
			wantType:  "service.TaskAssignedPayload",
		},
		{
			name:      "task.started",
			eventType: "task.started",
			raw:       json.RawMessage(`{"task_id":"t","job_id":"j","graph_id":"tm","title":"T"}`),
			wantType:  "service.TaskStartedPayload",
		},
		{
			name:      "task.completed",
			eventType: "task.completed",
			raw:       json.RawMessage(`{"task_id":"t","job_id":"j","graph_id":"tm","summary":"s","has_next_task":false}`),
			wantType:  "service.TaskCompletedPayload",
		},
		{
			name:      "task.failed",
			eventType: "task.failed",
			raw:       json.RawMessage(`{"task_id":"t","job_id":"j","graph_id":"tm","error":"e"}`),
			wantType:  "service.TaskFailedPayload",
		},
		{
			name:      "job.completed",
			eventType: "job.completed",
			raw:       json.RawMessage(`{"job_id":"j","title":"T","summary":"s"}`),
			wantType:  "service.JobCompletedPayload",
		},
		{
			name:      "progress.update",
			eventType: "progress.update",
			raw:       json.RawMessage(`{"state":{"jobs":[],"tasks":{},"reports":{},"active_sessions":[],"live_snapshots":[],"feed_entries":[]}}`),
			wantType:  "service.ProgressUpdatePayload",
		},
		{
			name:      "session.started",
			eventType: "session.started",
			raw:       json.RawMessage(`{"session_id":"s","worker_name":"a"}`),
			wantType:  "service.SessionStartedPayload",
		},
		{
			name:      "session.text",
			eventType: "session.text",
			raw:       json.RawMessage(`{"text":"hello"}`),
			wantType:  "service.SessionTextPayload",
		},
		{
			name:      "session.tool_call",
			eventType: "session.tool_call",
			raw:       json.RawMessage(`{"tool_call":{"id":"tc","name":"n","arguments":{}}}`),
			wantType:  "service.SessionToolCallPayload",
		},
		{
			name:      "session.tool_result",
			eventType: "session.tool_result",
			raw:       json.RawMessage(`{"result":{"call_id":"c","name":"n","result":"r"}}`),
			wantType:  "service.SessionToolResultPayload",
		},
		{
			name:      "session.done",
			eventType: "session.done",
			raw:       json.RawMessage(`{"worker_name":"a","status":"completed"}`),
			wantType:  "service.SessionDonePayload",
		},
		{
			name:      "definitions.reloaded",
			eventType: "definitions.reloaded",
			raw:       nil,
			wantNil:   true,
		},
		{
			name:      "operation.completed",
			eventType: "operation.completed",
			raw:       json.RawMessage(`{"kind":"k","result":{"operation_id":"o"}}`),
			wantType:  "service.OperationCompletedPayload",
		},
		{
			name:      "operation.failed",
			eventType: "operation.failed",
			raw:       json.RawMessage(`{"kind":"k","error":"e"}`),
			wantType:  "service.OperationFailedPayload",
		},
		{
			name:      "heartbeat",
			eventType: "heartbeat",
			raw:       json.RawMessage(`{"server_time":"2026-03-02T12:00:00Z"}`),
			wantType:  "service.HeartbeatPayload",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			payload, err := ParseSSEPayload(tt.eventType, tt.raw)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantNil {
				if payload != nil {
					t.Errorf("payload = %v (%T), want nil", payload, payload)
				}
				return
			}

			if payload == nil {
				t.Fatalf("payload is nil, want %s", tt.wantType)
			}

			// Verify the payload is the expected type by checking it's not nil.
			// The individual tests above verify field values; this table test
			// ensures all 19 types parse without error and return non-nil.
			gotType := typeString(payload)
			if gotType != tt.wantType {
				t.Errorf("payload type = %s, want %s", gotType, tt.wantType)
			}
		})
	}
}

// typeString returns a readable type string for payload type assertions.
func typeString(v any) string {
	switch v.(type) {
	case service.OperatorTextPayload:
		return "service.OperatorTextPayload"
	case service.OperatorDonePayload:
		return "service.OperatorDonePayload"
	case service.Blocker:
		return "service.Blocker"
	case service.BlockerResolvedPayload:
		return "service.BlockerResolvedPayload"
	case service.TaskAssignedPayload:
		return "service.TaskAssignedPayload"
	case service.TaskStartedPayload:
		return "service.TaskStartedPayload"
	case service.TaskCompletedPayload:
		return "service.TaskCompletedPayload"
	case service.TaskFailedPayload:
		return "service.TaskFailedPayload"
	case service.JobCompletedPayload:
		return "service.JobCompletedPayload"
	case service.ProgressUpdatePayload:
		return "service.ProgressUpdatePayload"
	case service.SessionStartedPayload:
		return "service.SessionStartedPayload"
	case service.SessionTextPayload:
		return "service.SessionTextPayload"
	case service.SessionToolCallPayload:
		return "service.SessionToolCallPayload"
	case service.SessionToolResultPayload:
		return "service.SessionToolResultPayload"
	case service.SessionDonePayload:
		return "service.SessionDonePayload"
	case service.OperationCompletedPayload:
		return "service.OperationCompletedPayload"
	case service.OperationFailedPayload:
		return "service.OperationFailedPayload"
	case service.HeartbeatPayload:
		return "service.HeartbeatPayload"
	default:
		return "unknown"
	}
}

// TestParseSSEPayload_OperatorCompaction verifies the compaction payload
// survives the wire round trip (server encode shape → client decode).
func TestParseSSEPayload_OperatorCompaction(t *testing.T) {
	t.Parallel()

	raw := `{"before_tokens":5200,"estimated_after_tokens":1800,"archive_file":"operator-2026-07-02T12-00-00Z.json"}`
	got, err := ParseSSEPayload(string(service.EventTypeOperatorCompaction), []byte(raw))
	if err != nil {
		t.Fatalf("ParseSSEPayload: %v", err)
	}
	p, ok := got.(service.OperatorCompactionPayload)
	if !ok {
		t.Fatalf("payload type = %T, want OperatorCompactionPayload", got)
	}
	if p.BeforeTokens != 5200 || p.EstimatedAfterTokens != 1800 {
		t.Errorf("tokens = %d/%d, want 5200/1800", p.BeforeTokens, p.EstimatedAfterTokens)
	}
	if p.ArchiveFile != "operator-2026-07-02T12-00-00Z.json" {
		t.Errorf("ArchiveFile = %q", p.ArchiveFile)
	}
}

// TestOperatorCompaction_WireRoundTrip runs the compaction payload through
// the REAL server-side encoder (server.EventPayloadToWire) and back through
// the client decoder, so a struct-tag typo on either side fails here rather
// than shipping as a silently dropped field.
func TestOperatorCompaction_WireRoundTrip(t *testing.T) {
	t.Parallel()

	in := service.OperatorCompactionPayload{
		BeforeTokens:         5200,
		EstimatedAfterTokens: 1800,
		ArchiveFile:          "operator-2026-07-02T12-00-00Z.json",
	}
	wire := server.EventPayloadToWire(service.Event{
		Type:    service.EventTypeOperatorCompaction,
		Payload: in,
	})
	data, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal wire payload: %v", err)
	}
	got, err := ParseSSEPayload(string(service.EventTypeOperatorCompaction), data)
	if err != nil {
		t.Fatalf("ParseSSEPayload: %v", err)
	}
	if got != in {
		t.Errorf("round trip = %+v, want %+v", got, in)
	}
}

// TestSessionCompaction_WireRoundTrip runs the worker compaction payload
// through the real server-side encoder and back through the client decoder.
func TestSessionCompaction_WireRoundTrip(t *testing.T) {
	t.Parallel()

	in := service.SessionCompactionPayload{
		SessionID:            "sess-1",
		Tier:                 2,
		BeforeTokens:         41000,
		EstimatedAfterTokens: 9000,
	}
	wire := server.EventPayloadToWire(service.Event{
		Type:    service.EventTypeSessionCompaction,
		Payload: in,
	})
	data, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal wire payload: %v", err)
	}
	got, err := ParseSSEPayload(string(service.EventTypeSessionCompaction), data)
	if err != nil {
		t.Fatalf("ParseSSEPayload: %v", err)
	}
	if got != in {
		t.Errorf("round trip = %+v, want %+v", got, in)
	}
}
