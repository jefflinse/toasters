package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/db"
	"github.com/jefflinse/toasters/internal/provider"
)

// mockStreamProvider implements provider.Provider with configurable ChatStream
// responses. It is distinct from the mockProvider in streaming_test.go, which
// always returns a closed channel and is only suitable for fetchModels tests.
type mockStreamProvider struct {
	response string
	err      error // if non-nil, ChatStream returns this error directly
}

func (m *mockStreamProvider) ChatStream(_ context.Context, _ provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	if m.err != nil {
		return nil, m.err
	}
	ch := make(chan provider.StreamEvent, 2)
	ch <- provider.StreamEvent{Type: provider.EventText, Text: m.response}
	ch <- provider.StreamEvent{Type: provider.EventDone}
	close(ch)
	return ch, nil
}

func (m *mockStreamProvider) Models(_ context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (m *mockStreamProvider) Name() string {
	return "mock-stream"
}

// --- valid test content ---

const validSkillMD = `---
name: test-skill
description: A test skill
---

# Test Skill

This is a test skill.`

const validAgentMD = `---
name: test-agent
description: A test agent
mode: worker
---

# Test Agent

This is a test agent.`

// validTeamJSON is the JSON envelope that generateTeamCmd expects from the LLM.
const validTeamJSON = `{"team_md": "---\nname: test-team\ndescription: A test team\nlead: test-agent\n---\n\n# Test Team\n\nThis is a test team.\n", "agent_names": ["test-agent"]}`

// --- generateSkillCmd tests ---

func TestGenerateSkillCmd_NilClient(t *testing.T) {
	t.Parallel()

	cmd := generateSkillCmd(nil, "anything")
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}

	msg := cmd()
	skillMsg, ok := msg.(skillGeneratedMsg)
	if !ok {
		t.Fatalf("expected skillGeneratedMsg, got %T", msg)
	}
	if skillMsg.err == nil {
		t.Fatal("expected non-nil error for nil client")
	}
	if !strings.Contains(skillMsg.err.Error(), "no LLM provider") {
		t.Errorf("error = %q, want it to mention 'no LLM provider'", skillMsg.err)
	}
	if skillMsg.content != "" {
		t.Errorf("content = %q, want empty on error", skillMsg.content)
	}
}

func TestGenerateSkillCmd_HappyPath(t *testing.T) {
	t.Parallel()

	p := &mockStreamProvider{response: validSkillMD}
	cmd := generateSkillCmd(p, "a skill for reading files")
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}

	msg := cmd()
	skillMsg, ok := msg.(skillGeneratedMsg)
	if !ok {
		t.Fatalf("expected skillGeneratedMsg, got %T", msg)
	}
	if skillMsg.err != nil {
		t.Fatalf("unexpected error: %v", skillMsg.err)
	}
	if skillMsg.content == "" {
		t.Error("expected non-empty content on success")
	}
	if !strings.Contains(skillMsg.content, "test-skill") {
		t.Errorf("content = %q, want it to contain 'test-skill'", skillMsg.content)
	}
}

func TestGenerateSkillCmd_LLMError(t *testing.T) {
	t.Parallel()

	llmErr := errors.New("connection refused")
	p := &mockStreamProvider{err: llmErr}
	cmd := generateSkillCmd(p, "anything")

	msg := cmd()
	skillMsg, ok := msg.(skillGeneratedMsg)
	if !ok {
		t.Fatalf("expected skillGeneratedMsg, got %T", msg)
	}
	if skillMsg.err == nil {
		t.Fatal("expected non-nil error when LLM fails")
	}
	if !errors.Is(skillMsg.err, llmErr) {
		t.Errorf("err = %v, want it to wrap %v", skillMsg.err, llmErr)
	}
	if skillMsg.content != "" {
		t.Errorf("content = %q, want empty on error", skillMsg.content)
	}
}

func TestGenerateSkillCmd_InvalidContent(t *testing.T) {
	t.Parallel()

	// Return content that has no frontmatter — ParseBytes will fail.
	p := &mockStreamProvider{response: "this is not a valid skill definition at all"}
	cmd := generateSkillCmd(p, "anything")

	msg := cmd()
	skillMsg, ok := msg.(skillGeneratedMsg)
	if !ok {
		t.Fatalf("expected skillGeneratedMsg, got %T", msg)
	}
	if skillMsg.err == nil {
		t.Fatal("expected non-nil error for invalid content")
	}
	if !strings.Contains(skillMsg.err.Error(), "not a valid skill definition") {
		t.Errorf("error = %q, want it to mention 'not a valid skill definition'", skillMsg.err)
	}
	if skillMsg.content != "" {
		t.Errorf("content = %q, want empty on error", skillMsg.content)
	}
}

