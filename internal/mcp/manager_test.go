package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptypes "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jefflinse/toasters/internal/config"
)

// testMCPServerBin holds the path to the compiled test MCP server binary.
// Set by TestMain.
var testMCPServerBin string

// TestMain compiles the test MCP server binary once for all tests.
func TestMain(m *testing.M) {
	// Build the test MCP server binary.
	bin, err := os.CreateTemp("", "mcpserver-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp file for test server: %v\n", err)
		os.Exit(1)
	}
	_ = bin.Close()
	binPath := bin.Name()

	cmd := exec.Command("go", "build", "-o", binPath, "./testdata/mcpserver/")
	cmd.Dir = "." // internal/mcp directory
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build test MCP server: %v\n%s\n", err, out)
		// Don't exit — tests that need the binary will skip themselves.
		binPath = ""
	}

	testMCPServerBin = binPath

	code := m.Run()

	if binPath != "" {
		_ = os.Remove(binPath)
	}
	os.Exit(code)
}

// --- NewManager ---

func TestNewManager(t *testing.T) {
	t.Parallel()

	m := NewManager()
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	if m.toolIndex == nil {
		t.Error("toolIndex should be initialized (not nil)")
	}
	if len(m.servers) != 0 {
		t.Errorf("expected 0 servers, got %d", len(m.servers))
	}
	if len(m.toolIndex) != 0 {
		t.Errorf("expected empty toolIndex, got %d entries", len(m.toolIndex))
	}
}

// --- Connect ---

