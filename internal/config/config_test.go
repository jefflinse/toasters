package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

// --- ValidTaskGranularity tests ---

func TestValidTaskGranularity_ValidValues(t *testing.T) {
	for _, v := range []string{"coarse", "moderate", "fine", "atomic"} {
		got := ValidTaskGranularity(v)
		if got != v {
			t.Errorf("ValidTaskGranularity(%q) = %q, want %q", v, got, v)
		}
	}
}

func TestValidTaskGranularity_InvalidValue_DefaultsToModerate(t *testing.T) {
	for _, v := range []string{"", "invalid", "FINE", "medium", "very-fine"} {
		got := ValidTaskGranularity(v)
		if got != "moderate" {
			t.Errorf("ValidTaskGranularity(%q) = %q, want %q", v, got, "moderate")
		}
	}
}

func TestLoad_TaskGranularity_Default(t *testing.T) {
	resetViper(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.TaskGranularity != "moderate" {
		t.Errorf("TaskGranularity: got %q, want %q", cfg.TaskGranularity, "moderate")
	}
}

func TestLoad_TaskGranularity_Override(t *testing.T) {
	resetViper(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	configDir := filepath.Join(tmpHome, ".config", "toasters")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	writeConfigYAML(t, configDir, `
task_granularity: fine
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.TaskGranularity != "fine" {
		t.Errorf("TaskGranularity: got %q, want %q", cfg.TaskGranularity, "fine")
	}
}

// --- ValidGranularity tests ---

func TestValidGranularity_ValidValues(t *testing.T) {
	for _, v := range []string{"xcoarse", "coarse", "medium", "fine", "xfine"} {
		if got := ValidGranularity("coarse", v); got != v {
			t.Errorf("ValidGranularity(coarse, %q) = %q, want %q", v, got, v)
		}
		if got := ValidGranularity("fine", v); got != v {
			t.Errorf("ValidGranularity(fine, %q) = %q, want %q", v, got, v)
		}
	}
}

func TestValidGranularity_InvalidValue_DefaultsToMedium(t *testing.T) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	for _, v := range []string{"", "bogus", "MEDIUM"} {
		if got := ValidGranularity("coarse", v); got != "medium" {
			t.Errorf("ValidGranularity(coarse, %q) = %q, want %q", v, got, "medium")
		}
	}
}

func TestGranularityLevels_ReturnsDefensiveCopy(t *testing.T) {
	a := GranularityLevels()
	if len(a) != 5 {
		t.Fatalf("expected 5 levels, got %d", len(a))
	}
	a[0] = "tampered"
	b := GranularityLevels()
	if b[0] == "tampered" {
		t.Errorf("mutation leaked into second call — caller got a shared slice")
	}
}

// --- SetTopLevelScalar round-trip (granularity keys) ---

func TestSetTopLevelScalar_GranularityRoundTrip(t *testing.T) {
	resetViper(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	configDir := filepath.Join(tmpHome, ".config", "toasters")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	writeConfigYAML(t, configDir, "task_granularity: moderate\ncoarse_granularity: medium\nfine_granularity: medium\n")

	if err := SetTopLevelScalar(configDir, "fine_granularity", "xfine"); err != nil {
		t.Fatalf("SetTopLevelScalar(fine_granularity): %v", err)
	}
	if err := SetTopLevelScalar(configDir, "coarse_granularity", "xcoarse"); err != nil {
		t.Fatalf("SetTopLevelScalar(coarse_granularity): %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.FineGranularity != "xfine" {
		t.Errorf("FineGranularity round-trip: got %q, want %q", cfg.FineGranularity, "xfine")
	}
	if cfg.CoarseGranularity != "xcoarse" {
		t.Errorf("CoarseGranularity round-trip: got %q, want %q", cfg.CoarseGranularity, "xcoarse")
	}
}

func TestSetTopLevelScalar_AddsMissingKey(t *testing.T) {
	resetViper(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	configDir := filepath.Join(tmpHome, ".config", "toasters")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// No granularity keys in the starting file.
	writeConfigYAML(t, configDir, "task_granularity: moderate\n")

	if err := SetTopLevelScalar(configDir, "fine_granularity", "fine"); err != nil {
		t.Fatalf("SetTopLevelScalar: %v", err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.FineGranularity != "fine" {
		t.Errorf("got %q, want %q", cfg.FineGranularity, "fine")
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
	if cfg.Operator.Provider != "" {
		t.Errorf("Operator.Provider: got %q, want %q", cfg.Operator.Provider, "")
	}
	if cfg.Operator.Model != "" {
		t.Errorf("Operator.Model: got %q, want %q", cfg.Operator.Model, "")
	}
	wantTeamsDir := filepath.Join(tmpHome, ".config", "toasters", "user", "teams")
	if cfg.Operator.TeamsDir != wantTeamsDir {
		t.Errorf("Operator.TeamsDir: got %q, want %q", cfg.Operator.TeamsDir, wantTeamsDir)
	}
}

// TestLoad_TeamsDir_DefaultsToUserTeams is a regression test for a bug where
// operator.teams_dir defaulted to ~/.config/toasters/teams (the old pre-migration
// path) instead of the correct ~/.config/toasters/user/teams. New teams created
// via the TUI would be written to the wrong directory when no config file was present.
func TestLoad_TeamsDir_DefaultsToUserTeams(t *testing.T) {
	resetViper(t)

	// Point viper at an empty temp dir — no config.yaml exists.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantTeamsDir := filepath.Join(tmpHome, ".config", "toasters", "user", "teams")
	if cfg.Operator.TeamsDir != wantTeamsDir {
		t.Errorf("Operator.TeamsDir: got %q, want %q\n(regression: default must be user/teams, not the old teams/ path)", cfg.Operator.TeamsDir, wantTeamsDir)
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
  model: gpt-custom
  teams_dir: /custom/teams
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
	if cfg.Operator.Model != "gpt-custom" {
		t.Errorf("Operator.Model: got %q, want %q", cfg.Operator.Model, "gpt-custom")
	}
	if cfg.Operator.TeamsDir != "/custom/teams" {
		t.Errorf("Operator.TeamsDir: got %q, want %q", cfg.Operator.TeamsDir, "/custom/teams")
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
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Overridden values.
	if cfg.Operator.Model != "my-model" {
		t.Errorf("Operator.Model: got %q, want %q", cfg.Operator.Model, "my-model")
	}

	// Default values should still be applied.
	if cfg.Operator.Provider != "" {
		t.Errorf("Operator.Provider: got %q, want %q (default)", cfg.Operator.Provider, "")
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
  model: [invalid yaml
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
	if cfg.Operator.Provider != "" {
		t.Errorf("Operator.Provider: got %q, want %q", cfg.Operator.Provider, "")
	}
}

// --- MCP config tests ---

func TestLoad_NoMCPKey_ZeroValueMCPConfig(t *testing.T) {
	resetViper(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	configDir := filepath.Join(tmpHome, ".config", "toasters")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	writeConfigYAML(t, configDir, `
operator:
  model: my-model
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.MCP.Servers) != 0 {
		t.Errorf("MCP.Servers: got %d servers, want 0", len(cfg.MCP.Servers))
	}
}

func TestLoad_MCPServers_UnmarshalsCorrectly(t *testing.T) {
	resetViper(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	configDir := filepath.Join(tmpHome, ".config", "toasters")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	writeConfigYAML(t, configDir, `
mcp:
  servers:
    - name: my-stdio-server
      transport: stdio
      command: /usr/local/bin/mcp-server
      args:
        - --port
        - "8080"
      env:
        FOO: bar
        BAZ: qux
    - name: my-sse-server
      transport: sse
      url: https://example.com/mcp
      headers:
        Authorization: Bearer token123
      enabled_tools:
        - tool_a
        - tool_b
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.MCP.Servers) != 2 {
		t.Fatalf("MCP.Servers: got %d servers, want 2", len(cfg.MCP.Servers))
	}

	s0 := cfg.MCP.Servers[0]
	if s0.Name != "my-stdio-server" {
		t.Errorf("Servers[0].Name: got %q, want %q", s0.Name, "my-stdio-server")
	}
	if s0.Transport != "stdio" {
		t.Errorf("Servers[0].Transport: got %q, want %q", s0.Transport, "stdio")
	}
	if s0.Command != "/usr/local/bin/mcp-server" {
		t.Errorf("Servers[0].Command: got %q, want %q", s0.Command, "/usr/local/bin/mcp-server")
	}
	if len(s0.Args) != 2 || s0.Args[0] != "--port" || s0.Args[1] != "8080" {
		t.Errorf("Servers[0].Args: got %v, want [--port 8080]", s0.Args)
	}
	// Note: Viper lowercases all map keys during unmarshal.
	if s0.Env["foo"] != "bar" || s0.Env["baz"] != "qux" {
		t.Errorf("Servers[0].Env: got %v", s0.Env)
	}

	s1 := cfg.MCP.Servers[1]
	if s1.Name != "my-sse-server" {
		t.Errorf("Servers[1].Name: got %q, want %q", s1.Name, "my-sse-server")
	}
	if s1.Transport != "sse" {
		t.Errorf("Servers[1].Transport: got %q, want %q", s1.Transport, "sse")
	}
	if s1.URL != "https://example.com/mcp" {
		t.Errorf("Servers[1].URL: got %q, want %q", s1.URL, "https://example.com/mcp")
	}
	// Note: Viper lowercases all map keys during unmarshal.
	if s1.Headers["authorization"] != "Bearer token123" {
		t.Errorf("Servers[1].Headers: got %v", s1.Headers)
	}
	if len(s1.EnabledTools) != 2 || s1.EnabledTools[0] != "tool_a" || s1.EnabledTools[1] != "tool_b" {
		t.Errorf("Servers[1].EnabledTools: got %v, want [tool_a tool_b]", s1.EnabledTools)
	}
}

func TestLoad_MCPEnvVarExpansion(t *testing.T) {
	resetViper(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("MCP_TOKEN", "secret-token")
	t.Setenv("MCP_HOST", "api.example.com")

	configDir := filepath.Join(tmpHome, ".config", "toasters")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	writeConfigYAML(t, configDir, `
mcp:
  servers:
    - name: test-server
      transport: sse
      url: https://${MCP_HOST}/mcp
      headers:
        Authorization: Bearer ${MCP_TOKEN}
      env:
        TOKEN: ${MCP_TOKEN}
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.MCP.Servers) != 1 {
		t.Fatalf("MCP.Servers: got %d servers, want 1", len(cfg.MCP.Servers))
	}

	s := cfg.MCP.Servers[0]
	if s.URL != "https://api.example.com/mcp" {
		t.Errorf("URL: got %q, want %q", s.URL, "https://api.example.com/mcp")
	}
	// Note: Viper lowercases all map keys during unmarshal.
	if s.Headers["authorization"] != "Bearer secret-token" {
		t.Errorf("Headers[authorization]: got %q, want %q", s.Headers["authorization"], "Bearer secret-token")
	}
	if s.Env["token"] != "secret-token" {
		t.Errorf("Env[token]: got %q, want %q", s.Env["token"], "secret-token")
	}
}

// --- BindFlags tests ---

// --- DatabasePath tests ---

func TestDatabasePath_EmptyString_DefaultsToWorkspaceDir(t *testing.T) {
	cfg := &Config{DatabasePath: ""}
	got, err := DatabasePath(cfg, "/my/workspace")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/my/workspace/toasters.db"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDatabasePath_ExplicitAbsolutePath_ReturnedUnchanged(t *testing.T) {
	cfg := &Config{DatabasePath: "/custom/path/my.db"}
	got, err := DatabasePath(cfg, "/my/workspace")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/custom/path/my.db"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDatabasePath_TildePath_ExpandsHome(t *testing.T) {
	cfg := &Config{DatabasePath: "~/data/toasters.db"}
	got, err := DatabasePath(cfg, "/my/workspace")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("getting home dir: %v", err)
	}
	want := filepath.Join(home, "data/toasters.db")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
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

// --- isPlaintextKey tests ---

func TestIsPlaintextKey_EmptyString_ReturnsFalse(t *testing.T) {
	if isPlaintextKey("") {
		t.Error("expected false for empty string")
	}
}

func TestIsPlaintextKey_PlaintextKey_ReturnsTrue(t *testing.T) {
	if !isPlaintextKey("sk-abc123") {
		t.Error("expected true for plaintext key")
	}
}

func TestIsPlaintextKey_EnvVarSyntax_ReturnsFalse(t *testing.T) {
	if isPlaintextKey("${ANTHROPIC_API_KEY}") {
		t.Error("expected false for env var syntax")
	}
}

func TestIsPlaintextKey_MixedEnvVarSyntax_ReturnsFalse(t *testing.T) {
	// A value like "Bearer ${TOKEN}" still contains ${, so it's not plaintext.
	if isPlaintextKey("Bearer ${TOKEN}") {
		t.Error("expected false for mixed env var syntax")
	}
}

// Provider-related tests (warnPlaintextAPIKeys, expandAPIKeys) removed —
// providers now live in providers/*.yaml files, loaded by the Loader.

// --- ensureConfigFilePermissions tests ---

func TestEnsureConfigFilePermissions_TooOpen_RestrictsTo0600(t *testing.T) {
	resetViper(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	configDir := filepath.Join(tmpHome, ".config", "toasters")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	cfgPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("operator:\n  model: test\n"), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	// Load config so viper knows the config file path.
	_, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// After Load, permissions should have been tightened.
	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected permissions 0600, got %04o", perm)
	}
}

func TestEnsureConfigFilePermissions_Already0600_NoChange(t *testing.T) {
	resetViper(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	configDir := filepath.Join(tmpHome, ".config", "toasters")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	cfgPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("operator:\n  model: test\n"), 0600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	// Capture slog output to verify no warning is emitted.
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	old := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(old)

	_, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	if strings.Contains(output, "permissions too open") {
		t.Errorf("should not warn when permissions are already 0600, got: %s", output)
	}

	// Permissions should remain 0600.
	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected permissions 0600, got %04o", perm)
	}
}

func TestEnsureConfigFilePermissions_NoConfigFile_NoError(t *testing.T) {
	resetViper(t)

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// No config file — Load should succeed and ensureConfigFilePermissions
	// should silently return (viper.ConfigFileUsed() returns "").
	_, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
