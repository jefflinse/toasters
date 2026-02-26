package agentfmt_test

import (
	"reflect"
	"testing"

	"github.com/jefflinse/toasters/internal/agentfmt"
)

func TestImportClaudeCode_AllFields(t *testing.T) {
	t.Parallel()

	fmYAML := `
name: code-builder
description: Builds production code
model: sonnet
maxTurns: 15
temperature: 0.7
topP: 0.95
tools:
  - read_file
  - write_file
  - bash
disallowedTools:
  - web_fetch
permissionMode: plan
mcpServers:
  - name: github
    transport: stdio
modelOptions:
  max_tokens: 8192
memory: Always run tests
hooks:
  pre_tool_call:
    command: echo "pre"
background: true
isolation: container
color: blue
`
	body := "You are a code builder."

	def, err := agentfmt.ImportClaudeCode(fmYAML, body, "fallback")
	if err != nil {
		t.Fatalf("ImportClaudeCode: %v", err)
	}

	if def.Name != "code-builder" {
		t.Errorf("Name = %q, want %q", def.Name, "code-builder")
	}
	if def.Description != "Builds production code" {
		t.Errorf("Description = %q, want %q", def.Description, "Builds production code")
	}
	// "sonnet" should expand to full model ID.
	if def.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", def.Model, "claude-sonnet-4-20250514")
	}
	if def.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", def.Provider, "anthropic")
	}
	if def.MaxTurns != 15 {
		t.Errorf("MaxTurns = %d, want %d", def.MaxTurns, 15)
	}
	if def.Temperature == nil || *def.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", def.Temperature)
	}
	if def.TopP == nil || *def.TopP != 0.95 {
		t.Errorf("TopP = %v, want 0.95", def.TopP)
	}
	if !reflect.DeepEqual(def.Tools, []string{"read_file", "write_file", "bash"}) {
		t.Errorf("Tools = %v, want [read_file write_file bash]", def.Tools)
	}
	if !reflect.DeepEqual(def.DisallowedTools, []string{"web_fetch"}) {
		t.Errorf("DisallowedTools = %v, want [web_fetch]", def.DisallowedTools)
	}
	if def.PermissionMode != "plan" {
		t.Errorf("PermissionMode = %q, want %q", def.PermissionMode, "plan")
	}
	if def.MCPServers == nil {
		t.Fatal("MCPServers is nil, want non-nil")
	}
	if def.ModelOptions == nil || def.ModelOptions["max_tokens"] != 8192 {
		t.Errorf("ModelOptions = %v, want max_tokens=8192", def.ModelOptions)
	}
	if def.Memory != "Always run tests" {
		t.Errorf("Memory = %q, want %q", def.Memory, "Always run tests")
	}
	if def.Hooks == nil {
		t.Error("Hooks is nil, want non-nil")
	}
	if !def.Background {
		t.Error("Background = false, want true")
	}
	if def.Isolation != "container" {
		t.Errorf("Isolation = %q, want %q", def.Isolation, "container")
	}
	if def.Color != "#0000FF" {
		t.Errorf("Color = %q, want %q", def.Color, "#0000FF")
	}
	if def.Body != "You are a code builder." {
		t.Errorf("Body = %q, want %q", def.Body, "You are a code builder.")
	}
}

func TestImportClaudeCode_ModelAlias_Sonnet(t *testing.T) {
	t.Parallel()

	fmYAML := `model: sonnet`
	def, err := agentfmt.ImportClaudeCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportClaudeCode: %v", err)
	}
	if def.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", def.Model, "claude-sonnet-4-20250514")
	}
	if def.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", def.Provider, "anthropic")
	}
}

func TestImportClaudeCode_ModelAlias_Opus(t *testing.T) {
	t.Parallel()

	fmYAML := `model: opus`
	def, err := agentfmt.ImportClaudeCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportClaudeCode: %v", err)
	}
	if def.Model != "claude-opus-4-20250514" {
		t.Errorf("Model = %q, want %q", def.Model, "claude-opus-4-20250514")
	}
	if def.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", def.Provider, "anthropic")
	}
}

func TestImportClaudeCode_ModelAlias_Haiku(t *testing.T) {
	t.Parallel()

	fmYAML := `model: haiku`
	def, err := agentfmt.ImportClaudeCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportClaudeCode: %v", err)
	}
	if def.Model != "claude-haiku-3-5-20241022" {
		t.Errorf("Model = %q, want %q", def.Model, "claude-haiku-3-5-20241022")
	}
	if def.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", def.Provider, "anthropic")
	}
}

func TestImportClaudeCode_ModelContainsClaude(t *testing.T) {
	t.Parallel()

	fmYAML := `model: claude-sonnet-4-20250514`
	def, err := agentfmt.ImportClaudeCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportClaudeCode: %v", err)
	}
	if def.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", def.Model, "claude-sonnet-4-20250514")
	}
	if def.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", def.Provider, "anthropic")
	}
}

func TestImportClaudeCode_NonAnthropicModel(t *testing.T) {
	t.Parallel()

	fmYAML := `model: gpt-4o`
	def, err := agentfmt.ImportClaudeCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportClaudeCode: %v", err)
	}
	if def.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", def.Model, "gpt-4o")
	}
	if def.Provider != "" {
		t.Errorf("Provider = %q, want empty", def.Provider)
	}
}

func TestImportClaudeCode_NoModel(t *testing.T) {
	t.Parallel()

	fmYAML := `name: no-model`
	def, err := agentfmt.ImportClaudeCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportClaudeCode: %v", err)
	}
	if def.Model != "" {
		t.Errorf("Model = %q, want empty", def.Model)
	}
	if def.Provider != "" {
		t.Errorf("Provider = %q, want empty", def.Provider)
	}
}