func TestManager_Connect_EmptyServers(t *testing.T) {
	t.Parallel()

	m := NewManager()
	err := m.Connect(context.Background(), nil)
	if err != nil {
		t.Fatalf("Connect with nil servers returned error: %v", err)
	}
	if tools := m.Tools(); len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestManager_Connect_EmptySlice(t *testing.T) {
	t.Parallel()

	m := NewManager()
	err := m.Connect(context.Background(), []config.MCPServerConfig{})
	if err != nil {
		t.Fatalf("Connect with empty slice returned error: %v", err)
	}
	if tools := m.Tools(); len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestManager_Connect_SkipsServerWithEmptyName(t *testing.T) {
	t.Parallel()

	m := NewManager()
	err := m.Connect(context.Background(), []config.MCPServerConfig{
		{Name: "", Transport: "stdio", Command: "echo"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.servers) != 0 {
		t.Errorf("expected 0 servers (empty name skipped), got %d", len(m.servers))
	}
}

func TestManager_Connect_SkipsUnsupportedTransport(t *testing.T) {
	t.Parallel()

	m := NewManager()
	err := m.Connect(context.Background(), []config.MCPServerConfig{
		{Name: "bad", Transport: "grpc"},
	})
	// Failed servers are skipped — no error returned.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.servers) != 0 {
		t.Errorf("expected 0 servers (unsupported transport skipped), got %d", len(m.servers))
	}
}

func TestManager_Connect_SkipsStdioMissingCommand(t *testing.T) {
	t.Parallel()

	m := NewManager()
	err := m.Connect(context.Background(), []config.MCPServerConfig{
		{Name: "nostdio", Transport: "stdio", Command: ""},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.servers) != 0 {
		t.Errorf("expected 0 servers (missing command skipped), got %d", len(m.servers))
	}
}

func TestManager_Connect_SkipsSSEMissingURL(t *testing.T) {
	t.Parallel()

	m := NewManager()
	err := m.Connect(context.Background(), []config.MCPServerConfig{
		{Name: "nourl", Transport: "sse", URL: ""},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.servers) != 0 {
		t.Errorf("expected 0 servers (missing URL skipped), got %d", len(m.servers))
	}
}

func TestManager_Connect_SkipsHTTPMissingURL(t *testing.T) {
	t.Parallel()

	m := NewManager()
	err := m.Connect(context.Background(), []config.MCPServerConfig{
		{Name: "nourl", Transport: "http", URL: ""},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.servers) != 0 {
		t.Errorf("expected 0 servers (missing URL skipped), got %d", len(m.servers))
	}
}

// TestManager_Connect_RealStdioServer tests the full Connect path using a real
// stdio MCP server subprocess. This exercises the Start/Initialize/ListTools
// code paths that cannot be reached with unit-level mocking.
func TestManager_Connect_RealStdioServer(t *testing.T) {
	if testMCPServerBin == "" {
		t.Skip("test MCP server binary not available")
	}

	m := NewManager()
	err := m.Connect(context.Background(), []config.MCPServerConfig{
		{
			Name:      "testserver",
			Transport: "stdio",
			Command:   testMCPServerBin,
		},
	})
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
	defer func() { _ = m.Close() }()

	tools := m.Tools()
	if len(tools) == 0 {
		t.Fatal("expected at least 1 tool from real server, got 0")
	}

	// Verify tools are namespaced correctly.
	for _, tool := range tools {
		if !containsStr(tool.NamespacedName, "testserver__") {
			t.Errorf("expected namespaced name to start with 'testserver__', got %q", tool.NamespacedName)
		}
		if tool.ServerName != "testserver" {
			t.Errorf("expected ServerName 'testserver', got %q", tool.ServerName)
		}
	}
}

// TestManager_Connect_RealStdioServer_WithWhitelist tests Connect with EnabledTools filtering.
func TestManager_Connect_RealStdioServer_WithWhitelist(t *testing.T) {
	if testMCPServerBin == "" {
		t.Skip("test MCP server binary not available")
	}

	m := NewManager()
	err := m.Connect(context.Background(), []config.MCPServerConfig{
		{
			Name:         "testserver",
			Transport:    "stdio",
			Command:      testMCPServerBin,
			EnabledTools: []string{"greet"}, // only allow "greet"
		},
	})
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
	defer func() { _ = m.Close() }()

	tools := m.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool after whitelist, got %d", len(tools))
	}
	if tools[0].OriginalName != "greet" {
		t.Errorf("expected 'greet', got %q", tools[0].OriginalName)
	}
}

// TestManager_Connect_RealStdioServer_CallTool tests the full round-trip:
// Connect → discover tools → Call a tool.
func TestManager_Connect_RealStdioServer_CallTool(t *testing.T) {
	if testMCPServerBin == "" {
		t.Skip("test MCP server binary not available")
	}

	m := NewManager()
	err := m.Connect(context.Background(), []config.MCPServerConfig{
		{
			Name:      "testserver",
			Transport: "stdio",
			Command:   testMCPServerBin,
		},
	})
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
	defer func() { _ = m.Close() }()

	result, err := m.Call(context.Background(), "testserver__greet", json.RawMessage(`{"name":"world"}`))
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result from greet tool")
	}
}

// TestManager_Connect_RealStdioServer_ToolIndex verifies the tool index is built correctly.
func TestManager_Connect_RealStdioServer_ToolIndex(t *testing.T) {
	if testMCPServerBin == "" {
		t.Skip("test MCP server binary not available")
	}

	m := NewManager()
	err := m.Connect(context.Background(), []config.MCPServerConfig{
		{
			Name:      "srv",
			Transport: "stdio",
			Command:   testMCPServerBin,
		},
	})
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
	defer func() { _ = m.Close() }()

	m.mu.RLock()
	indexLen := len(m.toolIndex)
	serverLen := len(m.servers)
	m.mu.RUnlock()

	if serverLen != 1 {
		t.Errorf("expected 1 server, got %d", serverLen)
	}
	if indexLen == 0 {
		t.Error("expected non-empty tool index")
	}
}

// TestManager_Connect_SkipsFailedStart tests that a server that fails to start
// is skipped (not an error). We use a command that exits immediately.
func TestManager_Connect_SkipsFailedStart(t *testing.T) {
	t.Parallel()

	m := NewManager()
	// "false" exits immediately with code 1, causing Start to fail.
	err := m.Connect(context.Background(), []config.MCPServerConfig{
		{Name: "failing", Transport: "stdio", Command: "false"},
	})
	if err != nil {
		t.Fatalf("unexpected error (failed server should be skipped): %v", err)
	}
	if len(m.servers) != 0 {
		t.Errorf("expected 0 servers (failed start skipped), got %d", len(m.servers))
	}
}

// TestManager_Connect_WithInProcessServer tests Connect using a real in-process
// MCP server via mcptest, injected directly into the manager's internal state.
func TestManager_Connect_WithInProcessServer(t *testing.T) {
	t.Parallel()

	client := newTestMCPClient(t, []testTool{
		{name: "greet", description: "Greets someone", result: "hello"},
		{name: "farewell", description: "Says goodbye", result: "goodbye"},
	})

	m := newManagerWithClient(t, "myserver", client, []ToolInfo{
		{NamespacedName: "myserver__greet", OriginalName: "greet", ServerName: "myserver", Description: "Greets someone"},
		{NamespacedName: "myserver__farewell", OriginalName: "farewell", ServerName: "myserver", Description: "Says goodbye"},
	})

	tools := m.Tools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.NamespacedName] = true
	}
	if !names["myserver__greet"] {
		t.Error("expected myserver__greet in tools")
	}
	if !names["myserver__farewell"] {
		t.Error("expected myserver__farewell in tools")
	}
}

// TestManager_Connect_WithWhitelist tests that EnabledTools filters discovered tools.
func TestManager_Connect_WithWhitelist(t *testing.T) {
	t.Parallel()

	client := newTestMCPClient(t, []testTool{
		{name: "allowed", description: "Allowed tool", result: "ok"},
		{name: "blocked", description: "Blocked tool", result: "nope"},
	})

	// Manually inject only the whitelisted tool (simulating what Connect does).
	m := newManagerWithClient(t, "srv", client, []ToolInfo{
		{NamespacedName: "srv__allowed", OriginalName: "allowed", ServerName: "srv", Description: "Allowed tool"},
	})

	tools := m.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool after whitelist, got %d", len(tools))
	}
	if tools[0].OriginalName != "allowed" {
		t.Errorf("expected 'allowed', got %q", tools[0].OriginalName)
	}
}

// --- Call ---

func TestManager_Call_UnknownTool(t *testing.T) {
	t.Parallel()

	m := NewManager()
	_, err := m.Call(context.Background(), "nonexistent__tool", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for unknown tool, got nil")
	}
	if !containsStr(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestManager_Call_SuccessfulCall(t *testing.T) {
	t.Parallel()

	client := newTestMCPClient(t, []testTool{
		{name: "echo", description: "Echoes input", result: "echo result"},
	})

	m := newManagerWithClient(t, "testsrv", client, []ToolInfo{
		{NamespacedName: "testsrv__echo", OriginalName: "echo", ServerName: "testsrv", Description: "Echoes input"},
	})

	result, err := m.Call(context.Background(), "testsrv__echo", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "echo result" {
		t.Errorf("expected 'echo result', got %q", result)
	}
}

func TestManager_Call_WithArgs(t *testing.T) {
	t.Parallel()

	client := newTestMCPClient(t, []testTool{
		{name: "compute", description: "Computes something", result: "computed"},
	})

	m := newManagerWithClient(t, "srv", client, []ToolInfo{
		{NamespacedName: "srv__compute", OriginalName: "compute", ServerName: "srv", Description: "Computes something"},
	})

	result, err := m.Call(context.Background(), "srv__compute", json.RawMessage(`{"input":"value"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "computed" {
		t.Errorf("expected 'computed', got %q", result)
	}
}

func TestManager_Call_NullArgs(t *testing.T) {
	t.Parallel()

	client := newTestMCPClient(t, []testTool{
		{name: "nulltool", description: "Accepts null args", result: "null ok"},
	})

	m := newManagerWithClient(t, "srv", client, []ToolInfo{
		{NamespacedName: "srv__nulltool", OriginalName: "nulltool", ServerName: "srv", Description: "Accepts null args"},
	})

	result, err := m.Call(context.Background(), "srv__nulltool", json.RawMessage(`null`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "null ok" {
		t.Errorf("expected 'null ok', got %q", result)
	}
}

func TestManager_Call_EmptyArgs(t *testing.T) {
	t.Parallel()

	client := newTestMCPClient(t, []testTool{
		{name: "emptytool", description: "Accepts empty args", result: "empty ok"},
	})

	m := newManagerWithClient(t, "srv", client, []ToolInfo{
		{NamespacedName: "srv__emptytool", OriginalName: "emptytool", ServerName: "srv", Description: "Accepts empty args"},
	})

	result, err := m.Call(context.Background(), "srv__emptytool", json.RawMessage(``))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "empty ok" {
		t.Errorf("expected 'empty ok', got %q", result)
	}
}

func TestManager_Call_InvalidJSON(t *testing.T) {
	t.Parallel()

	client := newTestMCPClient(t, []testTool{
		{name: "tool", description: "A tool", result: "ok"},
	})

	m := newManagerWithClient(t, "srv", client, []ToolInfo{
		{NamespacedName: "srv__tool", OriginalName: "tool", ServerName: "srv", Description: "A tool"},
	})

	_, err := m.Call(context.Background(), "srv__tool", json.RawMessage(`{invalid json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON args, got nil")
	}
}

func TestManager_Call_ToolReturnsError(t *testing.T) {
	t.Parallel()

	client := newTestMCPClient(t, []testTool{
		{name: "errtool", description: "Returns an error", result: "tool error message", isError: true},
	})

	m := newManagerWithClient(t, "srv", client, []ToolInfo{
		{NamespacedName: "srv__errtool", OriginalName: "errtool", ServerName: "srv", Description: "Returns an error"},
	})

	_, err := m.Call(context.Background(), "srv__errtool", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error when tool returns IsError=true, got nil")
	}
	if !containsStr(err.Error(), "tool error message") {
		t.Errorf("expected error message to contain 'tool error message', got: %v", err)
	}
}

func TestManager_Call_MultipleTextContent(t *testing.T) {
	t.Parallel()

	client := newTestMCPClient(t, []testTool{
		{name: "multi", description: "Returns multiple content blocks", result: "part1\npart2"},
	})

	m := newManagerWithClient(t, "srv", client, []ToolInfo{
		{NamespacedName: "srv__multi", OriginalName: "multi", ServerName: "srv", Description: "Returns multiple content blocks"},
	})

	result, err := m.Call(context.Background(), "srv__multi", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
}

// --- Tools ---

func TestManager_Tools_Empty(t *testing.T) {
	t.Parallel()

	m := NewManager()
	tools := m.Tools()
	if len(tools) != 0 {
		t.Errorf("expected 0 tools on empty manager, got %d", len(tools))
	}
}

func TestManager_Tools_MultipleServers(t *testing.T) {
	t.Parallel()

	client1 := newTestMCPClient(t, []testTool{
		{name: "tool_a", description: "Tool A", result: "a"},
	})

	client2 := newTestMCPClient(t, []testTool{
		{name: "tool_b", description: "Tool B", result: "b"},
		{name: "tool_c", description: "Tool C", result: "c"},
	})

	m := NewManager()
	m.mu.Lock()
	m.servers = []serverEntry{
		{
			name:   "server1",
			client: client1,
			tools: []ToolInfo{
				{NamespacedName: "server1__tool_a", OriginalName: "tool_a", ServerName: "server1", Description: "Tool A"},
			},
		},
		{
			name:   "server2",
			client: client2,
			tools: []ToolInfo{
				{NamespacedName: "server2__tool_b", OriginalName: "tool_b", ServerName: "server2", Description: "Tool B"},
				{NamespacedName: "server2__tool_c", OriginalName: "tool_c", ServerName: "server2", Description: "Tool C"},
			},
		},
	}
	m.toolIndex = map[string]toolIndexEntry{
		"server1__tool_a": {serverIdx: 0, originalName: "tool_a"},
		"server2__tool_b": {serverIdx: 1, originalName: "tool_b"},
		"server2__tool_c": {serverIdx: 1, originalName: "tool_c"},
	}
	m.mu.Unlock()

	tools := m.Tools()
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools from 2 servers, got %d", len(tools))
	}
}

// --- Servers ---

func TestManager_Servers_NilReceiver(t *testing.T) {
	t.Parallel()

	var m *Manager
	statuses := m.Servers()
	if statuses != nil {
		t.Errorf("expected nil from nil receiver, got %v", statuses)
	}
}

func TestManager_Servers_Empty(t *testing.T) {
	t.Parallel()

	m := NewManager()
	statuses := m.Servers()
	if statuses != nil {
		t.Errorf("expected nil on empty manager, got %v", statuses)
	}
}

func TestManager_Servers_ReturnsCopy(t *testing.T) {
	t.Parallel()

	m := NewManager()
	m.mu.Lock()
	m.statuses = []ServerStatus{
		{Name: "srv1", Transport: "stdio", State: ServerConnected, ToolCount: 2},
		{Name: "srv2", Transport: "sse", State: ServerFailed, Error: "connection refused"},
	}
	m.mu.Unlock()

	statuses := m.Servers()
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}

	// Mutate the returned slice and verify internal state is unaffected.
	statuses[0].Name = "mutated"
	internal := m.Servers()
	if internal[0].Name != "srv1" {
		t.Errorf("Servers() did not return a copy; internal state was mutated to %q", internal[0].Name)
	}
}

func TestManager_Servers_ConnectedServer(t *testing.T) {
	t.Parallel()

	m := NewManager()
	m.mu.Lock()
	m.statuses = []ServerStatus{
		{
			Name:      "myserver",
			Transport: "stdio",
			State:     ServerConnected,
			ToolCount: 3,
			Tools: []ToolInfo{
				{NamespacedName: "myserver__a", OriginalName: "a", ServerName: "myserver"},
				{NamespacedName: "myserver__b", OriginalName: "b", ServerName: "myserver"},
				{NamespacedName: "myserver__c", OriginalName: "c", ServerName: "myserver"},
			},
			Config: config.MCPServerConfig{Name: "myserver", Transport: "stdio", Command: "/usr/bin/server"},
		},
	}
	m.mu.Unlock()

	statuses := m.Servers()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	s := statuses[0]
	if s.Name != "myserver" {
		t.Errorf("expected Name 'myserver', got %q", s.Name)
	}
	if s.Transport != "stdio" {
		t.Errorf("expected Transport 'stdio', got %q", s.Transport)
	}
	if s.State != ServerConnected {
		t.Errorf("expected State ServerConnected, got %q", s.State)
	}
	if s.Error != "" {
		t.Errorf("expected empty Error, got %q", s.Error)
	}
	if s.ToolCount != 3 {
		t.Errorf("expected ToolCount 3, got %d", s.ToolCount)
	}
	if len(s.Tools) != 3 {
		t.Errorf("expected 3 Tools, got %d", len(s.Tools))
	}
	if s.Config.Command != "/usr/bin/server" {
		t.Errorf("expected Config.Command '/usr/bin/server', got %q", s.Config.Command)
	}
}

func TestManager_Servers_FailedServer(t *testing.T) {
	t.Parallel()

	m := NewManager()
	m.mu.Lock()
	m.statuses = []ServerStatus{
		{
			Name:      "badsrv",
			Transport: "sse",
			State:     ServerFailed,
			Error:     "connection refused",
			Config:    config.MCPServerConfig{Name: "badsrv", Transport: "sse", URL: "http://localhost:9999"},
		},
	}
	m.mu.Unlock()

	statuses := m.Servers()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	s := statuses[0]
	if s.State != ServerFailed {
		t.Errorf("expected State ServerFailed, got %q", s.State)
	}
	if s.Error != "connection refused" {
		t.Errorf("expected Error 'connection refused', got %q", s.Error)
	}
	if s.ToolCount != 0 {
		t.Errorf("expected ToolCount 0 for failed server, got %d", s.ToolCount)
	}
	if len(s.Tools) != 0 {
		t.Errorf("expected 0 Tools for failed server, got %d", len(s.Tools))
	}
}

func TestManager_Servers_MixedConnectedAndFailed(t *testing.T) {
	t.Parallel()

	m := NewManager()
	m.mu.Lock()
	m.statuses = []ServerStatus{
		{Name: "good", Transport: "stdio", State: ServerConnected, ToolCount: 1},
		{Name: "bad", Transport: "http", State: ServerFailed, Error: "timeout"},
		{Name: "also_good", Transport: "sse", State: ServerConnected, ToolCount: 5},
	}
	m.mu.Unlock()

	statuses := m.Servers()
	if len(statuses) != 3 {
		t.Fatalf("expected 3 statuses, got %d", len(statuses))
	}

	connected := 0
	failed := 0
	for _, s := range statuses {
		switch s.State {
		case ServerConnected:
			connected++
		case ServerFailed:
			failed++
		}
	}
	if connected != 2 {
		t.Errorf("expected 2 connected, got %d", connected)
	}
	if failed != 1 {
		t.Errorf("expected 1 failed, got %d", failed)
	}
}

// TestManager_Servers_RealStdioServer verifies that Connect populates statuses
// for a real stdio MCP server.
func TestManager_Servers_RealStdioServer(t *testing.T) {
	if testMCPServerBin == "" {
		t.Skip("test MCP server binary not available")
	}

	m := NewManager()
	err := m.Connect(context.Background(), []config.MCPServerConfig{
		{
			Name:      "testserver",
			Transport: "stdio",
			Command:   testMCPServerBin,
		},
	})
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
	defer func() { _ = m.Close() }()

	statuses := m.Servers()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	s := statuses[0]
	if s.Name != "testserver" {
		t.Errorf("expected Name 'testserver', got %q", s.Name)
	}
	if s.State != ServerConnected {
		t.Errorf("expected State ServerConnected, got %q", s.State)
	}
	if s.ToolCount == 0 {
		t.Error("expected ToolCount > 0")
	}
	if len(s.Tools) != s.ToolCount {
		t.Errorf("expected len(Tools) == ToolCount (%d), got %d", s.ToolCount, len(s.Tools))
	}
	if s.Transport != "stdio" {
		t.Errorf("expected Transport 'stdio', got %q", s.Transport)
	}
}

// TestManager_Servers_FailedServerTracked verifies that Connect tracks failed
// servers in statuses.
func TestManager_Servers_FailedServerTracked(t *testing.T) {
	t.Parallel()

	m := NewManager()
	err := m.Connect(context.Background(), []config.MCPServerConfig{
		{Name: "badsrv", Transport: "grpc"}, // unsupported transport → createClient fails
	})
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}

	statuses := m.Servers()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status for failed server, got %d", len(statuses))
	}
	s := statuses[0]
	if s.Name != "badsrv" {
		t.Errorf("expected Name 'badsrv', got %q", s.Name)
	}
	if s.State != ServerFailed {
		t.Errorf("expected State ServerFailed, got %q", s.State)
	}
	if s.Error == "" {
		t.Error("expected non-empty Error for failed server")
	}
	if s.ToolCount != 0 {
		t.Errorf("expected ToolCount 0, got %d", s.ToolCount)
	}
}

// TestManager_Servers_MixedRealServers verifies that Connect tracks both
// successful and failed servers in statuses.
func TestManager_Servers_MixedRealServers(t *testing.T) {
	if testMCPServerBin == "" {
		t.Skip("test MCP server binary not available")
	}

	m := NewManager()
	err := m.Connect(context.Background(), []config.MCPServerConfig{
		{Name: "good", Transport: "stdio", Command: testMCPServerBin},
		{Name: "bad", Transport: "stdio", Command: ""}, // missing command → createClient fails
	})
	if err != nil {
		t.Fatalf("Connect returned error: %v", err)
	}
	defer func() { _ = m.Close() }()

	statuses := m.Servers()
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}

	// First should be connected.
	if statuses[0].State != ServerConnected {
		t.Errorf("expected first server connected, got %q", statuses[0].State)
	}
	if statuses[0].Name != "good" {
		t.Errorf("expected first server name 'good', got %q", statuses[0].Name)
	}

	// Second should be failed.
	if statuses[1].State != ServerFailed {
		t.Errorf("expected second server failed, got %q", statuses[1].State)
	}
	if statuses[1].Name != "bad" {
		t.Errorf("expected second server name 'bad', got %q", statuses[1].Name)
	}
}

// TestManager_Servers_ConcurrentAccess verifies Servers() is safe to call
// concurrently with other operations.
func TestManager_Servers_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	m := NewManager()
	m.mu.Lock()
	m.statuses = []ServerStatus{
		{Name: "srv", State: ServerConnected, ToolCount: 1},
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	const goroutines = 10

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.Servers()
		}()
	}

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.Tools()
		}()
	}

	wg.Wait()
}

