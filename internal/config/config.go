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
	WorkspaceDir      string `mapstructure:"workspace_dir"`
	DatabasePath      string `mapstructure:"database_path"`
	TaskGranularity   string `mapstructure:"task_granularity"`
	CoarseGranularity string `mapstructure:"coarse_granularity"`
	FineGranularity   string `mapstructure:"fine_granularity"`
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
	ShowOperatorPanelByDefault bool `mapstructure:"show_operator_panel_by_default"`
	// FleetRowDensity controls how tall each LLM row in the fleet panel is:
	// "full" (label / model / bar / stats / activity) or "compact" (folded
	// onto fewer lines). Empty defaults to "full".
	FleetRowDensity string `mapstructure:"fleet_row_density"`
	// SidebarSide controls which side of the chat window the sidebar
	// (Jobs / Fleet / Blockers) renders on: "left" or "right". Empty
	// defaults to "left".
	SidebarSide string `mapstructure:"sidebar_side"`
	// OperatorCompactionThreshold is the percentage of the operator's
	// context window at which a compaction/handoff triggers. 0 disables
	// compaction. See docs/compaction-design.md.
	OperatorCompactionThreshold int `mapstructure:"operator_compaction_threshold"`
	// WorkerCompactionThreshold is the percentage of a worker session's
	// context window at which history compaction triggers. 0 disables
	// compaction.
	WorkerCompactionThreshold int            `mapstructure:"worker_compaction_threshold"`
	Operator                  OperatorConfig `mapstructure:"operator"`
	Workers                   WorkersConfig  `mapstructure:"agents"` // config key "agents" kept for backward compatibility
	MCP                       MCPConfig      `mapstructure:"mcp"`
	KB                        KBConfig       `mapstructure:"kb"`
}

// KBConfig holds configuration for the Knowledge Base feature. Enabled is a
// kill switch: when false, the job-note tools are not advertised to workers
// and Execute rejects them. Provider/Model/TopK gate the vector-store
// features (Part B): when Provider or Model is unset, semantic search is
// unavailable but job notes (Part A) keep working unaffected. See
// docs/kb-design.md.
type KBConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// Provider is the ID of a dedicated embedding provider entry (its own
	// scheduler, distinct from the operator/worker chat providers). Empty
	// means the vector store has no embedding backend configured.
	Provider string `mapstructure:"provider"`
	// Model is the embedding model name, e.g. "nomic-embed-text". Empty
	// means the vector store has no embedding model configured.
	Model string `mapstructure:"model"`
	// TopK is the number of nearest-neighbor results returned by a semantic
	// search query.
	TopK int `mapstructure:"top_k"`
	// DocPrefix is prepended to content before embedding it for storage
	// (Remember/Insert). e.g. "search_document: " for nomic-embed models,
	// which are trained on task-prefixed asymmetric retrieval pairs. Empty
	// means no prefix is applied — the right default for models that were
	// not trained with task prefixes.
	DocPrefix string `mapstructure:"doc_prefix"`
	// QueryPrefix is prepended to a query before embedding it for search
	// (Recall/Search). e.g. "search_query: " for nomic-embed models. Empty
	// means no prefix is applied. DocPrefix and QueryPrefix MUST match
	// whatever the stored vectors were embedded with — changing either after
	// facts have been written makes existing vectors incomparable to new
	// queries (and vice versa).
	QueryPrefix string `mapstructure:"query_prefix"`
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

// WorkersConfig holds default provider/model settings for workers.
// It is stored under the legacy "agents" key in config.yaml.
type WorkersConfig struct {
	Defaults WorkerDefaultsConfig `mapstructure:"defaults"`
}

// WorkerDefaultsConfig holds the default provider and model for workers.
type WorkerDefaultsConfig struct {
	Provider string `mapstructure:"provider"`
	Model    string `mapstructure:"model"`
}

// OperatorConfig holds configuration for the operator LLM backend.
type OperatorConfig struct {
	Provider string `mapstructure:"provider"` // provider ID; empty means operator is disabled until configured
	Model    string `mapstructure:"model"`
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
	viper.SetDefault("operator.provider", "lmstudio")
	viper.SetDefault("operator.model", "qwen/qwen3.6-35b-a3b")
	viper.SetDefault("task_granularity", "moderate")
	viper.SetDefault("coarse_granularity", "medium")
	viper.SetDefault("fine_granularity", "xfine")
	viper.SetDefault("worker_thinking_enabled", false)
	viper.SetDefault("worker_temperature", 0.1)
	viper.SetDefault("show_jobs_panel_by_default", true)
	viper.SetDefault("fleet_row_density", "full")
	viper.SetDefault("sidebar_side", "left")
	viper.SetDefault("show_operator_panel_by_default", true)
	viper.SetDefault("operator_compaction_threshold", DefaultOperatorCompactionThreshold)
	viper.SetDefault("worker_compaction_threshold", DefaultWorkerCompactionThreshold)
	viper.SetDefault("agents.defaults.provider", "")
	viper.SetDefault("agents.defaults.model", "")
	viper.SetDefault("kb.enabled", true)
	viper.SetDefault("kb.top_k", 5)

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

// FleetRowDensityLevels returns the allowed fleet-row-density values.
func FleetRowDensityLevels() []string {
	return []string{"full", "compact"}
}

// ValidFleetDensity normalizes a fleet-row-density value, defaulting an empty
// or unrecognized value to "full".
func ValidFleetDensity(value string) string {
	switch value {
	case "full", "compact":
		return value
	default:
		return "full"
	}
}

// SidebarSideOptions returns the allowed sidebar-side values.
func SidebarSideOptions() []string {
	return []string{"left", "right"}
}

// ValidSidebarSide normalizes a sidebar-side value, defaulting an empty or
// unrecognized value to "left".
func ValidSidebarSide(value string) string {
	switch value {
	case "left", "right":
		return value
	default:
		return "left"
	}
}

// Default compaction thresholds (percent of the resolved context window).
// The operator compacts earlier than workers: its session is long-lived and
// a digest handoff is cheap, while worker sessions are task-scoped and
// usually finish before filling their window.
const (
	DefaultOperatorCompactionThreshold = 50
	DefaultWorkerCompactionThreshold   = 70
)

// CompactionThresholdOptions returns the compaction-threshold values the
// settings UI cycles through: 0 disables compaction; otherwise a percentage
// of the resolved context window.
func CompactionThresholdOptions() []int {
	return []int{0, 30, 40, 50, 60, 70, 80, 90}
}

// ValidCompactionThreshold normalizes a compaction threshold percentage.
// 0 means "disabled" and passes through; positive values clamp to [30, 90]
// (below 30 compaction would thrash, above 90 it can't fire before
// overflow); negative values are nonsense and normalize to fallback.
func ValidCompactionThreshold(value, fallback int) int {
	switch {
	case value == 0:
		return 0
	case value < 0:
		return fallback
	case value < 30:
		return 30
	case value > 90:
		return 90
	default:
		return value
	}
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
