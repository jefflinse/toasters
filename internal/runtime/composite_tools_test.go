package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// mockMCPCaller is a test double for the MCPCaller interface.
type mockMCPCaller struct {
	callFn func(ctx context.Context, name string, args json.RawMessage) (string, error)
}

func (m *mockMCPCaller) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if m.callFn != nil {
		return m.callFn(ctx, name, args)
	}
	return "", errors.New("mockMCPCaller: callFn not set")
}

// --- NewCompositeTools ---

func TestNewCompositeTools(t *testing.T) {
	t.Parallel()

	core := NewCoreTools(t.TempDir())
	mcp := &mockMCPCaller{}
	mcpDefs := []ToolDef{
		{Name: "srv__tool", Description: "An MCP tool", Parameters: json.RawMessage(`{}`)},
	}

	ct := NewCompositeTools(core, mcp, mcpDefs)
	if ct == nil {
		t.Fatal("NewCompositeTools returned nil")
	}
	if ct.core != core {
		t.Error("core field not set correctly")
	}
	if ct.mcp != mcp {
		t.Error("mcp field not set correctly")
	}
	if len(ct.mcpDefs) != 1 {
		t.Errorf("expected 1 mcpDef, got %d", len(ct.mcpDefs))
	}
}

func TestNewCompositeTools_NilMCP(t *testing.T) {
	t.Parallel()

	core := NewCoreTools(t.TempDir())
	ct := NewCompositeTools(core, nil, nil)
	if ct == nil {
		t.Fatal("NewCompositeTools returned nil")
	}
	if ct.mcp != nil {
		t.Error("expected nil mcp")
	}
}

// --- Execute: core tool dispatch ---

