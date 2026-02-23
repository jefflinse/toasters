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
	Operator OperatorConfig `mapstructure:"operator"`
	Claude   ClaudeConfig   `mapstructure:"claude"`
}

// OperatorConfig holds configuration for the operator LLM backend.
type OperatorConfig struct {
	Endpoint    string `mapstructure:"endpoint"`
	APIKey      string `mapstructure:"api_key"`
	Model       string `mapstructure:"model"`
	TeamsDir    string `mapstructure:"teams_dir"`
	LogRequests bool   `mapstructure:"log_requests"`
}

// ClaudeConfig holds configuration for the Claude CLI.
type ClaudeConfig struct {
	Path           string `mapstructure:"path"`
	DefaultModel   string `mapstructure:"default_model"`
	PermissionMode string `mapstructure:"permission_mode"`
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

	teamsHome, _ := os.UserHomeDir() // fall back to "" on error
	viper.SetDefault("operator.endpoint", "http://localhost:1234")
	viper.SetDefault("operator.api_key", "")
	viper.SetDefault("operator.model", "")
	viper.SetDefault("operator.teams_dir", filepath.Join(teamsHome, ".opencode", "teams"))
	viper.SetDefault("operator.log_requests", false)
	viper.SetDefault("claude.path", "claude")
	viper.SetDefault("claude.default_model", "")
	viper.SetDefault("claude.permission_mode", "")

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

// WorkEffortsDir returns the directory where work efforts are stored.
func WorkEffortsDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return dir + "/work-efforts", nil
}

// BindFlags binds relevant cobra pflags to their Viper configuration keys.
func BindFlags(cmd *cobra.Command) {
	viper.BindPFlag("operator.endpoint", cmd.Flags().Lookup("operator-endpoint")) //nolint:errcheck
	viper.BindPFlag("claude.path", cmd.Flags().Lookup("claude-path"))             //nolint:errcheck
}
