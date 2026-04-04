package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const tokenFile = "server.token"

// EnsureToken loads the token from configDir/server.token if it exists and is
// non-empty, or generates a new 32-byte (64 hex character) token, writes it to
// that file with 0600 permissions, and returns it. It is idempotent: calling it
// multiple times with the same configDir always returns the same token once the
// file exists.
//
// If the file already exists but its permissions are too open (perm & 0077 != 0),
// a warning is logged and the permissions are tightened to 0600.
func EnsureToken(configDir string) (string, error) {
	existing, err := LoadToken(configDir)
	if err != nil {
		return "", fmt.Errorf("loading existing token: %w", err)
	}
	if existing != "" {
		return existing, nil
	}

	token, err := generateToken()
	if err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}

	path := filepath.Join(configDir, tokenFile)
	if err := os.WriteFile(path, []byte(token), fs.FileMode(0600)); err != nil {
		return "", fmt.Errorf("writing token file: %w", err)
	}

	return token, nil
}

// LoadToken reads the token from configDir/server.token. It returns ("", nil)
// if the file does not exist. If the file exists but its permissions allow
// group or other access (perm & 0077 != 0), a warning is logged and the
// permissions are tightened to 0600 before the token is returned.
func LoadToken(configDir string) (string, error) {
	path := filepath.Join(configDir, tokenFile)

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("stating token file: %w", err)
	}

	perm := info.Mode().Perm()
	if perm&0077 != 0 {
		slog.Warn("token file permissions too open, restricting to 0600",
			"path", path,
			"was", perm.String(),
		)
		if err := os.Chmod(path, fs.FileMode(0600)); err != nil {
			slog.Error("failed to chmod token file", "path", path, "error", err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading token file: %w", err)
	}

	return strings.TrimSpace(string(data)), nil
}

// generateToken produces 32 random bytes from crypto/rand and returns them
// hex-encoded as a 64-character string.
func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
