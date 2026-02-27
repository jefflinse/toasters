package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/provider"
)

// --------------------------------------------------------------------------
// updatePromptMode tests
// --------------------------------------------------------------------------

func TestUpdatePromptMode_NavigateDown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		key           tea.KeyPressMsg
		options       []string
		startSelected int
		wantSelected  int
	}{
		{"down from 0 with 2 options", specialKey(tea.KeyDown), []string{"A", "B"}, 0, 1},
		{"j from 0 with 2 options", keyPress('j'), []string{"A", "B"}, 0, 1},
		{"down from 1 with 2 options moves to Custom", specialKey(tea.KeyDown), []string{"A", "B"}, 1, 2},
		{"down at last option (Custom) stays", specialKey(tea.KeyDown), []string{"A", "B"}, 2, 2},
		{"down from 0 with 0 options moves to Custom", specialKey(tea.KeyDown), []string{}, 0, 0},
		{"j at last option stays", keyPress('j'), []string{"A"}, 1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.prompt.promptMode = true
			m.prompt.promptOptions = tt.options
			m.prompt.promptSelected = tt.startSelected

			result, _ := m.updatePromptMode(tt.key)
			got := result.(*Model)

			if got.prompt.promptSelected != tt.wantSelected {
				t.Errorf("promptSelected = %d, want %d", got.prompt.promptSelected, tt.wantSelected)
			}
		})
	}
}

func TestUpdatePromptMode_NavigateUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		key           tea.KeyPressMsg
		options       []string
		startSelected int
		wantSelected  int
	}{
		{"up from 1 with 2 options", specialKey(tea.KeyUp), []string{"A", "B"}, 1, 0},
		{"k from 2 with 2 options", keyPress('k'), []string{"A", "B"}, 2, 1},
		{"up from 0 stays at 0", specialKey(tea.KeyUp), []string{"A", "B"}, 0, 0},
		{"k from 0 stays at 0", keyPress('k'), []string{"A"}, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newMinimalModel(t)
			m.prompt.promptMode = true
			m.prompt.promptOptions = tt.options
			m.prompt.promptSelected = tt.startSelected

			result, _ := m.updatePromptMode(tt.key)
			got := result.(*Model)

			if got.prompt.promptSelected != tt.wantSelected {
				t.Errorf("promptSelected = %d, want %d", got.prompt.promptSelected, tt.wantSelected)
			}
		})
	}
}

func TestUpdatePromptMode_SelectPredefinedOption(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.prompt.promptMode = true
	m.prompt.promptOptions = []string{"Option A", "Option B", "Option C"}
	m.prompt.promptSelected = 1 // "Option B"
	m.prompt.promptPendingCall = provider.ToolCall{ID: "call-123", Name: "ask_user"}

	result, cmd := m.updatePromptMode(specialKey(tea.KeyEnter))
	got := result.(*Model)

	// promptMode should still be true — it's cleared by handleAskUserResponse, not updatePromptMode.
	// The cmd should produce an AskUserResponseMsg.
	if got.prompt.promptCustom {
		t.Error("promptCustom should be false after selecting a predefined option")
	}
	if cmd == nil {
		t.Fatal("expected non-nil cmd after selecting an option")
	}

	// Execute the cmd to verify it produces the correct message.
	msg := cmd()
	askMsg, ok := msg.(AskUserResponseMsg)
	if !ok {
		t.Fatalf("expected AskUserResponseMsg, got %T", msg)
	}
	if askMsg.Result != "Option B" {
		t.Errorf("Result = %q, want %q", askMsg.Result, "Option B")
	}
	if askMsg.Call.ID != "call-123" {
		t.Errorf("Call.ID = %q, want %q", askMsg.Call.ID, "call-123")
	}
}

