package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jefflinse/toasters/internal/provider"
)

func newTestRegistry(mp *mockProvider) *provider.Registry {
	reg := provider.NewRegistry()
	reg.Register(mp.Name(), mp)
	return reg
}

func TestRuntimeSpawnAgent(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Hello from agent"},
				{Type: provider.EventDone},
			}},
		},
	}

	rt := New(nil, newTestRegistry(mp))

	sess, err := rt.SpawnAgent(context.Background(), SpawnOpts{
		AgentID:        "agent-1",
		ProviderName:   "test",
		Model:          "test-model",
		InitialMessage: "Hello",
		WorkDir:        t.TempDir(),
	})
	assertNoError(t, err)

	if sess.ID() == "" {
		t.Fatal("session ID should not be empty")
	}

	// Wait for session to complete.
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete in time")
	}

	snap := sess.Snapshot()
	assertEqual(t, "completed", snap.Status)
	assertEqual(t, "agent-1", snap.AgentID)
}

func TestRuntimeSpawnAgentProviderNotFound(t *testing.T) {
	rt := New(nil, provider.NewRegistry())

	_, err := rt.SpawnAgent(context.Background(), SpawnOpts{
		ProviderName: "nonexistent",
	})
	assertError(t, err)
	assertContains(t, err.Error(), "not found")
}

func TestRuntimeGetSession(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Hello"},
				{Type: provider.EventDone},
			}},
		},
	}

	rt := New(nil, newTestRegistry(mp))

	sess, err := rt.SpawnAgent(context.Background(), SpawnOpts{
		ProviderName:   "test",
		InitialMessage: "Hello",
		WorkDir:        t.TempDir(),
	})
	assertNoError(t, err)

	// Should be findable.
	found, ok := rt.GetSession(sess.ID())
	if !ok {
		t.Fatal("session not found")
	}
	assertEqual(t, sess.ID(), found.ID())

	// Unknown session.
	_, ok = rt.GetSession("nonexistent")
	if ok {
		t.Fatal("should not find nonexistent session")
	}
}

func TestRuntimeCancelSession(t *testing.T) {
	// Provider that blocks (no EventDone).
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{
				events: []provider.StreamEvent{
					{Type: provider.EventText, Text: "Starting..."},
				},
				block: true,
			},
		},
	}

	rt := New(nil, newTestRegistry(mp))

	sess, err := rt.SpawnAgent(context.Background(), SpawnOpts{
		ProviderName:   "test",
		InitialMessage: "Block",
		WorkDir:        t.TempDir(),
	})
	assertNoError(t, err)

	// Give session time to start.
	time.Sleep(50 * time.Millisecond)

	err = rt.CancelSession(sess.ID())
	assertNoError(t, err)

	select {
	case <-sess.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("session did not exit after cancel")
	}
}

func TestRuntimeCancelSessionNotFound(t *testing.T) {
	rt := New(nil, provider.NewRegistry())
	err := rt.CancelSession("nonexistent")
	assertError(t, err)
	assertContains(t, err.Error(), "not found")
}

func TestRuntimeActiveSessions(t *testing.T) {
	// Provider that blocks (no EventDone).
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{
				events: []provider.StreamEvent{
					{Type: provider.EventText, Text: "Blocking..."},
				},
				block: true,
			},
			{
				events: []provider.StreamEvent{
					{Type: provider.EventText, Text: "Blocking..."},
				},
				block: true,
			},
		},
	}

	rt := New(nil, newTestRegistry(mp))

	sess1, err := rt.SpawnAgent(context.Background(), SpawnOpts{
		ProviderName:   "test",
		InitialMessage: "Block 1",
		WorkDir:        t.TempDir(),
	})
	assertNoError(t, err)

	sess2, err := rt.SpawnAgent(context.Background(), SpawnOpts{
		ProviderName:   "test",
		InitialMessage: "Block 2",
		WorkDir:        t.TempDir(),
	})
	assertNoError(t, err)

	// Give sessions time to start.
	time.Sleep(50 * time.Millisecond)

	active := rt.ActiveSessions()
	if len(active) != 2 {
		t.Fatalf("want 2 active sessions, got %d", len(active))
	}

	// Cancel both.
	_ = rt.CancelSession(sess1.ID())
	_ = rt.CancelSession(sess2.ID())

	<-sess1.Done()
	<-sess2.Done()

	// Give time for status to update.
	time.Sleep(50 * time.Millisecond)

	active = rt.ActiveSessions()
	if len(active) != 0 {
		t.Fatalf("want 0 active sessions, got %d", len(active))
	}
}

