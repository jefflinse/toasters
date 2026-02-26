package agentfmt_test

import (
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/agentfmt"
	"gopkg.in/yaml.v3"
)

func TestExportOpenCode_AllMappableFields(t *testing.T) {
	t.Parallel()

	temp := 0.6
	topP := 0.9
	def := &agentfmt.AgentDef{
		Name:         "oc-agent",
		Description:  "An OpenCode agent",
		Provider:     "anthropic",
		Model:        "claude-sonnet-4-20250514",
		MaxTurns:     20,
		Temperature:  &temp,
		TopP:         &topP,
		Tools:        []string{"read_file", "bash"},
		Disabled:     true,
		Permissions:  map[string]any{"_mode": "ask"},
		Color:        "#00FF00",
		Hidden:       true,
		ModelOptions: map[string]any{"max_tokens": 4096},
		Body:         "You are an OpenCode agent.",
	}

	fmYAML, body, warnings := agentfmt.ExportOpenCode(def)

	var out map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &out); err != nil {
		t.Fatalf("unmarshaling exported YAML: %v", err)
	}

	assertMapString(t, out, "name", "oc-agent")
	assertMapString(t, out, "description", "An OpenCode agent")
	// Provider/model should be combined.
	assertMapString(t, out, "provider", "anthropic/claude-sonnet-4-20250514")
	assertMapInt(t, out, "steps", 20)
	assertMapFloat64(t, out, "temperature", 0.6)
	assertMapFloat64(t, out, "top_p", 0.9)
	assertMapStringSlice(t, out, "tools", []string{"read_file", "bash"})

	if disable, ok := out["disable"]; !ok || disable != true {
		t.Errorf("disable = %v, want true", disable)
	}

	// Permission with _mode should be unwrapped to string.
	assertMapString(t, out, "permission", "ask")

	assertMapString(t, out, "color", "#00FF00")
	if hidden, ok := out["hidden"]; !ok || hidden != true {
		t.Errorf("hidden = %v, want true", hidden)
	}

	// model_options should be merged into top-level.
	assertMapInt(t, out, "max_tokens", 4096)

	if body != "You are an OpenCode agent." {
		t.Errorf("Body = %q, want %q", body, "You are an OpenCode agent.")
	}

	// No warnings expected (no lossy fields set).
	if len(warnings) != 0 {
		t.Errorf("expected 0 warnings, got %d: %v", len(warnings), warnings)
	}
}

func TestExportOpenCode_LossyFieldsGenerateWarnings(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name:            "lossy-oc",
		Skills:          []string{"code-review"},
		MCPServers:      []any{map[string]any{"name": "github"}},
		PermissionMode:  "plan",
		Memory:          "Always run tests",
		Hooks:           map[string]any{"pre_tool_call": "echo pre"},
		Background:      true,
		Isolation:       "container",
		DisallowedTools: []string{"web_fetch"},
	}

	_, _, warnings := agentfmt.ExportOpenCode(def)

	wantFields := map[string]bool{
		"skills":           false,
		"mcp_servers":      false,
		"permission_mode":  false,
		"memory":           false,
		"hooks":            false,
		"background":       false,
		"isolation":        false,
		"disallowed_tools": false,
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

func TestExportOpenCode_LossyFieldsNotInOutput(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name:            "lossy-oc",
		MCPServers:      []any{map[string]any{"name": "github"}},
		PermissionMode:  "plan",
		Memory:          "Always run tests",
		Hooks:           map[string]any{"pre_tool_call": "echo pre"},
		Background:      true,
		Isolation:       "container",
		DisallowedTools: []string{"web_fetch"},
	}

	fmYAML, _, _ := agentfmt.ExportOpenCode(def)

	var out map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &out); err != nil {
		t.Fatalf("unmarshaling exported YAML: %v", err)
	}

	droppedKeys := []string{"mcp_servers", "mcpServers", "permission_mode", "permissionMode", "memory", "hooks", "isolation", "disallowed_tools", "disallowedTools"}
	for _, key := range droppedKeys {
		if _, ok := out[key]; ok {
			t.Errorf("lossy field %q should not be in output", key)
		}
	}
}

func TestExportOpenCode_ProviderModelCombined(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name:     "combined",
		Provider: "anthropic",
		Model:    "claude-sonnet-4-20250514",
	}

	fmYAML, _, _ := agentfmt.ExportOpenCode(def)

	var out map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &out); err != nil {
		t.Fatalf("unmarshaling exported YAML: %v", err)
	}

	assertMapString(t, out, "provider", "anthropic/claude-sonnet-4-20250514")
}