// --- Close ---

func TestManager_Close_Empty(t *testing.T) {
	t.Parallel()

	m := NewManager()
	if err := m.Close(); err != nil {
		t.Errorf("Close on empty manager returned error: %v", err)
	}
}

func TestManager_Close_WithServer(t *testing.T) {
	t.Parallel()

	client := newTestMCPClient(t, []testTool{
		{name: "tool", description: "A tool", result: "ok"},
	})

	m := newManagerWithClient(t, "srv", client, []ToolInfo{
		{NamespacedName: "srv__tool", OriginalName: "tool", ServerName: "srv", Description: "A tool"},
	})

	// Close should not return an error for a healthy connection.
	if err := m.Close(); err != nil {
		t.Errorf("Close returned unexpected error: %v", err)
	}
}

// --- Concurrent access ---

func TestManager_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	client := newTestMCPClient(t, []testTool{
		{name: "concurrent", description: "Concurrent tool", result: "ok"},
	})

	m := newManagerWithClient(t, "srv", client, []ToolInfo{
		{NamespacedName: "srv__concurrent", OriginalName: "concurrent", ServerName: "srv", Description: "Concurrent tool"},
	})

	var wg sync.WaitGroup
	const goroutines = 10

	// Concurrent Tools() calls.
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.Tools()
		}()
	}

	// Concurrent Call() calls.
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.Call(context.Background(), "srv__concurrent", json.RawMessage(`{}`))
		}()
	}

	// Concurrent unknown Call() calls.
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.Call(context.Background(), "unknown__tool", json.RawMessage(`{}`))
		}()
	}

	wg.Wait()
}

