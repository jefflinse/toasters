package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// --- ToProviderTools ---

func TestToProviderTools_Empty(t *testing.T) {
	t.Parallel()

	result := ToProviderTools(nil)
	if len(result) != 0 {
		t.Errorf("expected 0 tools, got %d", len(result))
	}
}

func TestToProviderTools_EmptySlice(t *testing.T) {
	t.Parallel()

	result := ToProviderTools([]ToolInfo{})
	if len(result) != 0 {
		t.Errorf("expected 0 tools, got %d", len(result))
	}
}

func TestToProviderTools_SingleTool(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)
	tools := []ToolInfo{
		{
			NamespacedName: "srv__search",
			OriginalName:   "search",
			ServerName:     "srv",
			Description:    "Search for things",
			InputSchema:    schema,
		},
	}

	result := ToProviderTools(tools)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}

	tool := result[0]
	if tool.Name != "srv__search" {
		t.Errorf("expected Name 'srv__search', got %q", tool.Name)
	}
	if tool.Description != "Search for things" {
		t.Errorf("expected Description 'Search for things', got %q", tool.Description)
	}
	// Parameters should be the raw JSON bytes.
	if string(tool.Parameters) != string(schema) {
		t.Errorf("expected Parameters %q, got %q", string(schema), string(tool.Parameters))
	}
}

func TestToProviderTools_MultipleTools(t *testing.T) {
	t.Parallel()

	tools := []ToolInfo{
		{NamespacedName: "s__a", OriginalName: "a", ServerName: "s", Description: "A", InputSchema: json.RawMessage(`{}`)},
		{NamespacedName: "s__b", OriginalName: "b", ServerName: "s", Description: "B", InputSchema: json.RawMessage(`{}`)},
		{NamespacedName: "s__c", OriginalName: "c", ServerName: "s", Description: "C", InputSchema: json.RawMessage(`{}`)},
	}

	result := ToProviderTools(tools)
	if len(result) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(result))
	}
}

// --- ToRuntimeToolDefs ---

func TestToRuntimeToolDefs_Empty(t *testing.T) {
	t.Parallel()

	result := ToRuntimeToolDefs(nil)
	if len(result) != 0 {
		t.Errorf("expected 0 defs, got %d", len(result))
	}
}

func TestToRuntimeToolDefs_EmptySlice(t *testing.T) {
	t.Parallel()

	result := ToRuntimeToolDefs([]ToolInfo{})
	if len(result) != 0 {
		t.Errorf("expected 0 defs, got %d", len(result))
	}
}

func TestToRuntimeToolDefs_SingleTool(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
	tools := []ToolInfo{
		{
			NamespacedName: "fs__read",
			OriginalName:   "read",
			ServerName:     "fs",
			Description:    "Read a file",
			InputSchema:    schema,
		},
	}

	result := ToRuntimeToolDefs(tools)
	if len(result) != 1 {
		t.Fatalf("expected 1 def, got %d", len(result))
	}

	def := result[0]
	if def.Name != "fs__read" {
		t.Errorf("expected Name 'fs__read', got %q", def.Name)
	}
	if def.Description != "Read a file" {
		t.Errorf("expected Description 'Read a file', got %q", def.Description)
	}
	if string(def.Parameters) != string(schema) {
		t.Errorf("expected Parameters %q, got %q", string(schema), string(def.Parameters))
	}
}

func TestToRuntimeToolDefs_MultipleTools(t *testing.T) {
	t.Parallel()

	tools := []ToolInfo{
		{NamespacedName: "s__x", OriginalName: "x", ServerName: "s", Description: "X", InputSchema: json.RawMessage(`{}`)},
		{NamespacedName: "s__y", OriginalName: "y", ServerName: "s", Description: "Y", InputSchema: json.RawMessage(`{}`)},
	}

	result := ToRuntimeToolDefs(tools)
	if len(result) != 2 {
		t.Fatalf("expected 2 defs, got %d", len(result))
	}
	if result[0].Name != "s__x" {
		t.Errorf("expected 's__x', got %q", result[0].Name)
	}
	if result[1].Name != "s__y" {
		t.Errorf("expected 's__y', got %q", result[1].Name)
	}
}

// --- FilterTools ---

func TestFilterTools_EmptyWhitelist_ReturnsAll(t *testing.T) {
	t.Parallel()

	tools := []ToolInfo{
		{NamespacedName: "s__a", OriginalName: "a", ServerName: "s"},
		{NamespacedName: "s__b", OriginalName: "b", ServerName: "s"},
		{NamespacedName: "s__c", OriginalName: "c", ServerName: "s"},
	}

	result := FilterTools(tools, nil)
	if len(result) != 3 {
		t.Errorf("expected 3 tools with empty whitelist, got %d", len(result))
	}
}

