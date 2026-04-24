// Package bootstrap handles first-run setup and upgrade migration of the
// toasters config directory. Run is idempotent and safe to call on every startup.
package bootstrap

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Run performs first-run bootstrap and upgrade migration.
// It is idempotent — safe to call on every startup.
//
// configDir is the toasters config directory (e.g. ~/.config/toasters).
// systemFS is the embedded filesystem containing default system team files
// rooted at "system/" (e.g. defaults.SystemFiles).
// defaultConfig is the default config.yaml content to write on first run
// (e.g. defaults.DefaultConfig). It is only written if config.yaml does not
// already exist.
// UserFS is the embedded filesystem containing default user definition files
// (roles, toolchains, instructions, teams). Set by the caller before Run().
var UserFS embed.FS

func Run(configDir string, systemFS embed.FS, defaultConfig []byte) error {
	if err := firstRun(configDir, systemFS, defaultConfig); err != nil {
		return fmt.Errorf("first-run bootstrap: %w", err)
	}

	if err := providerIDMigration(configDir); err != nil {
		return fmt.Errorf("provider ID migration: %w", err)
	}

	if err := migrateDatabase(configDir); err != nil {
		return fmt.Errorf("database migration: %w", err)
	}

	if err := ensureDirectories(configDir); err != nil {
		return fmt.Errorf("ensuring directories: %w", err)
	}

	return nil
}

// firstRun copies embedded system files and creates the user directory structure.
// System files are synced on every startup so that binary upgrades deploy new
// definitions (roles, instructions) without requiring users to delete system/.
// User files are only written on first run to avoid overwriting customizations.
// If defaultConfig is non-nil and config.yaml does not already exist, it is
// written as the initial configuration.
func firstRun(configDir string, systemFS embed.FS, defaultConfig []byte) error {
	// Write default config.yaml if it doesn't exist yet (regardless of whether
	// this is a true first run — safe to do on every startup).
	if len(defaultConfig) > 0 {
		configPath := filepath.Join(configDir, "config.yaml")
		if !fileExists(configPath) {
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				return fmt.Errorf("creating config dir: %w", err)
			}
			if err := os.WriteFile(configPath, defaultConfig, 0o644); err != nil {
				return fmt.Errorf("writing default config.yaml: %w", err)
			}
			slog.Info("Wrote default config.yaml", "path", configPath)
		}
	}

	isFirstRun := false
	systemDir := filepath.Join(configDir, "system")
	if !dirExists(systemDir) {
		isFirstRun = true
	}

	// Always sync system files — these are managed by toasters, not the user.
	// New binary versions may add roles/, instructions/, or update agent prompts.
	if err := syncEmbeddedFS(systemFS, "system", systemDir); err != nil {
		return fmt.Errorf("syncing system files: %w", err)
	}

	// User files are only written on first run to avoid overwriting customizations.
	if isFirstRun {
		userDir := filepath.Join(configDir, "user")
		if UserFS != (embed.FS{}) {
			if err := copyEmbeddedFS(UserFS, "user", userDir); err != nil {
				return fmt.Errorf("copying default user files: %w", err)
			}
			slog.Info("Wrote default user files", "dir", userDir)
		} else {
			// No user files embedded — just create empty directories.
			for _, dir := range userDirs(configDir) {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return fmt.Errorf("creating %s: %w", dir, err)
				}
			}
		}
		slog.Info("Initialized toasters config", "dir", configDir)
	}

	return nil
}

