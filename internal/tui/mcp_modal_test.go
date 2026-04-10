package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jefflinse/toasters/internal/service"
)

// --------------------------------------------------------------------------
// renderMCPModal tests
// --------------------------------------------------------------------------

func TestRenderMCPModal_NoServers(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.width = 120
	m.height = 40
	m.mcpModal = mcpModalState{
		show:    true,
		servers: nil,
	}
	result := m.renderMCPModal()
	if !strings.Contains(result, "No MCP servers configured") {
		t.Error("expected 'No MCP servers configured' for empty server list")
	}
}

func TestRenderMCPModal_ConnectedServer(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.width = 120
	m.height = 40
	m.mcpModal = mcpModalState{
		show: true,
		servers: []service.MCPServerStatus{
			{
				Name:      "github",
				Transport: "stdio",
				State:     service.MCPServerStateConnected,
				ToolCount: 5,
				Tools: []service.MCPToolInfo{
					{OriginalName: "search_repos", Description: "Search repositories"},
					{OriginalName: "get_file", Description: "Get file contents"},
				},
			},
		},
	}
	result := m.renderMCPModal()
	if !strings.Contains(result, "github") {
		t.Error("expected server name 'github' in modal")
	}
	if !strings.Contains(result, "✓") {
		t.Error("expected ✓ for connected server")
	}
	if !strings.Contains(result, "stdio") {
		t.Error("expected transport 'stdio' in modal details")
	}
}

func TestRenderMCPModal_FailedServer(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.width = 120
	m.height = 40
	m.mcpModal = mcpModalState{
		show: true,
		servers: []service.MCPServerStatus{
			{
				Name:      "linear",
				Transport: "sse",
				State:     service.MCPServerStateFailed,
				Error:     "connection refused",
				ToolCount: 0,
			},
		},
	}
	result := m.renderMCPModal()
	if !strings.Contains(result, "linear") {
		t.Error("expected server name 'linear' in modal")
	}
	if !strings.Contains(result, "✗") {
		t.Error("expected ✗ for failed server")
	}
	if !strings.Contains(result, "connection refused") {
		t.Error("expected error message in modal")
	}
}

func TestRenderMCPModal_MixedServers(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.width = 120
	m.height = 40
	m.mcpModal = mcpModalState{
		show: true,
		servers: []service.MCPServerStatus{
			{Name: "github", Transport: "stdio", State: service.MCPServerStateConnected, ToolCount: 5},
			{Name: "linear", Transport: "sse", State: service.MCPServerStateFailed, Error: "timeout"},
		},
	}
	result := m.renderMCPModal()
	if !strings.Contains(result, "github") {
		t.Error("expected 'github' in modal")
	}
	if !strings.Contains(result, "linear") {
		t.Error("expected 'linear' in modal")
	}
	// Both status indicators should be present.
	if !strings.Contains(result, "✓") {
		t.Error("expected ✓ for connected server")
	}
	if !strings.Contains(result, "✗") {
		t.Error("expected ✗ for failed server")
	}
}

func TestRenderMCPModal_ServerWithStdioTransport(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.width = 120
	m.height = 40
	m.mcpModal = mcpModalState{
		show: true,
		servers: []service.MCPServerStatus{
			{
				Name:      "myserver",
				Transport: "stdio",
				State:     service.MCPServerStateConnected,
				ToolCount: 2,
			},
		},
	}
	result := m.renderMCPModal()
	if !strings.Contains(result, "myserver") {
		t.Error("expected server name 'myserver' in modal for stdio server")
	}
	if !strings.Contains(result, "stdio") {
		t.Error("expected transport 'stdio' in modal for stdio server")
	}
}

func TestRenderMCPModal_ServerWithSSETransport(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.width = 120
	m.height = 40
	m.mcpModal = mcpModalState{
		show: true,
		servers: []service.MCPServerStatus{
			{
				Name:      "remote",
				Transport: "sse",
				State:     service.MCPServerStateConnected,
				ToolCount: 3,
			},
		},
	}
	result := m.renderMCPModal()
	if !strings.Contains(result, "remote") {
		t.Error("expected server name 'remote' in modal for SSE server")
	}
	if !strings.Contains(result, "sse") {
		t.Error("expected transport 'sse' in modal for SSE server")
	}
}