func TestGenerateSkillCmd_CodeFencesStripped(t *testing.T) {
	t.Parallel()

	// Wrap valid content in markdown code fences — the cmd should strip them.
	fenced := "```yaml\n" + validSkillMD + "\n```"
	p := &mockStreamProvider{response: fenced}
	cmd := generateSkillCmd(p, "a skill")

	msg := cmd()
	skillMsg, ok := msg.(skillGeneratedMsg)
	if !ok {
		t.Fatalf("expected skillGeneratedMsg, got %T", msg)
	}
	if skillMsg.err != nil {
		t.Fatalf("unexpected error after stripping code fences: %v", skillMsg.err)
	}
	if strings.Contains(skillMsg.content, "```") {
		t.Errorf("content still contains code fences: %q", skillMsg.content)
	}
}

// --- generateAgentCmd tests ---

func TestGenerateAgentCmd_NilClient(t *testing.T) {
	t.Parallel()

	cmd := generateAgentCmd(nil, "anything")
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}

	msg := cmd()
	agentMsg, ok := msg.(agentGeneratedMsg)
	if !ok {
		t.Fatalf("expected agentGeneratedMsg, got %T", msg)
	}
	if agentMsg.err == nil {
		t.Fatal("expected non-nil error for nil client")
	}
	if !strings.Contains(agentMsg.err.Error(), "no LLM provider") {
		t.Errorf("error = %q, want it to mention 'no LLM provider'", agentMsg.err)
	}
	if agentMsg.content != "" {
		t.Errorf("content = %q, want empty on error", agentMsg.content)
	}
}

func TestGenerateAgentCmd_HappyPath(t *testing.T) {
	t.Parallel()

	p := &mockStreamProvider{response: validAgentMD}
	cmd := generateAgentCmd(p, "a code review agent")
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}

	msg := cmd()
	agentMsg, ok := msg.(agentGeneratedMsg)
	if !ok {
		t.Fatalf("expected agentGeneratedMsg, got %T", msg)
	}
	if agentMsg.err != nil {
		t.Fatalf("unexpected error: %v", agentMsg.err)
	}
	if agentMsg.content == "" {
		t.Error("expected non-empty content on success")
	}
	if !strings.Contains(agentMsg.content, "test-agent") {
		t.Errorf("content = %q, want it to contain 'test-agent'", agentMsg.content)
	}
}

func TestGenerateAgentCmd_LLMError(t *testing.T) {
	t.Parallel()

	llmErr := errors.New("timeout")
	p := &mockStreamProvider{err: llmErr}
	cmd := generateAgentCmd(p, "anything")

	msg := cmd()
	agentMsg, ok := msg.(agentGeneratedMsg)
	if !ok {
		t.Fatalf("expected agentGeneratedMsg, got %T", msg)
	}
	if agentMsg.err == nil {
		t.Fatal("expected non-nil error when LLM fails")
	}
	if !errors.Is(agentMsg.err, llmErr) {
		t.Errorf("err = %v, want it to wrap %v", agentMsg.err, llmErr)
	}
	if agentMsg.content != "" {
		t.Errorf("content = %q, want empty on error", agentMsg.content)
	}
}

func TestGenerateAgentCmd_InvalidContent(t *testing.T) {
	t.Parallel()

	// Return content with no frontmatter — ParseBytes will fail.
	p := &mockStreamProvider{response: "not a valid agent definition"}
	cmd := generateAgentCmd(p, "anything")

	msg := cmd()
	agentMsg, ok := msg.(agentGeneratedMsg)
	if !ok {
		t.Fatalf("expected agentGeneratedMsg, got %T", msg)
	}
	if agentMsg.err == nil {
		t.Fatal("expected non-nil error for invalid content")
	}
	if !strings.Contains(agentMsg.err.Error(), "not a valid agent definition") {
		t.Errorf("error = %q, want it to mention 'not a valid agent definition'", agentMsg.err)
	}
	if agentMsg.content != "" {
		t.Errorf("content = %q, want empty on error", agentMsg.content)
	}
}

// --- generateTeamCmd tests ---

func TestGenerateTeamCmd_NilClient(t *testing.T) {
	t.Parallel()

	agents := []*db.Agent{{Name: "agent1", Description: "does stuff"}}
	cmd := generateTeamCmd(nil, "anything", agents)
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}

	msg := cmd()
	teamMsg, ok := msg.(teamGeneratedMsg)
	if !ok {
		t.Fatalf("expected teamGeneratedMsg, got %T", msg)
	}
	if teamMsg.err == nil {
		t.Fatal("expected non-nil error for nil client")
	}
	if !strings.Contains(teamMsg.err.Error(), "no LLM provider") {
		t.Errorf("error = %q, want it to mention 'no LLM provider'", teamMsg.err)
	}
	if teamMsg.content != "" {
		t.Errorf("content = %q, want empty on error", teamMsg.content)
	}
}

