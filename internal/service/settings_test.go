package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jefflinse/toasters/internal/config"
	"github.com/jefflinse/toasters/internal/operator"
	"github.com/jefflinse/toasters/internal/prompt"
	"github.com/jefflinse/toasters/internal/provider"
	"github.com/jefflinse/toasters/internal/runtime"
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

// An invalid worker temperature must reject the whole update BEFORE any
// field is written — pre-fix, the granularity levers persisted first and a
// bad temperature left config.yaml half-updated (C21).
func TestUpdateSettings_InvalidTemperatureWritesNothing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	original := "coarse_granularity: medium\nfine_granularity: medium\n"
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	appCfg := &config.Config{
		CoarseGranularity: "medium",
		FineGranularity:   "medium",
		WorkerTemperature: 0.1,
	}
	svc := NewLocal(LocalConfig{ConfigDir: dir, AppConfig: appCfg})

	err := svc.UpdateSettings(context.Background(), Settings{
		CoarseGranularity: "xcoarse",
		FineGranularity:   "xfine",
		WorkerTemperature: 5.0, // out of [0, 2]
	})
	if err == nil {
		t.Fatal("expected an error for out-of-range temperature")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("err = %v, want errors.Is(_, ErrInvalid)", err)
	}

	data, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("re-read config: %v", readErr)
	}
	if string(data) != original {
		t.Errorf("config.yaml was modified despite validation failure:\n%s", data)
	}
	if appCfg.CoarseGranularity != "medium" || appCfg.FineGranularity != "medium" {
		t.Errorf("AppConfig granularity mutated despite validation failure: %q/%q",
			appCfg.CoarseGranularity, appCfg.FineGranularity)
	}
}