// migrateDatabase moves the SQLite database from the old config-dir location
// (~/.config/toasters/toasters.db) to the workspace root (~/toasters/toasters.db)
// if the old file exists and the new one does not. This is a one-time migration
// so that operational state (jobs, tasks, sessions) lives alongside the workspace
// rather than in the config directory.
//
// The migration only runs when database_path is not explicitly set in config.yaml
// (i.e. the user is relying on the default location). If the user has set a custom
// database_path, we leave everything alone.
func migrateDatabase(configDir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil // can't determine paths — skip silently
	}

	// Check if the user has explicitly set database_path in config.yaml.
	// If so, they're managing the location themselves — don't migrate.
	configPath := filepath.Join(configDir, "config.yaml")
	if fileExists(configPath) {
		data, err := os.ReadFile(configPath)
		if err == nil {
			var raw map[string]interface{}
			if yaml.Unmarshal(data, &raw) == nil {
				if _, ok := raw["database_path"]; ok {
					return nil // user has explicit database_path — skip migration
				}
			}
		}
	}

	oldDB := filepath.Join(home, ".config", "toasters", "toasters.db")
	newDB := filepath.Join(home, "toasters", "toasters.db")

	if !fileExists(oldDB) {
		return nil // nothing to migrate
	}
	if fileExists(newDB) {
		return nil // new location already has a DB — don't overwrite
	}

	// Ensure the workspace directory exists.
	if err := os.MkdirAll(filepath.Join(home, "toasters"), 0o755); err != nil {
		return fmt.Errorf("creating workspace dir: %w", err)
	}

	// Move the database file.
	if err := os.Rename(oldDB, newDB); err != nil {
		return fmt.Errorf("moving database: %w", err)
	}

	// Also move WAL and SHM files if they exist (SQLite WAL mode).
	for _, suffix := range []string{"-wal", "-shm"} {
		oldAux := oldDB + suffix
		if fileExists(oldAux) {
			newAux := newDB + suffix
			if err := os.Rename(oldAux, newAux); err != nil {
				slog.Warn("failed to move database auxiliary file",
					"file", oldAux, "error", err)
			}
		}
	}

	slog.Info("Migrated database to workspace",
		"from", oldDB, "to", newDB)
	return nil
}

// providerIDMigration migrates old-format config.yaml files to the new provider
// ID format. It is idempotent — the detection step prevents re-migration.
//
// Old format indicators:
//   - providers with name but no id
//   - operator with endpoint or api_key keys
//   - agents with default_provider or default_model keys
//
// The migration generates an id for each provider by slugifying its name,
// updates operator.provider to use the new ID, and moves agents default
// settings into the nested defaults map.
func providerIDMigration(configDir string) error {
	configPath := filepath.Join(configDir, "config.yaml")
	if !fileExists(configPath) {
		return nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		slog.Warn("provider ID migration: failed to parse config.yaml, skipping", "error", err)
		return nil
	}

	// We work with the root mapping node.
	var rootMap *yaml.Node
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		rootMap = root.Content[0]
	}
	if rootMap == nil || rootMap.Kind != yaml.MappingNode {
		return nil
	}

	// Detect old format and determine if migration is needed.
	needsMigration := false

	// Build name-to-id mapping from providers.
	nameToID := make(map[string]string)

	providersNode := mappingValue(rootMap, "providers")
	if providersNode != nil && providersNode.Kind == yaml.SequenceNode {
		for _, provNode := range providersNode.Content {
			if provNode.Kind != yaml.MappingNode {
				continue
			}
			nameVal := mappingValue(provNode, "name")
			idVal := mappingValue(provNode, "id")

			if nameVal != nil && nameVal.Value != "" && (idVal == nil || idVal.Value == "") {
				needsMigration = true
				newID := slugify(nameVal.Value)
				nameToID[nameVal.Value] = newID
			} else if nameVal != nil && nameVal.Value != "" && idVal != nil && idVal.Value != "" {
				nameToID[nameVal.Value] = idVal.Value
			}
		}

		// Apply: add id to each provider that lacks one.
		for _, provNode := range providersNode.Content {
			if provNode.Kind != yaml.MappingNode {
				continue
			}
			idVal := mappingValue(provNode, "id")
			nameVal := mappingValue(provNode, "name")
			if (idVal == nil || idVal.Value == "") && nameVal != nil && nameVal.Value != "" {
				newID := slugify(nameVal.Value)
				setMappingValue(provNode, "id", newID)
			}
		}
	}

	// Check operator for old keys.
	operatorNode := mappingValue(rootMap, "operator")
	if operatorNode != nil && operatorNode.Kind == yaml.MappingNode {
		if hasKey(operatorNode, "endpoint") || hasKey(operatorNode, "api_key") {
			needsMigration = true
			removeMappingKey(operatorNode, "endpoint")
			removeMappingKey(operatorNode, "api_key")
		}
		// Update provider value from old name to new ID.
		providerVal := mappingValue(operatorNode, "provider")
		if providerVal != nil && providerVal.Value != "" {
			if newID, ok := nameToID[providerVal.Value]; ok {
				providerVal.Value = newID
			}
		}
	}

	// Check agents for old keys.
	agentsNode := mappingValue(rootMap, "agents")
	if agentsNode != nil && agentsNode.Kind == yaml.MappingNode {
		dpVal := mappingValue(agentsNode, "default_provider")
		dmVal := mappingValue(agentsNode, "default_model")

		if dpVal != nil || dmVal != nil {
			needsMigration = true

			// Ensure a "defaults" mapping exists.
			defaultsNode := mappingValue(agentsNode, "defaults")
			if defaultsNode == nil {
				defaultsNode = &yaml.Node{Kind: yaml.MappingNode}
				agentsNode.Content = append(agentsNode.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Value: "defaults"},
					defaultsNode,
				)
			}

			if dpVal != nil {
				providerID := dpVal.Value
				// Resolve old name to new ID if applicable.
				if newID, ok := nameToID[providerID]; ok {
					providerID = newID
				}
				setMappingValue(defaultsNode, "provider", providerID)
				removeMappingKey(agentsNode, "default_provider")
			}
			if dmVal != nil {
				setMappingValue(defaultsNode, "model", dmVal.Value)
				removeMappingKey(agentsNode, "default_model")
			}
		}
	}

	if !needsMigration {
		return nil
	}

	// Backup original config.
	backupPath := configPath + ".pre-provider-id-migration"
	if err := os.WriteFile(backupPath, data, 0o600); err != nil {
		return fmt.Errorf("backing up config: %w", err)
	}

	// Write migrated config.
	out, err := yaml.Marshal(&root)
	if err != nil {
		return fmt.Errorf("marshaling migrated config: %w", err)
	}
	if err := os.WriteFile(configPath, out, 0o600); err != nil {
		return fmt.Errorf("writing migrated config: %w", err)
	}

	slog.Info("Migrated config.yaml to provider ID format", "backup", backupPath)
	return nil
}

