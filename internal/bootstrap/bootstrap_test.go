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
	assertDirExists(t, filepath.Join(configDir, "system", "roles"))
	assertDirExists(t, filepath.Join(configDir, "system", "skills"))
	assertFileExists(t, filepath.Join(configDir, "system", "roles", "operator.md"))
	assertFileExists(t, filepath.Join(configDir, "system", "roles", "coarse-decomposer.md"))
	assertFileExists(t, filepath.Join(configDir, "system", "roles", "fine-decomposer.md"))
	assertFileExists(t, filepath.Join(configDir, "system", "roles", "scheduler.md"))
	assertFileExists(t, filepath.Join(configDir, "system", "roles", "blocker-handler.md"))
	assertFileExists(t, filepath.Join(configDir, "system", "skills", "orchestration.md"))
	assertFileExists(t, filepath.Join(configDir, "system", "schemas", "decomposition-result.yaml"))
	assertFileExists(t, filepath.Join(configDir, "system", "graphs", "coarse-decompose.yaml"))
	assertFileExists(t, filepath.Join(configDir, "system", "graphs", "fine-decompose.yaml"))

	// Verify user/ structure was created.
	assertDirExists(t, filepath.Join(configDir, "user", "skills"))
	assertDirExists(t, filepath.Join(configDir, "user", "graphs"))

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
	operatorMD := filepath.Join(configDir, "system", "roles", "operator.md")
	sentinel := []byte("# customized by user\n")
	if err := os.WriteFile(operatorMD, sentinel, 0o644); err != nil {
		t.Fatalf("writing sentinel: %v", err)
	}

	// Second run.
	if err := Run(configDir, testFS(), testDefaultConfig()); err != nil {
		t.Fatalf("second Run() error: %v", err)
	}

	// Verify the customized file was NOT overwritten.
	data, err := os.ReadFile(operatorMD)
	if err != nil {
		t.Fatalf("reading operator.md: %v", err)
	}
	if string(data) != string(sentinel) {
		t.Errorf("system/roles/operator.md was overwritten: got %q, want %q", string(data), string(sentinel))
	}

	// Verify directories still exist.
	assertDirExists(t, filepath.Join(configDir, "system"))
	assertDirExists(t, filepath.Join(configDir, "user", "skills"))
	assertDirExists(t, filepath.Join(configDir, "user", "graphs"))
}

func TestRun_AlreadySetUp(t *testing.T) {
	configDir := t.TempDir()

	// First run to set everything up.
	if err := Run(configDir, testFS(), testDefaultConfig()); err != nil {
		t.Fatalf("first Run() error: %v", err)
	}

	// Record modification times.
	operatorMD := filepath.Join(configDir, "system", "roles", "operator.md")
	info1, err := os.Stat(operatorMD)
	if err != nil {
		t.Fatal(err)
	}

	// Run again — should be a no-op.
	if err := Run(configDir, testFS(), testDefaultConfig()); err != nil {
		t.Fatalf("second Run() error: %v", err)
	}

	// Verify system file was not modified.
	info2, err := os.Stat(operatorMD)
	if err != nil {
		t.Fatal(err)
	}
	if !info1.ModTime().Equal(info2.ModTime()) {
		t.Error("system/roles/operator.md was modified on second run")
	}

	// Verify all directories still exist.
	assertDirExists(t, filepath.Join(configDir, "system"))
	assertDirExists(t, filepath.Join(configDir, "system", "roles"))
	assertDirExists(t, filepath.Join(configDir, "system", "skills"))
	assertDirExists(t, filepath.Join(configDir, "user", "skills"))
	assertDirExists(t, filepath.Join(configDir, "user", "graphs"))
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

// --- providerIDMigration tests ---

// writeConfigYAML writes a config.yaml file into dir with the given content.
func writeConfigYAML(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0644); err != nil {
		t.Fatalf("writing config.yaml: %v", err)
	}
}