// TestUpdateSettings_PersistsSidebarSide verifies the sidebar-side pref is
// written to config.yaml and applied to the live AppConfig, and that an
// unrecognized value is normalized to "left" rather than rejected (it's a
// pure UI pref — same lenient treatment as fleet_row_density).
func TestUpdateSettings_PersistsSidebarSide(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath,
		[]byte("coarse_granularity: medium\nfine_granularity: medium\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	appCfg := &config.Config{CoarseGranularity: "medium", FineGranularity: "medium"}
	svc := NewLocal(LocalConfig{ConfigDir: dir, AppConfig: appCfg})

	err := svc.UpdateSettings(context.Background(), Settings{
		CoarseGranularity: "medium",
		FineGranularity:   "medium",
		SidebarSide:       "right",
	})
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("re-read config: %v", err)
	}
	if !strings.Contains(string(data), "sidebar_side: right") {
		t.Errorf("config.yaml missing sidebar_side, got:\n%s", data)
	}
	if appCfg.SidebarSide != "right" {
		t.Errorf("AppConfig.SidebarSide = %q, want %q", appCfg.SidebarSide, "right")
	}

	got, err := svc.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got.SidebarSide != "right" {
		t.Errorf("GetSettings SidebarSide = %q, want %q", got.SidebarSide, "right")
	}

	// A bogus value normalizes to "left" instead of failing the update.
	err = svc.UpdateSettings(context.Background(), Settings{
		CoarseGranularity: "medium",
		FineGranularity:   "medium",
		SidebarSide:       "diagonal",
	})
	if err != nil {
		t.Fatalf("UpdateSettings with bogus side: %v", err)
	}
	if appCfg.SidebarSide != "left" {
		t.Errorf("AppConfig.SidebarSide after bogus value = %q, want %q", appCfg.SidebarSide, "left")
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

// TestUpdateSettings_PersistsCompactionThresholds verifies both compaction
// thresholds are written to config.yaml as native ints, applied to the live
// AppConfig, and normalized (clamped) rather than rejected.
func TestUpdateSettings_PersistsCompactionThresholds(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath,
		[]byte("coarse_granularity: medium\nfine_granularity: medium\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	appCfg := &config.Config{CoarseGranularity: "medium", FineGranularity: "medium"}
	svc := NewLocal(LocalConfig{ConfigDir: dir, AppConfig: appCfg})

	err := svc.UpdateSettings(context.Background(), Settings{
		CoarseGranularity:           "medium",
		FineGranularity:             "medium",
		OperatorCompactionThreshold: 40,
		WorkerCompactionThreshold:   80,
	})
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("re-read config: %v", err)
	}
	for _, want := range []string{"operator_compaction_threshold: 40", "worker_compaction_threshold: 80"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("config.yaml missing %q, got:\n%s", want, data)
		}
	}
	if appCfg.OperatorCompactionThreshold != 40 || appCfg.WorkerCompactionThreshold != 80 {
		t.Errorf("AppConfig thresholds = %d/%d, want 40/80",
			appCfg.OperatorCompactionThreshold, appCfg.WorkerCompactionThreshold)
	}

	got, err := svc.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got.OperatorCompactionThreshold != 40 || got.WorkerCompactionThreshold != 80 {
		t.Errorf("GetSettings thresholds = %d/%d, want 40/80",
			got.OperatorCompactionThreshold, got.WorkerCompactionThreshold)
	}

	// 0 disables and persists as 0; out-of-range clamps instead of failing.
	err = svc.UpdateSettings(context.Background(), Settings{
		CoarseGranularity:           "medium",
		FineGranularity:             "medium",
		OperatorCompactionThreshold: 0,
		WorkerCompactionThreshold:   99,
	})
	if err != nil {
		t.Fatalf("UpdateSettings (disable/clamp): %v", err)
	}
	if appCfg.OperatorCompactionThreshold != 0 {
		t.Errorf("AppConfig.OperatorCompactionThreshold = %d, want 0 (disabled)", appCfg.OperatorCompactionThreshold)
	}
	if appCfg.WorkerCompactionThreshold != 90 {
		t.Errorf("AppConfig.WorkerCompactionThreshold = %d, want 90 (clamped)", appCfg.WorkerCompactionThreshold)
	}
}

// TestUpdateSettings_AppliesCompactionThresholdToOperator verifies a saved
// operator threshold reaches the live operator (which reads it at its next
// turn boundary) rather than requiring a restart.
func TestUpdateSettings_AppliesCompactionThresholdToOperator(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("coarse_granularity: medium\nfine_granularity: medium\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	op, err := operator.New(operator.Config{SystemPrompt: "test", CompactionThreshold: 50})
	if err != nil {
		t.Fatalf("operator.New: %v", err)
	}
	appCfg := &config.Config{CoarseGranularity: "medium", FineGranularity: "medium"}
	svc := NewLocal(LocalConfig{ConfigDir: dir, AppConfig: appCfg, Operator: op})

	err = svc.UpdateSettings(context.Background(), Settings{
		CoarseGranularity:           "medium",
		FineGranularity:             "medium",
		OperatorCompactionThreshold: 40,
		WorkerCompactionThreshold:   70,
	})
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if got := op.CompactionThreshold(); got != 40 {
		t.Errorf("operator threshold = %d, want 40 applied live", got)
	}

	// Disabling reaches the operator as 0.
	err = svc.UpdateSettings(context.Background(), Settings{
		CoarseGranularity:           "medium",
		FineGranularity:             "medium",
		OperatorCompactionThreshold: 0,
		WorkerCompactionThreshold:   70,
	})
	if err != nil {
		t.Fatalf("UpdateSettings (disable): %v", err)
	}
	if got := op.CompactionThreshold(); got != 0 {
		t.Errorf("operator threshold = %d, want 0 (disabled)", got)
	}
}

// TestUpdateSettings_AppliesWorkerThresholdToRuntime verifies a saved worker
// threshold reaches the live runtime (whose sessions read it at their next
// turn boundary).
func TestUpdateSettings_AppliesWorkerThresholdToRuntime(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("coarse_granularity: medium\nfine_granularity: medium\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	rt := runtime.New(nil, provider.NewRegistry())
	appCfg := &config.Config{CoarseGranularity: "medium", FineGranularity: "medium"}
	svc := NewLocal(LocalConfig{ConfigDir: dir, AppConfig: appCfg, Runtime: rt})

	err := svc.UpdateSettings(context.Background(), Settings{
		CoarseGranularity:           "medium",
		FineGranularity:             "medium",
		OperatorCompactionThreshold: 50,
		WorkerCompactionThreshold:   40,
	})
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if got := rt.CompactionThreshold(); got != 40 {
		t.Errorf("runtime threshold = %d, want 40 applied live", got)
	}
}