func TestCompositeTools_Execute_CoreToolTakesPriority(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	core := NewCoreTools(dir)

	// Write a file so read_file has something to read.
	writeTestFile(t, dir, "hello.txt", "hello world\n")

	mcpCalled := false
	mcp := &mockMCPCaller{
		callFn: func(ctx context.Context, name string, args json.RawMessage) (string, error) {
			mcpCalled = true
			return "mcp result", nil
		},
	}

	ct := NewCompositeTools(core, mcp, nil)

	result, err := ct.Execute(context.Background(), "read_file", mustJSON(t, map[string]any{
		"path": "hello.txt",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "hello world") {
		t.Errorf("expected 'hello world' in result, got %q", result)
	}
	if mcpCalled {
		t.Error("MCP caller should not have been called for a known core tool")
	}
}

func TestCompositeTools_Execute_CoreToolError_NotUnknown(t *testing.T) {
	t.Parallel()

	// read_file on a nonexistent file returns an error, but NOT "unknown tool".
	// The error should be propagated as-is, not dispatched to MCP.
	core := NewCoreTools(t.TempDir())

	mcpCalled := false
	mcp := &mockMCPCaller{
		callFn: func(ctx context.Context, name string, args json.RawMessage) (string, error) {
			mcpCalled = true
			return "mcp result", nil
		},
	}

	ct := NewCompositeTools(core, mcp, nil)

	_, err := ct.Execute(context.Background(), "read_file", mustJSON(t, map[string]any{
		"path": "nonexistent.txt",
	}))
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
	if mcpCalled {
		t.Error("MCP caller should not have been called for a non-'unknown tool' error")
	}
}

// --- Execute: MCP dispatch ---

func TestCompositeTools_Execute_MCPDispatch(t *testing.T) {
	t.Parallel()

	core := NewCoreTools(t.TempDir())

	var capturedName string
	var capturedArgs json.RawMessage
	mcp := &mockMCPCaller{
		callFn: func(ctx context.Context, name string, args json.RawMessage) (string, error) {
			capturedName = name
			capturedArgs = args
			return "mcp result", nil
		},
	}

	ct := NewCompositeTools(core, mcp, nil)

	args := json.RawMessage(`{"key":"value"}`)
	result, err := ct.Execute(context.Background(), "myserver__mytool", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "mcp result" {
		t.Errorf("expected 'mcp result', got %q", result)
	}
	if capturedName != "myserver__mytool" {
		t.Errorf("expected name 'myserver__mytool', got %q", capturedName)
	}
	if string(capturedArgs) != string(args) {
		t.Errorf("expected args %q, got %q", string(args), string(capturedArgs))
	}
}

func TestCompositeTools_Execute_MCPDispatch_ContextPropagated(t *testing.T) {
	t.Parallel()

	core := NewCoreTools(t.TempDir())

	type ctxKey struct{}
	var capturedCtx context.Context
	mcp := &mockMCPCaller{
		callFn: func(ctx context.Context, name string, args json.RawMessage) (string, error) {
			capturedCtx = ctx
			return "ok", nil
		},
	}

	ct := NewCompositeTools(core, mcp, nil)

	ctx := context.WithValue(context.Background(), ctxKey{}, "sentinel")
	_, err := ct.Execute(ctx, "srv__tool", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedCtx.Value(ctxKey{}) != "sentinel" {
		t.Error("context was not propagated to MCP caller")
	}
}

// --- Execute: unknown tool without __ ---

func TestCompositeTools_Execute_UnknownToolNoMCP(t *testing.T) {
	t.Parallel()

	core := NewCoreTools(t.TempDir())
	mcp := &mockMCPCaller{
		callFn: func(ctx context.Context, name string, args json.RawMessage) (string, error) {
			return "should not be called", nil
		},
	}

	ct := NewCompositeTools(core, mcp, nil)

	// "unknown_tool" has no "__" so it should NOT be dispatched to MCP.
	_, err := ct.Execute(context.Background(), "unknown_tool", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for unknown tool without '__', got nil")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("expected 'unknown tool' in error, got: %v", err)
	}
}

func TestCompositeTools_Execute_UnknownToolNoDoubleUnderscore(t *testing.T) {
	t.Parallel()

	core := NewCoreTools(t.TempDir())
	ct := NewCompositeTools(core, nil, nil)

	// Tool name without "__" should return the core's "unknown tool" error.
	_, err := ct.Execute(context.Background(), "completely_unknown", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("expected 'unknown tool' in error, got: %v", err)
	}
}

// --- Execute: nil MCP caller ---

func TestCompositeTools_Execute_MCPNilCaller(t *testing.T) {
	t.Parallel()

	core := NewCoreTools(t.TempDir())
	ct := NewCompositeTools(core, nil, nil)

	// Tool with "__" but nil MCP caller — should return the original "unknown tool" error.
	_, err := ct.Execute(context.Background(), "server__tool", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error when MCP caller is nil, got nil")
	}
	// The error should be the original "unknown tool" error from CoreTools.
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("expected 'unknown tool' in error, got: %v", err)
	}
}

// --- Execute: MCP error propagation ---

func TestCompositeTools_Execute_MCPError(t *testing.T) {
	t.Parallel()

	core := NewCoreTools(t.TempDir())
	mcpErr := errors.New("MCP server unavailable")
	mcp := &mockMCPCaller{
		callFn: func(ctx context.Context, name string, args json.RawMessage) (string, error) {
			return "", mcpErr
		},
	}

	ct := NewCompositeTools(core, mcp, nil)

	_, err := ct.Execute(context.Background(), "srv__tool", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error from MCP caller, got nil")
	}
	if !errors.Is(err, mcpErr) {
		t.Errorf("expected wrapped mcpErr, got: %v", err)
	}
}

func TestCompositeTools_Execute_MCPError_MessagePreserved(t *testing.T) {
	t.Parallel()

	core := NewCoreTools(t.TempDir())
	mcp := &mockMCPCaller{
		callFn: func(ctx context.Context, name string, args json.RawMessage) (string, error) {
			return "", errors.New("specific MCP failure reason")
		},
	}

	ct := NewCompositeTools(core, mcp, nil)

	_, err := ct.Execute(context.Background(), "srv__tool", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "specific MCP failure reason") {
		t.Errorf("expected error message to contain 'specific MCP failure reason', got: %v", err)
	}
}

// --- Definitions ---

func TestCompositeTools_Definitions_IncludesMCPDefs(t *testing.T) {
	t.Parallel()

	core := NewCoreTools(t.TempDir())
	mcpDefs := []ToolDef{
		{Name: "srv__tool1", Description: "MCP Tool 1", Parameters: json.RawMessage(`{}`)},
		{Name: "srv__tool2", Description: "MCP Tool 2", Parameters: json.RawMessage(`{}`)},
	}

	ct := NewCompositeTools(core, nil, mcpDefs)
	defs := ct.Definitions()

	// Should include all core defs plus the 2 MCP defs.
	coreDefs := core.Definitions()
	expectedLen := len(coreDefs) + 2
	if len(defs) != expectedLen {
		t.Errorf("expected %d definitions (core + MCP), got %d", expectedLen, len(defs))
	}

	// Verify MCP defs are present.
	nameSet := make(map[string]bool)
	for _, d := range defs {
		nameSet[d.Name] = true
	}
	if !nameSet["srv__tool1"] {
		t.Error("expected 'srv__tool1' in definitions")
	}
	if !nameSet["srv__tool2"] {
		t.Error("expected 'srv__tool2' in definitions")
	}
}

func TestCompositeTools_Definitions_EmptyMCPDefs(t *testing.T) {
	t.Parallel()

	core := NewCoreTools(t.TempDir())
	ct := NewCompositeTools(core, nil, nil)

	defs := ct.Definitions()
	coreDefs := core.Definitions()

	if len(defs) != len(coreDefs) {
		t.Errorf("expected %d definitions (core only), got %d", len(coreDefs), len(defs))
	}
}

func TestCompositeTools_Definitions_EmptyMCPDefsSlice(t *testing.T) {
	t.Parallel()

	core := NewCoreTools(t.TempDir())
	ct := NewCompositeTools(core, nil, []ToolDef{})

	defs := ct.Definitions()
	coreDefs := core.Definitions()

	if len(defs) != len(coreDefs) {
		t.Errorf("expected %d definitions (core only), got %d", len(coreDefs), len(defs))
	}
}

func TestCompositeTools_Definitions_CoreDefsFirst(t *testing.T) {
	t.Parallel()

	core := NewCoreTools(t.TempDir())
	coreDefs := core.Definitions()

	mcpDefs := []ToolDef{
		{Name: "srv__mcp_tool", Description: "MCP Tool", Parameters: json.RawMessage(`{}`)},
	}

	ct := NewCompositeTools(core, nil, mcpDefs)
	defs := ct.Definitions()

	// Core defs should come first.
	for i, coreDef := range coreDefs {
		if defs[i].Name != coreDef.Name {
			t.Errorf("expected core def %q at index %d, got %q", coreDef.Name, i, defs[i].Name)
		}
	}

	// MCP def should be last.
	lastDef := defs[len(defs)-1]
	if lastDef.Name != "srv__mcp_tool" {
		t.Errorf("expected MCP def 'srv__mcp_tool' at end, got %q", lastDef.Name)
	}
}

func TestCompositeTools_Definitions_MCPDefFields(t *testing.T) {
	t.Parallel()

	core := NewCoreTools(t.TempDir())
	schema := json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"}}}`)
	mcpDefs := []ToolDef{
		{Name: "srv__compute", Description: "Compute something", Parameters: schema},
	}

	ct := NewCompositeTools(core, nil, mcpDefs)
	defs := ct.Definitions()

	// Find the MCP def.
	var found *ToolDef
	for i := range defs {
		if defs[i].Name == "srv__compute" {
			found = &defs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected 'srv__compute' in definitions, not found")
	}
	if found.Description != "Compute something" {
		t.Errorf("expected Description 'Compute something', got %q", found.Description)
	}
	if string(found.Parameters) != string(schema) {
		t.Errorf("expected Parameters %q, got %q", string(schema), string(found.Parameters))
	}
}

// --- Execute: edge cases ---

func TestCompositeTools_Execute_DoubleUnderscoreInName(t *testing.T) {
	t.Parallel()

	core := NewCoreTools(t.TempDir())

	var capturedName string
	mcp := &mockMCPCaller{
		callFn: func(ctx context.Context, name string, args json.RawMessage) (string, error) {
			capturedName = name
			return "ok", nil
		},
	}

	ct := NewCompositeTools(core, mcp, nil)

	// Tool name with multiple "__" should still be dispatched to MCP.
	_, err := ct.Execute(context.Background(), "server__sub__tool", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedName != "server__sub__tool" {
		t.Errorf("expected full name 'server__sub__tool', got %q", capturedName)
	}
}

func TestCompositeTools_Execute_MCPSuccess_EmptyResult(t *testing.T) {
	t.Parallel()

	core := NewCoreTools(t.TempDir())
	mcp := &mockMCPCaller{
		callFn: func(ctx context.Context, name string, args json.RawMessage) (string, error) {
			return "", nil // empty result is valid
		},
	}

	ct := NewCompositeTools(core, mcp, nil)

	result, err := ct.Execute(context.Background(), "srv__tool", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty result, got %q", result)
	}
}
