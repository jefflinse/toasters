package agentfmt_test

import (
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/agentfmt"
	"gopkg.in/yaml.v3"
)

func TestExportClaudeCode_AllMappableFields(t *testing.T) {
	t.Parallel()

	temp := 0.7
	topP := 0.95
	def := &agentfmt.AgentDef{
		Name:            "code-builder",
		Description:     "Builds production code",
		Mode:            "primary",
		Model:           "claude-sonnet-4-20250514",
		Provider:        "anthropic",
		MaxTurns:        15,
		Temperature:     &temp,
		TopP:            &topP,
		Tools:           []string{"read_file", "write_file", "bash"},
		DisallowedTools: []string{"web_fetch"},
		PermissionMode:  "plan",
		MCPServers:      []any{map[string]any{"name": "github", "transport": "stdio"}},
		Memory:          "Always run tests",
		Hooks:           map[string]any{"pre_tool_call": map[string]any{"command": "echo pre"}},
		Background:      true,
		Isolation:       "container",
		Color:           "#0000FF",
		Body:            "You are a code builder.",
	}

	fmYAML, body, warnings := agentfmt.ExportClaudeCode(def)

	// Parse the output YAML to verify camelCase keys.
	var out map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &out); err != nil {
		t.Fatalf("unmarshaling exported YAML: %v", err)
	}

	assertMapString(t, out, "name", "code-builder")
	assertMapString(t, out, "description", "Builds production code")
	assertMapString(t, out, "mode", "primary")
	assertMapString(t, out, "model", "claude-sonnet-4-20250514")
	assertMapInt(t, out, "maxTurns", 15)
	assertMapFloat64(t, out, "temperature", 0.7)
	assertMapFloat64(t, out, "topP", 0.95)
	assertMapStringSlice(t, out, "tools", []string{"read_file", "write_file", "bash"})
	assertMapStringSlice(t, out, "disallowedTools", []string{"web_fetch"})
	assertMapString(t, out, "permissionMode", "plan")
	assertMapString(t, out, "memory", "Always run tests")
	assertMapString(t, out, "isolation", "container")
	assertMapString(t, out, "color", "#0000FF")

	if _, ok := out["mcpServers"]; !ok {
		t.Error("expected mcpServers in output")
	}
	if _, ok := out["hooks"]; !ok {
		t.Error("expected hooks in output")
	}
	if bg, ok := out["background"]; !ok || bg != true {
		t.Errorf("background = %v, want true", bg)
	}

	// Provider "anthropic" should NOT be in output.
	if _, ok := out["provider"]; ok {
		t.Error("provider should not be emitted for anthropic")
	}

	if body != "You are a code builder." {
		t.Errorf("Body = %q, want %q", body, "You are a code builder.")
	}

	// No warnings expected for this def (no lossy fields set).
	if len(warnings) != 0 {
		t.Errorf("expected 0 warnings, got %d: %v", len(warnings), warnings)
	}
}

func TestExportClaudeCode_LossyFieldsGenerateWarnings(t *testing.T) {
	t.Parallel()

	temp := 0.7
	topP := 0.95
	def := &agentfmt.AgentDef{
		Name:         "lossy-agent",
		Mode:         "worker",
		Skills:       []string{"code-review"},
		Temperature:  &temp,
		TopP:         &topP,
		Permissions:  map[string]any{"bash": map[string]any{"allow_all": true}},
		Hidden:       true,
		Disabled:     true,
		ModelOptions: map[string]any{"max_tokens": 8192},
	}

	_, _, warnings := agentfmt.ExportClaudeCode(def)

	wantFields := map[string]bool{
		"skills":        false,
		"permissions":   false,
		"hidden":        false,
		"disabled":      false,
		"model_options": false,
	}

	for _, w := range warnings {
		if _, ok := wantFields[w.Field]; ok {
			wantFields[w.Field] = true
		} else {
			t.Errorf("unexpected warning for field %q: %s", w.Field, w.Reason)
		}
	}

	for field, found := range wantFields {
		if !found {
			t.Errorf("missing warning for lossy field %q", field)
		}
	}
}