func TestGenerateTeamCmd_HappyPath(t *testing.T) {
	t.Parallel()

	agents := []*db.Agent{
		{Name: "test-agent", Description: "A test agent"},
	}
	p := &mockStreamProvider{response: validTeamJSON}
	cmd := generateTeamCmd(p, "a backend engineering team", agents)
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}

	msg := cmd()
	teamMsg, ok := msg.(teamGeneratedMsg)
	if !ok {
		t.Fatalf("expected teamGeneratedMsg, got %T", msg)
	}
	if teamMsg.err != nil {
		t.Fatalf("unexpected error: %v", teamMsg.err)
	}
	if teamMsg.content == "" {
		t.Error("expected non-empty content on success")
	}
	if !strings.Contains(teamMsg.content, "test-team") {
		t.Errorf("content = %q, want it to contain 'test-team'", teamMsg.content)
	}
	if len(teamMsg.agentNames) != 1 {
		t.Fatalf("expected 1 agent name, got %d", len(teamMsg.agentNames))
	}
	if teamMsg.agentNames[0] != "test-agent" {
		t.Errorf("agentNames[0] = %q, want %q", teamMsg.agentNames[0], "test-agent")
	}
}

func TestGenerateTeamCmd_LLMError(t *testing.T) {
	t.Parallel()

	llmErr := errors.New("rate limited")
	p := &mockStreamProvider{err: llmErr}
	cmd := generateTeamCmd(p, "anything", nil)

	msg := cmd()
	teamMsg, ok := msg.(teamGeneratedMsg)
	if !ok {
		t.Fatalf("expected teamGeneratedMsg, got %T", msg)
	}
	if teamMsg.err == nil {
		t.Fatal("expected non-nil error when LLM fails")
	}
	if !errors.Is(teamMsg.err, llmErr) {
		t.Errorf("err = %v, want it to wrap %v", teamMsg.err, llmErr)
	}
	if teamMsg.content != "" {
		t.Errorf("content = %q, want empty on error", teamMsg.content)
	}
}

func TestGenerateTeamCmd_InvalidJSON(t *testing.T) {
	t.Parallel()

	p := &mockStreamProvider{response: "this is not json at all"}
	cmd := generateTeamCmd(p, "anything", nil)

	msg := cmd()
	teamMsg, ok := msg.(teamGeneratedMsg)
	if !ok {
		t.Fatalf("expected teamGeneratedMsg, got %T", msg)
	}
	if teamMsg.err == nil {
		t.Fatal("expected non-nil error for invalid JSON")
	}
	if !strings.Contains(teamMsg.err.Error(), "parsing LLM JSON response") {
		t.Errorf("error = %q, want it to mention 'parsing LLM JSON response'", teamMsg.err)
	}
	if teamMsg.content != "" {
		t.Errorf("content = %q, want empty on error", teamMsg.content)
	}
}

func TestGenerateTeamCmd_ValidJSONButInvalidTeamMD(t *testing.T) {
	t.Parallel()

	// Valid JSON structure but team_md content has no frontmatter.
	invalidTeamJSON := `{"team_md": "this is not a valid team definition", "agent_names": ["agent1"]}`
	p := &mockStreamProvider{response: invalidTeamJSON}
	cmd := generateTeamCmd(p, "anything", nil)

	msg := cmd()
	teamMsg, ok := msg.(teamGeneratedMsg)
	if !ok {
		t.Fatalf("expected teamGeneratedMsg, got %T", msg)
	}
	if teamMsg.err == nil {
		t.Fatal("expected non-nil error for invalid team_md")
	}
	if !strings.Contains(teamMsg.err.Error(), "not a valid team definition") {
		t.Errorf("error = %q, want it to mention 'not a valid team definition'", teamMsg.err)
	}
	if teamMsg.content != "" {
		t.Errorf("content = %q, want empty on error", teamMsg.content)
	}
}

func TestGenerateTeamCmd_EmptyAgentNames(t *testing.T) {
	t.Parallel()

	// Valid JSON with empty agent_names — this is allowed (just no agents assigned).
	emptyAgentsJSON := `{"team_md": "---\nname: test-team\ndescription: A test team\nlead: test-agent\n---\n\n# Test Team\n\nThis is a test team.\n", "agent_names": []}`
	p := &mockStreamProvider{response: emptyAgentsJSON}
	cmd := generateTeamCmd(p, "a solo team", nil)

	msg := cmd()
	teamMsg, ok := msg.(teamGeneratedMsg)
	if !ok {
		t.Fatalf("expected teamGeneratedMsg, got %T", msg)
	}
	if teamMsg.err != nil {
		t.Fatalf("unexpected error for empty agent_names: %v", teamMsg.err)
	}
	if teamMsg.content == "" {
		t.Error("expected non-empty content on success")
	}
	if len(teamMsg.agentNames) != 0 {
		t.Errorf("agentNames = %v, want empty slice", teamMsg.agentNames)
	}
}