func TestFilterTools_EmptyWhitelistSlice_ReturnsAll(t *testing.T) {
	t.Parallel()

	tools := []ToolInfo{
		{NamespacedName: "s__a", OriginalName: "a", ServerName: "s"},
	}

	result := FilterTools(tools, []string{})
	if len(result) != 1 {
		t.Errorf("expected 1 tool with empty whitelist slice, got %d", len(result))
	}
}

func TestFilterTools_WithWhitelist_OnlyMatchingReturned(t *testing.T) {
	t.Parallel()

	tools := []ToolInfo{
		{NamespacedName: "s__alpha", OriginalName: "alpha", ServerName: "s"},
		{NamespacedName: "s__beta", OriginalName: "beta", ServerName: "s"},
		{NamespacedName: "s__gamma", OriginalName: "gamma", ServerName: "s"},
	}

	result := FilterTools(tools, []string{"alpha", "gamma"})
	if len(result) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result))
	}

	names := make(map[string]bool)
	for _, t := range result {
		names[t.OriginalName] = true
	}
	if !names["alpha"] {
		t.Error("expected 'alpha' in result")
	}
	if !names["gamma"] {
		t.Error("expected 'gamma' in result")
	}
	if names["beta"] {
		t.Error("expected 'beta' to be filtered out")
	}
}

func TestFilterTools_NoMatches_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	tools := []ToolInfo{
		{NamespacedName: "s__a", OriginalName: "a", ServerName: "s"},
		{NamespacedName: "s__b", OriginalName: "b", ServerName: "s"},
	}

	result := FilterTools(tools, []string{"x", "y", "z"})
	if len(result) != 0 {
		t.Errorf("expected 0 tools when no whitelist matches, got %d", len(result))
	}
}

func TestFilterTools_PartialMatch(t *testing.T) {
	t.Parallel()

	tools := []ToolInfo{
		{NamespacedName: "s__read", OriginalName: "read", ServerName: "s"},
		{NamespacedName: "s__write", OriginalName: "write", ServerName: "s"},
		{NamespacedName: "s__delete", OriginalName: "delete", ServerName: "s"},
	}

	result := FilterTools(tools, []string{"write"})
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}
	if result[0].OriginalName != "write" {
		t.Errorf("expected 'write', got %q", result[0].OriginalName)
	}
}

func TestFilterTools_FiltersByOriginalName_NotNamespaced(t *testing.T) {
	t.Parallel()

	// Whitelist uses original name, not namespaced name.
	tools := []ToolInfo{
		{NamespacedName: "server__tool", OriginalName: "tool", ServerName: "server"},
	}

	// Filtering by namespaced name should NOT match.
	result := FilterTools(tools, []string{"server__tool"})
	if len(result) != 0 {
		t.Errorf("expected 0 tools (filter is by OriginalName, not NamespacedName), got %d", len(result))
	}

	// Filtering by original name SHOULD match.
	result = FilterTools(tools, []string{"tool"})
	if len(result) != 1 {
		t.Errorf("expected 1 tool when filtering by OriginalName, got %d", len(result))
	}
}

func TestFilterTools_EmptyTools(t *testing.T) {
	t.Parallel()

	result := FilterTools(nil, []string{"something"})
	if len(result) != 0 {
		t.Errorf("expected 0 tools from nil input, got %d", len(result))
	}
}

func TestFilterTools_EmptyToolsEmptyWhitelist(t *testing.T) {
	t.Parallel()

	result := FilterTools(nil, nil)
	if len(result) != 0 {
		t.Errorf("expected 0 tools from nil input with nil whitelist, got %d", len(result))
	}
}

// --- ToolInfo field verification ---

func TestToolInfo_Fields(t *testing.T) {
	t.Parallel()

	info := ToolInfo{
		NamespacedName: "myserver__mytool",
		OriginalName:   "mytool",
		ServerName:     "myserver",
		Description:    "A test tool",
		InputSchema:    json.RawMessage(`{"type":"object"}`),
	}

	if !strings.Contains(info.NamespacedName, "__") {
		t.Error("NamespacedName should contain '__' separator")
	}
	if info.ServerName+"__"+info.OriginalName != info.NamespacedName {
		t.Errorf("NamespacedName should be ServerName__OriginalName, got %q", info.NamespacedName)
	}
}