func TestExportClaudeCode_LossyFieldsNotInOutput(t *testing.T) {
	t.Parallel()

	temp := 0.7
	def := &agentfmt.AgentDef{
		Name:         "lossy-agent",
		Mode:         "worker",
		Temperature:  &temp,
		Permissions:  map[string]any{"bash": true},
		Hidden:       true,
		Disabled:     true,
		ModelOptions: map[string]any{"max_tokens": 8192},
	}

	fmYAML, _, _ := agentfmt.ExportClaudeCode(def)

	var out map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &out); err != nil {
		t.Fatalf("unmarshaling exported YAML: %v", err)
	}

	droppedKeys := []string{"skills", "permissions", "hidden", "disabled", "model_options", "modelOptions"}
	for _, key := range droppedKeys {
		if _, ok := out[key]; ok {
			t.Errorf("lossy field %q should not be in output", key)
		}
	}
}

func TestExportClaudeCode_EmptyOptionalFieldsNoWarnings(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name:        "minimal",
		Description: "A minimal agent",
	}

	_, _, warnings := agentfmt.ExportClaudeCode(def)
	if len(warnings) != 0 {
		t.Errorf("expected 0 warnings for empty optional fields, got %d: %v", len(warnings), warnings)
	}
}

func TestExportClaudeCode_ProviderAnthropic_NotEmitted(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name:     "anthropic-agent",
		Provider: "anthropic",
		Model:    "claude-sonnet-4-20250514",
	}

	fmYAML, _, warnings := agentfmt.ExportClaudeCode(def)

	var out map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &out); err != nil {
		t.Fatalf("unmarshaling exported YAML: %v", err)
	}

	if _, ok := out["provider"]; ok {
		t.Error("provider 'anthropic' should not be emitted in Claude Code format")
	}

	// No warning for anthropic provider.
	for _, w := range warnings {
		if w.Field == "provider" {
			t.Error("should not warn for anthropic provider")
		}
	}
}

func TestExportClaudeCode_ProviderNonAnthropic_Warning(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name:     "openai-agent",
		Provider: "openai",
		Model:    "gpt-4o",
	}

	_, _, warnings := agentfmt.ExportClaudeCode(def)

	found := false
	for _, w := range warnings {
		if w.Field == "provider" {
			found = true
			if !strings.Contains(w.Reason, "non-Anthropic") {
				t.Errorf("provider warning reason = %q, want to contain 'non-Anthropic'", w.Reason)
			}
		}
	}
	if !found {
		t.Error("expected warning for non-Anthropic provider")
	}
}

func TestExportClaudeCode_ZeroValueDef(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{}

	fmYAML, body, warnings := agentfmt.ExportClaudeCode(def)

	if fmYAML != "" {
		t.Errorf("expected empty YAML for zero-value def, got %q", fmYAML)
	}
	if body != "" {
		t.Errorf("expected empty body, got %q", body)
	}
	if len(warnings) != 0 {
		t.Errorf("expected 0 warnings, got %d", len(warnings))
	}
}

func TestExportClaudeCode_NilDef(t *testing.T) {
	t.Parallel()

	fmYAML, body, warnings := agentfmt.ExportClaudeCode(nil)

	if fmYAML != "" {
		t.Errorf("expected empty YAML for nil def, got %q", fmYAML)
	}
	if body != "" {
		t.Errorf("expected empty body, got %q", body)
	}
	if warnings != nil {
		t.Errorf("expected nil warnings, got %v", warnings)
	}
}

func TestExportClaudeCode_BodyPreserved(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name: "body-test",
		Body: "Line one.\n\nLine two.\n\nLine three.",
	}

	_, body, _ := agentfmt.ExportClaudeCode(def)
	if body != "Line one.\n\nLine two.\n\nLine three." {
		t.Errorf("Body = %q, want multiline body preserved", body)
	}
}

