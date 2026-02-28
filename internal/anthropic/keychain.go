package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// refreshMu serializes token refresh operations to prevent concurrent
// goroutines from racing on Keychain writes and consuming rotated refresh tokens.
var refreshMu sync.Mutex

// anthropicHTTPClient is a shared HTTP client with proper timeouts for
// Anthropic API requests, replacing http.DefaultClient to prevent goroutine
// leaks on slow/unresponsive API servers.
var anthropicHTTPClient = &http.Client{
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

// goos is the runtime OS, overridable in tests.
var goos = runtime.GOOS

const (
	keychainService = "Claude Code-credentials"
	tokenURL        = "https://platform.claude.com/v1/oauth/token"
	clientID        = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
)

// formatAPIError extracts a human-readable error message from an Anthropic API
// error response body. Falls back to a truncated raw body if parsing fails.
func formatAPIError(statusCode int, body []byte) error {
	var apiErr struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error.Message != "" {
		return fmt.Errorf("anthropic API error (%d): %s", statusCode, apiErr.Error.Message)
	}
	// Fallback: truncate raw body to avoid dumping huge JSON into the TUI.
	s := string(body)
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return fmt.Errorf("anthropic API error (%d): %s", statusCode, s)
}

// keychainCredentials holds the OAuth token read from the macOS Keychain.
type keychainCredentials struct {
	AccessToken string
	ExpiresAt   int64 // unix millis
}

// keychainBlob is the full JSON structure stored in the Keychain.
// We preserve the entire blob so we can write it back after refresh.
type keychainBlob struct {
	ClaudeAiOauth keychainOauth `json:"claudeAiOauth"`
}

type keychainOauth struct {
	AccessToken      string   `json:"accessToken"`
	RefreshToken     string   `json:"refreshToken"`
	ExpiresAt        int64    `json:"expiresAt"`
	Scopes           []string `json:"scopes"`
	SubscriptionType string   `json:"subscriptionType"`
	RateLimitTier    string   `json:"rateLimitTier"`
}

// readKeychainBlob reads and parses the full credential blob from the macOS Keychain.
func readKeychainBlob() (*keychainBlob, error) {
	if goos != "darwin" {
		return nil, fmt.Errorf("keychain access is only supported on macOS; set ANTHROPIC_API_KEY environment variable instead")
	}

	cmd := exec.Command("security", "find-generic-password", "-s", keychainService, "-w")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("reading keychain: %w (is Claude Code signed in?)", err)
	}

	raw := strings.TrimSpace(string(out))

	var blob keychainBlob
	if err := json.Unmarshal([]byte(raw), &blob); err != nil {
		return nil, fmt.Errorf("parsing keychain credentials: %w", err)
	}

	if blob.ClaudeAiOauth.AccessToken == "" {
		return nil, fmt.Errorf("no access token found in keychain credentials")
	}

	return &blob, nil
}

// writeKeychainBlob writes the credential blob back to the macOS Keychain,
// replacing the existing entry.
func writeKeychainBlob(blob *keychainBlob) error {
	if goos != "darwin" {
		return fmt.Errorf("keychain access is only supported on macOS; set ANTHROPIC_API_KEY environment variable instead")
	}

	data, err := json.Marshal(blob)
	if err != nil {
		return fmt.Errorf("marshaling keychain blob: %w", err)
	}

	// Find the account name from the existing entry.
	findCmd := exec.Command("security", "find-generic-password", "-s", keychainService)
	findOut, err := findCmd.Output()
	if err != nil {
		return fmt.Errorf("finding keychain entry: %w", err)
	}

	// Parse the account name from the output (line like: "acct"<blob>="username").
	account := ""
	for _, line := range strings.Split(string(findOut), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, `"acct"`) {
			// Extract value between the last pair of quotes.
			if idx := strings.LastIndex(line, `="`); idx != -1 {
				account = strings.TrimSuffix(line[idx+2:], `"`)
			}
		}
	}

	if account == "" {
		return fmt.Errorf("could not determine keychain account name")
	}

	// Delete the old entry and add the new one.
	// security doesn't have an "update" command — you delete and re-add.
	delCmd := exec.Command("security", "delete-generic-password", "-s", keychainService, "-a", account)
	_ = delCmd.Run() // ignore error if entry doesn't exist

	addCmd := exec.Command("security", "add-generic-password",
		"-s", keychainService,
		"-a", account,
		"-w", string(data),
	)
	if err := addCmd.Run(); err != nil {
		return fmt.Errorf("writing keychain entry: %w", err)
	}

	return nil
}

// refreshAccessToken uses the refresh token to obtain a new access token
// from the Anthropic OAuth token endpoint.
func refreshAccessToken(refreshToken string) (*tokenResponse, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating token refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := anthropicHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token refresh request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}

	if tok.AccessToken == "" {
		return nil, fmt.Errorf("token refresh returned empty access token")
	}

	return &tok, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"` // seconds
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

// readKeychainCredentials reads the OAuth credentials from the macOS Keychain.
// If the access token is expired, it automatically refreshes it using the
// refresh token and writes the updated credentials back to the Keychain.
func readKeychainCredentials() (*keychainCredentials, error) {
	blob, err := readKeychainBlob()
	if err != nil {
		return nil, err
	}

	oauth := blob.ClaudeAiOauth

	// If the token is still valid, return it directly.
	if oauth.ExpiresAt == 0 || oauth.ExpiresAt > time.Now().UnixMilli() {
		return &keychainCredentials{
			AccessToken: oauth.AccessToken,
			ExpiresAt:   oauth.ExpiresAt,
		}, nil
	}

	// Token is expired — try to refresh.
	if oauth.RefreshToken == "" {
		return nil, fmt.Errorf("OAuth token expired and no refresh token available")
	}

	tok, err := refreshAccessToken(oauth.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("refreshing expired token: %w", err)
	}

	// Update the blob with the new tokens.
	blob.ClaudeAiOauth.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		blob.ClaudeAiOauth.RefreshToken = tok.RefreshToken
	}
	blob.ClaudeAiOauth.ExpiresAt = time.Now().UnixMilli() + tok.ExpiresIn*1000

	// Write the updated credentials back to the Keychain.
	// Ignore write errors — we still have a valid token for this request.
	_ = writeKeychainBlob(blob)

	return &keychainCredentials{
		AccessToken: tok.AccessToken,
		ExpiresAt:   blob.ClaudeAiOauth.ExpiresAt,
	}, nil
}

// ReadKeychainAccessToken reads the OAuth access token from the macOS Keychain.
// If the token is expired, it automatically refreshes it. This is the exported
// entry point for other packages that need Keychain-based authentication.
//
// The function is serialized via refreshMu to prevent concurrent goroutines from
// racing on Keychain writes and consuming rotated refresh tokens.
func ReadKeychainAccessToken() (string, error) {
	refreshMu.Lock()
	defer refreshMu.Unlock()

	creds, err := readKeychainCredentials()
	if err != nil {
		return "", err
	}
	return creds.AccessToken, nil
}
