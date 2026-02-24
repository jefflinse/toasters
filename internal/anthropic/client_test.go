package anthropic

import (
	"runtime"
	"strings"
	"testing"
)

// These tests mutate the package-level `goos` variable, so they must NOT
// run in parallel with each other.

func TestReadKeychainBlob_NonDarwin(t *testing.T) {
	orig := goos
	defer func() { goos = orig }()
	goos = "linux"

	_, err := readKeychainBlob()
	if err == nil {
		t.Fatal("expected error on non-darwin platform, got nil")
	}

	const wantSubstr = "keychain access is only supported on macOS"
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error message %q does not contain %q", err.Error(), wantSubstr)
	}
}

func TestWriteKeychainBlob_NonDarwin(t *testing.T) {
	orig := goos
	defer func() { goos = orig }()
	goos = "linux"

	err := writeKeychainBlob(&keychainBlob{})
	if err == nil {
		t.Fatal("expected error on non-darwin platform, got nil")
	}

	const wantSubstr = "keychain access is only supported on macOS"
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error message %q does not contain %q", err.Error(), wantSubstr)
	}
}

func TestReadKeychainBlob_DarwinPassthrough(t *testing.T) {
	orig := goos
	defer func() { goos = orig }()
	goos = "darwin"

	_, err := readKeychainBlob()

	// On a real macOS host the function will attempt to call the `security`
	// binary. It may succeed (unlikely in CI) or fail with a Keychain-related
	// error. Either way, the platform guard must NOT have fired.
	//
	// On non-macOS hosts the `security` binary doesn't exist, so exec will
	// fail — but the error will be an exec error, not the platform guard.
	if err == nil {
		return // success path — nothing more to check
	}

	const guardSubstr = "only supported on macOS"
	if strings.Contains(err.Error(), guardSubstr) {
		t.Errorf("expected a non-guard error when goos=darwin, got platform guard error: %v", err)
	}

	if runtime.GOOS != "darwin" {
		t.Logf("non-macOS host: got expected exec error: %v", err)
	}
}

func TestWriteKeychainBlob_DarwinPassthrough(t *testing.T) {
	// NOTE: We do NOT actually call writeKeychainBlob on macOS because it
	// would overwrite real Claude Code credentials in the Keychain.
	// Instead, we only verify the platform guard fires on non-darwin.
	if runtime.GOOS == "darwin" {
		t.Skip("skipping write test on macOS to avoid overwriting real Keychain credentials")
	}

	orig := goos
	defer func() { goos = orig }()
	goos = "darwin"

	// On non-macOS hosts with goos="darwin", the function will try to run
	// the `security` binary which doesn't exist — producing an exec error,
	// not the platform guard error.
	err := writeKeychainBlob(&keychainBlob{})
	if err == nil {
		return
	}

	const guardSubstr = "only supported on macOS"
	if strings.Contains(err.Error(), guardSubstr) {
		t.Errorf("expected a non-guard error when goos=darwin, got platform guard error: %v", err)
	}

	t.Logf("non-macOS host: got expected exec error: %v", err)
}