func TestRenderMCPModal_ServerWithTools(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.width = 120
	m.height = 40
	m.mcpModal = mcpModalState{
		show:      true,
		serverIdx: 0,
		focus:     1, // focus on tools panel
		servers: []service.MCPServerStatus{
			{
				Name:      "github",
				Transport: "stdio",
				State:     service.MCPServerStateConnected,
				ToolCount: 3,
				Tools: []service.MCPToolInfo{
					{OriginalName: "search_repos", Description: "Search repositories"},
					{OriginalName: "get_file", Description: "Get file contents"},
					{OriginalName: "create_issue", Description: "Create an issue"},
				},
			},
		},
	}
	result := m.renderMCPModal()
	if !strings.Contains(result, "search_repos") {
		t.Error("expected tool 'search_repos' in modal")
	}
	if !strings.Contains(result, "get_file") {
		t.Error("expected tool 'get_file' in modal")
	}
	if !strings.Contains(result, "create_issue") {
		t.Error("expected tool 'create_issue' in modal")
	}
	if !strings.Contains(result, "Tools (3)") {
		t.Error("expected 'Tools (3)' header in modal")
	}
}

func TestRenderMCPModal_ServerWithToolCount(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.width = 120
	m.height = 40
	m.mcpModal = mcpModalState{
		show: true,
		servers: []service.MCPServerStatus{
			{
				Name:      "github",
				Transport: "stdio",
				State:     service.MCPServerStateConnected,
				ToolCount: 5,
			},
		},
	}
	result := m.renderMCPModal()
	if !strings.Contains(result, "github") {
		t.Error("expected server name 'github' in modal")
	}
}

func TestRenderMCPModal_FooterContainsKeyHints(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.width = 120
	m.height = 40
	m.mcpModal = mcpModalState{
		show:    true,
		servers: nil,
	}
	result := m.renderMCPModal()
	if !strings.Contains(result, "Navigate") {
		t.Error("expected navigation key hint in footer")
	}
	if !strings.Contains(result, "Tab") {
		t.Error("expected Tab key hint in footer")
	}
	if !strings.Contains(result, "Esc") {
		t.Error("expected Esc key hint in footer")
	}
}

func TestRenderMCPModal_NarrowTerminal(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.width = 60
	m.height = 25
	m.mcpModal = mcpModalState{
		show: true,
		servers: []service.MCPServerStatus{
			{Name: "test", Transport: "stdio", State: service.MCPServerStateConnected, ToolCount: 1},
		},
	}
	// Should not panic on narrow terminal.
	result := m.renderMCPModal()
	if result == "" {
		t.Error("expected non-empty result for narrow terminal")
	}
}

func TestRenderMCPModal_SecondServerSelected(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.width = 120
	m.height = 40
	m.mcpModal = mcpModalState{
		show:      true,
		serverIdx: 1,
		servers: []service.MCPServerStatus{
			{Name: "server1", Transport: "stdio", State: service.MCPServerStateConnected, ToolCount: 2},
			{
				Name:      "server2",
				Transport: "sse",
				State:     service.MCPServerStateConnected,
				ToolCount: 3,
				Tools: []service.MCPToolInfo{
					{OriginalName: "tool_a"},
					{OriginalName: "tool_b"},
					{OriginalName: "tool_c"},
				},
			},
		},
	}
	result := m.renderMCPModal()
	// The right panel should show details for server2 (the selected one).
	if !strings.Contains(result, "tool_a") {
		t.Error("expected tools from server2 in right panel when serverIdx=1")
	}
}

// --------------------------------------------------------------------------
// updateMCPModal tests
// --------------------------------------------------------------------------

func TestUpdateMCPModal_EscCloses(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.mcpModal = mcpModalState{
		show: true,
		servers: []service.MCPServerStatus{
			{Name: "test", State: service.MCPServerStateConnected},
		},
	}
	result, cmd := m.updateMCPModal(specialKey(tea.KeyEscape))
	model := result.(*Model)
	if model.mcpModal.show {
		t.Error("expected modal to be closed after Esc")
	}
	if cmd != nil {
		t.Error("expected nil cmd after Esc")
	}
}