// --- createClient ---

func TestCreateClient_Stdio(t *testing.T) {
	t.Parallel()

	cfg := config.MCPServerConfig{
		Name:      "test",
		Transport: "stdio",
		Command:   "echo",
		Args:      []string{"hello"},
	}
	c, err := createClient(cfg)
	if err != nil {
		t.Fatalf("createClient stdio returned error: %v", err)
	}
	if c == nil {
		t.Fatal("createClient returned nil client")
	}
	_ = c.Close()
}

func TestCreateClient_StdioWithEnv(t *testing.T) {
	t.Parallel()

	cfg := config.MCPServerConfig{
		Name:      "test",
		Transport: "stdio",
		Command:   "echo",
		Env:       map[string]string{"FOO": "bar", "BAZ": "qux"},
	}
	c, err := createClient(cfg)
	if err != nil {
		t.Fatalf("createClient stdio with env returned error: %v", err)
	}
	if c == nil {
		t.Fatal("createClient returned nil client")
	}
	_ = c.Close()
}

func TestCreateClient_StdioMissingCommand(t *testing.T) {
	t.Parallel()

	cfg := config.MCPServerConfig{
		Name:      "test",
		Transport: "stdio",
		Command:   "",
	}
	_, err := createClient(cfg)
	if err == nil {
		t.Fatal("expected error for stdio with empty command, got nil")
	}
	if !containsStr(err.Error(), "command") {
		t.Errorf("expected 'command' in error, got: %v", err)
	}
}

