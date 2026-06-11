package config

import (
	"os"
	"path/filepath"
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
