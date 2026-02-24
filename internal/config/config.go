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
	WorkspaceDir string         `mapstructure:"workspace_dir"`
	Operator     OperatorConfig `mapstructure:"operator"`
	Claude       ClaudeConfig   `mapstructure:"claude"`
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
	viper.SetDefault("operator.provider", "local")
	viper.SetDefault("operator.endpoint", "http://localhost:1234")
	viper.SetDefault("operator.api_key", "")
	viper.SetDefault("operator.model", "")
	viper.SetDefault("operator.teams_dir", filepath.Join(home, ".config", "toasters", "teams"))
	viper.SetDefault("claude.path", "claude")
	viper.SetDefault("claude.default_model", "")
	viper.SetDefault("claude.permission_mode", "")
	viper.SetDefault("claude.slot_timeout_minutes", 15)

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

	return &cfg, nil
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

// BindFlags binds relevant cobra pflags to their Viper configuration keys.
func BindFlags(cmd *cobra.Command) {
	viper.BindPFlag("operator.endpoint", cmd.Flags().Lookup("operator-endpoint")) //nolint:errcheck
	viper.BindPFlag("claude.path", cmd.Flags().Lookup("claude-path"))             //nolint:errcheck
}