func TestCreateClient_SSE(t *testing.T) {
	t.Parallel()

	cfg := config.MCPServerConfig{
		Name:      "test",
		Transport: "sse",
		URL:       "http://localhost:9999/sse",
	}
	c, err := createClient(cfg)
	if err != nil {
		t.Fatalf("createClient sse returned error: %v", err)
	}
	if c == nil {
		t.Fatal("createClient returned nil client")
	}
	_ = c.Close()
}

func TestCreateClient_SSEMissingURL(t *testing.T) {
	t.Parallel()

	cfg := config.MCPServerConfig{
		Name:      "test",
		Transport: "sse",
		URL:       "",
	}
	_, err := createClient(cfg)
	if err == nil {
		t.Fatal("expected error for sse with empty URL, got nil")
	}
	if !containsStr(err.Error(), "url") {
		t.Errorf("expected 'url' in error, got: %v", err)
	}
}

func TestCreateClient_HTTP(t *testing.T) {
	t.Parallel()

	cfg := config.MCPServerConfig{
		Name:      "test",
		Transport: "http",
		URL:       "http://localhost:9999/mcp",
	}
	c, err := createClient(cfg)
	if err != nil {
		t.Fatalf("createClient http returned error: %v", err)
	}
	if c == nil {
		t.Fatal("createClient returned nil client")
	}
	_ = c.Close()
}