func TestUpdateMCPModal_TabTogglesFocus(t *testing.T) {
	t.Parallel()

	t.Run("tab from servers to tools", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.mcpModal = mcpModalState{
			show:  true,
			focus: 0,
			servers: []service.MCPServerStatus{
				{Name: "test", State: service.MCPServerStateConnected},
			},
		}
		result, _ := m.updateMCPModal(specialKey(tea.KeyTab))
		model := result.(*Model)
		if model.mcpModal.focus != 1 {
			t.Errorf("expected focus=1 after Tab from 0, got %d", model.mcpModal.focus)
		}
	})

	t.Run("tab from tools to servers", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.mcpModal = mcpModalState{
			show:  true,
			focus: 1,
			servers: []service.MCPServerStatus{
				{Name: "test", State: service.MCPServerStateConnected},
			},
		}
		result, _ := m.updateMCPModal(specialKey(tea.KeyTab))
		model := result.(*Model)
		if model.mcpModal.focus != 0 {
			t.Errorf("expected focus=0 after Tab from 1, got %d", model.mcpModal.focus)
		}
	})
}

func TestUpdateMCPModal_DownNavigatesServers(t *testing.T) {
	t.Parallel()

	t.Run("down moves to next server", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.mcpModal = mcpModalState{
			show:      true,
			serverIdx: 0,
			focus:     0,
			servers: []service.MCPServerStatus{
				{Name: "server1", State: service.MCPServerStateConnected},
				{Name: "server2", State: service.MCPServerStateConnected},
			},
		}
		result, _ := m.updateMCPModal(specialKey(tea.KeyDown))
		model := result.(*Model)
		if model.mcpModal.serverIdx != 1 {
			t.Errorf("expected serverIdx=1 after Down, got %d", model.mcpModal.serverIdx)
		}
		// toolIdx should reset when changing servers.
		if model.mcpModal.toolIdx != 0 {
			t.Errorf("expected toolIdx=0 after changing server, got %d", model.mcpModal.toolIdx)
		}
	})

	t.Run("down at last server stays", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.mcpModal = mcpModalState{
			show:      true,
			serverIdx: 1,
			focus:     0,
			servers: []service.MCPServerStatus{
				{Name: "server1", State: service.MCPServerStateConnected},
				{Name: "server2", State: service.MCPServerStateConnected},
			},
		}
		result, _ := m.updateMCPModal(specialKey(tea.KeyDown))
		model := result.(*Model)
		if model.mcpModal.serverIdx != 1 {
			t.Errorf("expected serverIdx=1 (unchanged at last), got %d", model.mcpModal.serverIdx)
		}
	})
}

func TestUpdateMCPModal_UpNavigatesServers(t *testing.T) {
	t.Parallel()

	t.Run("up moves to previous server", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.mcpModal = mcpModalState{
			show:      true,
			serverIdx: 1,
			focus:     0,
			servers: []service.MCPServerStatus{
				{Name: "server1", State: service.MCPServerStateConnected},
				{Name: "server2", State: service.MCPServerStateConnected},
			},
		}
		result, _ := m.updateMCPModal(specialKey(tea.KeyUp))
		model := result.(*Model)
		if model.mcpModal.serverIdx != 0 {
			t.Errorf("expected serverIdx=0 after Up, got %d", model.mcpModal.serverIdx)
		}
	})

	t.Run("up at first server stays", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.mcpModal = mcpModalState{
			show:      true,
			serverIdx: 0,
			focus:     0,
			servers: []service.MCPServerStatus{
				{Name: "server1", State: service.MCPServerStateConnected},
				{Name: "server2", State: service.MCPServerStateConnected},
			},
		}
		result, _ := m.updateMCPModal(specialKey(tea.KeyUp))
		model := result.(*Model)
		if model.mcpModal.serverIdx != 0 {
			t.Errorf("expected serverIdx=0 (unchanged at first), got %d", model.mcpModal.serverIdx)
		}
	})
}

func TestUpdateMCPModal_DownNavigatesTools(t *testing.T) {
	t.Parallel()

	t.Run("down moves to next tool", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.mcpModal = mcpModalState{
			show:      true,
			serverIdx: 0,
			toolIdx:   0,
			focus:     1, // tools panel
			servers: []service.MCPServerStatus{
				{
					Name:      "github",
					State:     service.MCPServerStateConnected,
					ToolCount: 3,
					Tools: []service.MCPToolInfo{
						{OriginalName: "tool1"},
						{OriginalName: "tool2"},
						{OriginalName: "tool3"},
					},
				},
			},
		}
		result, _ := m.updateMCPModal(specialKey(tea.KeyDown))
		model := result.(*Model)
		if model.mcpModal.toolIdx != 1 {
			t.Errorf("expected toolIdx=1 after Down, got %d", model.mcpModal.toolIdx)
		}
	})

	t.Run("down at last tool stays", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.mcpModal = mcpModalState{
			show:      true,
			serverIdx: 0,
			toolIdx:   2,
			focus:     1,
			servers: []service.MCPServerStatus{
				{
					Name:      "github",
					State:     service.MCPServerStateConnected,
					ToolCount: 3,
					Tools: []service.MCPToolInfo{
						{OriginalName: "tool1"},
						{OriginalName: "tool2"},
						{OriginalName: "tool3"},
					},
				},
			},
		}
		result, _ := m.updateMCPModal(specialKey(tea.KeyDown))
		model := result.(*Model)
		if model.mcpModal.toolIdx != 2 {
			t.Errorf("expected toolIdx=2 (unchanged at last), got %d", model.mcpModal.toolIdx)
		}
	})
}