func TestUpdatePromptMode_SelectCustomResponseOption(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.prompt.promptMode = true
	m.prompt.promptOptions = []string{"Option A", "Option B"}
	m.prompt.promptSelected = 2 // "Custom response..." (appended automatically)

	result, cmd := m.updatePromptMode(specialKey(tea.KeyEnter))
	got := result.(*Model)

	if !got.prompt.promptCustom {
		t.Error("promptCustom should be true after selecting 'Custom response...'")
	}
	// The cmd should be a focus command for the input, not an AskUserResponseMsg.
	// We can't easily inspect the cmd type, but it should be non-nil.
	if cmd == nil {
		t.Error("expected non-nil cmd (input focus) after selecting custom response")
	}
}

func TestUpdatePromptMode_SubmitCustomText(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.prompt.promptMode = true
	m.prompt.promptCustom = true
	m.prompt.promptOptions = []string{"Option A"}
	m.prompt.promptPendingCall = provider.ToolCall{ID: "call-456", Name: "ask_user"}
	m.input.SetValue("My custom answer")

	_, cmd := m.updatePromptMode(specialKey(tea.KeyEnter))

	if cmd == nil {
		t.Fatal("expected non-nil cmd after submitting custom text")
	}

	msg := cmd()
	askMsg, ok := msg.(AskUserResponseMsg)
	if !ok {
		t.Fatalf("expected AskUserResponseMsg, got %T", msg)
	}
	if askMsg.Result != "My custom answer" {
		t.Errorf("Result = %q, want %q", askMsg.Result, "My custom answer")
	}
	if askMsg.Call.ID != "call-456" {
		t.Errorf("Call.ID = %q, want %q", askMsg.Call.ID, "call-456")
	}
}

func TestUpdatePromptMode_SubmitEmptyCustomText(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.prompt.promptMode = true
	m.prompt.promptCustom = true
	m.prompt.promptOptions = []string{"Option A"}
	m.prompt.promptPendingCall = provider.ToolCall{ID: "call-789"}
	m.input.SetValue("   ") // whitespace only

	_, cmd := m.updatePromptMode(specialKey(tea.KeyEnter))

	if cmd == nil {
		t.Fatal("expected non-nil cmd after submitting empty custom text")
	}

	msg := cmd()
	askMsg, ok := msg.(AskUserResponseMsg)
	if !ok {
		t.Fatalf("expected AskUserResponseMsg, got %T", msg)
	}
	if askMsg.Result != "User provided no response." {
		t.Errorf("Result = %q, want %q", askMsg.Result, "User provided no response.")
	}
}

func TestUpdatePromptMode_EscCancelFromOptionSelection(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.prompt.promptMode = true
	m.prompt.promptCustom = false
	m.prompt.promptOptions = []string{"Option A", "Option B"}
	m.prompt.promptPendingCall = provider.ToolCall{ID: "call-esc"}

	_, cmd := m.updatePromptMode(specialKey(tea.KeyEscape))

	if cmd == nil {
		t.Fatal("expected non-nil cmd after esc cancellation")
	}

	msg := cmd()
	askMsg, ok := msg.(AskUserResponseMsg)
	if !ok {
		t.Fatalf("expected AskUserResponseMsg, got %T", msg)
	}
	if askMsg.Result != "User cancelled." {
		t.Errorf("Result = %q, want %q", askMsg.Result, "User cancelled.")
	}
	if askMsg.Call.ID != "call-esc" {
		t.Errorf("Call.ID = %q, want %q", askMsg.Call.ID, "call-esc")
	}
}

func TestUpdatePromptMode_EscFromCustomGoesBackToOptions(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.prompt.promptMode = true
	m.prompt.promptCustom = true
	m.prompt.promptOptions = []string{"Option A"}
	m.input.SetValue("some text")

	result, cmd := m.updatePromptMode(specialKey(tea.KeyEscape))
	got := result.(*Model)

	if got.prompt.promptCustom {
		t.Error("promptCustom should be false after esc from custom mode")
	}
	// Input should be reset.
	if got.input.Value() != "" {
		t.Errorf("input value = %q, want empty after esc from custom", got.input.Value())
	}
	// No AskUserResponseMsg should be produced — just go back to option selection.
	if cmd != nil {
		t.Error("expected nil cmd when going back from custom to option selection")
	}
}

