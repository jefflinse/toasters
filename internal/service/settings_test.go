package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/prompt"
)

// TestGetSettings_DefaultWhenNoConfig confirms the service returns valid
// defaults when no AppConfig is wired (used by tests / minimal bootstraps).
func TestGetSettings_DefaultWhenNoConfig(t *testing.T) {
	t.Parallel()

	svc := NewLocal(LocalConfig{ConfigDir: t.TempDir()})
	got, err := svc.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got.CoarseGranularity != "medium" {
		t.Errorf("default CoarseGranularity = %q, want %q", got.CoarseGranularity, "medium")
	}
	if got.FineGranularity != "medium" {
		t.Errorf("default FineGranularity = %q, want %q", got.FineGranularity, "medium")
	}
}

// TestGetSettings_ReadsFromAppConfig confirms the service echoes whatever
// lives in the live AppConfig struct.
func TestGetSettings_ReadsFromAppConfig(t *testing.T) {
	t.Parallel()

	svc := NewLocal(LocalConfig{
		ConfigDir: t.TempDir(),
		AppConfig: &config.Config{
			CoarseGranularity: "xcoarse",
			FineGranularity:   "xfine",
		},
	})
	got, err := svc.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got.CoarseGranularity != "xcoarse" {
		t.Errorf("CoarseGranularity got %q, want %q", got.CoarseGranularity, "xcoarse")
	}
	if got.FineGranularity != "xfine" {
		t.Errorf("FineGranularity got %q, want %q", got.FineGranularity, "xfine")
	}
}

// TestUpdateSettings_PersistsAndRefreshesEngine verifies the full service
// write path for both levers: update → config.yaml on disk → AppConfig
// mutated → prompt engine's synthetic coarse-granularity and fine-granularity
// instructions refreshed.
func TestUpdateSettings_PersistsAndRefreshesEngine(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath,
		[]byte("coarse_granularity: medium\nfine_granularity: medium\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Prompt engine with both levers' source instructions loaded.
	instDir := filepath.Join(dir, "instructions")
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		t.Fatalf("mkdir instructions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(instDir, "coarse-granularity-xcoarse.md"),
		[]byte("COARSE_XCOARSE"), 0o644); err != nil {
		t.Fatalf("write coarse instr: %v", err)
	}
	if err := os.WriteFile(filepath.Join(instDir, "fine-granularity-xfine.md"),
		[]byte("FINE_XFINE"), 0o644); err != nil {
		t.Fatalf("write fine instr: %v", err)
	}
	engine := prompt.NewEngine()
	if err := engine.LoadDir(dir, "test"); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}

	appCfg := &config.Config{CoarseGranularity: "medium", FineGranularity: "medium"}
	svc := NewLocal(LocalConfig{
		ConfigDir:    dir,
		AppConfig:    appCfg,
		PromptEngine: engine,
	})

	err := svc.UpdateSettings(context.Background(), Settings{
		CoarseGranularity: "xcoarse",
		FineGranularity:   "xfine",
	})
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	// 1. config.yaml on disk reflects the new values.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("re-read config: %v", err)
	}
	if !strings.Contains(string(data), "coarse_granularity: xcoarse") {
		t.Errorf("config.yaml missing coarse value, got:\n%s", data)
	}
	if !strings.Contains(string(data), "fine_granularity: xfine") {
		t.Errorf("config.yaml missing fine value, got:\n%s", data)
	}

	// 2. AppConfig in memory reflects the new values.
	if appCfg.CoarseGranularity != "xcoarse" {
		t.Errorf("AppConfig coarse not updated: got %q", appCfg.CoarseGranularity)
	}
	if appCfg.FineGranularity != "xfine" {
		t.Errorf("AppConfig fine not updated: got %q", appCfg.FineGranularity)
	}

	// 3. Prompt engine synthetic instructions refreshed.
	body, ok := engine.Instruction("coarse-granularity")
	if !ok {
		t.Fatalf("engine should have coarse-granularity instruction after update")
	}
	if body != "COARSE_XCOARSE" {
		t.Errorf("coarse-granularity body = %q, want %q", body, "COARSE_XCOARSE")
	}
	body, ok = engine.Instruction("fine-granularity")
	if !ok {
		t.Fatalf("engine should have fine-granularity instruction after update")
	}
	if body != "FINE_XFINE" {
		t.Errorf("fine-granularity body = %q, want %q", body, "FINE_XFINE")
	}
}

// TestUpdateSettings_RejectsInvalidValue ensures the service refuses to
// persist an unrecognized level and leaves the AppConfig unchanged. This
// is especially important now that we write both fields in one call —
// an invalid coarse value must not leave fine half-written.
func TestUpdateSettings_RejectsInvalidValue(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("coarse_granularity: medium\nfine_granularity: medium\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	appCfg := &config.Config{CoarseGranularity: "medium", FineGranularity: "medium"}
	svc := NewLocal(LocalConfig{ConfigDir: dir, AppConfig: appCfg})

	err := svc.UpdateSettings(context.Background(), Settings{
		CoarseGranularity: "bogus",
		FineGranularity:   "xfine",
	})
	if err == nil {
		t.Fatal("expected an error for invalid coarse value")
	}
	if appCfg.CoarseGranularity != "medium" {
		t.Errorf("AppConfig coarse should be unchanged, got %q", appCfg.CoarseGranularity)
	}
	if appCfg.FineGranularity != "medium" {
		t.Errorf("AppConfig fine should be unchanged after validation failure, got %q", appCfg.FineGranularity)
	}
}

// TestUpdateSettings_RejectedWhenNoAppConfig verifies the service can't be
// coerced into writing when no config is wired (LocalConfig.AppConfig nil).
func TestUpdateSettings_RejectedWhenNoAppConfig(t *testing.T) {
	t.Parallel()

	svc := NewLocal(LocalConfig{ConfigDir: t.TempDir()})
	err := svc.UpdateSettings(context.Background(), Settings{
		CoarseGranularity: "medium", FineGranularity: "medium",
	})
	if err == nil {
		t.Fatal("expected error when no AppConfig is wired")
	}
}