func TestExportClaudeCode_BackgroundFalseNotEmitted(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name:       "no-bg",
		Background: false,
	}

	fmYAML, _, _ := agentfmt.ExportClaudeCode(def)

	var out map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &out); err != nil {
		t.Fatalf("unmarshaling exported YAML: %v", err)
	}

	if _, ok := out["background"]; ok {
		t.Error("background=false should not be emitted")
	}
}

func TestExportClaudeCode_RoundTrip(t *testing.T) {
	t.Parallel()

	// Import a Claude Code definition, then export it back.
	fmYAML := `
name: roundtrip-cc
description: Round-trip test
mode: primary
model: sonnet
maxTurns: 10
tools:
  - read_file
  - bash
disallowedTools:
  - web_fetch
permissionMode: plan
memory: Run tests
color: blue
`
	body := "You are a round-trip agent."

	def, err := agentfmt.ImportClaudeCode(fmYAML, body, "fallback")
	if err != nil {
		t.Fatalf("ImportClaudeCode: %v", err)
	}

	exportedYAML, exportedBody, _ := agentfmt.ExportClaudeCode(def)

	// Parse the exported YAML.
	var out map[string]any
	if err := yaml.Unmarshal([]byte(exportedYAML), &out); err != nil {
		t.Fatalf("unmarshaling exported YAML: %v", err)
	}

	assertMapString(t, out, "name", "roundtrip-cc")
	assertMapString(t, out, "description", "Round-trip test")
	assertMapString(t, out, "mode", "primary")
	// Model alias was expanded on import.
	assertMapString(t, out, "model", "claude-sonnet-4-20250514")
	assertMapInt(t, out, "maxTurns", 10)
	assertMapStringSlice(t, out, "tools", []string{"read_file", "bash"})
	assertMapStringSlice(t, out, "disallowedTools", []string{"web_fetch"})
	assertMapString(t, out, "permissionMode", "plan")
	assertMapString(t, out, "memory", "Run tests")
	assertMapString(t, out, "color", "#0000FF") // "blue" was normalized on import

	if exportedBody != body {
		t.Errorf("Body = %q, want %q", exportedBody, body)
	}
}

// assertMapString checks that a map has a string value at the given key.
func assertMapString(t *testing.T, m map[string]any, key, want string) {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Errorf("missing key %q in output", key)
		return
	}
	s, ok := v.(string)
	if !ok {
		t.Errorf("key %q is %T, want string", key, v)
		return
	}
	if s != want {
		t.Errorf("key %q = %q, want %q", key, s, want)
	}
}

// assertMapInt checks that a map has an int value at the given key.
func assertMapInt(t *testing.T, m map[string]any, key string, want int) {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Errorf("missing key %q in output", key)
		return
	}
	switch n := v.(type) {
	case int:
		if n != want {
			t.Errorf("key %q = %d, want %d", key, n, want)
		}
	case float64:
		if int(n) != want {
			t.Errorf("key %q = %v, want %d", key, n, want)
		}
	default:
		t.Errorf("key %q is %T, want int", key, v)
	}
}

// assertMapStringSlice checks that a map has a []string value at the given key.
func assertMapStringSlice(t *testing.T, m map[string]any, key string, want []string) {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Errorf("missing key %q in output", key)
		return
	}
	items, ok := v.([]any)
	if !ok {
		t.Errorf("key %q is %T, want []any", key, v)
		return
	}
	if len(items) != len(want) {
		t.Errorf("key %q length = %d, want %d", key, len(items), len(want))
		return
	}
	for i, item := range items {
		s, ok := item.(string)
		if !ok {
			t.Errorf("key %q[%d] is %T, want string", key, i, item)
			continue
		}
		if s != want[i] {
			t.Errorf("key %q[%d] = %q, want %q", key, i, s, want[i])
		}
	}
}