func TestUpdatePromptMode_CustomModeDelegatesKeyToTextarea(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.prompt.promptMode = true
	m.prompt.promptCustom = true
	m.prompt.promptOptions = []string{"Option A"}
	m.input.Focus()

	// Typing a character in custom mode should delegate to the textarea.
	result, _ := m.updatePromptMode(keyPress('x'))
	got := result.(*Model)

	// The model should still be in custom mode.
	if !got.prompt.promptCustom {
		t.Error("promptCustom should remain true when typing in custom mode")
	}
}

// --------------------------------------------------------------------------
// handleToolCalls tests
// --------------------------------------------------------------------------

func TestHandleToolCalls_AssignTeam(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)

	args, _ := json.Marshal(map[string]string{"team_name": "alpha", "job_id": "job-42"})
	msg := ToolCallMsg{
		Calls: []provider.ToolCall{
			{
				ID:        "call-assign-1",
				Name:      "assign_team",
				Arguments: args,
			},
		},
	}

	result, _ := m.handleToolCalls(msg)
	got := result.(*Model)

	if !got.prompt.promptMode {
		t.Error("promptMode should be true for dispatch confirmation")
	}
	if !got.prompt.confirmDispatch {
		t.Error("confirmDispatch should be true")
	}
	if got.prompt.changingTeam {
		t.Error("changingTeam should be false initially")
	}
	if got.prompt.pendingDispatch.ID != "call-assign-1" {
		t.Errorf("pendingDispatch.ID = %q, want %q", got.prompt.pendingDispatch.ID, "call-assign-1")
	}
	if len(got.prompt.promptOptions) != 3 {
		t.Fatalf("promptOptions length = %d, want 3", len(got.prompt.promptOptions))
	}
	if got.prompt.promptOptions[0] != "Yes, dispatch" {
		t.Errorf("promptOptions[0] = %q, want %q", got.prompt.promptOptions[0], "Yes, dispatch")
	}
	if got.prompt.promptOptions[1] != "Change team" {
		t.Errorf("promptOptions[1] = %q, want %q", got.prompt.promptOptions[1], "Change team")
	}
	if got.prompt.promptOptions[2] != "Cancel" {
		t.Errorf("promptOptions[2] = %q, want %q", got.prompt.promptOptions[2], "Cancel")
	}

	// Verify the question mentions both team and job.
	if !strings.Contains(got.prompt.promptQuestion, "alpha") {
		t.Errorf("promptQuestion %q should contain team name 'alpha'", got.prompt.promptQuestion)
	}
	if !strings.Contains(got.prompt.promptQuestion, "job-42") {
		t.Errorf("promptQuestion %q should contain job ID 'job-42'", got.prompt.promptQuestion)
	}
}

