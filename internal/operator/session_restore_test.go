package operator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
)

func newRestoreTestOperator(t *testing.T, sessionFile, systemPrompt string) *Operator {
	t.Helper()
	mp := &mockProvider{name: "test"}
	op, err := New(Config{
		Runtime:      runtime.New(nil, newTestRegistry(mp)),
		Provider:     mp,
		Model:        "test-model",
		WorkDir:      t.TempDir(),
		SystemPrompt: systemPrompt,
		SessionFile:  sessionFile,
	})
	if err != nil {
		t.Fatalf("creating operator: %v", err)
	}
	return op
}

func (o *Operator) messagesSnapshot() []provider.Message {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]provider.Message, len(o.messages))
	copy(out, o.messages)
	return out
}

// A new operator pointed at the previous process's session file must wake up
// with the prior conversation — otherwise a restart wipes the operator's
// working memory while its jobs persist in SQLite.
func TestLoadSession_RestoresConversation(t *testing.T) {
	sessionFile := filepath.Join(t.TempDir(), "operator.json")

	first := newRestoreTestOperator(t, sessionFile, "You are the operator.")
	first.appendMessage(provider.Message{Role: "user", Content: "create a job for X"})
	first.appendMessage(provider.Message{Role: "assistant", Content: "created job-1"})

	second := newRestoreTestOperator(t, sessionFile, "You are the operator.")
	msgs := second.messagesSnapshot()
	if len(msgs) != 2 {
		t.Fatalf("restored %d messages, want 2", len(msgs))
	}
	if msgs[0].Content != "create a job for X" || msgs[1].Content != "created job-1" {
		t.Errorf("restored conversation mismatch: %+v", msgs)
	}
}

// The freshly composed system prompt wins over the persisted one, so prompt
// edits take effect on restart.
func TestLoadSession_KeepsNewSystemPrompt(t *testing.T) {
	sessionFile := filepath.Join(t.TempDir(), "operator.json")

	first := newRestoreTestOperator(t, sessionFile, "old prompt")
	first.appendMessage(provider.Message{Role: "user", Content: "hi"})

	second := newRestoreTestOperator(t, sessionFile, "new prompt")
	if second.systemPrompt != "new prompt" {
		t.Errorf("system prompt = %q, want the freshly composed one", second.systemPrompt)
	}
}

// A crash mid-turn leaves a trailing assistant message whose tool results
// were never recorded; restoring it verbatim would 400 the first request.
func TestLoadSession_TrimsIncompleteToolRound(t *testing.T) {
	sessionFile := filepath.Join(t.TempDir(), "operator.json")

	first := newRestoreTestOperator(t, sessionFile, "You are the operator.")
	first.appendMessage(provider.Message{Role: "user", Content: "do the thing"})
	first.appendMessage(provider.Message{
		Role:      "assistant",
		ToolCalls: []provider.ToolCall{{ID: "c1", Name: "create_job", Arguments: json.RawMessage(`{}`)}},
	})
	// Crash before the tool result is appended.

	second := newRestoreTestOperator(t, sessionFile, "You are the operator.")
	msgs := second.messagesSnapshot()
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("restored %+v, want only the trailing user message", msgs)
	}
}

func TestLoadSession_CorruptFileStartsFresh(t *testing.T) {
	sessionFile := filepath.Join(t.TempDir(), "operator.json")
	if err := os.WriteFile(sessionFile, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	op := newRestoreTestOperator(t, sessionFile, "You are the operator.")
	if n := len(op.messagesSnapshot()); n != 0 {
		t.Errorf("restored %d messages from corrupt file, want 0", n)
	}
}

func TestLoadSession_MissingFileStartsFresh(t *testing.T) {
	op := newRestoreTestOperator(t, filepath.Join(t.TempDir(), "nope.json"), "You are the operator.")
	if n := len(op.messagesSnapshot()); n != 0 {
		t.Errorf("restored %d messages with no session file, want 0", n)
	}
}

func TestTrimIncompleteTail(t *testing.T) {
	user := provider.Message{Role: "user", Content: "u"}
	plain := provider.Message{Role: "assistant", Content: "a"}
	withCalls := provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c1", Name: "t", Arguments: json.RawMessage(`{}`)}}}
	toolResult := provider.Message{Role: "tool", Content: "r", ToolCallID: "c1"}

	cases := []struct {
		name string
		in   []provider.Message
		want int
	}{
		{"clean turn end", []provider.Message{user, plain}, 2},
		{"trailing user", []provider.Message{user, plain, user}, 3},
		{"complete tool round kept up to assistant", []provider.Message{user, withCalls, toolResult, plain}, 4},
		{"dangling tool calls", []provider.Message{user, withCalls}, 1},
		{"dangling tool result", []provider.Message{user, withCalls, toolResult}, 1},
		{"all dangling", []provider.Message{withCalls, toolResult}, 0},
		{"empty", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := trimIncompleteTail(tc.in)
			if len(got) != tc.want {
				t.Errorf("kept %d messages, want %d (%+v)", len(got), tc.want, got)
			}
		})
	}
}
