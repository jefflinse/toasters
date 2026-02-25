package config

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Config holds all application configuration.
type Config struct {
	WorkspaceDir string           `mapstructure:"workspace_dir"`
	DatabasePath string           `mapstructure:"database_path"`
	Operator     OperatorConfig   `mapstructure:"operator"`
	Claude       ClaudeConfig     `mapstructure:"claude"`
	Providers    []ProviderConfig `mapstructure:"providers"`
	Agents       AgentsConfig     `mapstructure:"agents"`
	MCP          MCPConfig        `mapstructure:"mcp"`
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

// ProviderConfig holds configuration for a single LLM provider.
type ProviderConfig struct {
	Name     string `mapstructure:"name"`
	Type     string `mapstructure:"type"` // "openai" or "anthropic"
	Endpoint string `mapstructure:"endpoint"`
	APIKey   string `mapstructure:"api_key"`
	Model    string `mapstructure:"model"`
}

// AgentsConfig holds default provider/model settings for agents.
type AgentsConfig struct {
	DefaultProvider string `mapstructure:"default_provider"`
	DefaultModel    string `mapstructure:"default_model"`
}

// OperatorConfig holds configuration for the operator LLM backend.
type OperatorConfig struct {
	Provider string `mapstructure:"provider"` // "local" (default) or "anthropic"
	Endpoint string `mapstructure:"endpoint"`
	APIKey   string `mapstructure:"api_key"`
	Model    string `mapstructure:"model"`
	TeamsDir string `mapstructure:"teams_dir"`
}

// ClaudeConfig holds configuration for the Claude CLI.
type ClaudeConfig struct {
	Path               string `mapstructure:"path"`
	DefaultModel       string `mapstructure:"default_model"`
	PermissionMode     string `mapstructure:"permission_mode"`
	SlotTimeoutMinutes int    `mapstructure:"slot_timeout_minutes"`
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
	viper.SetDefault("database_path", filepath.Join(home, ".config", "toasters", "toasters.db"))
	viper.SetDefault("operator.provider", "local")
	viper.SetDefault("operator.endpoint", "http://localhost:1234")
	viper.SetDefault("operator.api_key", "")
	viper.SetDefault("operator.model", "")
	viper.SetDefault("operator.teams_dir", filepath.Join(home, ".config", "toasters", "teams"))
	viper.SetDefault("claude.path", "claude")
	viper.SetDefault("claude.default_model", "")
	viper.SetDefault("claude.permission_mode", "")
	viper.SetDefault("claude.slot_timeout_minutes", 15)
	viper.SetDefault("agents.default_provider", "")
	viper.SetDefault("agents.default_model", "")

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

	return &cfg, nil
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
// Absolute paths are returned unchanged.
func WorkspaceDir(cfg *Config) (string, error) {
	dir := cfg.WorkspaceDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "toasters"), nil
	}
	if dir == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return home, nil
	}
	if len(dir) >= 2 && dir[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, dir[2:]), nil
	}
	return dir, nil
}

// DatabasePath returns the resolved database file path from cfg.
// A leading ~ is expanded to the user's home directory.
func DatabasePath(cfg *Config) (string, error) {
	p := cfg.DatabasePath
	if p == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "toasters", "toasters.db"), nil
	}
	if len(p) >= 2 && p[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, p[2:]), nil
	}
	if p == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return home, nil
	}
	return p, nil
}

// BindFlags binds relevant cobra pflags to their Viper configuration keys.
func BindFlags(cmd *cobra.Command) {
	viper.BindPFlag("operator.endpoint", cmd.Flags().Lookup("operator-endpoint")) //nolint:errcheck
	viper.BindPFlag("claude.path", cmd.Flags().Lookup("claude-path"))             //nolint:errcheck
}