func TestHandleToolCalls_AskUser(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)

	args, _ := json.Marshal(map[string]any{
		"question": "Which database?",
		"options":  []string{"PostgreSQL", "MySQL", "SQLite"},
	})
	msg := ToolCallMsg{
		Calls: []provider.ToolCall{
			{
				ID:        "call-ask-1",
				Name:      "ask_user",
				Arguments: args,
			},
		},
	}

	result, _ := m.handleToolCalls(msg)
	got := result.(*Model)

	if !got.prompt.promptMode {
		t.Error("promptMode should be true for ask_user")
	}
	if got.prompt.confirmDispatch {
		t.Error("confirmDispatch should be false for ask_user")
	}
	if got.prompt.promptQuestion != "Which database?" {
		t.Errorf("promptQuestion = %q, want %q", got.prompt.promptQuestion, "Which database?")
	}
	if len(got.prompt.promptOptions) != 3 {
		t.Fatalf("promptOptions length = %d, want 3", len(got.prompt.promptOptions))
	}
	if got.prompt.promptOptions[0] != "PostgreSQL" {
		t.Errorf("promptOptions[0] = %q, want %q", got.prompt.promptOptions[0], "PostgreSQL")
	}
	if got.prompt.promptOptions[1] != "MySQL" {
		t.Errorf("promptOptions[1] = %q, want %q", got.prompt.promptOptions[1], "MySQL")
	}
	if got.prompt.promptOptions[2] != "SQLite" {
		t.Errorf("promptOptions[2] = %q, want %q", got.prompt.promptOptions[2], "SQLite")
	}
	if got.prompt.promptPendingCall.ID != "call-ask-1" {
		t.Errorf("promptPendingCall.ID = %q, want %q", got.prompt.promptPendingCall.ID, "call-ask-1")
	}
	if got.prompt.promptSelected != 0 {
		t.Errorf("promptSelected = %d, want 0", got.prompt.promptSelected)
	}
	if got.prompt.promptCustom {
		t.Error("promptCustom should be false initially")
	}
}

func TestHandleToolCalls_AskUser_InvalidJSON(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)

	msg := ToolCallMsg{
		Calls: []provider.ToolCall{
			{
				ID:        "call-ask-bad",
				Name:      "ask_user",
				Arguments: json.RawMessage("not-json"),
			},
		},
	}

	result, _ := m.handleToolCalls(msg)
	got := result.(*Model)

	if !got.prompt.promptMode {
		t.Error("promptMode should be true even with invalid JSON")
	}
	// Should fall back to defaults.
	if got.prompt.promptQuestion != "What would you like to do?" {
		t.Errorf("promptQuestion = %q, want default fallback", got.prompt.promptQuestion)
	}
	if len(got.prompt.promptOptions) != 0 {
		t.Errorf("promptOptions = %v, want empty slice for invalid JSON", got.prompt.promptOptions)
	}
}

func TestHandleToolCalls_EscalateToUser(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)

	args, _ := json.Marshal(map[string]string{
		"question": "Need API key",
		"context":  "The deployment requires credentials",
	})
	msg := ToolCallMsg{
		Calls: []provider.ToolCall{
			{
				ID:        "call-escalate-1",
				Name:      "escalate_to_user",
				Arguments: args,
			},
		},
	}

	result, _ := m.handleToolCalls(msg)
	got := result.(*Model)

	if !got.prompt.promptMode {
		t.Error("promptMode should be true for escalate_to_user")
	}
	// The question should include both question and context.
	wantQuestion := "Need API key\n\nThe deployment requires credentials"
	if got.prompt.promptQuestion != wantQuestion {
		t.Errorf("promptQuestion = %q, want %q", got.prompt.promptQuestion, wantQuestion)
	}
	if len(got.prompt.promptOptions) != 1 || got.prompt.promptOptions[0] != "Provide answer" {
		t.Errorf("promptOptions = %v, want [Provide answer]", got.prompt.promptOptions)
	}
	if got.prompt.promptPendingCall.ID != "call-escalate-1" {
		t.Errorf("promptPendingCall.ID = %q, want %q", got.prompt.promptPendingCall.ID, "call-escalate-1")
	}

	// Verify a chat entry was appended.
	found := false
	for _, entry := range got.chat.entries {
		if strings.Contains(entry.Message.Content, "Need API key") &&
			strings.Contains(entry.Message.Content, "deployment requires credentials") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a chat entry containing the escalation question and context")
	}
}

func TestHandleToolCalls_EscalateToUser_InvalidJSON(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)

	msg := ToolCallMsg{
		Calls: []provider.ToolCall{
			{
				ID:        "call-escalate-bad",
				Name:      "escalate_to_user",
				Arguments: json.RawMessage("bad-json"),
			},
		},
	}

	result, _ := m.handleToolCalls(msg)
	got := result.(*Model)

	if !got.prompt.promptMode {
		t.Error("promptMode should be true even with invalid JSON")
	}
	// Should fall back to default question.
	if got.prompt.promptQuestion != "A team has encountered a blocker." {
		t.Errorf("promptQuestion = %q, want default fallback", got.prompt.promptQuestion)
	}
}

