package config

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Config holds all application configuration.
// Providers are no longer stored here — they live in providers/*.yaml files
// and are loaded by the Loader.
type Config struct {
	WorkspaceDir      string         `mapstructure:"workspace_dir"`
	DatabasePath      string         `mapstructure:"database_path"`
	TaskGranularity   string         `mapstructure:"task_granularity"`
	CoarseGranularity string         `mapstructure:"coarse_granularity"`
	FineGranularity   string         `mapstructure:"fine_granularity"`
	// WorkerThinkingEnabled is the default value of the per-request
	// thinking/reasoning toggle for worker (graph) nodes. Roles may override
	// via the `thinking` field in their frontmatter.
	WorkerThinkingEnabled bool `mapstructure:"worker_thinking_enabled"`
	// WorkerTemperature is the default sampling temperature for worker
	// (graph) nodes. Roles may override via the `temperature` field in
	// their frontmatter.
	WorkerTemperature float64 `mapstructure:"worker_temperature"`
	// ShowJobsPanelByDefault forces the Jobs/Workers left panel to be
	// visible even when there are no jobs or runtime sessions to surface.
	// When false (default), the panel auto-hides on first run and reveals
	// itself once there's something to show.
	ShowJobsPanelByDefault bool `mapstructure:"show_jobs_panel_by_default"`
	// ShowOperatorPanelByDefault keeps the right Operator/sidebar panel
	// visible by default. When false, the panel is hidden until the user
	// reveals it via Ctrl+O.
	ShowOperatorPanelByDefault bool           `mapstructure:"show_operator_panel_by_default"`
	Operator                   OperatorConfig `mapstructure:"operator"`
	Agents            AgentsConfig   `mapstructure:"agents"`
	MCP               MCPConfig      `mapstructure:"mcp"`
}

// MCPServerConfig holds configuration for a single MCP server.
type MCPServerConfig struct {
	Name         string            `mapstructure:"name"`
	Transport    string            `mapstructure:"transport"`     // "stdio", "http", "sse"
	Command      string            `mapstructure:"command"`       // for stdio transport
	Args         []string          `mapstructure:"args"`          // for stdio transport
	Env          map[string]string `mapstructure:"env"`           // env vars for stdio subprocess
	URL          string            `mapstructure:"url"`           // for http/sse transport
	Headers      map[string]string `mapstructure:"headers"`       // for http/sse transport
	EnabledTools []string          `mapstructure:"enabled_tools"` // whitelist; empty = all
}

// MCPConfig holds configuration for all MCP servers.
type MCPConfig struct {
	Servers []MCPServerConfig `mapstructure:"servers"`
}

// AgentsConfig holds default provider/model settings for agents.
type AgentsConfig struct {
	Defaults AgentDefaultsConfig `mapstructure:"defaults"`
}

// AgentDefaultsConfig holds the default provider and model for agents.
type AgentDefaultsConfig struct {
	Provider string `mapstructure:"provider"`
	Model    string `mapstructure:"model"`
}

// OperatorConfig holds configuration for the operator LLM backend.
type OperatorConfig struct {
	Provider string `mapstructure:"provider"` // provider ID; empty means operator is disabled until configured
	Model    string `mapstructure:"model"`
	TeamsDir string `mapstructure:"teams_dir"`
}

// Load reads configuration from ~/.config/toasters/config.yaml, applying
// defaults for any values not present in the file.
func Load() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(home + "/.config/toasters")

	viper.SetDefault("workspace_dir", filepath.Join(home, "toasters"))
	viper.SetDefault("database_path", "")
	viper.SetDefault("operator.provider", "")
	viper.SetDefault("operator.model", "")
	viper.SetDefault("operator.teams_dir", filepath.Join(home, ".config", "toasters", "user", "teams"))
	viper.SetDefault("task_granularity", "moderate")
	viper.SetDefault("coarse_granularity", "medium")
	viper.SetDefault("fine_granularity", "medium")
	viper.SetDefault("worker_thinking_enabled", false)
	viper.SetDefault("worker_temperature", 0.1)
	viper.SetDefault("show_jobs_panel_by_default", false)
	viper.SetDefault("show_operator_panel_by_default", true)
	viper.SetDefault("agents.defaults.provider", "")
	viper.SetDefault("agents.defaults.model", "")

	if err := viper.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return nil, err
		}
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	expandMCPEnvVars(&cfg)
	ensureConfigFilePermissions()

	return &cfg, nil
}

// ValidTaskGranularity returns value if it is a recognized task granularity
// preset (coarse, moderate, fine, atomic). Otherwise it logs a warning and
// returns "moderate".
func ValidTaskGranularity(value string) string {
	switch value {
	case "coarse", "moderate", "fine", "atomic":
		return value
	default:
		slog.Warn("invalid task_granularity, defaulting to moderate", "value", value)
		return "moderate"
	}
}

