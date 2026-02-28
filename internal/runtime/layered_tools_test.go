package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// layeredMockToolExecutor is a mock ToolExecutor for layered tool tests.
// Unlike the existing mockToolExecutor (which returns "ok" for unknown tools),
// this one returns ErrUnknownTool for tools not in its handler, matching the
// contract that LayeredToolExecutor depends on for fall-through behavior.
type layeredMockToolExecutor struct {
	defs    []ToolDef
	handler func(ctx context.Context, name string, args json.RawMessage) (string, error)
}

func (m *layeredMockToolExecutor) Definitions() []ToolDef { return m.defs }

func (m *layeredMockToolExecutor) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if m.handler != nil {
		return m.handler(ctx, name, args)
	}
	return "", fmt.Errorf("%w: %s", ErrUnknownTool, name)
}

// TestLayeredToolExecutor_OverlayWins verifies that when both overlay and base
// define a tool with the same name, Execute() dispatches to the overlay.
func TestLayeredToolExecutor_OverlayWins(t *testing.T) {
	t.Parallel()

	base := &layeredMockToolExecutor{
		defs: []ToolDef{{Name: "report_progress", Description: "base version"}},
		handler: func(_ context.Context, name string, _ json.RawMessage) (string, error) {
			if name == "report_progress" {
				return "base handled report_progress", nil
			}
			return "", fmt.Errorf("%w: %s", ErrUnknownTool, name)
		},
	}

	overlay := &layeredMockToolExecutor{
		defs: []ToolDef{{Name: "report_progress", Description: "overlay version"}},
		handler: func(_ context.Context, name string, _ json.RawMessage) (string, error) {
			if name == "report_progress" {
				return "overlay handled report_progress", nil
			}
			return "", fmt.Errorf("%w: %s", ErrUnknownTool, name)
		},
	}

	layered := NewLayeredToolExecutor(base, overlay)

	result, err := layered.Execute(context.Background(), "report_progress", json.RawMessage(`{}`))
	assertNoError(t, err)
	assertEqual(t, "overlay handled report_progress", result)
}

// TestLayeredToolExecutor_FallsThrough verifies that when the overlay returns
// ErrUnknownTool, the call falls through to the base executor.
func TestLayeredToolExecutor_FallsThrough(t *testing.T) {
	t.Parallel()

	base := &layeredMockToolExecutor{
		defs: []ToolDef{{Name: "read_file", Description: "Read a file"}},
		handler: func(_ context.Context, name string, _ json.RawMessage) (string, error) {
			if name == "read_file" {
				return "base handled read_file", nil
			}
			return "", fmt.Errorf("%w: %s", ErrUnknownTool, name)
		},
	}

	// Overlay only handles "complete_task" — not "read_file".
	overlay := &layeredMockToolExecutor{
		defs: []ToolDef{{Name: "complete_task", Description: "Complete a task"}},
		handler: func(_ context.Context, name string, _ json.RawMessage) (string, error) {
			if name == "complete_task" {
				return "overlay handled complete_task", nil
			}
			return "", fmt.Errorf("%w: %s", ErrUnknownTool, name)
		},
	}

	layered := NewLayeredToolExecutor(base, overlay)

	result, err := layered.Execute(context.Background(), "read_file", json.RawMessage(`{}`))
	assertNoError(t, err)
	assertEqual(t, "base handled read_file", result)
}

// TestLayeredToolExecutor_Definitions_Dedup verifies that when both layers
// define a tool with the same name, Definitions() returns the overlay's version.
func TestLayeredToolExecutor_Definitions_Dedup(t *testing.T) {
	t.Parallel()

	base := &layeredMockToolExecutor{
		defs: []ToolDef{
			{Name: "report_progress", Description: "base report_progress"},
			{Name: "read_file", Description: "base read_file"},
		},
	}

	overlay := &layeredMockToolExecutor{
		defs: []ToolDef{
			{Name: "report_progress", Description: "overlay report_progress"},
			{Name: "complete_task", Description: "overlay complete_task"},
		},
	}

	layered := NewLayeredToolExecutor(base, overlay)
	defs := layered.Definitions()

	// Should have 3 unique tools: report_progress (overlay), complete_task (overlay), read_file (base).
	if len(defs) != 3 {
		t.Fatalf("want 3 definitions, got %d: %v", len(defs), toolNames(defs))
	}

	// Find report_progress and verify it's the overlay version.
	var found bool
	for _, d := range defs {
		if d.Name == "report_progress" {
			found = true
			if d.Description != "overlay report_progress" {
				t.Errorf("report_progress should be overlay version, got description %q", d.Description)
			}
		}
	}
	if !found {
		t.Error("report_progress not found in definitions")
	}
}

// TestLayeredToolExecutor_Definitions_Union verifies that tools unique to each
// layer are both present in Definitions().
func TestLayeredToolExecutor_Definitions_Union(t *testing.T) {
	t.Parallel()

	base := &layeredMockToolExecutor{
		defs: []ToolDef{
			{Name: "read_file", Description: "Read a file"},
			{Name: "write_file", Description: "Write a file"},
		},
	}

	overlay := &layeredMockToolExecutor{
		defs: []ToolDef{
			{Name: "complete_task", Description: "Complete a task"},
			{Name: "report_blocker", Description: "Report a blocker"},
		},
	}

	layered := NewLayeredToolExecutor(base, overlay)
	defs := layered.Definitions()

	if len(defs) != 4 {
		t.Fatalf("want 4 definitions, got %d: %v", len(defs), toolNames(defs))
	}

	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Name] = true
	}

	for _, want := range []string{"read_file", "write_file", "complete_task", "report_blocker"} {
		if !names[want] {
			t.Errorf("expected %q in definitions, not found", want)
		}
	}
}