func TestHandleToolCalls_EscalateToUser_NoContext(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)

	args, _ := json.Marshal(map[string]string{
		"question": "Need help",
		"context":  "",
	})
	msg := ToolCallMsg{
		Calls: []provider.ToolCall{
			{
				ID:        "call-escalate-nocontext",
				Name:      "escalate_to_user",
				Arguments: args,
			},
		},
	}

	result, _ := m.handleToolCalls(msg)
	got := result.(*Model)

	// When context is empty, the question should not have the double newline separator.
	if got.prompt.promptQuestion != "Need help" {
		t.Errorf("promptQuestion = %q, want %q", got.prompt.promptQuestion, "Need help")
	}
}

func TestHandleToolCalls_NormalToolExecution(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	// Use a temp dir so job_list doesn't fail.
	m.toolExec.WorkspaceDir = t.TempDir()

	msg := ToolCallMsg{
		Calls: []provider.ToolCall{
			{
				ID:        "call-joblist-1",
				Name:      "job_list",
				Arguments: json.RawMessage("{}"),
			},
		},
	}

	initialEntries := len(m.chat.entries)
	result, cmd := m.handleToolCalls(msg)
	got := result.(*Model)

	// Should not enter prompt mode for normal tools.
	if got.prompt.promptMode {
		t.Error("promptMode should be false for normal tool calls")
	}

	// Should have appended entries: assistant tool call turn + indicator (but NOT tool result yet — that's async).
	if len(got.chat.entries) <= initialEntries {
		t.Errorf("expected entries to grow from %d, got %d", initialEntries, len(got.chat.entries))
	}

	// Verify the tool call indicator was appended.
	foundIndicator := false
	for _, entry := range got.chat.entries {
		if strings.Contains(entry.Message.Content, "calling `job_list`") {
			foundIndicator = true
			break
		}
	}
	if !foundIndicator {
		t.Error("expected a tool call indicator entry containing 'calling `job_list`'")
	}

	// Tool results are now async — they arrive via ToolResultMsg, not inline.
	// Verify toolsInFlight is set.
	if !got.toolsInFlight {
		t.Error("toolsInFlight should be true after dispatching async tool execution")
	}

	// A cmd should be returned (executeToolsCmd).
	if cmd == nil {
		t.Error("expected non-nil cmd from handleToolCalls for async tool execution")
	}
}

func TestHandleToolCalls_StreamingSetToFalse(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.stream.streaming = true

	args, _ := json.Marshal(map[string]string{"question": "test?", "context": ""})
	msg := ToolCallMsg{
		Calls: []provider.ToolCall{
			{
				ID:        "call-ask-stream",
				Name:      "ask_user",
				Arguments: args,
			},
		},
	}

	result, _ := m.handleToolCalls(msg)
	got := result.(*Model)

	// streaming should be false after handleToolCalls intercepts a special tool.
	if got.stream.streaming {
		t.Error("streaming should be false after intercepting ask_user")
	}
}

// --------------------------------------------------------------------------
// handleAskUserResponse tests
// --------------------------------------------------------------------------

