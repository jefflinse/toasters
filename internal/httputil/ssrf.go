// Package httputil provides shared HTTP utilities including SSRF protection.
//
// All HTTP clients that fetch user-controlled URLs should use [NewSafeClient]
// or [IsPrivateIP] to prevent server-side request forgery against private
// networks.
package httputil

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// PrivateNetworks lists IP ranges that should not be accessible via
// user-controlled HTTP requests. This is the canonical CIDR list used for
// SSRF protection throughout the application.
var PrivateNetworks = []*net.IPNet{
	mustParseCIDR("127.0.0.0/8"),    // IPv4 loopback
	mustParseCIDR("10.0.0.0/8"),     // RFC 1918
	mustParseCIDR("172.16.0.0/12"),  // RFC 1918
	mustParseCIDR("192.168.0.0/16"), // RFC 1918
	mustParseCIDR("169.254.0.0/16"), // link-local
	mustParseCIDR("::1/128"),        // IPv6 loopback
	mustParseCIDR("fc00::/7"),       // IPv6 unique local
	mustParseCIDR("fe80::/10"),      // IPv6 link-local
}

func mustParseCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// IsPrivateIP reports whether ip falls within any of the [PrivateNetworks].
func IsPrivateIP(ip net.IP) bool {
	for _, network := range PrivateNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// NewSafeClient returns an [http.Client] with SSRF protection. The client
// resolves DNS before connecting and rejects any address that maps to a
// private or reserved IP range. The provided timeout applies to the overall
// request; the dial timeout is capped at 10 seconds.
func NewSafeClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, _, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
				if err != nil {
					return nil, err
				}
				for _, ip := range ips {
					if IsPrivateIP(ip.IP) {
						return nil, fmt.Errorf("access to private/reserved IP %s is blocked", ip.IP)
					}
				}
				dialer := &net.Dialer{Timeout: 10 * time.Second}
				return dialer.DialContext(ctx, network, addr)
			},
		},
	}
}

// SafeGet performs an HTTP GET with SSRF protection. It creates a one-off
// safe client with the given timeout. For repeated requests, prefer
// [NewSafeClient] and reuse the returned client.
func SafeGet(ctx context.Context, url string, timeout time.Duration) (*http.Response, error) {
	client := NewSafeClient(timeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	return client.Do(req)
}
