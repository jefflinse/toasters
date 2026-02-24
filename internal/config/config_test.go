package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// resetViper clears all viper global state between tests.
func resetViper(t *testing.T) {
	t.Helper()
	viper.Reset()
}

// writeConfigYAML writes a config.yaml file into dir with the given content.
func writeConfigYAML(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatalf("writing config.yaml: %v", err)
	}
}

// --- WorkspaceDir tests ---

func TestWorkspaceDir_EmptyString_ReturnsDefault(t *testing.T) {
	cfg := &Config{WorkspaceDir: ""}
	got, err := WorkspaceDir(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("getting home dir: %v", err)
	}
	want := filepath.Join(home, "toasters")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWorkspaceDir_TildeOnly_ReturnsHome(t *testing.T) {
	cfg := &Config{WorkspaceDir: "~"}
	got, err := WorkspaceDir(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("getting home dir: %v", err)
	}
	if got != home {
		t.Errorf("got %q, want %q", got, home)
	}
}

func TestWorkspaceDir_TildeSlashPath_ExpandsHome(t *testing.T) {
	cfg := &Config{WorkspaceDir: "~/my/workspace"}
	got, err := WorkspaceDir(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("getting home dir: %v", err)
	}
	want := filepath.Join(home, "my/workspace")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWorkspaceDir_AbsolutePath_ReturnedUnchanged(t *testing.T) {
	cfg := &Config{WorkspaceDir: "/opt/toasters/workspace"}
	got, err := WorkspaceDir(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "/opt/toasters/workspace"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWorkspaceDir_RelativePath_ReturnedUnchanged(t *testing.T) {
	cfg := &Config{WorkspaceDir: "relative/path"}
	got, err := WorkspaceDir(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "relative/path"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWorkspaceDir_TildeInMiddle_NotExpanded(t *testing.T) {
	// A tilde that is NOT at the start should not be expanded.
	cfg := &Config{WorkspaceDir: "/some/~path"}
	got, err := WorkspaceDir(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "/some/~path"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWorkspaceDir_TildeNoSlash_NotExpanded(t *testing.T) {
	// "~something" (no slash after tilde) should NOT be expanded — it's
	// treated as a relative path, not a home-dir reference.
	cfg := &Config{WorkspaceDir: "~something"}
	got, err := WorkspaceDir(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "~something"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- Dir tests ---

func TestDir_ReturnsConfigDir(t *testing.T) {
	got, err := Dir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("getting home dir: %v", err)
	}
	want := home + "/.config/toasters"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- Load tests ---

func TestLoad_MissingConfigFile_AppliesDefaults(t *testing.T) {
	resetViper(t)

	// Point viper at an empty temp dir — no config.yaml exists.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify defaults.
	wantWorkspace := filepath.Join(tmpHome, "toasters")
	if cfg.WorkspaceDir != wantWorkspace {
		t.Errorf("WorkspaceDir: got %q, want %q", cfg.WorkspaceDir, wantWorkspace)
	}
	if cfg.Operator.Provider != "local" {
		t.Errorf("Operator.Provider: got %q, want %q", cfg.Operator.Provider, "local")
	}
	if cfg.Operator.Endpoint != "http://localhost:1234" {
		t.Errorf("Operator.Endpoint: got %q, want %q", cfg.Operator.Endpoint, "http://localhost:1234")
	}
	if cfg.Operator.APIKey != "" {
		t.Errorf("Operator.APIKey: got %q, want %q", cfg.Operator.APIKey, "")
	}
	if cfg.Operator.Model != "" {
		t.Errorf("Operator.Model: got %q, want %q", cfg.Operator.Model, "")
	}
	wantTeamsDir := filepath.Join(tmpHome, ".config", "toasters", "teams")
	if cfg.Operator.TeamsDir != wantTeamsDir {
		t.Errorf("Operator.TeamsDir: got %q, want %q", cfg.Operator.TeamsDir, wantTeamsDir)
	}
	if cfg.Claude.Path != "claude" {
		t.Errorf("Claude.Path: got %q, want %q", cfg.Claude.Path, "claude")
	}
	if cfg.Claude.DefaultModel != "" {
		t.Errorf("Claude.DefaultModel: got %q, want %q", cfg.Claude.DefaultModel, "")
	}
	if cfg.Claude.PermissionMode != "" {
		t.Errorf("Claude.PermissionMode: got %q, want %q", cfg.Claude.PermissionMode, "")
	}
	if cfg.Claude.SlotTimeoutMinutes != 15 {
		t.Errorf("Claude.SlotTimeoutMinutes: got %d, want %d", cfg.Claude.SlotTimeoutMinutes, 15)
	}
}

func TestLoad_WithConfigFile_OverridesDefaults(t *testing.T) {
	resetViper(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	configDir := filepath.Join(tmpHome, ".config", "toasters")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	writeConfigYAML(t, configDir, `
workspace_dir: /custom/workspace
operator:
  provider: anthropic
  endpoint: http://example.com:9999
  api_key: sk-test-key
  model: gpt-custom
  teams_dir: /custom/teams
claude:
  path: /usr/local/bin/claude
  default_model: claude-opus-4-20250514
  permission_mode: plan
  slot_timeout_minutes: 30
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.WorkspaceDir != "/custom/workspace" {
		t.Errorf("WorkspaceDir: got %q, want %q", cfg.WorkspaceDir, "/custom/workspace")
	}
	if cfg.Operator.Provider != "anthropic" {
		t.Errorf("Operator.Provider: got %q, want %q", cfg.Operator.Provider, "anthropic")
	}
	if cfg.Operator.Endpoint != "http://example.com:9999" {
		t.Errorf("Operator.Endpoint: got %q, want %q", cfg.Operator.Endpoint, "http://example.com:9999")
	}
	if cfg.Operator.APIKey != "sk-test-key" {
		t.Errorf("Operator.APIKey: got %q, want %q", cfg.Operator.APIKey, "sk-test-key")
	}
	if cfg.Operator.Model != "gpt-custom" {
		t.Errorf("Operator.Model: got %q, want %q", cfg.Operator.Model, "gpt-custom")
	}
	if cfg.Operator.TeamsDir != "/custom/teams" {
		t.Errorf("Operator.TeamsDir: got %q, want %q", cfg.Operator.TeamsDir, "/custom/teams")
	}
	if cfg.Claude.Path != "/usr/local/bin/claude" {
		t.Errorf("Claude.Path: got %q, want %q", cfg.Claude.Path, "/usr/local/bin/claude")
	}
	if cfg.Claude.DefaultModel != "claude-opus-4-20250514" {
		t.Errorf("Claude.DefaultModel: got %q, want %q", cfg.Claude.DefaultModel, "claude-opus-4-20250514")
	}
	if cfg.Claude.PermissionMode != "plan" {
		t.Errorf("Claude.PermissionMode: got %q, want %q", cfg.Claude.PermissionMode, "plan")
	}
	if cfg.Claude.SlotTimeoutMinutes != 30 {
		t.Errorf("Claude.SlotTimeoutMinutes: got %d, want %d", cfg.Claude.SlotTimeoutMinutes, 30)
	}
}

func TestLoad_PartialConfig_MergesWithDefaults(t *testing.T) {
	resetViper(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	configDir := filepath.Join(tmpHome, ".config", "toasters")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	// Only override a few values — the rest should be defaults.
	writeConfigYAML(t, configDir, `
operator:
  model: my-model
claude:
  permission_mode: plan
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Overridden values.
	if cfg.Operator.Model != "my-model" {
		t.Errorf("Operator.Model: got %q, want %q", cfg.Operator.Model, "my-model")
	}
	if cfg.Claude.PermissionMode != "plan" {
		t.Errorf("Claude.PermissionMode: got %q, want %q", cfg.Claude.PermissionMode, "plan")
	}

	// Default values should still be applied.
	if cfg.Operator.Provider != "local" {
		t.Errorf("Operator.Provider: got %q, want %q (default)", cfg.Operator.Provider, "local")
	}
	if cfg.Operator.Endpoint != "http://localhost:1234" {
		t.Errorf("Operator.Endpoint: got %q, want %q (default)", cfg.Operator.Endpoint, "http://localhost:1234")
	}
	if cfg.Claude.Path != "claude" {
		t.Errorf("Claude.Path: got %q, want %q (default)", cfg.Claude.Path, "claude")
	}
	if cfg.Claude.SlotTimeoutMinutes != 15 {
		t.Errorf("Claude.SlotTimeoutMinutes: got %d, want %d (default)", cfg.Claude.SlotTimeoutMinutes, 15)
	}
	wantWorkspace := filepath.Join(tmpHome, "toasters")
	if cfg.WorkspaceDir != wantWorkspace {
		t.Errorf("WorkspaceDir: got %q, want %q (default)", cfg.WorkspaceDir, wantWorkspace)
	}
}

func TestLoad_MalformedYAML_ReturnsError(t *testing.T) {
	resetViper(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	configDir := filepath.Join(tmpHome, ".config", "toasters")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	writeConfigYAML(t, configDir, `
operator:
  endpoint: [invalid yaml
  this is not valid
`)

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for malformed YAML, got nil")
	}
}

func TestLoad_EmptyConfigFile_AppliesDefaults(t *testing.T) {
	resetViper(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	configDir := filepath.Join(tmpHome, ".config", "toasters")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	writeConfigYAML(t, configDir, "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// All defaults should be applied.
	if cfg.Operator.Provider != "local" {
		t.Errorf("Operator.Provider: got %q, want %q", cfg.Operator.Provider, "local")
	}
	if cfg.Operator.Endpoint != "http://localhost:1234" {
		t.Errorf("Operator.Endpoint: got %q, want %q", cfg.Operator.Endpoint, "http://localhost:1234")
	}
	if cfg.Claude.Path != "claude" {
		t.Errorf("Claude.Path: got %q, want %q", cfg.Claude.Path, "claude")
	}
	if cfg.Claude.SlotTimeoutMinutes != 15 {
		t.Errorf("Claude.SlotTimeoutMinutes: got %d, want %d", cfg.Claude.SlotTimeoutMinutes, 15)
	}
}

// --- BindFlags tests ---

func TestBindFlags_DoesNotPanic(t *testing.T) {
	resetViper(t)

	// BindFlags should not panic even when the flags don't exist on the command.
	// cobra.Command.Flags().Lookup returns nil for unknown flags, and
	// viper.BindPFlag handles nil gracefully.
	cmd := &cobra.Command{Use: "test"}
	BindFlags(cmd) // should not panic
}