func TestHandleAskUserResponse_DispatchConfirm_YesDispatch(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.toolExec.WorkspaceDir = t.TempDir()
	m.prompt.confirmDispatch = true
	m.prompt.promptMode = true
	m.prompt.pendingDispatch = provider.ToolCall{
		ID:        "call-dispatch-yes",
		Name:      "assign_team",
		Arguments: json.RawMessage(`{"team_name":"alpha","job_id":"job-1","task":"do stuff"}`),
	}

	msg := AskUserResponseMsg{Result: "Yes, dispatch"}

	result, cmd := m.handleAskUserResponse(msg)
	got := result.(*Model)

	if got.prompt.confirmDispatch {
		t.Error("confirmDispatch should be false after dispatch")
	}
	if got.prompt.promptMode {
		t.Error("promptMode should be false after dispatch")
	}

	// Should have appended the tool call indicator entry.
	foundToolCall := false
	for _, entry := range got.chat.entries {
		if entry.Message.Role == "assistant" && len(entry.Message.ToolCalls) > 0 &&
			entry.Message.ToolCalls[0].ID == "call-dispatch-yes" {
			foundToolCall = true
			break
		}
	}
	if !foundToolCall {
		t.Error("expected tool call indicator entry for dispatch")
	}

	// Tool results are now async — they arrive via ToolResultMsg, not inline.
	if !got.toolsInFlight {
		t.Error("toolsInFlight should be true after dispatching async tool execution")
	}

	if cmd == nil {
		t.Error("expected non-nil cmd (executeToolsCmd) after dispatch")
	}
}

func TestHandleAskUserResponse_DispatchConfirm_Cancel(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.prompt.confirmDispatch = true
	m.prompt.promptMode = true
	m.prompt.pendingDispatch = provider.ToolCall{
		ID:        "call-dispatch-cancel",
		Name:      "assign_team",
		Arguments: json.RawMessage(`{"team_name":"beta","job_id":"job-2"}`),
	}

	msg := AskUserResponseMsg{Result: "Cancel"}

	result, cmd := m.handleAskUserResponse(msg)
	got := result.(*Model)

	if got.prompt.confirmDispatch {
		t.Error("confirmDispatch should be false after cancel")
	}

	// Should have appended a tool result with "User cancelled the dispatch."
	found := false
	for _, entry := range got.chat.entries {
		if entry.Message.Role == "tool" && strings.Contains(entry.Message.Content, "User cancelled the dispatch") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected tool result entry containing 'User cancelled the dispatch'")
	}

	if cmd == nil {
		t.Error("expected non-nil cmd (startStream) after dispatch cancellation")
	}
}

func TestHandleAskUserResponse_DispatchConfirm_ChangeTeam(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.prompt.confirmDispatch = true
	m.prompt.promptMode = true
	m.teams = []agents.Team{
		{Name: "alpha"},
		{Name: "beta"},
		{Name: "gamma"},
	}
	m.prompt.pendingDispatch = provider.ToolCall{
		ID:        "call-dispatch-change",
		Name:      "assign_team",
		Arguments: json.RawMessage(`{"team_name":"alpha","job_id":"job-3"}`),
	}

	msg := AskUserResponseMsg{Result: "Change team"}

	result, cmd := m.handleAskUserResponse(msg)
	got := result.(*Model)

	// Should enter the "change team" sub-prompt.
	if !got.prompt.promptMode {
		t.Error("promptMode should be true for change team sub-prompt")
	}
	if !got.prompt.confirmDispatch {
		t.Error("confirmDispatch should remain true during change team flow")
	}
	if !got.prompt.changingTeam {
		t.Error("changingTeam should be true")
	}
	if got.prompt.promptQuestion != "Select a team:" {
		t.Errorf("promptQuestion = %q, want %q", got.prompt.promptQuestion, "Select a team:")
	}
	if len(got.prompt.promptOptions) != 3 {
		t.Fatalf("promptOptions length = %d, want 3", len(got.prompt.promptOptions))
	}
	if got.prompt.promptOptions[0] != "alpha" || got.prompt.promptOptions[1] != "beta" || got.prompt.promptOptions[2] != "gamma" {
		t.Errorf("promptOptions = %v, want [alpha beta gamma]", got.prompt.promptOptions)
	}
	if got.prompt.promptSelected != 0 {
		t.Errorf("promptSelected = %d, want 0", got.prompt.promptSelected)
	}

	// cmd should be input focus, not startStream.
	if cmd == nil {
		t.Error("expected non-nil cmd (input focus) for change team sub-prompt")
	}
}

