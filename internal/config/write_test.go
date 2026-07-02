package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateProviderID(t *testing.T) {
	valid := []string{
		"anthropic",
		"lm-studio",
		"my_provider",
		"openai.v2",
		"a",
		"0local",
	}
	for _, id := range valid {
		if err := ValidateProviderID(id); err != nil {
			t.Errorf("ValidateProviderID(%q) = %v, want nil", id, err)
		}
	}

	invalid := []string{
		"",
		".",
		"..",
		"../escape",
		"../../config",
		"..\\..\\config",
		"providers/../../../tmp/evil",
		"/etc/passwd",
		"foo/bar",
		"-leading-hyphen",
		".hidden",
		"with space",
		"null\x00byte",
	}
	for _, id := range invalid {
		if err := ValidateProviderID(id); err == nil {
			t.Errorf("ValidateProviderID(%q) = nil, want error", id)
		}
	}
}

func TestAddProvider_RejectsPathTraversalID(t *testing.T) {
	configDir := t.TempDir()

	err := AddProvider(configDir, ProviderEntry{
		ID:   "../evil",
		Name: "Evil",
		Type: "openai",
	})
	if err == nil {
		t.Fatal("expected error for traversal ID, got nil")
	}

	// Nothing must have been written outside (or inside) the providers dir.
	if _, statErr := os.Stat(filepath.Join(configDir, "evil.yaml")); !os.IsNotExist(statErr) {
		t.Errorf("traversal ID escaped providers dir: %v", statErr)
	}
}

func TestUpdateProvider_RejectsPathTraversalID(t *testing.T) {
	configDir := t.TempDir()

	// A traversal ID must not be able to overwrite config.yaml.
	configPath := filepath.Join(configDir, "config.yaml")
	original := []byte("workspace_dir: ~/toasters\n")
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	err := UpdateProvider(configDir, ProviderEntry{
		ID:   "../config",
		Name: "Evil",
		Type: "openai",
	})
	if err == nil {
		t.Fatal("expected error for traversal ID, got nil")
	}

	data, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != string(original) {
		t.Error("config.yaml was overwritten via provider ID traversal")
	}
}

func TestAddProvider_ValidIDRoundTrip(t *testing.T) {
	configDir := t.TempDir()

	entry := ProviderEntry{ID: "lm-studio", Name: "LM Studio", Type: "local", Endpoint: "http://localhost:1234/v1"}
	if err := AddProvider(configDir, entry); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	path := filepath.Join(configDir, "providers", "lm-studio.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected provider file at %s: %v", path, err)
	}

	// Adding the same ID again must conflict.
	if err := AddProvider(configDir, entry); err == nil {
		t.Error("expected 'already exists' error on duplicate add")
	}

	// UpdateProvider with a valid ID overwrites in place.
	entry.Name = "LM Studio (renamed)"
	if err := UpdateProvider(configDir, entry); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
}

func TestUpdateProvider_PreservesUnknownKeys(t *testing.T) {
	configDir := t.TempDir()
	providersDir := filepath.Join(configDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(providersDir, "lmstudio.yaml")

	// A hand-edited provider file with keys ProviderEntry doesn't carry.
	original := `id: lmstudio
name: LMStudio
type: local
endpoint: http://localhost:1234/v1
model: gemma-4-26b
concurrency: 2
context_window: 8192
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	// An API-driven edit that only knows the ProviderEntry fields.
	err := UpdateProvider(configDir, ProviderEntry{
		ID:       "lmstudio",
		Name:     "LM Studio (renamed)",
		Type:     "local",
		Endpoint: "http://localhost:9999/v1",
	})
	if err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		"name: LM Studio (renamed)",
		"endpoint: http://localhost:9999/v1",
		"model: gemma-4-26b",
		"concurrency: 2",
		"context_window: 8192",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("updated file missing %q:\n%s", want, got)
		}
	}
}

func TestUpdateProvider_EmptyOptionalFieldsRemoveKeys(t *testing.T) {
	configDir := t.TempDir()
	providersDir := filepath.Join(configDir, "providers")
	if err := os.MkdirAll(providersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(providersDir, "cloud.yaml")
	original := `id: cloud
name: Cloud
type: anthropic
api_key: secret
endpoint: http://old.example.com
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	// Clearing endpoint and api_key must remove the keys, matching the old
	// whole-file-overwrite semantics.
	err := UpdateProvider(configDir, ProviderEntry{ID: "cloud", Name: "Cloud", Type: "anthropic"})
	if err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, "api_key") || strings.Contains(got, "endpoint") {
		t.Errorf("cleared keys still present:\n%s", got)
	}
}

func TestUpdateProvider_CreatesMissingFile(t *testing.T) {
	configDir := t.TempDir()
	err := UpdateProvider(configDir, ProviderEntry{
		ID: "fresh", Name: "Fresh", Type: "local", Endpoint: "http://localhost:1234",
	})
	if err != nil {
		t.Fatalf("UpdateProvider (upsert): %v", err)
	}
	data, err := os.ReadFile(filepath.Join(configDir, "providers", "fresh.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"id: fresh", "name: Fresh", "type: local"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("created file missing %q:\n%s", want, data)
		}
	}
}