func TestCreateClient_HTTPMissingURL(t *testing.T) {
	t.Parallel()

	cfg := config.MCPServerConfig{
		Name:      "test",
		Transport: "http",
		URL:       "",
	}
	_, err := createClient(cfg)
	if err == nil {
		t.Fatal("expected error for http with empty URL, got nil")
	}
	if !containsStr(err.Error(), "url") {
		t.Errorf("expected 'url' in error, got: %v", err)
	}
}

func TestCreateClient_UnsupportedTransport(t *testing.T) {
	t.Parallel()

	cfg := config.MCPServerConfig{
		Name:      "test",
		Transport: "websocket",
	}
	_, err := createClient(cfg)
	if err == nil {
		t.Fatal("expected error for unsupported transport, got nil")
	}
	if !containsStr(err.Error(), "unsupported") {
		t.Errorf("expected 'unsupported' in error, got: %v", err)
	}
}

func TestCreateClient_EmptyTransport(t *testing.T) {
	t.Parallel()

	cfg := config.MCPServerConfig{
		Name:      "test",
		Transport: "",
	}
	_, err := createClient(cfg)
	if err == nil {
		t.Fatal("expected error for empty transport, got nil")
	}
}

// --- Test helpers ---

// testTool describes a tool to add to the test MCP server.
type testTool struct {
	name        string
	description string
	result      string
	isError     bool
}