func TestImportClaudeCode_NamedColorNormalization(t *testing.T) {
	t.Parallel()

	fmYAML := `color: purple`
	def, err := agentfmt.ImportClaudeCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportClaudeCode: %v", err)
	}
	if def.Color != "#800080" {
		t.Errorf("Color = %q, want %q", def.Color, "#800080")
	}
}

func TestImportClaudeCode_HexColorPassthrough(t *testing.T) {
	t.Parallel()

	fmYAML := `color: "#FF9800"`
	def, err := agentfmt.ImportClaudeCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportClaudeCode: %v", err)
	}
	if def.Color != "#FF9800" {
		t.Errorf("Color = %q, want %q", def.Color, "#FF9800")
	}
}

func TestImportClaudeCode_MCPServersAsList(t *testing.T) {
	t.Parallel()

	fmYAML := `
mcpServers:
  - name: github
    transport: stdio
  - name: jira
    transport: sse
`
	def, err := agentfmt.ImportClaudeCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportClaudeCode: %v", err)
	}
	if def.MCPServers == nil {
		t.Fatal("MCPServers is nil, want non-nil")
	}
	servers, ok := def.MCPServers.([]any)
	if !ok {
		t.Fatalf("MCPServers is %T, want []any", def.MCPServers)
	}
	if len(servers) != 2 {
		t.Errorf("MCPServers length = %d, want 2", len(servers))
	}
}

func TestImportClaudeCode_MCPServersAsMap(t *testing.T) {
	t.Parallel()

	fmYAML := `
mcpServers:
  github:
    transport: stdio
  jira:
    transport: sse
`
	def, err := agentfmt.ImportClaudeCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportClaudeCode: %v", err)
	}
	if def.MCPServers == nil {
		t.Fatal("MCPServers is nil, want non-nil")
	}
	servers, ok := def.MCPServers.(map[string]any)
	if !ok {
		t.Fatalf("MCPServers is %T, want map[string]any", def.MCPServers)
	}
	if len(servers) != 2 {
		t.Errorf("MCPServers length = %d, want 2", len(servers))
	}
}

func TestImportClaudeCode_Minimal(t *testing.T) {
	t.Parallel()

	fmYAML := `
name: minimal
description: A minimal agent
`
	def, err := agentfmt.ImportClaudeCode(fmYAML, "Prompt.", "fallback")
	if err != nil {
		t.Fatalf("ImportClaudeCode: %v", err)
	}
	if def.Name != "minimal" {
		t.Errorf("Name = %q, want %q", def.Name, "minimal")
	}
	if def.Description != "A minimal agent" {
		t.Errorf("Description = %q, want %q", def.Description, "A minimal agent")
	}
	if def.Body != "Prompt." {
		t.Errorf("Body = %q, want %q", def.Body, "Prompt.")
	}
}

func TestImportClaudeCode_DefaultName(t *testing.T) {
	t.Parallel()

	fmYAML := `description: No name set`
	def, err := agentfmt.ImportClaudeCode(fmYAML, "", "default-name")
	if err != nil {
		t.Fatalf("ImportClaudeCode: %v", err)
	}
	if def.Name != "default-name" {
		t.Errorf("Name = %q, want %q", def.Name, "default-name")
	}
}

func TestImportClaudeCode_EmptyFrontmatter(t *testing.T) {
	t.Parallel()

	def, err := agentfmt.ImportClaudeCode("", "Just a body.", "empty")
	if err != nil {
		t.Fatalf("ImportClaudeCode: %v", err)
	}
	if def.Name != "empty" {
		t.Errorf("Name = %q, want %q", def.Name, "empty")
	}
	if def.Body != "Just a body." {
		t.Errorf("Body = %q, want %q", def.Body, "Just a body.")
	}
}

func TestImportClaudeCode_EmptyOptionalFields(t *testing.T) {
	t.Parallel()

	fmYAML := `name: sparse`
	def, err := agentfmt.ImportClaudeCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportClaudeCode: %v", err)
	}

	if def.MaxTurns != 0 {
		t.Errorf("MaxTurns = %d, want 0", def.MaxTurns)
	}
	if def.Temperature != nil {
		t.Errorf("Temperature = %v, want nil", def.Temperature)
	}
	if def.TopP != nil {
		t.Errorf("TopP = %v, want nil", def.TopP)
	}
	if def.Tools != nil {
		t.Errorf("Tools = %v, want nil", def.Tools)
	}
	if def.DisallowedTools != nil {
		t.Errorf("DisallowedTools = %v, want nil", def.DisallowedTools)
	}
	if def.PermissionMode != "" {
		t.Errorf("PermissionMode = %q, want empty", def.PermissionMode)
	}
	if def.MCPServers != nil {
		t.Errorf("MCPServers = %v, want nil", def.MCPServers)
	}
	if def.ModelOptions != nil {
		t.Errorf("ModelOptions = %v, want nil", def.ModelOptions)
	}
	if def.Memory != "" {
		t.Errorf("Memory = %q, want empty", def.Memory)
	}
	if def.Hooks != nil {
		t.Errorf("Hooks = %v, want nil", def.Hooks)
	}
	if def.Background {
		t.Error("Background = true, want false")
	}
	if def.Isolation != "" {
		t.Errorf("Isolation = %q, want empty", def.Isolation)
	}
	if def.Color != "" {
		t.Errorf("Color = %q, want empty", def.Color)
	}
}

func TestImportClaudeCode_InvalidYAML(t *testing.T) {
	t.Parallel()

	fmYAML := `[invalid yaml: {`
	_, err := agentfmt.ImportClaudeCode(fmYAML, "", "test")
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}