func TestHandleAskUserResponse_DispatchConfirm_ChangingTeamSelection(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.toolExec.WorkspaceDir = t.TempDir()
	m.prompt.confirmDispatch = true
	m.prompt.changingTeam = true
	m.prompt.promptMode = true
	m.prompt.pendingDispatch = provider.ToolCall{
		ID:        "call-dispatch-changed",
		Name:      "assign_team",
		Arguments: json.RawMessage(`{"team_name":"alpha","job_id":"job-4","task":"do stuff"}`),
	}

	msg := AskUserResponseMsg{Result: "beta"}

	result, cmd := m.handleAskUserResponse(msg)
	got := result.(*Model)

	if got.prompt.changingTeam {
		t.Error("changingTeam should be false after team selection")
	}
	if got.prompt.confirmDispatch {
		t.Error("confirmDispatch should be false after team selection")
	}
	if got.prompt.promptMode {
		t.Error("promptMode should be false after team selection")
	}

	// The pending dispatch args should have been rewritten with the new team name.
	foundToolCall := false
	for _, entry := range got.chat.entries {
		if entry.Message.Role == "assistant" && len(entry.Message.ToolCalls) > 0 {
			var args map[string]any
			_ = json.Unmarshal(entry.Message.ToolCalls[0].Arguments, &args)
			if args["team_name"] == "beta" {
				foundToolCall = true
				break
			}
		}
	}
	if !foundToolCall {
		t.Error("expected the tool call entry to have team_name rewritten to 'beta'")
	}

	// Tool results are now async — they arrive via ToolResultMsg, not inline.
	if !got.toolsInFlight {
		t.Error("toolsInFlight should be true after dispatching async tool execution")
	}

	if cmd == nil {
		t.Error("expected non-nil cmd (executeToolsCmd) after team change")
	}
}

func TestHandleAskUserResponse_NormalAskUser(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	// No confirmDispatch flag set.
	m.prompt.promptMode = true
	m.prompt.promptCustom = true
	m.prompt.promptQuestion = "What color?"
	m.prompt.promptOptions = []string{"Red", "Blue"}
	m.prompt.promptSelected = 1
	m.input.SetValue("some leftover text")

	pendingCall := provider.ToolCall{
		ID:   "call-normal-ask",
		Name: "ask_user",
	}

	msg := AskUserResponseMsg{Call: pendingCall, Result: "Blue"}

	result, cmd := m.handleAskUserResponse(msg)
	got := result.(*Model)

	// All prompt state should be cleared.
	if got.prompt.promptMode {
		t.Error("promptMode should be false")
	}
	if got.prompt.promptCustom {
		t.Error("promptCustom should be false")
	}
	if got.prompt.promptQuestion != "" {
		t.Errorf("promptQuestion = %q, want empty", got.prompt.promptQuestion)
	}
	if got.prompt.promptOptions != nil {
		t.Errorf("promptOptions should be nil, got %v", got.prompt.promptOptions)
	}
	if got.prompt.promptSelected != 0 {
		t.Errorf("promptSelected = %d, want 0", got.prompt.promptSelected)
	}
	if got.input.Value() != "" {
		t.Errorf("input value = %q, want empty", got.input.Value())
	}

	// Should have appended an assistant tool call entry and a tool result entry.
	foundAssistantToolCall := false
	foundToolResult := false
	for _, entry := range got.chat.entries {
		if entry.Message.Role == "assistant" && len(entry.Message.ToolCalls) > 0 {
			if entry.Message.ToolCalls[0].ID == "call-normal-ask" {
				foundAssistantToolCall = true
			}
		}
		if entry.Message.Role == "tool" && entry.Message.ToolCallID == "call-normal-ask" {
			if entry.Message.Content != "Blue" {
				t.Errorf("tool result content = %q, want %q", entry.Message.Content, "Blue")
			}
			foundToolResult = true
		}
	}
	if !foundAssistantToolCall {
		t.Error("expected assistant entry with tool call for 'call-normal-ask'")
	}
	if !foundToolResult {
		t.Error("expected tool result entry for 'call-normal-ask'")
	}

	// Should return a cmd to start a new stream.
	if cmd == nil {
		t.Error("expected non-nil cmd (startStream) after normal ask_user response")
	}
}

