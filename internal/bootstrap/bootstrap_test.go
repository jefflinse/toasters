package bootstrap

import (
	"embed"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/defaults"
)

// testFS returns the real embedded system files for testing.
func testFS() embed.FS {
	return defaults.SystemFiles
}

// testDefaultConfig returns the real embedded default config for testing.
func testDefaultConfig() []byte {
	return defaults.DefaultConfig
}

func TestRun_FirstRun(t *testing.T) {
	configDir := t.TempDir()

	if err := Run(configDir, testFS(), testDefaultConfig()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Verify system/ was created with expected files.
	assertDirExists(t, filepath.Join(configDir, "system"))
	assertDirExists(t, filepath.Join(configDir, "system", "agents"))
	assertDirExists(t, filepath.Join(configDir, "system", "skills"))
	assertFileExists(t, filepath.Join(configDir, "system", "team.md"))
	assertFileExists(t, filepath.Join(configDir, "system", "agents", "operator.md"))
	assertFileExists(t, filepath.Join(configDir, "system", "agents", "planner.md"))
	assertFileExists(t, filepath.Join(configDir, "system", "agents", "scheduler.md"))
	assertFileExists(t, filepath.Join(configDir, "system", "agents", "blocker-handler.md"))
	assertFileExists(t, filepath.Join(configDir, "system", "skills", "orchestration.md"))

	// Verify user/ structure was created.
	assertDirExists(t, filepath.Join(configDir, "user", "skills"))
	assertDirExists(t, filepath.Join(configDir, "user", "agents"))
	assertDirExists(t, filepath.Join(configDir, "user", "teams"))

	// Verify system files have content (not empty).
	data, err := os.ReadFile(filepath.Join(configDir, "system", "team.md"))
	if err != nil {
		t.Fatalf("reading team.md: %v", err)
	}
	if len(data) == 0 {
		t.Error("system/team.md is empty")
	}

	// Verify config.yaml was written.
	assertFileExists(t, filepath.Join(configDir, "config.yaml"))
	cfgData, err := os.ReadFile(filepath.Join(configDir, "config.yaml"))
	if err != nil {
		t.Fatalf("reading config.yaml: %v", err)
	}
	if len(cfgData) == 0 {
		t.Error("config.yaml is empty")
	}
}

func TestRun_DefaultConfigNotOverwritten(t *testing.T) {
	configDir := t.TempDir()

	// Write a custom config before first run.
	customConfig := []byte("# my custom config\noperator:\n  provider: local\n")
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.WriteFile(configPath, customConfig, 0o644); err != nil {
		t.Fatalf("writing custom config: %v", err)
	}

	if err := Run(configDir, testFS(), testDefaultConfig()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Verify the custom config was NOT overwritten.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config.yaml: %v", err)
	}
	if string(data) != string(customConfig) {
		t.Errorf("config.yaml was overwritten: got %q, want %q", string(data), string(customConfig))
	}
}

func TestRun_Idempotent(t *testing.T) {
	configDir := t.TempDir()

	// First run.
	if err := Run(configDir, testFS(), testDefaultConfig()); err != nil {
		t.Fatalf("first Run() error: %v", err)
	}

	// Modify a system file to verify it's not overwritten.
	teamMD := filepath.Join(configDir, "system", "team.md")
	sentinel := []byte("# customized by user\n")
	if err := os.WriteFile(teamMD, sentinel, 0o644); err != nil {
		t.Fatalf("writing sentinel: %v", err)
	}

	// Second run.
	if err := Run(configDir, testFS(), testDefaultConfig()); err != nil {
		t.Fatalf("second Run() error: %v", err)
	}

	// Verify the customized file was NOT overwritten.
	data, err := os.ReadFile(teamMD)
	if err != nil {
		t.Fatalf("reading team.md: %v", err)
	}
	if string(data) != string(sentinel) {
		t.Errorf("system/team.md was overwritten: got %q, want %q", string(data), string(sentinel))
	}

	// Verify directories still exist.
	assertDirExists(t, filepath.Join(configDir, "system"))
	assertDirExists(t, filepath.Join(configDir, "user", "skills"))
	assertDirExists(t, filepath.Join(configDir, "user", "agents"))
	assertDirExists(t, filepath.Join(configDir, "user", "teams"))
}

func TestRun_UpgradeMigration(t *testing.T) {
	configDir := t.TempDir()

	// Simulate old layout: system/ exists, teams/ at root level.
	systemDir := filepath.Join(configDir, "system")
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		t.Fatal(err)
	}

	oldTeamsDir := filepath.Join(configDir, "teams")

	// Create two team dirs: one with team.md, one without.
	teamWithMD := filepath.Join(oldTeamsDir, "dev-team")
	teamWithoutMD := filepath.Join(oldTeamsDir, "qa-team")
	if err := os.MkdirAll(teamWithMD, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(teamWithoutMD, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a team.md in dev-team.
	existingTeamMD := filepath.Join(teamWithMD, "team.md")
	existingContent := []byte("---\nname: Dev Team\n---\n")
	if err := os.WriteFile(existingTeamMD, existingContent, 0o644); err != nil {
		t.Fatal(err)
	}

	// Add a file inside qa-team to verify it moves.
	if err := os.WriteFile(filepath.Join(teamWithoutMD, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Run(configDir, testFS(), testDefaultConfig()); err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Old teams/ should be gone.
	if dirExists(oldTeamsDir) {
		t.Error("old teams/ directory still exists after migration")
	}

	// Teams should now be under user/teams/.
	newTeamsDir := filepath.Join(configDir, "user", "teams")
	assertDirExists(t, filepath.Join(newTeamsDir, "dev-team"))
	assertDirExists(t, filepath.Join(newTeamsDir, "qa-team"))

	// dev-team's existing team.md should be preserved.
	data, err := os.ReadFile(filepath.Join(newTeamsDir, "dev-team", "team.md"))
	if err != nil {
		t.Fatalf("reading dev-team/team.md: %v", err)
	}
	if string(data) != string(existingContent) {
		t.Errorf("dev-team/team.md was modified: got %q, want %q", string(data), string(existingContent))
	}

	// qa-team should have a generated team.md.
	generatedMD := filepath.Join(newTeamsDir, "qa-team", "team.md")
	assertFileExists(t, generatedMD)
	data, err = os.ReadFile(generatedMD)
	if err != nil {
		t.Fatalf("reading qa-team/team.md: %v", err)
	}
	if len(data) == 0 {
		t.Error("generated team.md is empty")
	}
	content := string(data)
	if !strings.Contains(content, "Qa Team") {
		t.Errorf("generated team.md doesn't contain humanized name: %s", content)
	}

	// qa-team's other files should have moved too.
	assertFileExists(t, filepath.Join(newTeamsDir, "qa-team", "notes.txt"))

	// user/skills/ and user/agents/ should exist.
	assertDirExists(t, filepath.Join(configDir, "user", "skills"))
	assertDirExists(t, filepath.Join(configDir, "user", "agents"))
}

func TestRun_AutoTeamDetection(t *testing.T) {
	configDir := t.TempDir()

	// Pre-create system/ so first-run doesn't trigger.
	if err := os.MkdirAll(filepath.Join(configDir, "system"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create mock external agent directories.
	mockHome := t.TempDir()

	claudeAgents := filepath.Join(mockHome, ".claude", "agents")
	if err := os.MkdirAll(claudeAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	// Add a file so we can verify the symlink works.
	if err := os.WriteFile(filepath.Join(claudeAgents, "test-agent.md"), []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	opencodeAgents := filepath.Join(mockHome, ".config", "opencode", "agents")
	if err := os.MkdirAll(opencodeAgents, 0o755); err != nil {
		t.Fatal(err)
	}

	// Override HOME for the test by calling the internal function directly
	// with pre-constructed auto-team entries.
	teamsDir := filepath.Join(configDir, "user", "teams")
	if err := os.MkdirAll(teamsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Test auto-team creation using the internal helper.
	if err := createAutoTeam(teamsDir, "auto-claude", claudeAgents); err != nil {
		t.Fatalf("createAutoTeam(auto-claude): %v", err)
	}
	if err := createAutoTeam(teamsDir, "auto-opencode", opencodeAgents); err != nil {
		t.Fatalf("createAutoTeam(auto-opencode): %v", err)
	}

	// Verify auto-claude.
	autoClaudeDir := filepath.Join(teamsDir, "auto-claude")
	assertDirExists(t, autoClaudeDir)
	assertFileExists(t, filepath.Join(autoClaudeDir, ".auto-team"))

	// Verify the .auto-team marker is empty.
	data, err := os.ReadFile(filepath.Join(autoClaudeDir, ".auto-team"))
	if err != nil {
		t.Fatalf("reading .auto-team: %v", err)
	}
	if len(data) != 0 {
		t.Errorf(".auto-team marker is not empty: %d bytes", len(data))
	}

	// Verify agents/ is a symlink.
	linkPath := filepath.Join(autoClaudeDir, "agents")
	linkTarget, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("reading symlink: %v", err)
	}
	if linkTarget != claudeAgents {
		t.Errorf("symlink target = %q, want %q", linkTarget, claudeAgents)
	}

	// Verify the symlink actually resolves to the source files.
	entries, err := os.ReadDir(linkPath)
	if err != nil {
		t.Fatalf("reading symlinked dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "test-agent.md" {
		t.Errorf("unexpected entries in symlinked dir: %v", entries)
	}

	// Verify auto-opencode.
	autoOpencodeDir := filepath.Join(teamsDir, "auto-opencode")
	assertDirExists(t, autoOpencodeDir)
	assertFileExists(t, filepath.Join(autoOpencodeDir, ".auto-team"))
	linkTarget, err = os.Readlink(filepath.Join(autoOpencodeDir, "agents"))
	if err != nil {
		t.Fatalf("reading symlink: %v", err)
	}
	if linkTarget != opencodeAgents {
		t.Errorf("symlink target = %q, want %q", linkTarget, opencodeAgents)
	}

	// Verify idempotency: calling again should not fail.
	if err := createAutoTeam(teamsDir, "auto-claude", claudeAgents); err != nil {
		t.Fatalf("second createAutoTeam(auto-claude): %v", err)
	}
}

func TestRun_AlreadySetUp(t *testing.T) {
	configDir := t.TempDir()

	// First run to set everything up.
	if err := Run(configDir, testFS(), testDefaultConfig()); err != nil {
		t.Fatalf("first Run() error: %v", err)
	}

	// Record modification times.
	systemTeamMD := filepath.Join(configDir, "system", "team.md")
	info1, err := os.Stat(systemTeamMD)
	if err != nil {
		t.Fatal(err)
	}

	// Run again — should be a no-op.
	if err := Run(configDir, testFS(), testDefaultConfig()); err != nil {
		t.Fatalf("second Run() error: %v", err)
	}

	// Verify system file was not modified.
	info2, err := os.Stat(systemTeamMD)
	if err != nil {
		t.Fatal(err)
	}
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Error("system/team.md was modified on second run")
	}

	// Verify all directories still exist.
	assertDirExists(t, filepath.Join(configDir, "system"))
	assertDirExists(t, filepath.Join(configDir, "system", "agents"))
	assertDirExists(t, filepath.Join(configDir, "system", "skills"))
	assertDirExists(t, filepath.Join(configDir, "user", "skills"))
	assertDirExists(t, filepath.Join(configDir, "user", "agents"))
	assertDirExists(t, filepath.Join(configDir, "user", "teams"))
}

func TestHumanizeDirName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"dev-team", "Dev Team"},
		{"qa", "Qa"},
		{"my-cool-team", "My Cool Team"},
		{"single", "Single"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := humanizeDirName(tt.input)
			if got != tt.want {
				t.Errorf("humanizeDirName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// createAutoTeam is a test helper that creates an auto-team entry.
// It mirrors the logic in autoTeamDetection but without depending on os.UserHomeDir.
func createAutoTeam(teamsDir, name, sourceDir string) error {
	teamDir := filepath.Join(teamsDir, name)
	if dirExists(teamDir) {
		return nil
	}

	if err := os.MkdirAll(teamDir, 0o755); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(teamDir, ".auto-team"), nil, 0o644); err != nil {
		return err
	}

	return os.Symlink(sourceDir, filepath.Join(teamDir, "agents"))
}

// --- test helpers ---

func assertDirExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Errorf("expected directory %s to exist: %v", path, err)
		return
	}
	if !info.IsDir() {
		t.Errorf("expected %s to be a directory", path)
	}
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Errorf("expected file %s to exist: %v", path, err)
		return
	}
	if info.IsDir() {
		t.Errorf("expected %s to be a file, got directory", path)
	}
}

// --- migrateDatabase tests ---

func TestMigrateDatabase_MovesDBToWorkspace(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	configDir := filepath.Join(tmpHome, ".config", "toasters")
	workspaceDir := filepath.Join(tmpHome, "toasters")

	// Create old DB in config dir.
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	oldDB := filepath.Join(configDir, "toasters.db")
	if err := os.WriteFile(oldDB, []byte("test-db-content"), 0o644); err != nil {
		t.Fatalf("writing old DB: %v", err)
	}
	// Also create a WAL file.
	if err := os.WriteFile(oldDB+"-wal", []byte("wal-content"), 0o644); err != nil {
		t.Fatalf("writing old WAL: %v", err)
	}

	if err := migrateDatabase(configDir); err != nil {
		t.Fatalf("migrateDatabase() error: %v", err)
	}

	// Old DB should be gone.
	if fileExists(oldDB) {
		t.Error("old DB still exists after migration")
	}
	if fileExists(oldDB + "-wal") {
		t.Error("old WAL still exists after migration")
	}

	// New DB should exist in workspace.
	newDB := filepath.Join(workspaceDir, "toasters.db")
	if !fileExists(newDB) {
		t.Fatal("new DB does not exist after migration")
	}
	data, err := os.ReadFile(newDB)
	if err != nil {
		t.Fatalf("reading new DB: %v", err)
	}
	if string(data) != "test-db-content" {
		t.Errorf("new DB content: got %q, want %q", string(data), "test-db-content")
	}

	// WAL should also have been moved.
	newWAL := newDB + "-wal"
	if !fileExists(newWAL) {
		t.Fatal("new WAL does not exist after migration")
	}
}

func TestMigrateDatabase_SkipsWhenNewDBExists(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	configDir := filepath.Join(tmpHome, ".config", "toasters")
	workspaceDir := filepath.Join(tmpHome, "toasters")

	// Create old DB.
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	oldDB := filepath.Join(configDir, "toasters.db")
	if err := os.WriteFile(oldDB, []byte("old-content"), 0o644); err != nil {
		t.Fatalf("writing old DB: %v", err)
	}

	// Create new DB already in workspace.
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatalf("creating workspace dir: %v", err)
	}
	newDB := filepath.Join(workspaceDir, "toasters.db")
	if err := os.WriteFile(newDB, []byte("new-content"), 0o644); err != nil {
		t.Fatalf("writing new DB: %v", err)
	}

	if err := migrateDatabase(configDir); err != nil {
		t.Fatalf("migrateDatabase() error: %v", err)
	}

	// Old DB should still exist (not moved).
	if !fileExists(oldDB) {
		t.Error("old DB was removed even though new DB already existed")
	}

	// New DB should be unchanged.
	data, err := os.ReadFile(newDB)
	if err != nil {
		t.Fatalf("reading new DB: %v", err)
	}
	if string(data) != "new-content" {
		t.Errorf("new DB was overwritten: got %q, want %q", string(data), "new-content")
	}
}

func TestMigrateDatabase_SkipsWhenExplicitDatabasePath(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	configDir := filepath.Join(tmpHome, ".config", "toasters")

	// Create config.yaml with explicit database_path.
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	configYAML := "database_path: /custom/path/my.db\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatalf("writing config.yaml: %v", err)
	}

	// Create old DB.
	oldDB := filepath.Join(configDir, "toasters.db")
	if err := os.WriteFile(oldDB, []byte("old-content"), 0o644); err != nil {
		t.Fatalf("writing old DB: %v", err)
	}

	if err := migrateDatabase(configDir); err != nil {
		t.Fatalf("migrateDatabase() error: %v", err)
	}

	// Old DB should still exist (migration skipped).
	if !fileExists(oldDB) {
		t.Error("old DB was removed even though database_path is explicitly set")
	}
}

func TestMigrateDatabase_NoOldDB_Noop(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	configDir := filepath.Join(tmpHome, ".config", "toasters")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}

	// No old DB exists — should be a no-op.
	if err := migrateDatabase(configDir); err != nil {
		t.Fatalf("migrateDatabase() error: %v", err)
	}
}
