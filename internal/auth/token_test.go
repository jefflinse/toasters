package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureToken_GeneratesHexToken(t *testing.T) {
	dir := t.TempDir()
	token, err := EnsureToken(dir)
	if err != nil {
		t.Fatalf("EnsureToken: unexpected error: %v", err)
	}
	if len(token) != 64 {
		t.Errorf("token length = %d, want 64", len(token))
	}
	for _, c := range token {
		if !isHexChar(c) {
			t.Errorf("token contains non-hex character %q", c)
			break
		}
	}
}

func TestEnsureToken_CreatesFileWith0600Permissions(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureToken(dir); err != nil {
		t.Fatalf("EnsureToken: unexpected error: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, tokenFile))
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file permissions = %04o, want 0600", perm)
	}
}

func TestEnsureToken_IsIdempotent(t *testing.T) {
	dir := t.TempDir()

	first, err := EnsureToken(dir)
	if err != nil {
		t.Fatalf("first EnsureToken: %v", err)
	}

	second, err := EnsureToken(dir)
	if err != nil {
		t.Fatalf("second EnsureToken: %v", err)
	}

	if first != second {
		t.Errorf("token changed between calls: first=%q second=%q", first, second)
	}
}

func TestLoadToken_ReturnEmptyWhenFileAbsent(t *testing.T) {
	dir := t.TempDir()
	token, err := LoadToken(dir)
	if err != nil {
		t.Fatalf("LoadToken: unexpected error: %v", err)
	}
	if token != "" {
		t.Errorf("LoadToken returned %q, want empty string", token)
	}
}

func TestLoadToken_ReturnsTokenWhenFileExists(t *testing.T) {
	dir := t.TempDir()
	want, err := EnsureToken(dir)
	if err != nil {
		t.Fatalf("EnsureToken: %v", err)
	}

	got, err := LoadToken(dir)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if got != want {
		t.Errorf("LoadToken = %q, want %q", got, want)
	}
}

func TestLoadToken_TightensOpenPermissions(t *testing.T) {
	dir := t.TempDir()

	// Create a token file with overly open permissions.
	path := filepath.Join(dir, tokenFile)
	if err := os.WriteFile(path, []byte("deadbeef"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := LoadToken(dir)
	if err != nil {
		t.Fatalf("LoadToken: unexpected error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file permissions after tightening = %04o, want 0600", perm)
	}
}

// isHexChar reports whether r is a valid lowercase hex digit.
func isHexChar(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
}
