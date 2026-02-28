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

// ---------------------------------------------------------------------------
// formatAPIError
// ---------------------------------------------------------------------------

func TestFormatAPIError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		body       []byte
		wantSubstr string
	}{
		{
			name:       "valid JSON error body",
			statusCode: 400,
			body:       []byte(`{"error":{"type":"invalid_request_error","message":"max_tokens must be positive"}}`),
			wantSubstr: "max_tokens must be positive",
		},
		{
			name:       "valid JSON with status code",
			statusCode: 429,
			body:       []byte(`{"error":{"type":"rate_limit_error","message":"rate limited"}}`),
			wantSubstr: "429",
		},
		{
			name:       "invalid JSON falls back to raw body",
			statusCode: 500,
			body:       []byte(`not json at all`),
			wantSubstr: "not json at all",
		},
		{
			name:       "empty error message falls back to raw body",
			statusCode: 500,
			body:       []byte(`{"error":{"type":"server_error","message":""}}`),
			wantSubstr: "server_error",
		},
		{
			name:       "empty body",
			statusCode: 502,
			body:       []byte(``),
			wantSubstr: "502",
		},
		{
			name:       "long body is truncated",
			statusCode: 500,
			body:       []byte(strings.Repeat("x", 300)),
			wantSubstr: "...",
		},
		{
			name:       "body at exactly 200 chars is not truncated",
			statusCode: 500,
			body:       []byte(strings.Repeat("y", 200)),
			wantSubstr: strings.Repeat("y", 200),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := formatAPIError(tt.statusCode, tt.body)
			if err == nil {
				t.Fatal("expected non-nil error")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantSubstr)
			}
		})
	}
}

func TestFormatAPIError_LongBodyTruncation(t *testing.T) {
	t.Parallel()
	body := []byte(strings.Repeat("z", 300))
	err := formatAPIError(500, body)
	msg := err.Error()
	// The raw body portion should be 200 chars + "..."
	if !strings.HasSuffix(msg, "...") {
		t.Errorf("expected truncated message to end with '...', got %q", msg)
	}
	// Should NOT contain the full 300-char string.
	if strings.Contains(msg, strings.Repeat("z", 300)) {
		t.Error("expected body to be truncated, but full body is present")
	}
}

func TestFormatAPIError_200CharsNotTruncated(t *testing.T) {
	t.Parallel()
	body := []byte(strings.Repeat("a", 200))
	err := formatAPIError(500, body)
	msg := err.Error()
	if strings.HasSuffix(msg, "...") {
		t.Errorf("200-char body should not be truncated, but got %q", msg)
	}
}
