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
func Run(configDir string, systemFS embed.FS) error {
	if err := firstRun(configDir, systemFS); err != nil {
		return fmt.Errorf("first-run bootstrap: %w", err)
	}

	if err := upgradeMigration(configDir); err != nil {
		return fmt.Errorf("upgrade migration: %w", err)
	}

	if err := autoTeamDetection(configDir); err != nil {
		return fmt.Errorf("auto-team detection: %w", err)
	}

	if err := ensureDirectories(configDir); err != nil {
		return fmt.Errorf("ensuring directories: %w", err)
	}

	return nil
}

// firstRun copies embedded system files and creates the user directory structure
// when the system/ directory doesn't exist yet.
func firstRun(configDir string, systemFS embed.FS) error {
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

// humanizeDirName converts a kebab-case directory name to a title-cased name.
// e.g. "dev-team" → "Dev Team", "qa" → "Qa".
func humanizeDirName(name string) string {
	parts := strings.Split(name, "-")
	for i, p := range parts {
		if len(p) > 0 {
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