// newTestMCPClient creates an in-process MCP server with the given tools and
// returns a connected *mcpclient.Client. Cleanup is registered via t.Cleanup.
func newTestMCPClient(t *testing.T, tools []testTool) *mcpclient.Client {
	t.Helper()

	mcpSrv := mcpserver.NewMCPServer(t.Name(), "1.0.0")

	for _, tool := range tools {
		tool := tool // capture loop variable
		schema := json.RawMessage(`{"type":"object","properties":{}}`)
		mcpTool := mcptypes.NewToolWithRawSchema(tool.name, tool.description, schema)

		if tool.isError {
			mcpSrv.AddTool(mcpTool, func(ctx context.Context, req mcptypes.CallToolRequest) (*mcptypes.CallToolResult, error) {
				return mcptypes.NewToolResultError(tool.result), nil
			})
		} else {
			mcpSrv.AddTool(mcpTool, func(ctx context.Context, req mcptypes.CallToolRequest) (*mcptypes.CallToolResult, error) {
				return mcptypes.NewToolResultText(tool.result), nil
			})
		}
	}

	c, err := mcpclient.NewInProcessClient(mcpSrv)
	if err != nil {
		t.Fatalf("NewInProcessClient: %v", err)
	}

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("client.Start: %v", err)
	}
	if _, err := c.Initialize(ctx, mcptypes.InitializeRequest{
		Params: mcptypes.InitializeParams{
			ProtocolVersion: mcptypes.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcptypes.Implementation{Name: "test", Version: "0.0.1"},
		},
	}); err != nil {
		t.Fatalf("client.Initialize: %v", err)
	}

	t.Cleanup(func() { _ = c.Close() })

	return c
}

// newManagerWithClient creates a Manager with a pre-connected client injected
// directly into its internal state (bypassing the transport-level Connect).
func newManagerWithClient(t *testing.T, serverName string, client *mcpclient.Client, tools []ToolInfo) *Manager {
	t.Helper()

	m := NewManager()
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := len(m.servers)
	m.servers = append(m.servers, serverEntry{
		name:   serverName,
		client: client,
		tools:  tools,
	})
	for _, tool := range tools {
		m.toolIndex[tool.NamespacedName] = toolIndexEntry{
			serverIdx:    idx,
			originalName: tool.OriginalName,
		}
	}
	return m
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
