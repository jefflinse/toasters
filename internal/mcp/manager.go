package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptypes "github.com/mark3labs/mcp-go/mcp"

	"github.com/jefflinse/toasters/internal/config"
)

// MCPCaller is the interface for dispatching MCP tool calls.
// Defined here so other packages can depend on this interface without
// importing internal/mcp directly.
type MCPCaller interface {
	Call(ctx context.Context, namespacedName string, args json.RawMessage) (string, error)
}

// ServerConnectionState represents the connection state of an MCP server.
type ServerConnectionState string

const (
	ServerConnected ServerConnectionState = "connected"
	ServerFailed    ServerConnectionState = "failed"
)

// ServerStatus holds the connection status and metadata for a configured MCP server.
type ServerStatus struct {
	Name      string
	Transport string // "stdio", "sse", "http"
	State     ServerConnectionState
	Error     string // empty if connected
	ToolCount int
	Tools     []ToolInfo             // the discovered tools for this server
	Config    config.MCPServerConfig // the original config
}

// ToolInfo holds metadata about a discovered MCP tool.
type ToolInfo struct {
	NamespacedName string // "{server_name}__{tool_name}"
	OriginalName   string // original tool name from the MCP server
	ServerName     string // which server this tool belongs to
	Description    string
	InputSchema    json.RawMessage // JSON Schema for tool parameters
}

// serverEntry holds a connected MCP server and its discovered tools.
type serverEntry struct {
	name   string
	client *mcpclient.Client
	tools  []ToolInfo
	cfg    config.MCPServerConfig
}

// toolIndexEntry maps a namespaced tool name to its server and original name.
type toolIndexEntry struct {
	serverIdx    int
	originalName string
}

// Manager connects to MCP servers and dispatches tool calls.
type Manager struct {
	mu        sync.RWMutex
	servers   []serverEntry
	toolIndex map[string]toolIndexEntry // namespaced tool name → server + original name
	statuses  []ServerStatus            // per-server connection status (includes failed servers)
}

// NewManager creates a new Manager.
func NewManager() *Manager {
	return &Manager{
		toolIndex: make(map[string]toolIndexEntry),
	}
}

// Connect connects to all configured MCP servers, discovers their tools,
// and builds the tool index. Failed servers are logged and skipped.
func (m *Manager) Connect(ctx context.Context, servers []config.MCPServerConfig) error {
	var newServers []serverEntry
	var newStatuses []ServerStatus
	newToolIndex := make(map[string]toolIndexEntry)

	for _, cfg := range servers {
		if cfg.Name == "" {
			slog.Warn("skipping MCP server with empty name")
			continue
		}
		if strings.Contains(cfg.Name, "__") {
			slog.Warn("skipping MCP server: name must not contain '__'", "server", cfg.Name)
			continue
		}

		c, err := createClient(cfg)
		if err != nil {
			slog.Warn("failed to create MCP client", "server", cfg.Name, "error", err)
			newStatuses = append(newStatuses, ServerStatus{
				Name:      cfg.Name,
				Transport: cfg.Transport,
				State:     ServerFailed,
				Error:     fmt.Sprintf("creating client: %v", err),
				Config:    cfg,
			})
			continue
		}

		// Start the transport (for SSE/HTTP clients that need explicit start).
		if err := c.Start(ctx); err != nil {
			slog.Warn("failed to start MCP client", "server", cfg.Name, "error", err)
			_ = c.Close()
			newStatuses = append(newStatuses, ServerStatus{
				Name:      cfg.Name,
				Transport: cfg.Transport,
				State:     ServerFailed,
				Error:     fmt.Sprintf("starting client: %v", err),
				Config:    cfg,
			})
			continue
		}

		// Initialize the MCP session.
		_, err = c.Initialize(ctx, mcptypes.InitializeRequest{
			Params: mcptypes.InitializeParams{
				ProtocolVersion: mcptypes.LATEST_PROTOCOL_VERSION,
				ClientInfo: mcptypes.Implementation{
					Name:    "toasters",
					Version: "0.1.0",
				},
			},
		})
		if err != nil {
			slog.Warn("failed to initialize MCP server", "server", cfg.Name, "error", err)
			_ = c.Close()
			newStatuses = append(newStatuses, ServerStatus{
				Name:      cfg.Name,
				Transport: cfg.Transport,
				State:     ServerFailed,
				Error:     fmt.Sprintf("initializing: %v", err),
				Config:    cfg,
			})
			continue
		}

		// Discover tools.
		toolsResult, err := c.ListTools(ctx, mcptypes.ListToolsRequest{})
		if err != nil {
			slog.Warn("failed to list MCP tools", "server", cfg.Name, "error", err)
			_ = c.Close()
			newStatuses = append(newStatuses, ServerStatus{
				Name:      cfg.Name,
				Transport: cfg.Transport,
				State:     ServerFailed,
				Error:     fmt.Sprintf("listing tools: %v", err),
				Config:    cfg,
			})
			continue
		}

		// Build whitelist set for fast lookup.
		whitelist := make(map[string]bool, len(cfg.EnabledTools))
		for _, name := range cfg.EnabledTools {
			whitelist[name] = true
		}

		var tools []ToolInfo
		for _, t := range toolsResult.Tools {
			// Reject tool names containing "__" to preserve namespace integrity.
			if strings.Contains(t.Name, "__") {
				slog.Warn("skipping MCP tool: name must not contain '__'", "tool", t.Name, "server", cfg.Name)
				continue
			}

			// Apply whitelist filter.
			if len(whitelist) > 0 && !whitelist[t.Name] {
				continue
			}

			// Marshal the input schema to JSON.
			schemaBytes, err := json.Marshal(t.InputSchema)
			if err != nil {
				slog.Warn("failed to marshal MCP tool schema", "tool", t.Name, "server", cfg.Name, "error", err)
				schemaBytes = json.RawMessage(`{"type":"object","properties":{}}`)
			}

			tools = append(tools, ToolInfo{
				NamespacedName: cfg.Name + "__" + t.Name,
				OriginalName:   t.Name,
				ServerName:     cfg.Name,
				Description:    t.Description,
				InputSchema:    schemaBytes,
			})
		}

		serverIdx := len(newServers)
		newServers = append(newServers, serverEntry{
			name:   cfg.Name,
			client: c,
			tools:  tools,
			cfg:    cfg,
		})

		for _, tool := range tools {
			newToolIndex[tool.NamespacedName] = toolIndexEntry{
				serverIdx:    serverIdx,
				originalName: tool.OriginalName,
			}
		}

		newStatuses = append(newStatuses, ServerStatus{
			Name:      cfg.Name,
			Transport: cfg.Transport,
			State:     ServerConnected,
			ToolCount: len(tools),
			Tools:     tools,
			Config:    cfg,
		})

		slog.Info("MCP server connected", "server", cfg.Name, "tools", len(tools))
	}

	m.mu.Lock()
	m.servers = newServers
	m.toolIndex = newToolIndex
	m.statuses = newStatuses
	m.mu.Unlock()

	return nil
}