func TestUpdateMCPModal_UpNavigatesTools(t *testing.T) {
	t.Parallel()

	t.Run("up moves to previous tool", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.mcpModal = mcpModalState{
			show:      true,
			serverIdx: 0,
			toolIdx:   1,
			focus:     1,
			servers: []service.MCPServerStatus{
				{
					Name:      "github",
					State:     service.MCPServerStateConnected,
					ToolCount: 2,
					Tools: []service.MCPToolInfo{
						{OriginalName: "tool1"},
						{OriginalName: "tool2"},
					},
				},
			},
		}
		result, _ := m.updateMCPModal(specialKey(tea.KeyUp))
		model := result.(*Model)
		if model.mcpModal.toolIdx != 0 {
			t.Errorf("expected toolIdx=0 after Up, got %d", model.mcpModal.toolIdx)
		}
	})

	t.Run("up at first tool stays", func(t *testing.T) {
		t.Parallel()
		m := newMinimalModel(t)
		m.mcpModal = mcpModalState{
			show:      true,
			serverIdx: 0,
			toolIdx:   0,
			focus:     1,
			servers: []service.MCPServerStatus{
				{
					Name:      "github",
					State:     service.MCPServerStateConnected,
					ToolCount: 2,
					Tools: []service.MCPToolInfo{
						{OriginalName: "tool1"},
						{OriginalName: "tool2"},
					},
				},
			},
		}
		result, _ := m.updateMCPModal(specialKey(tea.KeyUp))
		model := result.(*Model)
		if model.mcpModal.toolIdx != 0 {
			t.Errorf("expected toolIdx=0 (unchanged at first), got %d", model.mcpModal.toolIdx)
		}
	})
}

func TestUpdateMCPModal_DownResetsToolIdxOnServerChange(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.mcpModal = mcpModalState{
		show:      true,
		serverIdx: 0,
		toolIdx:   2, // was browsing tools on server 0
		focus:     0, // switch focus to servers
		servers: []service.MCPServerStatus{
			{Name: "server1", State: service.MCPServerStateConnected, ToolCount: 3,
				Tools: []service.MCPToolInfo{{OriginalName: "a"}, {OriginalName: "b"}, {OriginalName: "c"}}},
			{Name: "server2", State: service.MCPServerStateConnected, ToolCount: 1,
				Tools: []service.MCPToolInfo{{OriginalName: "x"}}},
		},
	}
	result, _ := m.updateMCPModal(specialKey(tea.KeyDown))
	model := result.(*Model)
	if model.mcpModal.toolIdx != 0 {
		t.Errorf("expected toolIdx reset to 0 when changing server, got %d", model.mcpModal.toolIdx)
	}
}

func TestUpdateMCPModal_NoServersNoNavigation(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.mcpModal = mcpModalState{
		show:    true,
		servers: nil,
	}
	// Should not panic with empty server list.
	result, _ := m.updateMCPModal(specialKey(tea.KeyDown))
	model := result.(*Model)
	if model.mcpModal.serverIdx != 0 {
		t.Errorf("expected serverIdx=0 with no servers, got %d", model.mcpModal.serverIdx)
	}
}

// --------------------------------------------------------------------------
// MCPStatusMsg handler tests
// --------------------------------------------------------------------------

