package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/agents"
	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/gateway"
	"github.com/jefflinse/toasters/internal/llm"
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
			m.promptMode = true
			m.promptOptions = tt.options
			m.promptSelected = tt.startSelected

			result, _ := m.updatePromptMode(tt.key)
			got := result.(*Model)

			if got.promptSelected != tt.wantSelected {
				t.Errorf("promptSelected = %d, want %d", got.promptSelected, tt.wantSelected)
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
			m.promptMode = true
			m.promptOptions = tt.options
			m.promptSelected = tt.startSelected

			result, _ := m.updatePromptMode(tt.key)
			got := result.(*Model)

			if got.promptSelected != tt.wantSelected {
				t.Errorf("promptSelected = %d, want %d", got.promptSelected, tt.wantSelected)
			}
		})
	}
}

func TestUpdatePromptMode_SelectPredefinedOption(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.promptMode = true
	m.promptOptions = []string{"Option A", "Option B", "Option C"}
	m.promptSelected = 1 // "Option B"
	m.promptPendingCall = llm.ToolCall{ID: "call-123", Function: llm.ToolCallFunction{Name: "ask_user"}}

	result, cmd := m.updatePromptMode(specialKey(tea.KeyEnter))
	got := result.(*Model)

	// promptMode should still be true — it's cleared by handleAskUserResponse, not updatePromptMode.
	// The cmd should produce an AskUserResponseMsg.
	if got.promptCustom {
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
	m.promptMode = true
	m.promptOptions = []string{"Option A", "Option B"}
	m.promptSelected = 2 // "Custom response..." (appended automatically)

	result, cmd := m.updatePromptMode(specialKey(tea.KeyEnter))
	got := result.(*Model)

	if !got.promptCustom {
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
	m.promptMode = true
	m.promptCustom = true
	m.promptOptions = []string{"Option A"}
	m.promptPendingCall = llm.ToolCall{ID: "call-456", Function: llm.ToolCallFunction{Name: "ask_user"}}
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
	m.promptMode = true
	m.promptCustom = true
	m.promptOptions = []string{"Option A"}
	m.promptPendingCall = llm.ToolCall{ID: "call-789"}
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
	m.promptMode = true
	m.promptCustom = false
	m.promptOptions = []string{"Option A", "Option B"}
	m.promptPendingCall = llm.ToolCall{ID: "call-esc"}

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
	m.promptMode = true
	m.promptCustom = true
	m.promptOptions = []string{"Option A"}
	m.input.SetValue("some text")

	result, cmd := m.updatePromptMode(specialKey(tea.KeyEscape))
	got := result.(*Model)

	if got.promptCustom {
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
	m.promptMode = true
	m.promptCustom = true
	m.promptOptions = []string{"Option A"}
	m.input.Focus()

	// Typing a character in custom mode should delegate to the textarea.
	result, _ := m.updatePromptMode(keyPress('x'))
	got := result.(*Model)

	// The model should still be in custom mode.
	if !got.promptCustom {
		t.Error("promptCustom should remain true when typing in custom mode")
	}
}

// --------------------------------------------------------------------------
// handleToolCalls tests
// --------------------------------------------------------------------------

func TestHandleToolCalls_KillSlot(t *testing.T) {
	t.Parallel()

	claudeCfg := config.ClaudeConfig{Path: "/usr/bin/true"}
	gw := gateway.New(claudeCfg, t.TempDir(), func() {})

	m := newMinimalModel(t)
	m.gateway = gw

	args, _ := json.Marshal(map[string]int{"slot_id": 2})
	msg := ToolCallMsg{
		Calls: []llm.ToolCall{
			{
				ID:       "call-kill-1",
				Function: llm.ToolCallFunction{Name: "kill_slot", Arguments: string(args)},
			},
		},
	}

	result, _ := m.handleToolCalls(msg)
	got := result.(*Model)

	if !got.promptMode {
		t.Error("promptMode should be true for kill confirmation")
	}
	if !got.confirmKill {
		t.Error("confirmKill should be true")
	}
	if got.confirmDispatch {
		t.Error("confirmDispatch should be false")
	}
	if got.pendingKillSlot != 2 {
		t.Errorf("pendingKillSlot = %d, want 2", got.pendingKillSlot)
	}
	if got.promptPendingCall.ID != "call-kill-1" {
		t.Errorf("promptPendingCall.ID = %q, want %q", got.promptPendingCall.ID, "call-kill-1")
	}
	if len(got.promptOptions) != 2 || got.promptOptions[0] != "Yes, kill" || got.promptOptions[1] != "Cancel" {
		t.Errorf("promptOptions = %v, want [Yes, kill Cancel]", got.promptOptions)
	}
	if got.promptSelected != 0 {
		t.Errorf("promptSelected = %d, want 0", got.promptSelected)
	}
	// Verify a chat entry was appended for the kill confirmation question.
	found := false
	for _, entry := range got.entries {
		if strings.Contains(entry.Message.Content, "Kill slot 2") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a chat entry containing 'Kill slot 2'")
	}
}

func TestHandleToolCalls_AssignTeam(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)

	args, _ := json.Marshal(map[string]string{"team_name": "alpha", "job_id": "job-42"})
	msg := ToolCallMsg{
		Calls: []llm.ToolCall{
			{
				ID:       "call-assign-1",
				Function: llm.ToolCallFunction{Name: "assign_team", Arguments: string(args)},
			},
		},
	}

	result, _ := m.handleToolCalls(msg)
	got := result.(*Model)

	if !got.promptMode {
		t.Error("promptMode should be true for dispatch confirmation")
	}
	if !got.confirmDispatch {
		t.Error("confirmDispatch should be true")
	}
	if got.confirmKill {
		t.Error("confirmKill should be false")
	}
	if got.changingTeam {
		t.Error("changingTeam should be false initially")
	}
	if got.pendingDispatch.ID != "call-assign-1" {
		t.Errorf("pendingDispatch.ID = %q, want %q", got.pendingDispatch.ID, "call-assign-1")
	}
	if len(got.promptOptions) != 3 {
		t.Fatalf("promptOptions length = %d, want 3", len(got.promptOptions))
	}
	if got.promptOptions[0] != "Yes, dispatch" {
		t.Errorf("promptOptions[0] = %q, want %q", got.promptOptions[0], "Yes, dispatch")
	}
	if got.promptOptions[1] != "Change team" {
		t.Errorf("promptOptions[1] = %q, want %q", got.promptOptions[1], "Change team")
	}
	if got.promptOptions[2] != "Cancel" {
		t.Errorf("promptOptions[2] = %q, want %q", got.promptOptions[2], "Cancel")
	}

	// Verify the question mentions both team and job.
	if !strings.Contains(got.promptQuestion, "alpha") {
		t.Errorf("promptQuestion %q should contain team name 'alpha'", got.promptQuestion)
	}
	if !strings.Contains(got.promptQuestion, "job-42") {
		t.Errorf("promptQuestion %q should contain job ID 'job-42'", got.promptQuestion)
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
		Calls: []llm.ToolCall{
			{
				ID:       "call-ask-1",
				Function: llm.ToolCallFunction{Name: "ask_user", Arguments: string(args)},
			},
		},
	}

	result, _ := m.handleToolCalls(msg)
	got := result.(*Model)

	if !got.promptMode {
		t.Error("promptMode should be true for ask_user")
	}
	if got.confirmKill || got.confirmDispatch {
		t.Error("confirmKill and confirmDispatch should be false for ask_user")
	}
	if got.promptQuestion != "Which database?" {
		t.Errorf("promptQuestion = %q, want %q", got.promptQuestion, "Which database?")
	}
	if len(got.promptOptions) != 3 {
		t.Fatalf("promptOptions length = %d, want 3", len(got.promptOptions))
	}
	if got.promptOptions[0] != "PostgreSQL" {
		t.Errorf("promptOptions[0] = %q, want %q", got.promptOptions[0], "PostgreSQL")
	}
	if got.promptOptions[1] != "MySQL" {
		t.Errorf("promptOptions[1] = %q, want %q", got.promptOptions[1], "MySQL")
	}
	if got.promptOptions[2] != "SQLite" {
		t.Errorf("promptOptions[2] = %q, want %q", got.promptOptions[2], "SQLite")
	}
	if got.promptPendingCall.ID != "call-ask-1" {
		t.Errorf("promptPendingCall.ID = %q, want %q", got.promptPendingCall.ID, "call-ask-1")
	}
	if got.promptSelected != 0 {
		t.Errorf("promptSelected = %d, want 0", got.promptSelected)
	}
	if got.promptCustom {
		t.Error("promptCustom should be false initially")
	}
}

func TestHandleToolCalls_AskUser_InvalidJSON(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)

	msg := ToolCallMsg{
		Calls: []llm.ToolCall{
			{
				ID:       "call-ask-bad",
				Function: llm.ToolCallFunction{Name: "ask_user", Arguments: "not-json"},
			},
		},
	}

	result, _ := m.handleToolCalls(msg)
	got := result.(*Model)

	if !got.promptMode {
		t.Error("promptMode should be true even with invalid JSON")
	}
	// Should fall back to defaults.
	if got.promptQuestion != "What would you like to do?" {
		t.Errorf("promptQuestion = %q, want default fallback", got.promptQuestion)
	}
	if len(got.promptOptions) != 0 {
		t.Errorf("promptOptions = %v, want empty slice for invalid JSON", got.promptOptions)
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
		Calls: []llm.ToolCall{
			{
				ID:       "call-escalate-1",
				Function: llm.ToolCallFunction{Name: "escalate_to_user", Arguments: string(args)},
			},
		},
	}

	result, _ := m.handleToolCalls(msg)
	got := result.(*Model)

	if !got.promptMode {
		t.Error("promptMode should be true for escalate_to_user")
	}
	// The question should include both question and context.
	wantQuestion := "Need API key\n\nThe deployment requires credentials"
	if got.promptQuestion != wantQuestion {
		t.Errorf("promptQuestion = %q, want %q", got.promptQuestion, wantQuestion)
	}
	if len(got.promptOptions) != 1 || got.promptOptions[0] != "Provide answer" {
		t.Errorf("promptOptions = %v, want [Provide answer]", got.promptOptions)
	}
	if got.promptPendingCall.ID != "call-escalate-1" {
		t.Errorf("promptPendingCall.ID = %q, want %q", got.promptPendingCall.ID, "call-escalate-1")
	}

	// Verify a chat entry was appended.
	found := false
	for _, entry := range got.entries {
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
		Calls: []llm.ToolCall{
			{
				ID:       "call-escalate-bad",
				Function: llm.ToolCallFunction{Name: "escalate_to_user", Arguments: "bad-json"},
			},
		},
	}

	result, _ := m.handleToolCalls(msg)
	got := result.(*Model)

	if !got.promptMode {
		t.Error("promptMode should be true even with invalid JSON")
	}
	// Should fall back to default question.
	if got.promptQuestion != "A team has encountered a blocker." {
		t.Errorf("promptQuestion = %q, want default fallback", got.promptQuestion)
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
		Calls: []llm.ToolCall{
			{
				ID:       "call-escalate-nocontext",
				Function: llm.ToolCallFunction{Name: "escalate_to_user", Arguments: string(args)},
			},
		},
	}

	result, _ := m.handleToolCalls(msg)
	got := result.(*Model)

	// When context is empty, the question should not have the double newline separator.
	if got.promptQuestion != "Need help" {
		t.Errorf("promptQuestion = %q, want %q", got.promptQuestion, "Need help")
	}
}

func TestHandleToolCalls_NormalToolExecution(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	// Use a temp dir so job_list doesn't fail.
	m.toolExec.WorkspaceDir = t.TempDir()

	msg := ToolCallMsg{
		Calls: []llm.ToolCall{
			{
				ID:       "call-joblist-1",
				Function: llm.ToolCallFunction{Name: "job_list", Arguments: "{}"},
			},
		},
	}

	initialEntries := len(m.entries)
	result, cmd := m.handleToolCalls(msg)
	got := result.(*Model)

	// Should not enter prompt mode for normal tools.
	if got.promptMode {
		t.Error("promptMode should be false for normal tool calls")
	}

	// Should have appended entries: assistant tool call turn + indicator (but NOT tool result yet — that's async).
	if len(got.entries) <= initialEntries {
		t.Errorf("expected entries to grow from %d, got %d", initialEntries, len(got.entries))
	}

	// Verify the tool call indicator was appended.
	foundIndicator := false
	for _, entry := range got.entries {
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
	m.streaming = true

	args, _ := json.Marshal(map[string]string{"question": "test?", "context": ""})
	msg := ToolCallMsg{
		Calls: []llm.ToolCall{
			{
				ID:       "call-ask-stream",
				Function: llm.ToolCallFunction{Name: "ask_user", Arguments: string(args)},
			},
		},
	}

	result, _ := m.handleToolCalls(msg)
	got := result.(*Model)

	// streaming should be false after handleToolCalls intercepts a special tool.
	if got.streaming {
		t.Error("streaming should be false after intercepting ask_user")
	}
}

func TestHandleToolCalls_MultipleCallsFirstSpecialWins(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)

	killArgs, _ := json.Marshal(map[string]int{"slot_id": 0})
	askArgs, _ := json.Marshal(map[string]any{"question": "test?", "options": []string{}})

	claudeCfg := config.ClaudeConfig{Path: "/usr/bin/true"}
	gw := gateway.New(claudeCfg, t.TempDir(), func() {})
	m.gateway = gw

	msg := ToolCallMsg{
		Calls: []llm.ToolCall{
			{
				ID:       "call-kill-first",
				Function: llm.ToolCallFunction{Name: "kill_slot", Arguments: string(killArgs)},
			},
			{
				ID:       "call-ask-second",
				Function: llm.ToolCallFunction{Name: "ask_user", Arguments: string(askArgs)},
			},
		},
	}

	result, _ := m.handleToolCalls(msg)
	got := result.(*Model)

	// The first special tool (kill_slot) should be intercepted; the second should be ignored.
	if !got.confirmKill {
		t.Error("confirmKill should be true — kill_slot was first")
	}
	if got.promptPendingCall.ID != "call-kill-first" {
		t.Errorf("promptPendingCall.ID = %q, want %q", got.promptPendingCall.ID, "call-kill-first")
	}
}

// --------------------------------------------------------------------------
// handleAskUserResponse tests
// --------------------------------------------------------------------------

func TestHandleAskUserResponse_TimeoutConfirm_Continue(t *testing.T) {
	t.Parallel()

	claudeCfg := config.ClaudeConfig{Path: "/usr/bin/true"}
	gw := gateway.New(claudeCfg, t.TempDir(), func() {})

	m := newMinimalModel(t)
	m.gateway = gw
	m.confirmTimeout = true
	m.promptMode = true
	m.pendingTimeoutSlot = 1
	m.promptOptions = []string{"Continue (+15m)", "Kill"}

	msg := AskUserResponseMsg{Result: "Continue (+15m)"}

	result, cmd := m.handleAskUserResponse(msg)
	got := result.(*Model)

	if got.confirmTimeout {
		t.Error("confirmTimeout should be false after response")
	}
	if got.promptMode {
		t.Error("promptMode should be false after timeout response")
	}
	if got.promptOptions != nil {
		t.Errorf("promptOptions should be nil, got %v", got.promptOptions)
	}

	// Should have appended an entry about extending.
	found := false
	for _, entry := range got.entries {
		if strings.Contains(entry.Message.Content, "Slot 1 extended") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected entry about slot extension")
	}

	// cmd should be input focus.
	if cmd == nil {
		t.Error("expected non-nil cmd (input focus)")
	}
}

func TestHandleAskUserResponse_TimeoutConfirm_Kill(t *testing.T) {
	t.Parallel()

	claudeCfg := config.ClaudeConfig{Path: "/usr/bin/true"}
	gw := gateway.New(claudeCfg, t.TempDir(), func() {})

	m := newMinimalModel(t)
	m.gateway = gw
	m.confirmTimeout = true
	m.promptMode = true
	m.pendingTimeoutSlot = 2
	m.promptOptions = []string{"Continue (+15m)", "Kill"}

	msg := AskUserResponseMsg{Result: "Kill"}

	result, _ := m.handleAskUserResponse(msg)
	got := result.(*Model)

	if got.confirmTimeout {
		t.Error("confirmTimeout should be false after response")
	}
	if got.promptMode {
		t.Error("promptMode should be false after timeout response")
	}

	// Should have appended an entry about killing.
	found := false
	for _, entry := range got.entries {
		if strings.Contains(entry.Message.Content, "Slot 2 killed") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected entry about slot being killed")
	}
}

func TestHandleAskUserResponse_KillConfirm_Yes(t *testing.T) {
	t.Parallel()

	claudeCfg := config.ClaudeConfig{Path: "/usr/bin/true"}
	gw := gateway.New(claudeCfg, t.TempDir(), func() {})

	m := newMinimalModel(t)
	m.gateway = gw
	m.confirmKill = true
	m.promptMode = true
	m.pendingKillSlot = 0
	m.promptPendingCall = llm.ToolCall{ID: "call-kill-confirm"}

	msg := AskUserResponseMsg{Result: "Yes, kill"}

	result, cmd := m.handleAskUserResponse(msg)
	got := result.(*Model)

	if got.confirmKill {
		t.Error("confirmKill should be false after response")
	}
	if got.promptMode {
		t.Error("promptMode should be false after kill response")
	}

	// Should have appended a tool result entry with "killed slot 0".
	found := false
	for _, entry := range got.entries {
		if entry.Message.Role == "tool" && strings.Contains(entry.Message.Content, "killed slot 0") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected tool result entry containing 'killed slot 0'")
	}

	// Should return a cmd to start a new stream.
	if cmd == nil {
		t.Error("expected non-nil cmd (startStream) after kill confirmation")
	}
}

func TestHandleAskUserResponse_KillConfirm_Cancel(t *testing.T) {
	t.Parallel()

	claudeCfg := config.ClaudeConfig{Path: "/usr/bin/true"}
	gw := gateway.New(claudeCfg, t.TempDir(), func() {})

	m := newMinimalModel(t)
	m.gateway = gw
	m.confirmKill = true
	m.promptMode = true
	m.pendingKillSlot = 1
	m.promptPendingCall = llm.ToolCall{ID: "call-kill-cancel"}

	msg := AskUserResponseMsg{Result: "Cancel"}

	result, cmd := m.handleAskUserResponse(msg)
	got := result.(*Model)

	if got.confirmKill {
		t.Error("confirmKill should be false after cancel")
	}

	// Should have appended a tool result entry with "User cancelled the kill."
	found := false
	for _, entry := range got.entries {
		if entry.Message.Role == "tool" && strings.Contains(entry.Message.Content, "User cancelled the kill") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected tool result entry containing 'User cancelled the kill'")
	}

	if cmd == nil {
		t.Error("expected non-nil cmd (startStream) after kill cancellation")
	}
}

func TestHandleAskUserResponse_DispatchConfirm_YesDispatch(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.toolExec.WorkspaceDir = t.TempDir()
	m.confirmDispatch = true
	m.promptMode = true
	m.pendingDispatch = llm.ToolCall{
		ID:       "call-dispatch-yes",
		Function: llm.ToolCallFunction{Name: "assign_team", Arguments: `{"team_name":"alpha","job_id":"job-1","task":"do stuff"}`},
	}

	msg := AskUserResponseMsg{Result: "Yes, dispatch"}

	result, cmd := m.handleAskUserResponse(msg)
	got := result.(*Model)

	if got.confirmDispatch {
		t.Error("confirmDispatch should be false after dispatch")
	}
	if got.promptMode {
		t.Error("promptMode should be false after dispatch")
	}

	// Should have appended entries for the tool call and result.
	foundToolResult := false
	for _, entry := range got.entries {
		if entry.Message.Role == "tool" && entry.Message.ToolCallID == "call-dispatch-yes" {
			foundToolResult = true
			break
		}
	}
	if !foundToolResult {
		t.Error("expected tool result entry for dispatch")
	}

	if cmd == nil {
		t.Error("expected non-nil cmd (startStream) after dispatch")
	}
}

func TestHandleAskUserResponse_DispatchConfirm_Cancel(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	m.confirmDispatch = true
	m.promptMode = true
	m.pendingDispatch = llm.ToolCall{
		ID:       "call-dispatch-cancel",
		Function: llm.ToolCallFunction{Name: "assign_team", Arguments: `{"team_name":"beta","job_id":"job-2"}`},
	}

	msg := AskUserResponseMsg{Result: "Cancel"}

	result, cmd := m.handleAskUserResponse(msg)
	got := result.(*Model)

	if got.confirmDispatch {
		t.Error("confirmDispatch should be false after cancel")
	}

	// Should have appended a tool result with "User cancelled the dispatch."
	found := false
	for _, entry := range got.entries {
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
	m.confirmDispatch = true
	m.promptMode = true
	m.teams = []agents.Team{
		{Name: "alpha"},
		{Name: "beta"},
		{Name: "gamma"},
	}
	m.pendingDispatch = llm.ToolCall{
		ID:       "call-dispatch-change",
		Function: llm.ToolCallFunction{Name: "assign_team", Arguments: `{"team_name":"alpha","job_id":"job-3"}`},
	}

	msg := AskUserResponseMsg{Result: "Change team"}

	result, cmd := m.handleAskUserResponse(msg)
	got := result.(*Model)

	// Should enter the "change team" sub-prompt.
	if !got.promptMode {
		t.Error("promptMode should be true for change team sub-prompt")
	}
	if !got.confirmDispatch {
		t.Error("confirmDispatch should remain true during change team flow")
	}
	if !got.changingTeam {
		t.Error("changingTeam should be true")
	}
	if got.promptQuestion != "Select a team:" {
		t.Errorf("promptQuestion = %q, want %q", got.promptQuestion, "Select a team:")
	}
	if len(got.promptOptions) != 3 {
		t.Fatalf("promptOptions length = %d, want 3", len(got.promptOptions))
	}
	if got.promptOptions[0] != "alpha" || got.promptOptions[1] != "beta" || got.promptOptions[2] != "gamma" {
		t.Errorf("promptOptions = %v, want [alpha beta gamma]", got.promptOptions)
	}
	if got.promptSelected != 0 {
		t.Errorf("promptSelected = %d, want 0", got.promptSelected)
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
	m.confirmDispatch = true
	m.changingTeam = true
	m.promptMode = true
	m.pendingDispatch = llm.ToolCall{
		ID:       "call-dispatch-changed",
		Function: llm.ToolCallFunction{Name: "assign_team", Arguments: `{"team_name":"alpha","job_id":"job-4","task":"do stuff"}`},
	}

	msg := AskUserResponseMsg{Result: "beta"}

	result, cmd := m.handleAskUserResponse(msg)
	got := result.(*Model)

	if got.changingTeam {
		t.Error("changingTeam should be false after team selection")
	}
	if got.confirmDispatch {
		t.Error("confirmDispatch should be false after team selection")
	}
	if got.promptMode {
		t.Error("promptMode should be false after team selection")
	}

	// The pending dispatch args should have been rewritten with the new team name.
	foundToolCall := false
	for _, entry := range got.entries {
		if entry.Message.Role == "assistant" && len(entry.Message.ToolCalls) > 0 {
			var args map[string]any
			_ = json.Unmarshal([]byte(entry.Message.ToolCalls[0].Function.Arguments), &args)
			if args["team_name"] == "beta" {
				foundToolCall = true
				break
			}
		}
	}
	if !foundToolCall {
		t.Error("expected the tool call entry to have team_name rewritten to 'beta'")
	}

	if cmd == nil {
		t.Error("expected non-nil cmd (startStream) after team change")
	}
}

func TestHandleAskUserResponse_NormalAskUser(t *testing.T) {
	t.Parallel()

	m := newMinimalModel(t)
	// No confirmTimeout, confirmKill, or confirmDispatch flags set.
	m.promptMode = true
	m.promptCustom = true
	m.promptQuestion = "What color?"
	m.promptOptions = []string{"Red", "Blue"}
	m.promptSelected = 1
	m.input.SetValue("some leftover text")

	pendingCall := llm.ToolCall{
		ID:       "call-normal-ask",
		Function: llm.ToolCallFunction{Name: "ask_user"},
	}

	msg := AskUserResponseMsg{Call: pendingCall, Result: "Blue"}

	result, cmd := m.handleAskUserResponse(msg)
	got := result.(*Model)

	// All prompt state should be cleared.
	if got.promptMode {
		t.Error("promptMode should be false")
	}
	if got.promptCustom {
		t.Error("promptCustom should be false")
	}
	if got.promptQuestion != "" {
		t.Errorf("promptQuestion = %q, want empty", got.promptQuestion)
	}
	if got.promptOptions != nil {
		t.Errorf("promptOptions should be nil, got %v", got.promptOptions)
	}
	if got.promptSelected != 0 {
		t.Errorf("promptSelected = %d, want 0", got.promptSelected)
	}
	if got.input.Value() != "" {
		t.Errorf("input value = %q, want empty", got.input.Value())
	}

	// Should have appended an assistant tool call entry and a tool result entry.
	foundAssistantToolCall := false
	foundToolResult := false
	for _, entry := range got.entries {
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
	m.promptMode = true
	m.promptOptions = []string{"Yes", "No"}
	m.promptSelected = 0
	m.promptPendingCall = llm.ToolCall{
		ID:       "call-flow-1",
		Function: llm.ToolCallFunction{Name: "ask_user"},
	}

	// Step 1: Navigate down to "No".
	result1, _ := m.updatePromptMode(specialKey(tea.KeyDown))
	mp := result1.(*Model)
	if mp.promptSelected != 1 {
		t.Fatalf("after down, promptSelected = %d, want 1", mp.promptSelected)
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

	if mp.promptMode {
		t.Error("promptMode should be false after handling response")
	}

	// Should have appended entries.
	if len(mp.entries) == 0 {
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
	m.promptMode = true
	m.promptOptions = []string{"Option A"}
	m.promptSelected = 0
	m.promptPendingCall = llm.ToolCall{
		ID:       "call-flow-custom",
		Function: llm.ToolCallFunction{Name: "ask_user"},
	}

	// Step 1: Navigate to "Custom response..." (index 1, since there's 1 option + Custom).
	result1, _ := m.updatePromptMode(specialKey(tea.KeyDown))
	mp := result1.(*Model)
	if mp.promptSelected != 1 {
		t.Fatalf("after down, promptSelected = %d, want 1", mp.promptSelected)
	}

	// Step 2: Press enter to enter custom mode.
	result2, _ := mp.updatePromptMode(specialKey(tea.KeyEnter))
	mp = result2.(*Model)
	if !mp.promptCustom {
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