func TestExportOpenCode_ProviderOnly(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name:     "provider-only",
		Provider: "openai",
	}

	fmYAML, _, _ := agentfmt.ExportOpenCode(def)

	var out map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &out); err != nil {
		t.Fatalf("unmarshaling exported YAML: %v", err)
	}

	assertMapString(t, out, "provider", "openai")
}

func TestExportOpenCode_ModelOnly(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name:  "model-only",
		Model: "gpt-4o",
	}

	fmYAML, _, _ := agentfmt.ExportOpenCode(def)

	var out map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &out); err != nil {
		t.Fatalf("unmarshaling exported YAML: %v", err)
	}

	// With no provider, just the model is used as the provider field.
	assertMapString(t, out, "provider", "gpt-4o")
}

func TestExportOpenCode_PermissionsWithModeKey_Unwrapped(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name:        "perm-mode",
		Permissions: map[string]any{"_mode": "auto"},
	}

	fmYAML, _, _ := agentfmt.ExportOpenCode(def)

	var out map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &out); err != nil {
		t.Fatalf("unmarshaling exported YAML: %v", err)
	}

	// Should be unwrapped to a string.
	assertMapString(t, out, "permission", "auto")
}

func TestExportOpenCode_PermissionsAsMap(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name: "perm-map",
		Permissions: map[string]any{
			"bash":       map[string]any{"allow_all": true},
			"write_file": map[string]any{"allow_patterns": []string{"*.go"}},
		},
	}

	fmYAML, _, _ := agentfmt.ExportOpenCode(def)

	var out map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &out); err != nil {
		t.Fatalf("unmarshaling exported YAML: %v", err)
	}

	perm, ok := out["permission"]
	if !ok {
		t.Fatal("missing permission in output")
	}
	permMap, ok := perm.(map[string]any)
	if !ok {
		t.Fatalf("permission is %T, want map[string]any", perm)
	}
	if _, ok := permMap["bash"]; !ok {
		t.Error("permission map missing 'bash' key")
	}
}

func TestExportOpenCode_ModelOptionsMergedIntoTopLevel(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name:         "opts-merge",
		ModelOptions: map[string]any{"max_tokens": 4096, "stop_sequences": []string{"END"}},
	}

	fmYAML, _, _ := agentfmt.ExportOpenCode(def)

	var out map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &out); err != nil {
		t.Fatalf("unmarshaling exported YAML: %v", err)
	}

	assertMapInt(t, out, "max_tokens", 4096)

	// model_options key itself should not be present.
	if _, ok := out["model_options"]; ok {
		t.Error("model_options should be merged into top-level, not present as a key")
	}
}

func TestExportOpenCode_ModelOptionsCollisionWarning(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name:         "collision-test",
		MaxTurns:     10,
		ModelOptions: map[string]any{"steps": 99, "custom_key": "value"},
	}

	fmYAML, _, warnings := agentfmt.ExportOpenCode(def)

	// "steps" collides with the explicit max_turns→steps mapping.
	var collisionFound bool
	for _, w := range warnings {
		if w.Field == "model_options.steps" {
			collisionFound = true
		}
	}
	if !collisionFound {
		t.Error("expected collision warning for model_options.steps")
	}

	// Verify the explicit "steps" value wins (10, not 99).
	var out map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &out); err != nil {
		t.Fatalf("unmarshaling exported YAML: %v", err)
	}
	assertMapInt(t, out, "steps", 10)

	// Non-colliding key should be present.
	assertMapString(t, out, "custom_key", "value")
}

func TestExportOpenCode_EmptyOptionalFieldsNoWarnings(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name:        "minimal-oc",
		Description: "Minimal",
	}

	_, _, warnings := agentfmt.ExportOpenCode(def)
	if len(warnings) != 0 {
		t.Errorf("expected 0 warnings for empty optional fields, got %d: %v", len(warnings), warnings)
	}
}

func TestExportOpenCode_BodyPreserved(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name: "body-test",
		Body: "Line one.\n\nLine two.",
	}

	_, body, _ := agentfmt.ExportOpenCode(def)
	if body != "Line one.\n\nLine two." {
		t.Errorf("Body = %q, want multiline body preserved", body)
	}
}