// Call dispatches a tool call to the appropriate MCP server.
// It holds only a read lock to look up the server, then releases before
// making the (potentially slow) MCP call.
//
// Note: calls that have already acquired the server reference are still
// concurrent with Close(). This is acceptable — the fix below prevents new
// calls from entering after Close() has zeroed the index.
func (m *Manager) Call(ctx context.Context, namespacedName string, args json.RawMessage) (string, error) {
	m.mu.RLock()
	entry, ok := m.toolIndex[namespacedName]
	if !ok {
		m.mu.RUnlock()
		return "", fmt.Errorf("MCP tool %q not found", namespacedName)
	}
	server := m.servers[entry.serverIdx]
	originalName := entry.originalName
	m.mu.RUnlock()

	// Parse args as map[string]any for the MCP call.
	var argsMap map[string]any
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &argsMap); err != nil {
			return "", fmt.Errorf("parsing args for MCP tool %q: %w", namespacedName, err)
		}
	}

	result, err := server.client.CallTool(ctx, mcptypes.CallToolRequest{
		Params: mcptypes.CallToolParams{
			Name:      originalName,
			Arguments: argsMap,
		},
	})
	if err != nil {
		return "", fmt.Errorf("calling MCP tool %q: %w", namespacedName, err)
	}

	if result.IsError {
		// Extract error text from content blocks.
		var parts []string
		for _, c := range result.Content {
			if tc, ok := c.(mcptypes.TextContent); ok {
				parts = append(parts, tc.Text)
			}
		}
		return "", fmt.Errorf("MCP tool %q returned error: %s", namespacedName, strings.Join(parts, " "))
	}

	// Join text content blocks into a single string.
	var textParts []string
	for _, c := range result.Content {
		if tc, ok := c.(mcptypes.TextContent); ok {
			textParts = append(textParts, tc.Text)
		}
	}

	return strings.Join(textParts, "\n"), nil
}

// Tools returns all discovered tools (thread-safe).
func (m *Manager) Tools() []ToolInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var all []ToolInfo
	for _, s := range m.servers {
		all = append(all, s.tools...)
	}
	return all
}

// Servers returns the connection status of all configured MCP servers.
// Safe to call on a nil receiver (returns nil).
// Returns a copy of the internal state.
func (m *Manager) Servers() []ServerStatus {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.statuses) == 0 {
		return nil
	}
	result := make([]ServerStatus, len(m.statuses))
	copy(result, m.statuses)
	return result
}

// Close closes all MCP server connections.
// Servers and the tool index are zeroed under the lock before closing clients,
// so new Call() invocations will get "tool not found" rather than calling into
// a closed client. Calls already past the read lock are still concurrent with
// Close() — this is acceptable for the common case.
func (m *Manager) Close() error {
	m.mu.Lock()
	servers := m.servers
	m.servers = nil
	m.toolIndex = make(map[string]toolIndexEntry)
	m.statuses = nil
	m.mu.Unlock()

	var firstErr error
	for _, s := range servers {
		if err := s.client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// createClient creates an mcp-go client for the given server config.
func createClient(cfg config.MCPServerConfig) (*mcpclient.Client, error) {
	switch cfg.Transport {
	case "stdio":
		if cfg.Command == "" {
			return nil, fmt.Errorf("stdio transport requires a command")
		}
		// Build env slice in "KEY=VALUE" format.
		var env []string
		for k, v := range cfg.Env {
			env = append(env, k+"="+v)
		}
		c, err := mcpclient.NewStdioMCPClient(cfg.Command, env, cfg.Args...)
		if err != nil {
			return nil, fmt.Errorf("creating stdio client: %w", err)
		}
		return c, nil

	case "sse":
		if cfg.URL == "" {
			return nil, fmt.Errorf("sse transport requires a url")
		}
		c, err := mcpclient.NewSSEMCPClient(cfg.URL, mcpclient.WithHeaders(cfg.Headers))
		if err != nil {
			return nil, fmt.Errorf("creating SSE client: %w", err)
		}
		return c, nil

	case "http":
		if cfg.URL == "" {
			return nil, fmt.Errorf("http transport requires a url")
		}
		c, err := mcpclient.NewStreamableHttpClient(cfg.URL)
		if err != nil {
			return nil, fmt.Errorf("creating HTTP client: %w", err)
		}
		return c, nil

	default:
		return nil, fmt.Errorf("unsupported transport %q (must be stdio, sse, or http)", cfg.Transport)
	}
}