// TestLayeredToolExecutor_Definitions_OverlayFirst verifies that overlay
// definitions appear before base definitions in the returned slice.
func TestLayeredToolExecutor_Definitions_OverlayFirst(t *testing.T) {
	t.Parallel()

	base := &layeredMockToolExecutor{
		defs: []ToolDef{{Name: "read_file", Description: "base"}},
	}
	overlay := &layeredMockToolExecutor{
		defs: []ToolDef{{Name: "complete_task", Description: "overlay"}},
	}

	layered := NewLayeredToolExecutor(base, overlay)
	defs := layered.Definitions()

	if len(defs) != 2 {
		t.Fatalf("want 2 definitions, got %d", len(defs))
	}
	// Overlay defs come first.
	assertEqual(t, "complete_task", defs[0].Name)
	assertEqual(t, "read_file", defs[1].Name)
}

// TestLayeredToolExecutor_OverlayError verifies that when the overlay returns
// a non-ErrUnknownTool error, it is returned directly without falling through
// to the base.
func TestLayeredToolExecutor_OverlayError(t *testing.T) {
	t.Parallel()

	baseCalled := false
	base := &layeredMockToolExecutor{
		defs: []ToolDef{{Name: "complete_task", Description: "base"}},
		handler: func(_ context.Context, name string, _ json.RawMessage) (string, error) {
			baseCalled = true
			return "base result", nil
		},
	}

	overlayErr := errors.New("overlay internal error")
	overlay := &layeredMockToolExecutor{
		defs: []ToolDef{{Name: "complete_task", Description: "overlay"}},
		handler: func(_ context.Context, name string, _ json.RawMessage) (string, error) {
			// Return a non-ErrUnknownTool error.
			return "", overlayErr
		},
	}

	layered := NewLayeredToolExecutor(base, overlay)

	_, err := layered.Execute(context.Background(), "complete_task", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error from overlay, got nil")
	}
	if !errors.Is(err, overlayErr) {
		t.Errorf("expected overlay error, got: %v", err)
	}
	if baseCalled {
		t.Error("base should NOT have been called when overlay returns a non-ErrUnknownTool error")
	}
}

// TestLayeredToolExecutor_BothUnknown verifies that when both overlay and base
// return ErrUnknownTool, the base's error is returned.
func TestLayeredToolExecutor_BothUnknown(t *testing.T) {
	t.Parallel()

	base := &layeredMockToolExecutor{}    // no handler → returns ErrUnknownTool
	overlay := &layeredMockToolExecutor{} // no handler → returns ErrUnknownTool

	layered := NewLayeredToolExecutor(base, overlay)

	_, err := layered.Execute(context.Background(), "nonexistent", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrUnknownTool) {
		t.Errorf("expected ErrUnknownTool, got: %v", err)
	}
}

// TestLayeredToolExecutor_ContextPropagated verifies that the context is
// forwarded to both overlay and base executors.
func TestLayeredToolExecutor_ContextPropagated(t *testing.T) {
	t.Parallel()

	type ctxKey struct{}

	var capturedCtx context.Context
	base := &layeredMockToolExecutor{
		defs: []ToolDef{{Name: "read_file", Description: "base"}},
		handler: func(ctx context.Context, name string, _ json.RawMessage) (string, error) {
			capturedCtx = ctx
			return "ok", nil
		},
	}

	// Overlay doesn't handle read_file → falls through to base.
	overlay := &layeredMockToolExecutor{}

	layered := NewLayeredToolExecutor(base, overlay)

	ctx := context.WithValue(context.Background(), ctxKey{}, "sentinel")
	_, err := layered.Execute(ctx, "read_file", json.RawMessage(`{}`))
	assertNoError(t, err)

	if capturedCtx.Value(ctxKey{}) != "sentinel" {
		t.Error("context was not propagated to base executor")
	}
}

// TestLayeredToolExecutor_ArgsPropagated verifies that arguments are forwarded
// correctly to both overlay and base.
func TestLayeredToolExecutor_ArgsPropagated(t *testing.T) {
	t.Parallel()

	expectedArgs := json.RawMessage(`{"key":"value"}`)

	var capturedArgs json.RawMessage
	base := &layeredMockToolExecutor{
		defs: []ToolDef{{Name: "read_file", Description: "base"}},
		handler: func(_ context.Context, name string, args json.RawMessage) (string, error) {
			capturedArgs = args
			return "ok", nil
		},
	}

	overlay := &layeredMockToolExecutor{} // falls through

	layered := NewLayeredToolExecutor(base, overlay)

	_, err := layered.Execute(context.Background(), "read_file", expectedArgs)
	assertNoError(t, err)

	if string(capturedArgs) != string(expectedArgs) {
		t.Errorf("expected args %q, got %q", string(expectedArgs), string(capturedArgs))
	}
}