func TestMCPStatusMsg_FailedServerCreatesToast(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	msg := MCPStatusMsg{
		Servers: []service.MCPServerStatus{
			{Name: "github", State: service.MCPServerStateFailed, Error: "connection refused"},
		},
	}
	result, _ := m.Update(msg)
	model := result.(*Model)
	if len(model.toasts) == 0 {
		t.Fatal("expected at least one toast for failed server")
	}
	found := false
	for _, toast := range model.toasts {
		if strings.Contains(toast.message, "github") && strings.Contains(toast.message, "failed") {
			found = true
			break
		}
	}
	if !found {
		var msgs []string
		for _, toast := range model.toasts {
			msgs = append(msgs, toast.message)
		}
		t.Errorf("expected a toast mentioning 'github' and 'failed', got toasts: %v", msgs)
	}
}

func TestMCPStatusMsg_ConnectedServerCreatesToast(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	msg := MCPStatusMsg{
		Servers: []service.MCPServerStatus{
			{Name: "github", State: service.MCPServerStateConnected, ToolCount: 5},
		},
	}
	result, _ := m.Update(msg)
	model := result.(*Model)
	if len(model.toasts) == 0 {
		t.Fatal("expected at least one toast for connected server")
	}
	found := false
	for _, toast := range model.toasts {
		if strings.Contains(toast.message, "1 server") && strings.Contains(toast.message, "5 tools") {
			found = true
			break
		}
	}
	if !found {
		// The actual format is "🔌 MCP: 1 server, 5 tools"
		var msgs []string
		for _, toast := range model.toasts {
			msgs = append(msgs, toast.message)
		}
		t.Errorf("expected a summary toast with server and tool counts, got toasts: %v", msgs)
	}
}

func TestMCPStatusMsg_NoServersNoToasts(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	msg := MCPStatusMsg{Servers: nil}
	result, _ := m.Update(msg)
	model := result.(*Model)
	if len(model.toasts) != 0 {
		var msgs []string
		for _, toast := range model.toasts {
			msgs = append(msgs, toast.message)
		}
		t.Errorf("expected no toasts for nil servers, got %d: %v", len(model.toasts), msgs)
	}
}

func TestMCPStatusMsg_MixedServersCreatesBothToasts(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	msg := MCPStatusMsg{
		Servers: []service.MCPServerStatus{
			{Name: "github", State: service.MCPServerStateConnected, ToolCount: 5},
			{Name: "linear", State: service.MCPServerStateFailed, Error: "timeout"},
			{Name: "jira", State: service.MCPServerStateConnected, ToolCount: 3},
		},
	}
	result, _ := m.Update(msg)
	model := result.(*Model)

	// Should have at least 2 toasts: one for the failed server, one summary for connected.
	if len(model.toasts) < 2 {
		t.Fatalf("expected at least 2 toasts for mixed servers, got %d", len(model.toasts))
	}

	// Check for the failure toast.
	foundFailed := false
	for _, toast := range model.toasts {
		if strings.Contains(toast.message, "linear") && strings.Contains(toast.message, "failed") {
			foundFailed = true
			break
		}
	}
	if !foundFailed {
		t.Error("expected a toast for failed server 'linear'")
	}

	// Check for the summary toast (2 connected servers, 8 total tools).
	foundSummary := false
	for _, toast := range model.toasts {
		if strings.Contains(toast.message, "2 servers") && strings.Contains(toast.message, "8 tools") {
			foundSummary = true
			break
		}
	}
	if !foundSummary {
		var msgs []string
		for _, toast := range model.toasts {
			msgs = append(msgs, toast.message)
		}
		t.Errorf("expected summary toast with '2 servers' and '8 tools', got: %v", msgs)
	}
}

func TestMCPStatusMsg_OnlyFailedServersNoSummaryToast(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	msg := MCPStatusMsg{
		Servers: []service.MCPServerStatus{
			{Name: "server1", State: service.MCPServerStateFailed, Error: "err1"},
			{Name: "server2", State: service.MCPServerStateFailed, Error: "err2"},
		},
	}
	result, _ := m.Update(msg)
	model := result.(*Model)

	// Should have 2 failure toasts but no summary toast (connectedCount == 0).
	if len(model.toasts) != 2 {
		t.Fatalf("expected 2 toasts for 2 failed servers, got %d", len(model.toasts))
	}
	for _, toast := range model.toasts {
		if strings.Contains(toast.message, "servers") && strings.Contains(toast.message, "tools") {
			t.Error("should not have a summary toast when no servers connected")
		}
	}
}

func TestMCPStatusMsg_EmptyServersSlice(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	msg := MCPStatusMsg{Servers: []service.MCPServerStatus{}}
	result, _ := m.Update(msg)
	model := result.(*Model)
	if len(model.toasts) != 0 {
		t.Errorf("expected no toasts for empty servers slice, got %d", len(model.toasts))
	}
}