func TestGenerateTeamCmd_EmptyTeamMD(t *testing.T) {
	t.Parallel()

	// Valid JSON but team_md is an empty string — the cmd should return an error.
	emptyTeamMDJSON := `{"team_md": "", "agent_names": ["agent1"]}`
	p := &mockStreamProvider{response: emptyTeamMDJSON}
	cmd := generateTeamCmd(p, "anything", nil)

	msg := cmd()
	teamMsg, ok := msg.(teamGeneratedMsg)
	if !ok {
		t.Fatalf("expected teamGeneratedMsg, got %T", msg)
	}
	if teamMsg.err == nil {
		t.Fatal("expected non-nil error for empty team_md")
	}
	if !strings.Contains(teamMsg.err.Error(), "empty team_md") {
		t.Errorf("error = %q, want it to mention 'empty team_md'", teamMsg.err)
	}
}

func TestGenerateTeamCmd_AgentListIncludedInPrompt(t *testing.T) {
	t.Parallel()

	// Verify that agent descriptions are captured at call time (not at execution
	// time), and that agents with no description fall back to "(no description)".
	agents := []*db.Agent{
		{Name: "alpha", Description: "does alpha things"},
		{Name: "beta", Description: ""},
	}

	// Use a provider that captures the request to inspect the system prompt.
	var capturedReq provider.ChatRequest
	capturingProvider := &capturingStreamProvider{
		response: validTeamJSON,
		capture:  &capturedReq,
	}

	cmd := generateTeamCmd(capturingProvider, "a team", agents)
	msg := cmd()

	teamMsg, ok := msg.(teamGeneratedMsg)
	if !ok {
		t.Fatalf("expected teamGeneratedMsg, got %T", msg)
	}
	if teamMsg.err != nil {
		t.Fatalf("unexpected error: %v", teamMsg.err)
	}

	// The system prompt should contain both agent names and the fallback description.
	if !strings.Contains(capturedReq.System, "alpha") {
		t.Errorf("system prompt = %q, want it to contain 'alpha'", capturedReq.System)
	}
	if !strings.Contains(capturedReq.System, "does alpha things") {
		t.Errorf("system prompt = %q, want it to contain 'does alpha things'", capturedReq.System)
	}
	if !strings.Contains(capturedReq.System, "beta") {
		t.Errorf("system prompt = %q, want it to contain 'beta'", capturedReq.System)
	}
	if !strings.Contains(capturedReq.System, "(no description)") {
		t.Errorf("system prompt = %q, want it to contain '(no description)' for agent with empty description", capturedReq.System)
	}
}

// capturingStreamProvider records the ChatRequest it receives so tests can
// inspect the prompt that was sent to the LLM.
type capturingStreamProvider struct {
	response string
	capture  *provider.ChatRequest
}

func (c *capturingStreamProvider) ChatStream(_ context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	if c.capture != nil {
		*c.capture = req
	}
	ch := make(chan provider.StreamEvent, 2)
	ch <- provider.StreamEvent{Type: provider.EventText, Text: c.response}
	ch <- provider.StreamEvent{Type: provider.EventDone}
	close(ch)
	return ch, nil
}

func (c *capturingStreamProvider) Models(_ context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (c *capturingStreamProvider) Name() string {
	return "capturing"
}

// --- stripCodeFences tests ---

func TestStripCodeFences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no fences",
			input: "plain content",
			want:  "plain content",
		},
		{
			name:  "backtick fence no language",
			input: "```\ncontent\n```",
			want:  "content",
		},
		{
			name:  "backtick fence with yaml language",
			input: "```yaml\ncontent\n```",
			want:  "content",
		},
		{
			name:  "backtick fence with json language",
			input: "```json\n{\"key\": \"value\"}\n```",
			want:  "{\"key\": \"value\"}",
		},
		{
			name:  "leading and trailing whitespace stripped",
			input: "  \n  content  \n  ",
			want:  "content",
		},
		{
			name:  "opening fence only",
			input: "```yaml\ncontent without closing fence",
			want:  "content without closing fence",
		},
		{
			name:  "closing fence only",
			input: "content without opening fence\n```",
			want:  "content without opening fence",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "multiline content preserved",
			input: "```\nline1\nline2\nline3\n```",
			want:  "line1\nline2\nline3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := stripCodeFences(tt.input)
			if got != tt.want {
				t.Errorf("stripCodeFences(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