func TestProviderIDMigration_AddsIDToProviders(t *testing.T) {
	configDir := t.TempDir()

	writeConfigYAML(t, configDir, `
providers:
  - name: LM Studio
    type: local
    model: my-model
  - name: Anthropic
    type: anthropic
    api_key: sk-test
`)

	if err := providerIDMigration(configDir); err != nil {
		t.Fatalf("providerIDMigration() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(configDir, "config.yaml"))
	if err != nil {
		t.Fatalf("reading config.yaml: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "id: lm-studio") {
		t.Errorf("expected id 'lm-studio' in config, got:\n%s", content)
	}
	if !strings.Contains(content, "id: anthropic") {
		t.Errorf("expected id 'anthropic' in config, got:\n%s", content)
	}
}

func TestProviderIDMigration_RemovesOperatorEndpointAndAPIKey(t *testing.T) {
	configDir := t.TempDir()

	writeConfigYAML(t, configDir, `
operator:
  provider: local
  endpoint: http://localhost:9999
  api_key: sk-secret
  model: test-model
`)

	if err := providerIDMigration(configDir); err != nil {
		t.Fatalf("providerIDMigration() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(configDir, "config.yaml"))
	if err != nil {
		t.Fatalf("reading config.yaml: %v", err)
	}

	content := string(data)
	if strings.Contains(content, "endpoint:") {
		t.Errorf("expected 'endpoint' to be removed from operator, got:\n%s", content)
	}
	if strings.Contains(content, "api_key:") {
		t.Errorf("expected 'api_key' to be removed from operator, got:\n%s", content)
	}
	// model and provider should still be present.
	if !strings.Contains(content, "provider:") {
		t.Errorf("expected 'provider' to remain in operator, got:\n%s", content)
	}
	if !strings.Contains(content, "model:") {
		t.Errorf("expected 'model' to remain in operator, got:\n%s", content)
	}
}

func TestProviderIDMigration_MovesAgentsDefaults(t *testing.T) {
	configDir := t.TempDir()

	writeConfigYAML(t, configDir, `
agents:
  default_provider: lm-studio
  default_model: gpt-4
`)

	if err := providerIDMigration(configDir); err != nil {
		t.Fatalf("providerIDMigration() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(configDir, "config.yaml"))
	if err != nil {
		t.Fatalf("reading config.yaml: %v", err)
	}

	content := string(data)
	if strings.Contains(content, "default_provider:") {
		t.Errorf("expected 'default_provider' to be removed, got:\n%s", content)
	}
	if strings.Contains(content, "default_model:") {
		t.Errorf("expected 'default_model' to be removed, got:\n%s", content)
	}
	if !strings.Contains(content, "defaults:") {
		t.Errorf("expected 'defaults' key in agents, got:\n%s", content)
	}
	if !strings.Contains(content, "provider: lm-studio") {
		t.Errorf("expected 'provider: lm-studio' in defaults, got:\n%s", content)
	}
	if !strings.Contains(content, "model: gpt-4") {
		t.Errorf("expected 'model: gpt-4' in defaults, got:\n%s", content)
	}
}

func TestProviderIDMigration_UpdatesOperatorProviderRef(t *testing.T) {
	configDir := t.TempDir()

	writeConfigYAML(t, configDir, `
providers:
  - name: Local
    type: local
operator:
  provider: Local
`)

	if err := providerIDMigration(configDir); err != nil {
		t.Fatalf("providerIDMigration() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(configDir, "config.yaml"))
	if err != nil {
		t.Fatalf("reading config.yaml: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "provider: local") {
		t.Errorf("expected operator provider to be updated to slugified ID 'local', got:\n%s", content)
	}
}

func TestProviderIDMigration_Idempotent(t *testing.T) {
	configDir := t.TempDir()

	writeConfigYAML(t, configDir, `
providers:
  - name: LM Studio
    type: local
operator:
  provider: LM Studio
  endpoint: http://localhost:1234
agents:
  default_provider: LM Studio
  default_model: test-model
`)

	// First migration.
	if err := providerIDMigration(configDir); err != nil {
		t.Fatalf("first providerIDMigration() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(configDir, "config.yaml"))
	if err != nil {
		t.Fatalf("reading config.yaml after first migration: %v", err)
	}
	firstResult := string(data)

	// Second migration should be a no-op — file content should not change.
	if err := providerIDMigration(configDir); err != nil {
		t.Fatalf("second providerIDMigration() error: %v", err)
	}

	data2, err := os.ReadFile(filepath.Join(configDir, "config.yaml"))
	if err != nil {
		t.Fatalf("reading config.yaml after second migration: %v", err)
	}
	secondResult := string(data2)

	if firstResult != secondResult {
		t.Errorf("second migration changed the file:\nfirst:\n%s\nsecond:\n%s", firstResult, secondResult)
	}
}

func TestProviderIDMigration_SkipsNewFormat(t *testing.T) {
	configDir := t.TempDir()

	// Config already in new format — has id fields, no operator endpoint/api_key,
	// nested agents.defaults.
	writeConfigYAML(t, configDir, `
providers:
  - id: lm-studio
    name: LM Studio
    type: local
operator:
  provider: lm-studio
  model: test-model
agents:
  defaults:
    provider: lm-studio
    model: test-model
`)

	// Read original content to compare after migration.
	original, err := os.ReadFile(filepath.Join(configDir, "config.yaml"))
	if err != nil {
		t.Fatalf("reading original config: %v", err)
	}

	if err := providerIDMigration(configDir); err != nil {
		t.Fatalf("providerIDMigration() error: %v", err)
	}

	after, err := os.ReadFile(filepath.Join(configDir, "config.yaml"))
	if err != nil {
		t.Fatalf("reading config after migration: %v", err)
	}

	if string(original) != string(after) {
		t.Errorf("new-format config should not be modified:\noriginal:\n%s\nafter:\n%s", string(original), string(after))
	}

	// No backup should have been created.
	backupPath := filepath.Join(configDir, "config.yaml.pre-provider-id-migration")
	if fileExists(backupPath) {
		t.Error("backup file should not exist when migration is skipped")
	}
}

func TestProviderIDMigration_CreatesBackup(t *testing.T) {
	configDir := t.TempDir()

	originalContent := `providers:
  - name: LM Studio
    type: local
operator:
  endpoint: http://localhost:1234
`
	writeConfigYAML(t, configDir, originalContent)

	if err := providerIDMigration(configDir); err != nil {
		t.Fatalf("providerIDMigration() error: %v", err)
	}

	backupPath := filepath.Join(configDir, "config.yaml.pre-provider-id-migration")
	if !fileExists(backupPath) {
		t.Fatal("expected backup file to be created")
	}

	backupData, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("reading backup file: %v", err)
	}

	if string(backupData) != originalContent {
		t.Errorf("backup file content mismatch:\ngot:\n%s\nwant:\n%s", string(backupData), originalContent)
	}
}