func TestMCPStatusMsg_ToastLevels(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	msg := MCPStatusMsg{
		Servers: []service.MCPServerStatus{
			{Name: "good", State: service.MCPServerStateConnected, ToolCount: 3},
			{Name: "bad", State: service.MCPServerStateFailed, Error: "nope"},
		},
	}
	result, _ := m.Update(msg)
	model := result.(*Model)

	var foundWarning, foundInfo bool
	for _, toast := range model.toasts {
		if strings.Contains(toast.message, "bad") && toast.level == toastWarning {
			foundWarning = true
		}
		if strings.Contains(toast.message, "1 server") && toast.level == toastInfo {
			foundInfo = true
		}
	}
	if !foundWarning {
		t.Error("expected warning-level toast for failed server")
	}
	if !foundInfo {
		t.Error("expected info-level toast for connected server summary")
	}
}

// --------------------------------------------------------------------------
// MCP modal integration: Update dispatches to updateMCPModal when modal is open
// --------------------------------------------------------------------------

func TestUpdate_DispatchesToMCPModalWhenOpen(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.mcpModal = mcpModalState{
		show: true,
		servers: []service.MCPServerStatus{
			{Name: "test", State: service.MCPServerStateConnected},
		},
	}
	// Pressing Esc via Update should close the modal.
	result, _ := m.Update(specialKey(tea.KeyEscape))
	model := result.(*Model)
	if model.mcpModal.show {
		t.Error("expected Update to dispatch Esc to updateMCPModal and close modal")
	}
}

func TestUpdate_DoesNotDispatchToMCPModalWhenClosed(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.mcpModal = mcpModalState{
		show: false,
	}
	// Pressing Esc via Update should NOT trigger MCP modal logic.
	// (It should go through normal key handling instead.)
	result, _ := m.Update(specialKey(tea.KeyEscape))
	model := result.(*Model)
	// Modal should still be closed.
	if model.mcpModal.show {
		t.Error("modal should remain closed when not shown")
	}
}

// --------------------------------------------------------------------------
// allCommands count verification
// --------------------------------------------------------------------------

func TestAllCommandsCount(t *testing.T) {
	t.Parallel()
	// Verify the total command count is 12 (including /mcp, /models, /providers, /skills, /agents, /jobs).
	if len(allCommands) != 12 {
		t.Errorf("expected 12 commands in allCommands, got %d", len(allCommands))
	}
	// Verify /mcp is present.
	found := false
	for _, cmd := range allCommands {
		if cmd.Name == "/mcp" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected /mcp in allCommands")
	}
}

// --------------------------------------------------------------------------
// mcpModalState zero value
// --------------------------------------------------------------------------

func TestMCPModalState_ZeroValue(t *testing.T) {
	t.Parallel()
	var state mcpModalState
	if state.show {
		t.Error("zero value mcpModalState should have show=false")
	}
	if state.serverIdx != 0 {
		t.Error("zero value mcpModalState should have serverIdx=0")
	}
	if state.toolIdx != 0 {
		t.Error("zero value mcpModalState should have toolIdx=0")
	}
	if state.focus != 0 {
		t.Error("zero value mcpModalState should have focus=0")
	}
	if state.servers != nil {
		t.Error("zero value mcpModalState should have nil servers")
	}
}

// --------------------------------------------------------------------------
// Render with many servers (stress test for layout)
// --------------------------------------------------------------------------

func TestRenderMCPModal_ManyServers(t *testing.T) {
	t.Parallel()
	m := newMinimalModel(t)
	m.width = 120
	m.height = 40

	var servers []service.MCPServerStatus
	for i := range 10 {
		servers = append(servers, service.MCPServerStatus{
			Name:      fmt.Sprintf("server-%d", i),
			Transport: "stdio",
			State:     service.MCPServerStateConnected,
			ToolCount: i + 1,
		})
	}
	m.mcpModal = mcpModalState{
		show:    true,
		servers: servers,
	}
	// Should not panic with many servers.
	result := m.renderMCPModal()
	if result == "" {
		t.Error("expected non-empty result for many servers")
	}
	// At least the first server should be visible.
	if !strings.Contains(result, "server-0") {
		t.Error("expected 'server-0' in modal with many servers")
	}
}
