package httputil

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip      string
		private bool
	}{
		// IPv4 loopback
		{"127.0.0.1", true},
		{"127.255.255.255", true},

		// RFC 1918 — 10.0.0.0/8
		{"10.0.0.1", true},
		{"10.255.255.255", true},

		// RFC 1918 — 172.16.0.0/12
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"172.32.0.1", false}, // just outside the range

		// RFC 1918 — 192.168.0.0/16
		{"192.168.0.1", true},
		{"192.168.255.255", true},

		// Link-local
		{"169.254.0.1", true},
		{"169.254.255.255", true},

		// IPv6 loopback
		{"::1", true},

		// IPv6 unique local
		{"fc00::1", true},
		{"fdff::1", true},

		// IPv6 link-local
		{"fe80::1", true},

		// Public IPs — should NOT be private
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"93.184.216.34", false},
		{"2607:f8b0:4004:800::200e", false}, // Google public IPv6
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("failed to parse IP %q", tt.ip)
			}
			got := IsPrivateIP(ip)
			if got != tt.private {
				t.Errorf("IsPrivateIP(%s) = %v, want %v", tt.ip, got, tt.private)
			}
		})
	}
}

func TestPrivateNetworks_Coverage(t *testing.T) {
	// Verify the canonical list has the expected number of entries.
	if got := len(PrivateNetworks); got != 8 {
		t.Errorf("PrivateNetworks has %d entries, want 8", got)
	}

	// Verify each expected CIDR is present.
	expected := []string{
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"::1/128",
		"fc00::/7",
		"fe80::/10",
	}
	for _, cidr := range expected {
		found := false
		_, want, _ := net.ParseCIDR(cidr)
		for _, got := range PrivateNetworks {
			if got.String() == want.String() {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("PrivateNetworks missing expected CIDR %s", cidr)
		}
	}
}

func TestNewSafeClient_BlocksPrivateIP(t *testing.T) {
	client := NewSafeClient(5 * time.Second)

	// Attempt to connect to a private IP — should be blocked at the dial level.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:9999", nil)
	if err != nil {
		t.Fatalf("creating request: %v", err)
	}

	_, err = client.Do(req)
	if err == nil {
		t.Fatal("expected error for private IP, got nil")
	}
	if got := err.Error(); !contains(got, "private/reserved IP") {
		t.Errorf("expected SSRF block error, got: %v", err)
	}
}

func TestNewSafeClient_AllowsPublicIP(t *testing.T) {
	// We can't easily test a real public IP in unit tests, but we can verify
	// the client is created with the expected timeout.
	client := NewSafeClient(42 * time.Second)
	if client.Timeout != 42*time.Second {
		t.Errorf("client timeout = %v, want 42s", client.Timeout)
	}
}

func TestSafeGet_BlocksPrivateIP(t *testing.T) {
	_, err := SafeGet(context.Background(), "http://127.0.0.1:9999", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for private IP, got nil")
	}
	if got := err.Error(); !contains(got, "private/reserved IP") {
		t.Errorf("expected SSRF block error, got: %v", err)
	}
}

func TestSafeGet_InvalidURL(t *testing.T) {
	_, err := SafeGet(context.Background(), "://bad", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for invalid URL, got nil")
	}
}

func TestNewSafeClient_RespectsTimeout(t *testing.T) {
	// Create a server that delays longer than the client timeout.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = fmt.Fprint(w, "too late")
	}))
	defer srv.Close()

	// Use a very short timeout — but we can't test against the httptest server
	// because it binds to 127.0.0.1 which is blocked by SSRF. Instead, just
	// verify the timeout is set correctly.
	client := NewSafeClient(100 * time.Millisecond)
	if client.Timeout != 100*time.Millisecond {
		t.Errorf("client timeout = %v, want 100ms", client.Timeout)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