// granularityLevels lists the shared preset levels used by both
// coarse_granularity and fine_granularity. Ordered from coarsest (most work
// per output unit) to finest (least work per output unit).
var granularityLevels = []string{"xcoarse", "coarse", "medium", "fine", "xfine"}

// GranularityLevels returns the allowed granularity values in order from
// coarsest to finest. Used by coarse_granularity and fine_granularity.
func GranularityLevels() []string {
	out := make([]string, len(granularityLevels))
	copy(out, granularityLevels)
	return out
}

// ValidGranularity returns value if it is one of the recognized granularity
// presets. Otherwise it logs a warning (tagged with kind, e.g. "coarse" or
// "fine") and returns "medium".
func ValidGranularity(kind, value string) string {
	for _, v := range granularityLevels {
		if v == value {
			return value
		}
	}
	slog.Warn("invalid granularity, defaulting to medium", "kind", kind, "value", value)
	return "medium"
}

// isPlaintextKey returns true if key is a non-empty API key value that does
// not use the ${ENV_VAR} syntax for environment variable substitution.
func isPlaintextKey(key string) bool {
	return key != "" && !strings.Contains(key, "${")
}

// ensureConfigFilePermissions checks the config file permissions and tightens
// them to 0600 if group or other bits are set (i.e. perm & 0077 != 0).
func ensureConfigFilePermissions() {
	cfgFile := viper.ConfigFileUsed()
	if cfgFile == "" {
		return
	}
	info, err := os.Stat(cfgFile)
	if err != nil {
		return
	}
	perm := info.Mode().Perm()
	if perm&0077 != 0 {
		slog.Warn("config file permissions too open, restricting to 0600",
			"path", cfgFile,
			"was", perm.String(),
		)
		if err := os.Chmod(cfgFile, fs.FileMode(0600)); err != nil {
			slog.Error("failed to chmod config file", "path", cfgFile, "error", err)
		}
	}
}

// expandMCPEnvVars expands ${VAR} references in MCP server configuration fields
// (Command, Args, URL, Env values, and Headers values) using os.Getenv.
func expandMCPEnvVars(cfg *Config) {
	for i := range cfg.MCP.Servers {
		s := &cfg.MCP.Servers[i]
		s.Command = os.Expand(s.Command, os.Getenv)
		for j, arg := range s.Args {
			s.Args[j] = os.Expand(arg, os.Getenv)
		}
		if s.URL != "" {
			s.URL = os.Expand(s.URL, os.Getenv)
		}
		for k, v := range s.Env {
			s.Env[k] = os.Expand(v, os.Getenv)
		}
		for k, v := range s.Headers {
			s.Headers[k] = os.Expand(v, os.Getenv)
		}
	}
}

// expandTilde expands a leading "~" in path to the user's home directory.
// If path is empty, fallback is returned. If os.UserHomeDir fails, the error is returned.
func expandTilde(path, fallback string) (string, error) {
	if path == "" {
		return fallback, nil
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return home, nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

// Dir returns the toasters config directory (~/.config/toasters).
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return home + "/.config/toasters", nil
}

// WorkspaceDir returns the resolved workspace directory from cfg.
// A leading ~ is expanded to the user's home directory.
// Absolute paths are returned unchanged without calling os.UserHomeDir.
func WorkspaceDir(cfg *Config) (string, error) {
	if cfg.WorkspaceDir != "" && !strings.HasPrefix(cfg.WorkspaceDir, "~") {
		return cfg.WorkspaceDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return expandTilde(cfg.WorkspaceDir, filepath.Join(home, "toasters"))
}

// DatabasePath returns the resolved database file path from cfg.
// A leading ~ is expanded to the user's home directory.
// Absolute paths are returned unchanged without calling os.UserHomeDir.
//
// When database_path is not explicitly set, the database defaults to
// <workspaceDir>/toasters.db so that operational state (jobs, tasks,
// sessions) lives alongside the workspace rather than in the config
// directory. This allows the config directory to be version-controlled
// without including transient job state.
func DatabasePath(cfg *Config, workspaceDir string) (string, error) {
	if cfg.DatabasePath != "" && !strings.HasPrefix(cfg.DatabasePath, "~") {
		return cfg.DatabasePath, nil
	}
	return expandTilde(cfg.DatabasePath, filepath.Join(workspaceDir, "toasters.db"))
}

// BindFlags binds relevant cobra pflags to their Viper configuration keys.
func BindFlags(_ *cobra.Command) {
}