func TestRuntimeSpawnAndWait(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventText, Text: "Child result"},
				{Type: provider.EventDone},
			}},
		},
	}

	rt := New(nil, newTestRegistry(mp))

	result, err := rt.SpawnAndWait(context.Background(), SpawnOpts{
		ProviderName:   "test",
		InitialMessage: "Do work",
		WorkDir:        t.TempDir(),
	})
	assertNoError(t, err)
	assertEqual(t, "Child result", result)
}

func TestRuntimeSpawnAndWaitCancelled(t *testing.T) {
	// Provider that blocks.
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{
				events: []provider.StreamEvent{
					{Type: provider.EventText, Text: "Blocking..."},
				},
				block: true,
			},
		},
	}

	rt := New(nil, newTestRegistry(mp))

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := rt.SpawnAndWait(ctx, SpawnOpts{
			ProviderName:   "test",
			InitialMessage: "Block",
			WorkDir:        t.TempDir(),
		})
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	err := <-done
	assertError(t, err)
}

func TestRuntimeSpawnAndWaitFailed(t *testing.T) {
	mp := &mockProvider{
		name: "test",
		responses: []mockResponse{
			{events: []provider.StreamEvent{
				{Type: provider.EventError, Error: errors.New("provider error")},
			}},
		},
	}

	rt := New(nil, newTestRegistry(mp))

	_, err := rt.SpawnAndWait(context.Background(), SpawnOpts{
		ProviderName:   "test",
		InitialMessage: "Fail",
		WorkDir:        t.TempDir(),
	})
	assertError(t, err)
}

func TestRuntimeImplementsAgentSpawner(t *testing.T) {
	// Compile-time check that Runtime implements AgentSpawner.
	var _ AgentSpawner = (*Runtime)(nil)
}

// TestFilteredToolExecutor is a unit test for the filteredToolExecutor type
// introduced in runtime.go. It verifies that:
//   - Definitions() returns only the allowed subset, not the full inner set.
//   - Execute() delegates to the inner executor and returns its result.
func TestFilteredToolExecutor(t *testing.T) {
	inner := &mockToolExecutor{
		results: map[string]string{
			"read_file": "file contents",
			"shell":     "command output",
			"web_fetch": "page body",
		},
		defs: []ToolDef{
			{Name: "read_file", Description: "Read a file"},
			{Name: "shell", Description: "Run a shell command"},
			{Name: "web_fetch", Description: "Fetch a URL"},
		},
	}

	allowed := []ToolDef{
		{Name: "read_file", Description: "Read a file"},
		{Name: "shell", Description: "Run a shell command"},
	}

	f := &filteredToolExecutor{inner: inner, allowed: allowed}

	t.Run("Definitions returns only allowed subset", func(t *testing.T) {
		defs := f.Definitions()
		if len(defs) != 2 {
			t.Fatalf("want 2 definitions, got %d", len(defs))
		}
		names := make(map[string]bool, len(defs))
		for _, d := range defs {
			names[d.Name] = true
		}
		if !names["read_file"] {
			t.Error("expected read_file in filtered definitions")
		}
		if !names["shell"] {
			t.Error("expected shell in filtered definitions")
		}
		if names["web_fetch"] {
			t.Error("web_fetch should NOT appear in filtered definitions")
		}
	})

	t.Run("Execute delegates to inner executor", func(t *testing.T) {
		result, err := f.Execute(context.Background(), "read_file", []byte(`{}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "file contents" {
			t.Errorf("want %q, got %q", "file contents", result)
		}
	})

	t.Run("Execute rejects tools not in allowed list", func(t *testing.T) {
		// filteredToolExecutor enforces the allowlist at call time; tools not in
		// the allowed set are rejected with ErrUnknownTool even if the inner
		// executor knows how to handle them.
		_, err := f.Execute(context.Background(), "web_fetch", []byte(`{}`))
		if err == nil {
			t.Fatal("expected error for tool not in allowed list, got nil")
		}
		if !errors.Is(err, ErrUnknownTool) {
			t.Errorf("want ErrUnknownTool, got %v", err)
		}
	})
}
