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
func Run(configDir string, systemFS embed.FS, defaultConfig []byte) error {
	if err := firstRun(configDir, systemFS, defaultConfig); err != nil {
		return fmt.Errorf("first-run bootstrap: %w", err)
	}

	if err := upgradeMigration(configDir); err != nil {
		return fmt.Errorf("upgrade migration: %w", err)
	}

	if err := autoTeamDetection(configDir); err != nil {
		return fmt.Errorf("auto-team detection: %w", err)
	}

	if err := migrateDatabase(configDir); err != nil {
		return fmt.Errorf("database migration: %w", err)
	}

	if err := ensureDirectories(configDir); err != nil {
		return fmt.Errorf("ensuring directories: %w", err)
	}

	return nil
}

// firstRun copies embedded system files and creates the user directory structure
// when the system/ directory doesn't exist yet. If defaultConfig is non-nil and
// config.yaml does not already exist, it is written as the initial configuration.
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

	systemDir := filepath.Join(configDir, "system")
	if dirExists(systemDir) {
		return nil
	}

	// Copy all files from the embedded FS to configDir/system/.
	if err := copyEmbeddedFS(systemFS, "system", systemDir); err != nil {
		return fmt.Errorf("copying system files: %w", err)
	}

	// Create empty user directory structure.
	for _, dir := range userDirs(configDir) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}

	slog.Info("Initialized toasters config", "dir", configDir)
	return nil
}

// upgradeMigration moves the old top-level teams/ directory to user/teams/
// and generates basic team.md files where missing.
func upgradeMigration(configDir string) error {
	oldTeamsDir := filepath.Join(configDir, "teams")
	newTeamsDir := filepath.Join(configDir, "user", "teams")

	// Only migrate if the old teams/ dir exists and user/teams/ does not.
	if !dirExists(oldTeamsDir) {
		return nil
	}
	if dirExists(newTeamsDir) {
		// Both exist — ambiguous state. Don't touch either.
		return nil
	}

	// Ensure user/ parent directories exist (but NOT user/teams/ — the rename
	// will create that).
	for _, dir := range []string{
		filepath.Join(configDir, "user", "skills"),
		filepath.Join(configDir, "user", "agents"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}

	// Move teams/ → user/teams/. The user/ parent was created above.
	if err := os.Rename(oldTeamsDir, newTeamsDir); err != nil {
		return fmt.Errorf("moving teams to user/teams: %w", err)
	}

	// Generate basic team.md for any team directory that lacks one.
	entries, err := os.ReadDir(newTeamsDir)
	if err != nil {
		return fmt.Errorf("reading user/teams: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		teamMD := filepath.Join(newTeamsDir, entry.Name(), "team.md")
		if fileExists(teamMD) {
			continue
		}
		if err := generateBasicTeamMD(teamMD, entry.Name()); err != nil {
			return fmt.Errorf("generating team.md for %s: %w", entry.Name(), err)
		}
	}

	slog.Info("Migrated teams to new layout", "from", oldTeamsDir, "to", newTeamsDir)
	return nil
}

// autoTeamDetection checks for agent directories from other tools and creates
// symlinked auto-team entries under user/teams/.
func autoTeamDetection(configDir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}

	autoTeams := []struct {
		name      string
		sourceDir string
	}{
		{"auto-claude", filepath.Join(home, ".claude", "agents")},
		{"auto-opencode", filepath.Join(home, ".config", "opencode", "agents")},
	}

	teamsDir := filepath.Join(configDir, "user", "teams")

	for _, at := range autoTeams {
		if !dirExists(at.sourceDir) {
			continue
		}

		teamDir := filepath.Join(teamsDir, at.name)
		if dirExists(teamDir) {
			// Already set up — skip for idempotency.
			continue
		}

		if err := os.MkdirAll(teamDir, 0o755); err != nil {
			return fmt.Errorf("creating auto-team dir %s: %w", at.name, err)
		}

		// Create .auto-team marker file.
		markerPath := filepath.Join(teamDir, ".auto-team")
		if err := os.WriteFile(markerPath, nil, 0o644); err != nil {
			return fmt.Errorf("creating .auto-team marker for %s: %w", at.name, err)
		}

		// Create agents/ symlink pointing to the source directory.
		linkPath := filepath.Join(teamDir, "agents")
		if err := os.Symlink(at.sourceDir, linkPath); err != nil {
			return fmt.Errorf("creating agents symlink for %s: %w", at.name, err)
		}

		slog.Info("Detected auto-team", "name", at.name, "source", at.sourceDir)
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

// ensureDirectories creates all required directories if they don't already exist.
func ensureDirectories(configDir string) error {
	dirs := append(
		[]string{filepath.Join(configDir, "system")},
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
		filepath.Join(configDir, "user", "agents"),
		filepath.Join(configDir, "user", "teams"),
	}
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

// generateBasicTeamMD writes a minimal team.md file using the directory name
// as the team name.
func generateBasicTeamMD(path, dirName string) error {
	name := humanizeDirName(dirName)
	// Use yaml.Marshal for safe YAML encoding of the name.
	type teamFrontmatter struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	fm := teamFrontmatter{
		Name:        name,
		Description: fmt.Sprintf("Team %s (migrated from legacy layout)", name),
	}
	data, err := yaml.Marshal(&fm)
	if err != nil {
		return fmt.Errorf("marshaling team frontmatter: %w", err)
	}
	content := fmt.Sprintf("---\n%s---\n", string(data))
	return os.WriteFile(path, []byte(content), 0o644)
}

// abbreviations maps lowercase words to their preferred casing for common
// abbreviations that should not be simple title-cased.
var abbreviations = map[string]string{
	"qa":     "QA",
	"ci":     "CI",
	"cd":     "CD",
	"api":    "API",
	"ui":     "UI",
	"ux":     "UX",
	"db":     "DB",
	"ml":     "ML",
	"ai":     "AI",
	"sre":    "SRE",
	"devops": "DevOps",
}

// humanizeDirName converts a kebab-case directory name to a human-readable name.
// Common abbreviations (QA, CI, API, etc.) are uppercased; other words are title-cased.
// e.g. "dev-team" → "Dev Team", "qa" → "QA", "api-gateway" → "API Gateway".
func humanizeDirName(name string) string {
	parts := strings.Split(name, "-")
	for i, p := range parts {
		if len(p) == 0 {
			continue
		}
		if replacement, ok := abbreviations[strings.ToLower(p)]; ok {
			parts[i] = replacement
		} else {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
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
