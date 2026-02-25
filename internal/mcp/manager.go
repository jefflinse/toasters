package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	newToolIndex := make(map[string]toolIndexEntry)

	for _, cfg := range servers {
		if cfg.Name == "" {
			log.Printf("mcp: skipping server with empty name")
			continue
		}
		if strings.Contains(cfg.Name, "__") {
			log.Printf("mcp: skipping server %q: name must not contain '__'", cfg.Name)
			continue
		}

		c, err := createClient(cfg)
		if err != nil {
			log.Printf("mcp: failed to create client for server %q: %v", cfg.Name, err)
			continue
		}

		// Start the transport (for SSE/HTTP clients that need explicit start).
		if err := c.Start(ctx); err != nil {
			log.Printf("mcp: failed to start client for server %q: %v", cfg.Name, err)
			_ = c.Close()
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
			log.Printf("mcp: failed to initialize server %q: %v", cfg.Name, err)
			_ = c.Close()
			continue
		}

		// Discover tools.
		toolsResult, err := c.ListTools(ctx, mcptypes.ListToolsRequest{})
		if err != nil {
			log.Printf("mcp: failed to list tools for server %q: %v", cfg.Name, err)
			_ = c.Close()
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
				log.Printf("mcp: skipping tool %q on server %q: tool name must not contain '__'", t.Name, cfg.Name)
				continue
			}

			// Apply whitelist filter.
			if len(whitelist) > 0 && !whitelist[t.Name] {
				continue
			}

			// Marshal the input schema to JSON.
			schemaBytes, err := json.Marshal(t.InputSchema)
			if err != nil {
				log.Printf("mcp: failed to marshal schema for tool %q on server %q: %v", t.Name, cfg.Name, err)
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

		log.Printf("mcp: connected to server %q, discovered %d tools", cfg.Name, len(tools))
	}

	m.mu.Lock()
	m.servers = newServers
	m.toolIndex = newToolIndex
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