// --------------------------------------------------------------------------
// Integration: updatePromptMode → handleAskUserResponse flow
// --------------------------------------------------------------------------

func TestPromptMode_FullFlow_SelectAndRespond(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.prompt.promptMode = true
	m.prompt.promptOptions = []string{"Yes", "No"}
	m.prompt.promptSelected = 0
	m.prompt.promptPendingCall = provider.ToolCall{
		ID:   "call-flow-1",
		Name: "ask_user",
	}

	// Step 1: Navigate down to "No".
	result1, _ := m.updatePromptMode(specialKey(tea.KeyDown))
	mp := result1.(*Model)
	if mp.prompt.promptSelected != 1 {
		t.Fatalf("after down, promptSelected = %d, want 1", mp.prompt.promptSelected)
	}

	// Step 2: Press enter to select "No".
	result2, cmd := mp.updatePromptMode(specialKey(tea.KeyEnter))
	mp = result2.(*Model)
	if cmd == nil {
		t.Fatal("expected non-nil cmd after selecting option")
	}

	// Step 3: Execute the cmd to get the AskUserResponseMsg.
	rawMsg := cmd()
	askMsg, ok := rawMsg.(AskUserResponseMsg)
	if !ok {
		t.Fatalf("expected AskUserResponseMsg, got %T", rawMsg)
	}
	if askMsg.Result != "No" {
		t.Errorf("Result = %q, want %q", askMsg.Result, "No")
	}

	// Step 4: Feed the AskUserResponseMsg into handleAskUserResponse.
	result3, streamCmd := mp.handleAskUserResponse(askMsg)
	mp = result3.(*Model)

	if mp.prompt.promptMode {
		t.Error("promptMode should be false after handling response")
	}

	// Should have appended entries.
	if len(mp.chat.entries) == 0 {
		t.Error("expected entries to be non-empty after handling response")
	}

	// Should return a startStream cmd.
	if streamCmd == nil {
		t.Error("expected non-nil cmd (startStream) after handling response")
	}
}

func TestPromptMode_FullFlow_CustomResponse(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.prompt.promptMode = true
	m.prompt.promptOptions = []string{"Option A"}
	m.prompt.promptSelected = 0
	m.prompt.promptPendingCall = provider.ToolCall{
		ID:   "call-flow-custom",
		Name: "ask_user",
	}

	// Step 1: Navigate to "Custom response..." (index 1, since there's 1 option + Custom).
	result1, _ := m.updatePromptMode(specialKey(tea.KeyDown))
	mp := result1.(*Model)
	if mp.prompt.promptSelected != 1 {
		t.Fatalf("after down, promptSelected = %d, want 1", mp.prompt.promptSelected)
	}

	// Step 2: Press enter to enter custom mode.
	result2, _ := mp.updatePromptMode(specialKey(tea.KeyEnter))
	mp = result2.(*Model)
	if !mp.prompt.promptCustom {
		t.Fatal("promptCustom should be true after selecting Custom response")
	}

	// Step 3: Type custom text.
	mp.input.SetValue("My custom input")

	// Step 4: Press enter to submit.
	_, cmd := mp.updatePromptMode(specialKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("expected non-nil cmd after submitting custom text")
	}

	rawMsg := cmd()
	askMsg, ok := rawMsg.(AskUserResponseMsg)
	if !ok {
		t.Fatalf("expected AskUserResponseMsg, got %T", rawMsg)
	}
	if askMsg.Result != "My custom input" {
		t.Errorf("Result = %q, want %q", askMsg.Result, "My custom input")
	}
}