var (
	reNonAlnum  = regexp.MustCompile(`[^a-z0-9-]`)
	reMultiDash = regexp.MustCompile(`-+`)
)

// slugify converts a name to a slug: lowercase, spaces to hyphens, strip
// non-alphanumeric characters (except hyphens).
// e.g. "LM Studio" → "lm-studio", "Anthropic" → "anthropic".
func slugify(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, " ", "-")
	s = reNonAlnum.ReplaceAllString(s, "")
	s = reMultiDash.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "provider"
	}
	return s
}

// mappingValue returns the value node for a given key in a mapping node, or nil.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// setMappingValue sets or adds a key-value pair in a mapping node.
func setMappingValue(node *yaml.Node, key, value string) {
	// Update existing key if present.
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1].Value = value
			return
		}
	}
	// Append new key-value pair.
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value},
	)
}

// hasKey reports whether a mapping node contains a given key.
func hasKey(node *yaml.Node, key string) bool {
	if node.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return true
		}
	}
	return false
}

// removeMappingKey removes a key-value pair from a mapping node.
func removeMappingKey(node *yaml.Node, key string) {
	if node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}

// ensureDirectories creates all required directories if they don't already exist.
func ensureDirectories(configDir string) error {
	dirs := append(
		[]string{
			filepath.Join(configDir, "system"),
			filepath.Join(configDir, "providers"),
		},
		userDirs(configDir)...,
	)
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	return nil
}

// userDirs returns the paths for the user directory structure.
func userDirs(configDir string) []string {
	return []string{
		filepath.Join(configDir, "user", "skills"),
		filepath.Join(configDir, "user", "graphs"),
		filepath.Join(configDir, "user", "roles"),
		filepath.Join(configDir, "user", "toolchains"),
		filepath.Join(configDir, "user", "instructions"),
	}
}

// syncEmbeddedFS ensures all files from an embedded filesystem subtree exist
// on disk. Directories are always created; files are only written when they
// don't already exist. This lets binary upgrades deploy new definitions (e.g.
// system/roles/) without overwriting files the user may have customized.
func syncEmbeddedFS(fsys embed.FS, root, destDir string) error {
	return fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("computing relative path: %w", err)
		}
		target := filepath.Join(destDir, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		// Skip files that already exist — don't overwrite user customizations.
		if fileExists(target) {
			return nil
		}

		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return fmt.Errorf("reading embedded %s: %w", path, err)
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// copyEmbeddedFS copies all files from an embedded filesystem subtree to a
// destination directory on disk.
func copyEmbeddedFS(fsys embed.FS, root, destDir string) error {
	return fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Compute the relative path from the root and the target on disk.
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("computing relative path: %w", err)
		}
		target := filepath.Join(destDir, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return fmt.Errorf("reading embedded %s: %w", path, err)
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// dirExists reports whether path exists and is a directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// fileExists reports whether path exists and is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
