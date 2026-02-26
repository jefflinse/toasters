package agentfmt_test

import (
	"reflect"
	"testing"

	"github.com/jefflinse/toasters/internal/agentfmt"
)

func TestImportOpenCode_AllFields(t *testing.T) {
	t.Parallel()

	fmYAML := `
name: oc-agent
description: An OpenCode agent
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
	body := "You are an OpenCode agent."

	def, err := agentfmt.ImportOpenCode(fmYAML, body, "fallback")
	if err != nil {
		t.Fatalf("ImportOpenCode: %v", err)
	}

	if def.Name != "oc-agent" {
		t.Errorf("Name = %q, want %q", def.Name, "oc-agent")
	}
	if def.Description != "An OpenCode agent" {
		t.Errorf("Description = %q, want %q", def.Description, "An OpenCode agent")
	}
	// Provider/model should be split from "anthropic/claude-sonnet-4-20250514".
	if def.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", def.Provider, "anthropic")
	}
	if def.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", def.Model, "claude-sonnet-4-20250514")
	}
	if def.MaxTurns != 20 {
		t.Errorf("MaxTurns = %d, want %d", def.MaxTurns, 20)
	}
	if def.Temperature == nil || *def.Temperature != 0.6 {
		t.Errorf("Temperature = %v, want 0.6", def.Temperature)
	}
	if def.TopP == nil || *def.TopP != 0.9 {
		t.Errorf("TopP = %v, want 0.9", def.TopP)
	}
	if !reflect.DeepEqual(def.Tools, []string{"read_file", "bash"}) {
		t.Errorf("Tools = %v, want [read_file bash]", def.Tools)
	}
	if !def.Disabled {
		t.Error("Disabled = false, want true")
	}
	// "permission: ask" → {"_mode": "ask"}
	wantPerms := map[string]any{"_mode": "ask"}
	if !reflect.DeepEqual(def.Permissions, wantPerms) {
		t.Errorf("Permissions = %v, want %v", def.Permissions, wantPerms)
	}
	if def.Color != "#00FF00" {
		t.Errorf("Color = %q, want %q", def.Color, "#00FF00")
	}
	if !def.Hidden {
		t.Error("Hidden = false, want true")
	}
	if def.Body != "You are an OpenCode agent." {
		t.Errorf("Body = %q, want %q", def.Body, "You are an OpenCode agent.")
	}
}

func TestImportOpenCode_ProviderModelSplit(t *testing.T) {
	t.Parallel()

	fmYAML := `provider: anthropic/claude-sonnet-4-20250514`
	def, err := agentfmt.ImportOpenCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportOpenCode: %v", err)
	}
	if def.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", def.Provider, "anthropic")
	}
	if def.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", def.Model, "claude-sonnet-4-20250514")
	}
}

func TestImportOpenCode_ProviderWithoutModel(t *testing.T) {
	t.Parallel()

	fmYAML := `provider: anthropic`
	def, err := agentfmt.ImportOpenCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportOpenCode: %v", err)
	}
	if def.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", def.Provider, "anthropic")
	}
	if def.Model != "" {
		t.Errorf("Model = %q, want empty", def.Model)
	}
}

func TestImportOpenCode_ProviderSlashOnly(t *testing.T) {
	t.Parallel()

	// Edge case: "anthropic/" with trailing slash but no model.
	fmYAML := `provider: anthropic/`
	def, err := agentfmt.ImportOpenCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportOpenCode: %v", err)
	}
	if def.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", def.Provider, "anthropic")
	}
	// Empty model part after slash — model stays empty.
	if def.Model != "" {
		t.Errorf("Model = %q, want empty", def.Model)
	}
}

func TestImportOpenCode_SeparateProviderAndModel(t *testing.T) {
	t.Parallel()

	fmYAML := `
provider: openai
model: gpt-4o
`
	def, err := agentfmt.ImportOpenCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportOpenCode: %v", err)
	}
	if def.Provider != "openai" {
		t.Errorf("Provider = %q, want %q", def.Provider, "openai")
	}
	if def.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", def.Model, "gpt-4o")
	}
}

func TestImportOpenCode_PermissionAsString(t *testing.T) {
	t.Parallel()

	fmYAML := `permission: auto`
	def, err := agentfmt.ImportOpenCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportOpenCode: %v", err)
	}
	want := map[string]any{"_mode": "auto"}
	if !reflect.DeepEqual(def.Permissions, want) {
		t.Errorf("Permissions = %v, want %v", def.Permissions, want)
	}
}

func TestImportOpenCode_PermissionAsMap(t *testing.T) {
	t.Parallel()

	fmYAML := `
permission:
  bash:
    allow_all: true
  write_file:
    allow_patterns:
      - "*.go"
`
	def, err := agentfmt.ImportOpenCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportOpenCode: %v", err)
	}
	if def.Permissions == nil {
		t.Fatal("Permissions is nil, want non-nil")
	}
	bashPerms, ok := def.Permissions["bash"]
	if !ok {
		t.Fatal("Permissions missing 'bash' key")
	}
	bashMap, ok := bashPerms.(map[string]any)
	if !ok {
		t.Fatalf("Permissions['bash'] is %T, want map[string]any", bashPerms)
	}
	if bashMap["allow_all"] != true {
		t.Errorf("Permissions['bash']['allow_all'] = %v, want true", bashMap["allow_all"])
	}
}

func TestImportOpenCode_PermissionAbsent(t *testing.T) {
	t.Parallel()

	fmYAML := `name: no-perm`
	def, err := agentfmt.ImportOpenCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportOpenCode: %v", err)
	}
	if def.Permissions != nil {
		t.Errorf("Permissions = %v, want nil", def.Permissions)
	}
}

func TestImportOpenCode_NamedColorNormalization(t *testing.T) {
	t.Parallel()

	fmYAML := `color: cyan`
	def, err := agentfmt.ImportOpenCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportOpenCode: %v", err)
	}
	if def.Color != "#00FFFF" {
		t.Errorf("Color = %q, want %q", def.Color, "#00FFFF")
	}
}

func TestImportOpenCode_Minimal(t *testing.T) {
	t.Parallel()

	fmYAML := `
name: minimal-oc
description: Minimal OpenCode agent
`
	def, err := agentfmt.ImportOpenCode(fmYAML, "Prompt.", "fallback")
	if err != nil {
		t.Fatalf("ImportOpenCode: %v", err)
	}
	if def.Name != "minimal-oc" {
		t.Errorf("Name = %q, want %q", def.Name, "minimal-oc")
	}
	if def.Description != "Minimal OpenCode agent" {
		t.Errorf("Description = %q, want %q", def.Description, "Minimal OpenCode agent")
	}
	if def.Body != "Prompt." {
		t.Errorf("Body = %q, want %q", def.Body, "Prompt.")
	}
}

func TestImportOpenCode_DefaultName(t *testing.T) {
	t.Parallel()

	fmYAML := `description: No name`
	def, err := agentfmt.ImportOpenCode(fmYAML, "", "default-oc")
	if err != nil {
		t.Fatalf("ImportOpenCode: %v", err)
	}
	if def.Name != "default-oc" {
		t.Errorf("Name = %q, want %q", def.Name, "default-oc")
	}
}

func TestImportOpenCode_EmptyFrontmatter(t *testing.T) {
	t.Parallel()

	def, err := agentfmt.ImportOpenCode("", "Body only.", "empty-oc")
	if err != nil {
		t.Fatalf("ImportOpenCode: %v", err)
	}
	if def.Name != "empty-oc" {
		t.Errorf("Name = %q, want %q", def.Name, "empty-oc")
	}
	if def.Body != "Body only." {
		t.Errorf("Body = %q, want %q", def.Body, "Body only.")
	}
}

func TestImportOpenCode_EmptyOptionalFields(t *testing.T) {
	t.Parallel()

	fmYAML := `name: sparse-oc`
	def, err := agentfmt.ImportOpenCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportOpenCode: %v", err)
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
	if def.Disabled {
		t.Error("Disabled = true, want false")
	}
	if def.Permissions != nil {
		t.Errorf("Permissions = %v, want nil", def.Permissions)
	}
	if def.Color != "" {
		t.Errorf("Color = %q, want empty", def.Color)
	}
	if def.Hidden {
		t.Error("Hidden = true, want false")
	}
}

func TestImportOpenCode_UnrecognizedFieldsInModelOptions(t *testing.T) {
	t.Parallel()

	fmYAML := `
name: extras
custom_field: custom_value
another_field: 42
`
	def, err := agentfmt.ImportOpenCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportOpenCode: %v", err)
	}
	if def.ModelOptions == nil {
		t.Fatal("ModelOptions is nil, want non-nil")
	}
	if def.ModelOptions["custom_field"] != "custom_value" {
		t.Errorf("ModelOptions['custom_field'] = %v, want %q", def.ModelOptions["custom_field"], "custom_value")
	}
	if def.ModelOptions["another_field"] != 42 {
		t.Errorf("ModelOptions['another_field'] = %v, want 42", def.ModelOptions["another_field"])
	}
}

func TestImportOpenCode_ExplicitModelOptions(t *testing.T) {
	t.Parallel()

	fmYAML := `
name: with-opts
model_options:
  max_tokens: 4096
  stop_sequences:
    - END
`
	def, err := agentfmt.ImportOpenCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportOpenCode: %v", err)
	}
	if def.ModelOptions == nil {
		t.Fatal("ModelOptions is nil, want non-nil")
	}
	if def.ModelOptions["max_tokens"] != 4096 {
		t.Errorf("ModelOptions['max_tokens'] = %v, want 4096", def.ModelOptions["max_tokens"])
	}
}

func TestImportOpenCode_ModelOptionsWithUnrecognizedMerge(t *testing.T) {
	t.Parallel()

	fmYAML := `
name: merged
model_options:
  max_tokens: 4096
extra_setting: true
`
	def, err := agentfmt.ImportOpenCode(fmYAML, "", "test")
	if err != nil {
		t.Fatalf("ImportOpenCode: %v", err)
	}
	if def.ModelOptions == nil {
		t.Fatal("ModelOptions is nil, want non-nil")
	}
	// Explicit model_options field.
	if def.ModelOptions["max_tokens"] != 4096 {
		t.Errorf("ModelOptions['max_tokens'] = %v, want 4096", def.ModelOptions["max_tokens"])
	}
	// Unrecognized field merged in.
	if def.ModelOptions["extra_setting"] != true {
		t.Errorf("ModelOptions['extra_setting'] = %v, want true", def.ModelOptions["extra_setting"])
	}
}

func TestImportOpenCode_InvalidYAML(t *testing.T) {
	t.Parallel()

	fmYAML := `[invalid yaml: {`
	_, err := agentfmt.ImportOpenCode(fmYAML, "", "test")
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}
