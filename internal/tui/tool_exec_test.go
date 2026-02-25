package tui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/llm"
	"github.com/jefflinse/toasters/internal/llm/tools"
)

func TestExecuteToolsCmd_BasicResults(t *testing.T) {
	t.Parallel()

	// Use a real ToolExecutor with no gateway — job_list with an empty workspace
	// returns an empty list, which is a valid non-error result.
	executor := tools.NewToolExecutor(nil, nil, t.TempDir())

	calls := []llm.ToolCall{
		{
			ID:   "call-1",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "job_list",
				Arguments: "{}",
			},
		},
	}

	ctx := context.Background()
	cmd := executeToolsCmd(calls, executor, ctx)
	msg := cmd()

	result, ok := msg.(ToolResultMsg)
	if !ok {
		t.Fatalf("expected ToolResultMsg, got %T", msg)
	}
	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}
	if result.Results[0].CallID != "call-1" {
		t.Errorf("CallID = %q, want %q", result.Results[0].CallID, "call-1")
	}
	if result.Results[0].Name != "job_list" {
		t.Errorf("Name = %q, want %q", result.Results[0].Name, "job_list")
	}
	if result.Results[0].Err != nil {
		t.Errorf("unexpected error: %v", result.Results[0].Err)
	}
}

func TestExecuteToolsCmd_MultipleTools(t *testing.T) {
	t.Parallel()

	executor := tools.NewToolExecutor(nil, nil, t.TempDir())

	calls := []llm.ToolCall{
		{
			ID:   "call-1",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "job_list",
				Arguments: "{}",
			},
		},
		{
			ID:   "call-2",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "job_list",
				Arguments: "{}",
			},
		},
	}

	ctx := context.Background()
	cmd := executeToolsCmd(calls, executor, ctx)
	msg := cmd()

	result, ok := msg.(ToolResultMsg)
	if !ok {
		t.Fatalf("expected ToolResultMsg, got %T", msg)
	}
	if len(result.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.Results))
	}
	// Verify ordering matches call ordering.
	if result.Results[0].CallID != "call-1" {
		t.Errorf("Results[0].CallID = %q, want %q", result.Results[0].CallID, "call-1")
	}
	if result.Results[1].CallID != "call-2" {
		t.Errorf("Results[1].CallID = %q, want %q", result.Results[1].CallID, "call-2")
	}
}

func TestExecuteToolsCmd_ErrorHandling(t *testing.T) {
	t.Parallel()

	executor := tools.NewToolExecutor(nil, nil, t.TempDir())

	// list_directory with a non-existent path should return an error.
	calls := []llm.ToolCall{
		{
			ID:   "call-err",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "list_directory",
				Arguments: `{"path":"/nonexistent/path/that/does/not/exist"}`,
			},
		},
	}

	ctx := context.Background()
	cmd := executeToolsCmd(calls, executor, ctx)
	msg := cmd()

	result, ok := msg.(ToolResultMsg)
	if !ok {
		t.Fatalf("expected ToolResultMsg, got %T", msg)
	}
	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}
	if result.Results[0].Err == nil {
		t.Error("expected error for non-existent directory, got nil")
	}
	if result.Results[0].CallID != "call-err" {
		t.Errorf("CallID = %q, want %q", result.Results[0].CallID, "call-err")
	}
}

func TestExecuteToolsCmd_Cancellation(t *testing.T) {
	t.Parallel()

	executor := tools.NewToolExecutor(nil, nil, t.TempDir())

	calls := []llm.ToolCall{
		{
			ID:   "call-1",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "job_list",
				Arguments: "{}",
			},
		},
		{
			ID:   "call-2",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "job_list",
				Arguments: "{}",
			},
		},
	}

	// Cancel the context before executing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cmd := executeToolsCmd(calls, executor, ctx)
	msg := cmd()

	result, ok := msg.(ToolResultMsg)
	if !ok {
		t.Fatalf("expected ToolResultMsg, got %T", msg)
	}
	// With pre-cancelled context, the first call should detect cancellation
	// and break, producing only one result with an error.
	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result (cancelled), got %d", len(result.Results))
	}
	if result.Results[0].Err == nil {
		t.Error("expected cancellation error, got nil")
	}
	if result.Results[0].CallID != "call-1" {
		t.Errorf("CallID = %q, want %q", result.Results[0].CallID, "call-1")
	}
}