func TestExportOpenCode_ZeroValueDef(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{}

	fmYAML, body, warnings := agentfmt.ExportOpenCode(def)

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

func TestExportOpenCode_NilDef(t *testing.T) {
	t.Parallel()

	fmYAML, body, warnings := agentfmt.ExportOpenCode(nil)

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

func TestExportOpenCode_DisabledFalseNotEmitted(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name:     "no-disable",
		Disabled: false,
	}

	fmYAML, _, _ := agentfmt.ExportOpenCode(def)

	var out map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &out); err != nil {
		t.Fatalf("unmarshaling exported YAML: %v", err)
	}

	if _, ok := out["disable"]; ok {
		t.Error("disable=false should not be emitted")
	}
}

func TestExportOpenCode_HiddenFalseNotEmitted(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name:   "no-hidden",
		Hidden: false,
	}

	fmYAML, _, _ := agentfmt.ExportOpenCode(def)

	var out map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &out); err != nil {
		t.Fatalf("unmarshaling exported YAML: %v", err)
	}

	if _, ok := out["hidden"]; ok {
		t.Error("hidden=false should not be emitted")
	}
}

func TestExportOpenCode_ModePassedThrough(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name: "mode-test",
		Mode: "worker",
	}

	fmYAML, _, _ := agentfmt.ExportOpenCode(def)

	var out map[string]any
	if err := yaml.Unmarshal([]byte(fmYAML), &out); err != nil {
		t.Fatalf("unmarshaling exported YAML: %v", err)
	}

	assertMapString(t, out, "mode", "worker")
}

func TestExportOpenCode_BackgroundWarning(t *testing.T) {
	t.Parallel()

	def := &agentfmt.AgentDef{
		Name:       "bg-agent",
		Background: true,
	}

	_, _, warnings := agentfmt.ExportOpenCode(def)

	found := false
	for _, w := range warnings {
		if w.Field == "background" {
			found = true
			if !strings.Contains(w.Reason, "not supported by OpenCode") {
				t.Errorf("background warning reason = %q, want to contain 'not supported by OpenCode'", w.Reason)
			}
		}
	}
	if !found {
		t.Error("expected warning for background field")
	}
}

func TestExportOpenCode_RoundTrip(t *testing.T) {
	t.Parallel()

	// Import an OpenCode definition, then export it back.
	fmYAML := `
name: roundtrip-oc
description: Round-trip test
provider: anthropic/claude-sonnet-4-20250514
steps: 20
temperature: 0.6
top_p: 0.9
tools:
  - read_file
  - bash
disable: true
permission: ask
color: green
hidden: true
`
	body := "You are a round-trip agent."

	def, err := agentfmt.ImportOpenCode(fmYAML, body, "fallback")
	if err != nil {
		t.Fatalf("ImportOpenCode: %v", err)
	}

	exportedYAML, exportedBody, _ := agentfmt.ExportOpenCode(def)

	var out map[string]any
	if err := yaml.Unmarshal([]byte(exportedYAML), &out); err != nil {
		t.Fatalf("unmarshaling exported YAML: %v", err)
	}

	assertMapString(t, out, "name", "roundtrip-oc")
	assertMapString(t, out, "description", "Round-trip test")
	assertMapString(t, out, "provider", "anthropic/claude-sonnet-4-20250514")
	assertMapInt(t, out, "steps", 20)
	assertMapFloat64(t, out, "temperature", 0.6)
	assertMapFloat64(t, out, "top_p", 0.9)
	assertMapStringSlice(t, out, "tools", []string{"read_file", "bash"})

	if disable, ok := out["disable"]; !ok || disable != true {
		t.Errorf("disable = %v, want true", disable)
	}
	assertMapString(t, out, "permission", "ask")
	assertMapString(t, out, "color", "#00FF00") // "green" was normalized on import

	if hidden, ok := out["hidden"]; !ok || hidden != true {
		t.Errorf("hidden = %v, want true", hidden)
	}

	if exportedBody != body {
		t.Errorf("Body = %q, want %q", exportedBody, body)
	}
}

// assertMapFloat64 checks that a map has a float64 value at the given key.
func assertMapFloat64(t *testing.T, m map[string]any, key string, want float64) {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Errorf("missing key %q in output", key)
		return
	}
	switch n := v.(type) {
	case float64:
		if n != want {
			t.Errorf("key %q = %v, want %v", key, n, want)
		}
	case int:
		if float64(n) != want {
			t.Errorf("key %q = %v, want %v", key, n, want)
		}
	default:
		t.Errorf("key %q is %T, want float64", key, v)
	}
}