func TestExecuteToolsCmd_ResultOrdering(t *testing.T) {
	t.Parallel()

	executor := tools.NewToolExecutor(nil, nil, t.TempDir())

	calls := []llm.ToolCall{
		{
			ID:   "alpha",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "job_list",
				Arguments: "{}",
			},
		},
		{
			ID:   "beta",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "job_list",
				Arguments: "{}",
			},
		},
		{
			ID:   "gamma",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "job_list",
				Arguments: "{}",
			},
		},
	}

	ctx := context.Background()
	cmd := executeToolsCmd(calls, executor, ctx)
	msg := cmd()

	result, ok := msg.(ToolResultMsg)
	if !ok {
		t.Fatalf("expected ToolResultMsg, got %T", msg)
	}
	if len(result.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result.Results))
	}
	expectedIDs := []string{"alpha", "beta", "gamma"}
	for i, want := range expectedIDs {
		if result.Results[i].CallID != want {
			t.Errorf("Results[%d].CallID = %q, want %q", i, result.Results[i].CallID, want)
		}
	}
}

func TestToolResultMsg_HandlerAppendsEntries(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	// Seed with a system message so messagesFromEntries has something.
	m.appendEntry(ChatEntry{
		Message:   llm.Message{Role: "system", Content: "system prompt"},
		Timestamp: time.Now(),
	})

	initialEntries := len(m.entries)

	msg := ToolResultMsg{
		Results: []ToolResult{
			{CallID: "call-1", Name: "job_list", Result: "[]"},
			{CallID: "call-2", Name: "list_directory", Result: "file.txt"},
		},
	}

	result, _ := m.Update(msg)
	got := result.(*Model)

	// Should have appended 2 tool result entries.
	expectedEntries := initialEntries + 2
	if len(got.entries) != expectedEntries {
		t.Errorf("entries count = %d, want %d", len(got.entries), expectedEntries)
	}

	// Verify the tool result entries.
	for i, tr := range msg.Results {
		entry := got.entries[initialEntries+i]
		if entry.Message.Role != "tool" {
			t.Errorf("entry[%d].Role = %q, want %q", i, entry.Message.Role, "tool")
		}
		if entry.Message.ToolCallID != tr.CallID {
			t.Errorf("entry[%d].ToolCallID = %q, want %q", i, entry.Message.ToolCallID, tr.CallID)
		}
		if entry.Message.Content != tr.Result {
			t.Errorf("entry[%d].Content = %q, want %q", i, entry.Message.Content, tr.Result)
		}
	}

	// toolsInFlight should be cleared.
	if got.toolsInFlight {
		t.Error("toolsInFlight should be false after ToolResultMsg")
	}
}

func TestToolResultMsg_HandlerFormatsErrors(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.appendEntry(ChatEntry{
		Message:   llm.Message{Role: "system", Content: "system prompt"},
		Timestamp: time.Now(),
	})

	initialEntries := len(m.entries)

	msg := ToolResultMsg{
		Results: []ToolResult{
			{CallID: "call-err", Name: "list_directory", Err: context.Canceled},
		},
	}

	result, _ := m.Update(msg)
	got := result.(*Model)

	if len(got.entries) != initialEntries+1 {
		t.Fatalf("entries count = %d, want %d", len(got.entries), initialEntries+1)
	}

	entry := got.entries[initialEntries]
	if entry.Message.Role != "tool" {
		t.Errorf("Role = %q, want %q", entry.Message.Role, "tool")
	}
	wantContent := "error: context canceled"
	if entry.Message.Content != wantContent {
		t.Errorf("Content = %q, want %q", entry.Message.Content, wantContent)
	}
}

func TestEscCancelsToolsInFlight(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.appendEntry(ChatEntry{
		Message:   llm.Message{Role: "system", Content: "system prompt"},
		Timestamp: time.Now(),
	})

	// Simulate tools in flight.
	ctx, cancel := context.WithCancel(context.Background())
	m.toolsInFlight = true
	m.toolCancelFunc = cancel

	initialEntries := len(m.entries)

	// Press Escape.
	result, _ := m.Update(specialKey(tea.KeyEscape))
	got := result.(*Model)

	if got.toolsInFlight {
		t.Error("toolsInFlight should be false after Escape")
	}
	if got.toolCancelFunc != nil {
		t.Error("toolCancelFunc should be nil after Escape")
	}

	// Should have appended a cancellation message.
	if len(got.entries) != initialEntries+1 {
		t.Fatalf("entries count = %d, want %d", len(got.entries), initialEntries+1)
	}
	entry := got.entries[initialEntries]
	if entry.Message.Content != "[tool calls cancelled]" {
		t.Errorf("Content = %q, want %q", entry.Message.Content, "[tool calls cancelled]")
	}

	// Verify the context was actually cancelled.
	if ctx.Err() == nil {
		t.Error("context should be cancelled after Escape")
	}
}
